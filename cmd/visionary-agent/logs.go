package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Log collection, off until VISIONARY_AGENT_LOGS names sources: journald,
// docker (every container's output), and file globs (/var/log/nginx/*.log).
// Lines go to the server's log ingest in batches, gzipped NDJSON with the
// server key; the server parses, scrubs and stores them like any other
// source. Where each source got to (the journal cursor, file offsets) is kept
// in VISIONARY_AGENT_STATE, so a restart neither repeats nor loses lines; a
// source seen for the first time starts at its end.

const (
	logQueueSize   = 20000
	logBatchLines  = 1000
	logFlushEvery  = 2 * time.Second
	logMaxLine     = 64 << 10
	logPollFiles   = time.Second
	logScanSources = 15 * time.Second
)

// logEntry is one line, in the fields the ingest endpoint reads.
type logEntry struct {
	Time      string `json:"time"`
	Message   string `json:"message"`
	Level     string `json:"level,omitempty"`
	Service   string `json:"service,omitempty"`
	Host      string `json:"host"`
	Container string `json:"container,omitempty"`
	Source    string `json:"source"`
	File      string `json:"log.file,omitempty"`
	Unit      string `json:"systemd.unit,omitempty"`
	PID       string `json:"pid,omitempty"`
	Stream    string `json:"stream,omitempty"`
	Image     string `json:"container.image,omitempty"`
}

type logShipper struct {
	cfg     config
	log     *slog.Logger
	url     string
	client  *http.Client
	queue   chan *logEntry
	dropped atomic.Int64
	state   *logState
	docker  *dockerCollector
}

func newLogShipper(cfg config, logger *slog.Logger, docker *dockerCollector) *logShipper {
	return &logShipper{
		cfg: cfg, log: logger, url: cfg.url + "/api/ingest/logs",
		client: &http.Client{Timeout: 30 * time.Second},
		queue:  make(chan *logEntry, logQueueSize),
		state:  loadLogState(cfg.stateDir),
		docker: docker,
	}
}

// push queues a line; a full queue drops it rather than block a reader.
func (s *logShipper) push(e *logEntry) {
	if e.Host == "" {
		e.Host = s.cfg.host
	}
	if e.Service == "" {
		e.Service = s.cfg.service
	}
	select {
	case s.queue <- e:
	default:
		s.dropped.Add(1)
	}
}

// run starts the sources and ships until ctx ends.
func (s *logShipper) run(ctx context.Context) {
	var files []string
	for _, src := range s.cfg.logs {
		switch src {
		case "journald":
			go s.journald(ctx)
		case "docker", "containers":
			if s.docker == nil {
				s.log.Warn("logs: docker named in VISIONARY_AGENT_LOGS but VISIONARY_AGENT_DOCKER is off")
				continue
			}
			go s.containers(ctx)
		default:
			files = append(files, src)
		}
	}
	if len(files) > 0 {
		go s.tailFiles(ctx, files)
	}
	s.log.Info("logs: collecting", "sources", strings.Join(s.cfg.logs, ","))
	s.ship(ctx)
}

// ship batches the queue and posts it.
func (s *logShipper) ship(ctx context.Context) {
	t := time.NewTicker(logFlushEvery)
	defer t.Stop()
	var batch []*logEntry
	backoff := time.Duration(0)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.post(ctx, batch); err != nil {
			backoff = min(max(backoff*2, 2*time.Second), time.Minute)
			s.log.Warn("logs: send failed; will retry", "lines", len(batch), "err", err, "in", backoff.String())
			if len(batch) > logQueueSize {
				batch = batch[len(batch)-logQueueSize:]
			}
			return
		}
		backoff = 0
		batch = batch[:0]
		s.state.save()
	}
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			for len(s.queue) > 0 && len(batch) < logQueueSize {
				batch = append(batch, <-s.queue)
			}
			_ = s.post(fctx, batch)
			cancel()
			s.state.save()
			return
		case e := <-s.queue:
			batch = append(batch, e)
			if len(batch) >= logBatchLines && backoff == 0 {
				flush()
			}
		case <-t.C:
			if n := s.dropped.Swap(0); n > 0 {
				s.log.Warn("logs: queue full; lines dropped", "dropped", n)
			}
			if backoff > 0 {
				backoff -= logFlushEvery
				if backoff > 0 {
					continue
				}
				backoff = 0
			}
			flush()
		}
	}
}

