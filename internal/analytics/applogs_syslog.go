package analytics

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

// The syslog receiver: RFC 5424 and RFC 3164 messages over UDP and TCP
// (newline-delimited or octet-counted), on the address in Settings → Logs,
// off while it is empty. Syslog has no keys: listen on a private address or
// behind a firewall. Lines are batched for a second before they are stored.

type syslogRunner struct {
	// ctx is the server's, which the listeners live on.
	ctx   context.Context
	mu    sync.Mutex
	addr  string
	stop  func()
	err   string
	batch []*logLine
}

var syslogRunners sync.Map // *Server → *syslogRunner

func (s *Server) syslog() *syslogRunner {
	v, _ := syslogRunners.LoadOrStore(s, &syslogRunner{})
	return v.(*syslogRunner)
}

// syslogStart follows the setting: it (re)binds when the address changes
// and flushes received lines every second.
func (s *Server) syslogStart(ctx context.Context) {
	rn := s.syslog()
	rn.mu.Lock()
	rn.ctx = ctx
	rn.mu.Unlock()
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		check := time.Time{}
		for {
			select {
			case <-ctx.Done():
				rn.mu.Lock()
				if rn.stop != nil {
					rn.stop()
				}
				rn.mu.Unlock()
				return
			case <-tick.C:
			}
			if time.Since(check) >= 30*time.Second {
				check = time.Now()
				s.syslogApply(ctx, strings.TrimSpace(s.logServer().Syslog))
			}
			rn.mu.Lock()
			batch := rn.batch
			rn.batch = nil
			rn.mu.Unlock()
			if len(batch) > 0 {
				wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if _, err := s.ingestLogLines(wctx, batch); err != nil {
					s.logger.Warn("syslog write", "err", err)
				}
				cancel()
			}
		}
	}()
}

// syslogApply binds addr, or stops listening when it is empty.
func (s *Server) syslogApply(ctx context.Context, addr string) {
	rn := s.syslog()
	rn.mu.Lock()
	defer rn.mu.Unlock()
	if addr == rn.addr && (rn.stop != nil || addr == "") {
		return
	}
	if rn.stop != nil {
		rn.stop()
		rn.stop = nil
	}
	rn.addr, rn.err = addr, ""
	if addr == "" {
		return
	}
	lctx, cancel := context.WithCancel(ctx)
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		cancel()
		rn.err = err.Error()
		s.logger.Warn("syslog: listen udp", "addr", addr, "err", err)
		return
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		cancel()
		_ = pc.Close()
		rn.err = err.Error()
		s.logger.Warn("syslog: listen tcp", "addr", addr, "err", err)
		return
	}
	rn.stop = func() {
		cancel()
		_ = pc.Close()
		_ = ln.Close()
	}
	s.logger.Info("syslog: listening", "addr", addr)
	go s.syslogUDP(pc)
	go s.syslogTCP(lctx, ln)
}

// syslogReapply applies a changed address at once rather than within 30
// seconds; before the receiver has started it does nothing.
func (s *Server) syslogReapply() {
	rn := s.syslog()
	rn.mu.Lock()
	ctx := rn.ctx
	rn.mu.Unlock()
	if ctx != nil {
		s.syslogApply(ctx, strings.TrimSpace(s.logServer().Syslog))
	}
}

// syslogStatus is what Settings shows: the address bound and any error.
func (s *Server) syslogStatus() (addr, errText string) {
	rn := s.syslog()
	rn.mu.Lock()
	defer rn.mu.Unlock()
	if rn.stop != nil {
		addr = rn.addr
	}
	return addr, rn.err
}

const syslogMaxPending = 20000

func (s *Server) syslogAdd(msg string, peer string) {
	l := parseSyslog(msg, s.logServer().SyslogService)
	if l == nil {
		return
	}
	if l.host == "" {
		l.host = peer
	}
	rn := s.syslog()
	rn.mu.Lock()
	if len(rn.batch) < syslogMaxPending {
		rn.batch = append(rn.batch, l)
	}
	rn.mu.Unlock()
}

func (s *Server) syslogUDP(pc net.PacketConn) {
	buf := make([]byte, 64<<10)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		host, _, _ := net.SplitHostPort(from.String())
		for _, m := range strings.Split(string(buf[:n]), "\n") {
			if strings.TrimSpace(m) != "" {
				s.syslogAdd(m, host)
			}
		}
	}
}

func (s *Server) syslogTCP(ctx context.Context, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			stop := context.AfterFunc(ctx, func() { _ = c.Close() })
			defer stop()
			host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
			r := bufio.NewReaderSize(c, 64<<10)
			for {
				_ = c.SetReadDeadline(time.Now().Add(10 * time.Minute))
				m, err := readSyslogFrame(r)
				if m != "" {
					s.syslogAdd(m, host)
				}
				if err != nil {
					return
				}
			}
		}()
	}
}