func (s *logShipper) post(ctx context.Context, batch []*logEntry) error {
	if len(batch) == 0 {
		return nil
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	enc := json.NewEncoder(zw)
	for _, e := range batch {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer "+s.cfg.key)
	req.Header.Set("User-Agent", "visionary-agent/"+version)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusRequestEntityTooLarge {
			// The server will never take this batch; drop it.
			s.log.Warn("logs: batch refused", "status", resp.Status, "body", strings.TrimSpace(string(b)))
			return nil
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// ── State ─────────────────────────────────────────────────────────────────

// logState is where each source got to.
type logState struct {
	path string
	mu   sync.Mutex
	// Journal is the cursor of the last journal entry read.
	Journal string `json:"journal"`
	// Files are offsets by path, with the file's identity to notice
	// rotation.
	Files map[string]fileMark `json:"files"`
	// Containers are the time of the last line read, by container id.
	Containers map[string]string `json:"containers"`
}

type fileMark struct {
	Offset int64  `json:"offset"`
	ID     string `json:"id"`
}

func loadLogState(dir string) *logState {
	st := &logState{Files: map[string]fileMark{}, Containers: map[string]string{}}
	if dir == "" {
		return st
	}
	st.path = filepath.Join(dir, "logs.json")
	if b, err := os.ReadFile(st.path); err == nil {
		_ = json.Unmarshal(b, st)
		if st.Files == nil {
			st.Files = map[string]fileMark{}
		}
		if st.Containers == nil {
			st.Containers = map[string]string{}
		}
	}
	return st
}

func (st *logState) save() {
	if st.path == "" {
		return
	}
	st.mu.Lock()
	b, err := json.Marshal(st)
	st.mu.Unlock()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(st.path), 0o700)
	tmp := st.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, st.path)
	}
}

// ── journald ──────────────────────────────────────────────────────────────

// journald follows the journal with journalctl, from the saved cursor or
// from now.
func (s *logShipper) journald(ctx context.Context) {
	for ctx.Err() == nil {
		args := []string{"-f", "-o", "json", "--no-pager"}
		s.state.mu.Lock()
		cursor := s.state.Journal
		s.state.mu.Unlock()
		if cursor != "" {
			args = append(args, "--after-cursor", cursor)
		} else {
			args = append(args, "--since", "now")
		}
		cmd := exec.CommandContext(ctx, "journalctl", args...)
		out, err := cmd.StdoutPipe()
		if err == nil {
			err = cmd.Start()
		}
		if err != nil {
			s.log.Warn("logs: journalctl unavailable", "err", err)
			sleepCtx(ctx, time.Minute)
			continue
		}
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			e, cur, ok := journalEntry(sc.Bytes())
			if !ok {
				continue
			}
			s.push(e)
			s.state.mu.Lock()
			s.state.Journal = cur
			s.state.mu.Unlock()
		}
		_ = cmd.Wait()
		sleepCtx(ctx, 5*time.Second)
	}
}

// journalPriority maps syslog priorities (0 emerg .. 7 debug).
var journalPriority = [...]string{"fatal", "fatal", "fatal", "error", "warn", "info", "info", "debug"}

// journalEntry reads one line of journalctl -o json; it returns the entry's
// cursor too.
func journalEntry(b []byte) (*logEntry, string, bool) {
	var j map[string]any
	if json.Unmarshal(b, &j) != nil {
		return nil, "", false
	}
	str := func(k string) string {
		switch v := j[k].(type) {
		case string:
			return v
		case []any:
			// A binary MESSAGE comes as an array of bytes.
			var out []byte
			for _, x := range v {
				if f, ok := x.(float64); ok {
					out = append(out, byte(f))
				}
			}
			return string(out)
		}
		return ""
	}
	msg := strings.TrimRight(str("MESSAGE"), "\n")
	if msg == "" {
		return nil, str("__CURSOR"), false
	}
	e := &logEntry{Message: msg, Source: "journald", Unit: str("_SYSTEMD_UNIT"), PID: str("_PID"), Host: str("_HOSTNAME"), Container: str("CONTAINER_NAME")}
	if us, err := strconv.ParseInt(str("__REALTIME_TIMESTAMP"), 10, 64); err == nil {
		e.Time = time.UnixMicro(us).UTC().Format(time.RFC3339Nano)
	}
	if p, err := strconv.Atoi(str("PRIORITY")); err == nil && p >= 0 && p < len(journalPriority) {
		e.Level = journalPriority[p]
	}
	e.Service = str("SYSLOG_IDENTIFIER")
	if u := strings.TrimSuffix(e.Unit, ".service"); u != "" && e.Service == "" {
		e.Service = u
	}
	if e.Host != "" {
		e.Host = hostName(e.Host)
	}
	return e, str("__CURSOR"), true
}

// ── Files ─────────────────────────────────────────────────────────────────