// readSyslogFrame reads one message: octet-counted ("123 <34>1 ...", RFC
// 6587) when it starts with a length, else up to the next newline.
func readSyslogFrame(r *bufio.Reader) (string, error) {
	b, err := r.Peek(1)
	if err != nil {
		return "", err
	}
	if b[0] >= '1' && b[0] <= '9' {
		lenText, err := r.ReadString(' ')
		if err != nil {
			return "", err
		}
		n, err := strconv.Atoi(strings.TrimSpace(lenText))
		if err != nil || n <= 0 || n > 1<<20 {
			return "", errors.New("bad frame length")
		}
		msg := make([]byte, n)
		if _, err := io.ReadFull(r, msg); err != nil {
			return "", err
		}
		return string(msg), nil
	}
	line, err := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

var (
	// <PRI>1 TIMESTAMP HOSTNAME APP-NAME PROCID MSGID [SD...] MSG
	syslog5424 = regexp.MustCompile(`^<(\d{1,3})>1 (\S+) (\S+) (\S+) (\S+) (\S+) (-|(?:\[(?:[^\]\\]|\\.)*\])+)(?: (.*))?$`)
	// <PRI>Mmm dd hh:mm:ss HOSTNAME TAG[PID]: MSG
	syslog3164 = regexp.MustCompile(`^<(\d{1,3})>([A-Z][a-z]{2} [ \d]\d \d{2}:\d{2}:\d{2}) (\S+) ([^:\[\s]+)(?:\[(\d+)\])?: ?(.*)$`)
	sdParam    = regexp.MustCompile(`([^\s=\]]+)="((?:[^"\\]|\\.)*)"`)
)

// syslogSev maps syslog severities (0 emergency .. 7 debug).
func syslogSev(pri int) int {
	switch pri % 8 {
	case 0, 1, 2:
		return logrules.Fatal
	case 3:
		return logrules.Error
	case 4:
		return logrules.Warn
	case 7:
		return logrules.Debug
	}
	return logrules.Info
}

var syslogFacilities = []string{"kern", "user", "mail", "daemon", "auth", "syslog", "lpr", "news", "uucp", "cron", "authpriv", "ftp", "ntp", "audit", "alert", "clock",
	"local0", "local1", "local2", "local3", "local4", "local5", "local6", "local7"}

// parseSyslog reads one message; a line in neither format is kept as it is.
func parseSyslog(msg, defaultService string) *logLine {
	msg = strings.TrimRight(strings.TrimPrefix(msg, "\ufeff"), "\r\n\x00")
	if strings.TrimSpace(msg) == "" {
		return nil
	}
	l := &logLine{source: "syslog", service: defaultService, attrs: map[string]any{}}
	if m := syslog5424.FindStringSubmatch(msg); m != nil {
		pri, _ := strconv.Atoi(m[1])
		l.sev = syslogSev(pri)
		l.attrs["syslog.facility"] = facilityName(pri)
		if t, err := time.Parse(time.RFC3339Nano, m[2]); err == nil {
			l.at = t
		}
		l.host = dash(m[3])
		if app := dash(m[4]); app != "" {
			l.service = app
		}
		if p := dash(m[5]); p != "" {
			l.attrs["syslog.procid"] = p
		}
		if id := dash(m[6]); id != "" {
			l.attrs["syslog.msgid"] = id
		}
		if m[7] != "-" {
			for _, sd := range strings.SplitAfter(m[7], "]") {
				sd = strings.TrimPrefix(strings.TrimSpace(sd), "[")
				id, params, _ := strings.Cut(strings.TrimSuffix(sd, "]"), " ")
				if id == "" {
					continue
				}
				for _, p := range sdParam.FindAllStringSubmatch(params, -1) {
					l.attrs[id+"."+p[1]] = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\]`, `]`).Replace(p[2])
				}
			}
		}
		l.message = strings.TrimPrefix(m[8], "\ufeff")
		return l
	}
	if m := syslog3164.FindStringSubmatch(msg); m != nil {
		pri, _ := strconv.Atoi(m[1])
		l.sev = syslogSev(pri)
		l.attrs["syslog.facility"] = facilityName(pri)
		if t, err := time.ParseInLocation("Jan _2 15:04:05", m[2], time.Local); err == nil {
			now := time.Now()
			t = t.AddDate(now.Year(), 0, 0)
			if t.After(now.Add(24 * time.Hour)) {
				t = t.AddDate(-1, 0, 0)
			}
			l.at = t
		}
		l.host, l.service = m[3], m[4]
		if m[5] != "" {
			l.attrs["syslog.procid"] = m[5]
		}
		l.message = m[6]
		return l
	}
	if strings.HasPrefix(msg, "<") {
		if end := strings.IndexByte(msg, '>'); end > 1 && end <= 4 {
			if pri, err := strconv.Atoi(msg[1:end]); err == nil {
				l.sev = syslogSev(pri)
				l.attrs["syslog.facility"] = facilityName(pri)
				msg = msg[end+1:]
			}
		}
	}
	l.message = msg
	return l
}

func facilityName(pri int) string {
	if f := pri / 8; f < len(syslogFacilities) {
		return syslogFacilities[f]
	}
	return strconv.Itoa(pri / 8)
}

func dash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}