// tailFiles follows the files matching globs, picking up new files and
// rotated ones.
func (s *logShipper) tailFiles(ctx context.Context, globs []string) {
	t := time.NewTicker(logPollFiles)
	defer t.Stop()
	scan := time.Time{}
	var paths []string
	for {
		if time.Since(scan) >= logScanSources {
			scan = time.Now()
			paths = paths[:0]
			for _, g := range globs {
				m, _ := filepath.Glob(g)
				paths = append(paths, m...)
			}
		}
		for _, p := range paths {
			s.readFile(p)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// readFile reads what was appended to path since the saved offset. A file
// seen for the first time starts at its end; a file whose identity changed
// (rotated) or that shrank (truncated) starts again from the beginning.
func (s *logShipper) readFile(path string) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return
	}
	id := fileID(fi)
	s.state.mu.Lock()
	mark, known := s.state.Files[path]
	s.state.mu.Unlock()
	switch {
	case !known:
		s.setMark(path, fileMark{Offset: fi.Size(), ID: id})
		return
	case mark.ID != id || fi.Size() < mark.Offset:
		mark = fileMark{ID: id}
	}
	if fi.Size() == mark.Offset {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(mark.Offset, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(io.LimitReader(f, 8<<20), 64<<10)
	read := mark.Offset
	service := fileService(path)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			// A line still being written waits for its newline.
			break
		}
		read += int64(len(line))
		text := strings.TrimRight(line, "\r\n")
		if text == "" {
			continue
		}
		if len(text) > logMaxLine {
			text = text[:logMaxLine]
		}
		s.push(&logEntry{Time: time.Now().UTC().Format(time.RFC3339Nano), Message: text, Source: "file", File: path, Service: service})
	}
	s.setMark(path, fileMark{Offset: read, ID: id})
}

func (s *logShipper) setMark(path string, m fileMark) {
	s.state.mu.Lock()
	s.state.Files[path] = m
	s.state.mu.Unlock()
}

// fileService names lines by their file: /var/log/nginx/access.log is
// nginx, /srv/app/logs/app.log is app.
func fileService(path string) string {
	dir := filepath.Base(filepath.Dir(path))
	if dir == "log" || dir == "logs" || dir == "." || dir == "/" {
		base := filepath.Base(path)
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	return dir
}

// ── Containers ────────────────────────────────────────────────────────────

// containers follows the output of every running container, starting a
// stream for each new one and letting finished ones end.
func (s *logShipper) containers(ctx context.Context) {
	var mu sync.Mutex
	following := map[string]bool{}
	for {
		if s.docker.socket() != "" {
			list, err := s.docker.list(ctx, false)
			if err == nil {
				for _, c := range list {
					mu.Lock()
					on := following[c.ID]
					following[c.ID] = true
					mu.Unlock()
					if on {
						continue
					}
					go func(c listed) {
						s.followContainer(ctx, c)
						mu.Lock()
						delete(following, c.ID)
						mu.Unlock()
					}(c)
				}
			}
		}
		if !sleepCtx(ctx, logScanSources) {
			return
		}
	}
}

// containerService names a container's lines: its compose service or
// Kubernetes container label, else its name.
func containerService(c listed) string {
	for _, k := range []string{"com.docker.compose.service", "io.kubernetes.container.name", "com.docker.swarm.service.name"} {
		if v := c.Labels[k]; v != "" {
			return v
		}
	}
	return c.name()
}

func (s *logShipper) followContainer(ctx context.Context, c listed) {
	var info struct {
		Config struct {
			Tty bool `json:"Tty"`
		} `json:"Config"`
	}
	if err := s.docker.get(ctx, "/containers/"+c.ID+"/json", &info); err != nil {
		return
	}
	s.state.mu.Lock()
	since := s.state.Containers[c.ID]
	s.state.mu.Unlock()
	q := "follow=1&stdout=1&stderr=1&timestamps=1&tail=0"
	if since != "" {
		if t, err := time.Parse(time.RFC3339Nano, since); err == nil {
			q = fmt.Sprintf("follow=1&stdout=1&stderr=1&timestamps=1&since=%d.%09d", t.Unix(), t.Nanosecond()+1)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://engine/containers/"+c.ID+"/logs?"+q, nil)
	if err != nil {
		return
	}
	resp, err := s.docker.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	name, service := c.name(), containerService(c)
	emit := func(stream, line string) {
		at, text := splitDockerTime(line)
		if text == "" {
			return
		}
		e := &logEntry{Time: at, Message: text, Source: "docker", Container: name, Service: service, Stream: stream, Image: c.Image}
		s.push(e)
		if at != "" {
			s.state.mu.Lock()
			s.state.Containers[c.ID] = at
			s.state.mu.Unlock()
		}
	}
	if info.Config.Tty {
		readLines(resp.Body, func(l string) { emit("stdout", l) })
		return
	}
	_ = demuxDocker(resp.Body, emit)
}

// demuxDocker reads the engine's multiplexed log stream: frames of an
// 8-byte header (stream type, 3 zero bytes, big-endian length) and payload.
// Lines can span frames.
func demuxDocker(r io.Reader, emit func(stream, line string)) error {
	hdr := make([]byte, 8)
	partial := map[string]string{}
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			for st, p := range partial {
				if p != "" {
					emit(st, p)
				}
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		if n > 16<<20 {
			return fmt.Errorf("frame of %d bytes", n)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return err
		}
		stream := "stdout"
		if hdr[0] == 2 {
			stream = "stderr"
		}
		text := partial[stream] + string(payload)
		lines := strings.Split(text, "\n")
		for _, l := range lines[:len(lines)-1] {
			emit(stream, strings.TrimRight(l, "\r"))
		}
		partial[stream] = lines[len(lines)-1]
		if len(partial[stream]) > logMaxLine {
			emit(stream, partial[stream][:logMaxLine])
			partial[stream] = ""
		}
	}
}

func readLines(r io.Reader, emit func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), logMaxLine)
	for sc.Scan() {
		emit(strings.TrimRight(sc.Text(), "\r"))
	}
}

// splitDockerTime separates the RFC 3339 timestamp the engine puts before
// each line (timestamps=1).
func splitDockerTime(line string) (string, string) {
	at, rest, ok := strings.Cut(line, " ")
	if ok {
		if _, err := time.Parse(time.RFC3339Nano, at); err == nil {
			return at, rest
		}
	}
	return "", line
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
