package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

// The pipeline every line the server receives goes through (OTLP, the
// ingest endpoints, syslog, the host agent), before it is stored in
// app_logs: lines the SDKs write themselves already followed the same rules
// in the SDK.
//
//  1. Parse: a message that is a JSON object or logfmt, or matches an access
//     log or a parse rule of Settings → Logs, gives its fields to the line.
//  2. Level: the source's own, else one stated in the text, else info.
//  3. Settings → Logs: drop rules and the level for the service.
//  4. Secrets scrubbed, fields encoded and capped (logrules).
//  5. The per-service cap (lines per minute); past it lines are dropped and
//     one warn line says how many.

// logLine is one line received by the server.
type logLine struct {
	at                                            time.Time
	service, instance, version, source, host, ctr string
	sev                                           int
	logger, message                               string
	attrs                                         map[string]any
	requestID, traceID, spanID, userID, sessionID string
	file                                          string
	line                                          int
	fn                                            string
}

var logsTable = &otlpTable{"app_logs", []string{
	"created_at", "service", "instance", "version", "source", "host", "container", "level", "severity", "logger",
	"message", "attrs", "request_id", "trace_id", "span_id", "user_id", "session_id", "code_file", "code_line",
	"code_func", "size",
}}

// logServerSettings are Settings → Logs options only the server uses,
// stored as logserver.* (the SDKs read logs.* only).
type logServerSettings struct {
	// Rate caps lines per minute per service across server-side sources;
	// 0 is no cap.
	Rate int `json:"rate"`
	// Redact are extra field names whose values are never stored.
	Redact []string `json:"redact"`
	// Rules parse plain-text lines into fields.
	Rules []parseRule `json:"rules"`
	// Syslog is the listen address of the syslog receiver ("" is off).
	Syslog string `json:"syslog"`
	// SyslogService names lines whose sender gives no app name.
	SyslogService string `json:"syslog_service"`
}

const (
	settingLogServer   = "logserver.config"
	defaultLogRate     = 120000
	maxIngestLogFields = 200
)

func defaultLogServerSettings() logServerSettings {
	return logServerSettings{Rate: defaultLogRate, Redact: []string{}, Rules: []parseRule{}}
}

// logRules is Settings → Logs as the SDKs read it.
func (s *Server) logRules() *logrules.Settings {
	if l := s.live().Logs; l != nil {
		return l
	}
	return logrules.Defaults(logrules.Info)
}

func (s *Server) logServer() logServerSettings {
	if l := s.live().LogServer; l != nil {
		return *l
	}
	return defaultLogServerSettings()
}

// parseLogSettings reads logs.* and logserver.* from the stored settings.
func parseLogSettings(m map[string][2]string) (*logrules.Settings, *logServerSettings) {
	var rows [][]string
	for name, v := range m {
		if strings.HasPrefix(name, "logs.") {
			rows = append(rows, []string{name, v[0]})
		}
	}
	ls := defaultLogServerSettings()
	if v := m[settingLogServer]; v[0] != "" {
		var stored logServerSettings
		if json.Unmarshal([]byte(v[0]), &stored) == nil {
			ls = stored
			if ls.Redact == nil {
				ls.Redact = []string{}
			}
			if ls.Rules == nil {
				ls.Rules = []parseRule{}
			}
			for i := range ls.Rules {
				_ = ls.Rules[i].compile()
			}
		}
	}
	return logrules.Parse(rows, logrules.Info), &ls
}

// logIngest keeps the per-service minute counters.
type logIngest struct {
	mu      sync.Mutex
	minute  int64
	counts  map[string]int
	dropped map[string]int
}

var logIngests sync.Map // *Server → *logIngest

func (s *Server) logIngestState() *logIngest {
	v, _ := logIngests.LoadOrStore(s, &logIngest{counts: map[string]int{}, dropped: map[string]int{}})
	return v.(*logIngest)
}

// ingestLogLines stores lines through the pipeline. It returns how many
// were kept; an error means the database refused them.
func (s *Server) ingestLogLines(ctx context.Context, lines []*logLine) (int, error) {
	if len(lines) == 0 {
		return 0, nil
	}
	rules, conf := s.logRules(), s.logServer()
	if !rules.Enabled {
		return 0, nil
	}
	secret := logrules.SecretKeys(conf.Redact)
	now := time.Now()
	rows := &otlpRows{}
	kept := 0
	st := s.logIngestState()
	var notes []*logLine
	st.mu.Lock()
	if minute := now.Unix() / 60; minute != st.minute {
		for svc, n := range st.dropped {
			notes = append(notes, &logLine{at: now, service: svc, source: "visionary", sev: logrules.Warn, logger: "visionary",
				message: fmt.Sprintf("visionary: %d log lines dropped over the cap of %d a minute (Settings → Logs)", n, conf.Rate)})
		}
		st.minute, st.counts, st.dropped = minute, map[string]int{}, map[string]int{}
	}
	st.mu.Unlock()
	for _, l := range lines {
		normalizeLogLine(l, conf.Rules, now)
		if rules.Dropped(l.service, l.logger, l.message, l.sev) || l.sev < rules.Floor(l.service, "", "", now) {
			continue
		}
		if conf.Rate > 0 {
			st.mu.Lock()
			st.counts[l.service]++
			n := st.counts[l.service]
			over := n > conf.Rate && (l.sev < logrules.Error || n > 10*conf.Rate)
			if over {
				st.dropped[l.service]++
			}
			st.mu.Unlock()
			if over {
				continue
			}
		}
		rows.add(logsTable, logRow(l, secret))
		kept++
	}
	for _, n := range notes {
		rows.add(logsTable, logRow(n, secret))
	}
	if rows.empty() {
		return kept, nil
	}
	return kept, s.otlp().write(ctx, rows)
}

// logRow is the line in logsTable's column order.
func logRow(l *logLine, secret map[string]bool) []any {
	msg := logrules.Truncate(logrules.Scrub(l.message), logrules.MaxMessage)
	attrs := ""
	if len(l.attrs) > 0 {
		attrs = logrules.EncodeAttrs(l.attrs, secret)
	}
	var line any
	if l.line > 0 {
		line = l.line
	}
	return []any{
		l.at.UTC().Format(tsLayout), otlpNullStr(clip(l.service, 255)), otlpNullStr(clip(l.instance, 255)),
		otlpNullStr(clip(l.version, 255)), clip(l.source, 32), otlpNullStr(clip(l.host, 255)), otlpNullStr(clip(l.ctr, 255)),
		logrules.Name(l.sev), l.sev, otlpNullStr(clip(l.logger, 255)), msg, otlpNullStr(attrs),
		otlpNullStr(clip(l.requestID, 255)), otlpNullStr(clip(l.traceID, 64)), otlpNullStr(clip(l.spanID, 32)),
		otlpNullStr(clip(l.userID, 255)), otlpNullStr(clip(l.sessionID, 255)), otlpNullStr(clip(l.file, 512)), line,
		otlpNullStr(clip(l.fn, 255)), len(msg) + len(attrs),
	}
}

// normalizeLogLine parses the message and settles time and level.
func normalizeLogLine(l *logLine, rules []parseRule, now time.Time) {
	if l.attrs == nil {
		l.attrs = map[string]any{}
	}
	l.message = strings.TrimRight(l.message, "\r\n")
	switch {
	case parseJSONMessage(l):
	case applyParseRules(l, rules):
	case parseAccessLog(l):
	case parseLogfmt(l):
	}
	if l.sev == 0 {
		if sev := logrules.TextLevel(l.message); sev > 0 {
			l.sev, l.message = sev, logrules.StripLevelWord(l.message, sev)
		}
	}
	if l.sev == 0 {
		l.sev = logrules.Info
	}
	l.sev = logrules.Clamp(l.sev)
	if l.at.IsZero() || l.at.After(now.Add(time.Hour)) {
		l.at = now
	}
	if l.source == "" {
		l.source = "http"
	}
	if len(l.attrs) > maxIngestLogFields {
		n := 0
		for k := range l.attrs {
			if n++; n > maxIngestLogFields {
				delete(l.attrs, k)
			}
		}
	}
}

// takeLogFields moves well-known fields of a structured line to the line's
// columns and returns what is left for attrs. Keys follow the common
// shippers: ECS (Elastic), OpenTelemetry, Loki labels, Bunyan/pino, logrus,
// zap, Serilog.
func takeLogFields(l *logLine, fields map[string]any) {
	for k, v := range fields {
		s := fieldString(v)
		switch strings.ToLower(k) {
		case "message", "msg", "log", "@m", "@mt", "short_message":
			if l.message == "" || l.message == originalOf(l) {
				l.message = s
				delete(fields, k)
			}
		case "level", "lvl", "severity", "loglevel", "log.level", "levelname", "level_name", "@l", "severity_text", "detected_level":
			if n := levelOf(v); n > 0 {
				l.sev = n
				delete(fields, k)
			}
		case "time", "ts", "timestamp", "@timestamp", "@t", "datetime", "time_unix_nano":
			if t, ok := timeOf(v); ok {
				l.at = t
				delete(fields, k)
			}
		case "service", "service.name", "service_name", "app", "application", "job":
			if l.service == "" && s != "" {
				l.service = s
				delete(fields, k)
			}
		case "version", "service.version", "release":
			if l.version == "" && s != "" {
				l.version = s
				delete(fields, k)
			}
		case "host", "hostname", "host.name", "host.hostname":
			if l.host == "" && s != "" {
				l.host = s
				delete(fields, k)
			}
		case "container", "container.name", "container_name":
			if l.ctr == "" && s != "" {
				l.ctr = s
				delete(fields, k)
			}
		case "logger", "logger_name", "log.logger", "sourcecontext":
			if l.logger == "" && s != "" {
				l.logger = s
				delete(fields, k)
			}
		case "request_id", "requestid", "request.id", "http.request.id", "x-request-id", "req_id":
			if l.requestID == "" && s != "" {
				l.requestID = s
				delete(fields, k)
			}
		case "trace_id", "traceid", "trace.id", "dd.trace_id":
			if l.traceID == "" && s != "" {
				l.traceID = s
				delete(fields, k)
			}
		case "span_id", "spanid", "span.id", "dd.span_id":
			if l.spanID == "" && s != "" {
				l.spanID = s
				delete(fields, k)
			}
		case "user_id", "userid", "user.id", "enduser.id":
			if l.userID == "" && s != "" {
				l.userID = s
				delete(fields, k)
			}
		case "session_id", "sessionid", "session.id":
			if l.sessionID == "" && s != "" {
				l.sessionID = s
				delete(fields, k)
			}
		case "caller", "log.origin.file.name", "code.filepath":
			if l.file == "" && s != "" {
				file, line, ok := strings.Cut(s, ":")
				l.file = file
				if ok {
					l.line, _ = strconv.Atoi(line)
				}
				delete(fields, k)
			}
		case "log.origin.file.line", "code.lineno", "lineno":
			if n, err := strconv.Atoi(s); err == nil && l.line == 0 {
				l.line = n
				delete(fields, k)
			}
		case "log.origin.function", "code.function", "funcname":
			if l.fn == "" && s != "" {
				l.fn = s
				delete(fields, k)
			}
		}
	}
	for k, v := range fields {
		l.attrs[k] = v
	}
}

// originalOf is the raw text a structured message replaced; set while
// parsing, so "message" inside the JSON wins over the JSON text itself.
func originalOf(l *logLine) string {
	if s, ok := l.attrs[rawKey].(string); ok {
		return s
	}
	return ""
}

const rawKey = "\x00raw"

// parseJSONMessage reads a message that is one JSON object.
func parseJSONMessage(l *logLine) bool {
	m := strings.TrimSpace(l.message)
	if len(m) < 2 || m[0] != '{' || m[len(m)-1] != '}' {
		return false
	}
	var obj map[string]any
	dec := json.NewDecoder(strings.NewReader(m))
	dec.UseNumber()
	if dec.Decode(&obj) != nil {
		return false
	}
	fields := map[string]any{}
	flatten(fields, "", obj)
	l.attrs[rawKey] = l.message
	takeLogFields(l, fields)
	delete(l.attrs, rawKey)
	if l.message == m {
		// No message field: keep the line readable rather than repeating
		// the JSON that is now in attrs.
		l.message = summarizeFields(fields)
	}
	return true
}

// summarizeFields is a short text for a structured line with no message.
func summarizeFields(fields map[string]any) string {
	var parts []string
	for k, v := range fields {
		parts = append(parts, k+"="+fieldString(v))
		if len(parts) == 6 {
			break
		}
	}
	return strings.Join(parts, " ")
}

// flatten turns nested objects into dotted keys; arrays stay JSON text.
func flatten(out map[string]any, prefix string, v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			flatten(out, prefix+k+".", x)
		}
		if len(t) == 0 && prefix != "" {
			out[strings.TrimSuffix(prefix, ".")] = "{}"
		}
	default:
		key := strings.TrimSuffix(prefix, ".")
		switch x := t.(type) {
		case json.Number:
			if n, err := x.Int64(); err == nil {
				out[key] = n
			} else if f, err := x.Float64(); err == nil {
				out[key] = f
			} else {
				out[key] = x.String()
			}
		case []any:
			b, _ := json.Marshal(x)
			out[key] = string(b)
		case nil:
		default:
			out[key] = x
		}
	}
}

func fieldString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case bool:
		return strconv.FormatBool(t)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// levelOf reads a level name, or a number on the common scales: 1..6,
// bunyan/pino 10..60, syslog 0..7 is not guessed (ambiguous with 1..6).
func levelOf(v any) int {
	s := strings.TrimSpace(fieldString(v))
	if n := logrules.ParseLevel(s); n > 0 {
		return n
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	switch {
	case f >= 10 && f <= 60:
		return logrules.Clamp(int(f) / 10)
	case f >= 1 && f <= 6:
		return int(f)
	}
	return 0
}

// timeOf reads RFC 3339 text, Visionary's format, or epoch seconds,
// milliseconds, microseconds or nanoseconds.
func timeOf(v any) (time.Time, bool) {
	s := strings.TrimSpace(fieldString(v))
	if s == "" {
		return time.Time{}, false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		switch {
		case f > 1e17:
			return time.Unix(0, int64(f)), true
		case f > 1e14:
			return time.UnixMicro(int64(f)), true
		case f > 1e11:
			return time.UnixMilli(int64(f)), true
		case f > 1e8:
			sec := int64(f)
			return time.Unix(sec, int64((f-float64(sec))*1e9)), true
		}
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, tsLayout, "2006-01-02 15:04:05.000", "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006/01/02 15:04:05", time.RFC1123Z, "02/Jan/2006:15:04:05 -0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// logfmtPair finds key=value pairs, quoted or bare.
var logfmtPair = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.\-]*)=("(?:[^"\\]|\\.)*"|[^\s]*)`)

// parseLogfmt reads "time=... level=info msg=\"hello\" k=v" when the line
// is mostly pairs and has a msg or level.
func parseLogfmt(l *logLine) bool {
	m := l.message
	if !strings.Contains(m, "=") {
		return false
	}
	pairs := logfmtPair.FindAllStringSubmatchIndex(m, -1)
	if len(pairs) < 2 {
		return false
	}
	covered := 0
	fields := map[string]any{}
	for _, p := range pairs {
		covered += p[1] - p[0]
		k, v := m[p[2]:p[3]], m[p[4]:p[5]]
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		}
		fields[k] = v
	}
	_, hasMsg := fields["msg"]
	_, hasLevel := fields["level"]
	if (!hasMsg && !hasLevel) || covered*10 < len(strings.TrimSpace(m))*7 {
		return false
	}
	l.attrs[rawKey] = l.message
	takeLogFields(l, fields)
	delete(l.attrs, rawKey)
	if l.message == m {
		l.message = summarizeFields(fields)
	}
	return true
}

// accessLog matches the combined format nginx and Apache write.
var accessLog = regexp.MustCompile(`^(\S+) \S+ (\S+) \[([^\]]+)\] "(\S+) (\S+)(?: (\S+))?" (\d{3}) (\d+|-)(?: "([^"]*)" "([^"]*)")?`)

func parseAccessLog(l *logLine) bool {
	m := accessLog.FindStringSubmatch(l.message)
	if m == nil {
		return false
	}
	status, _ := strconv.Atoi(m[7])
	l.attrs["client.ip"] = m[1]
	if m[2] != "-" {
		l.attrs["user"] = m[2]
	}
	l.attrs["http.method"], l.attrs["http.path"], l.attrs["http.status"] = m[4], m[5], status
	if m[8] != "-" {
		n, _ := strconv.Atoi(m[8])
		l.attrs["http.bytes"] = n
	}
	if m[9] != "" && m[9] != "-" {
		l.attrs["http.referer"] = m[9]
	}
	if m[10] != "" && m[10] != "-" {
		l.attrs["user_agent"] = m[10]
	}
	if t, err := time.Parse("02/Jan/2006:15:04:05 -0700", m[3]); err == nil && l.at.IsZero() {
		l.at = t
	}
	if l.sev == 0 {
		switch {
		case status >= 500:
			l.sev = logrules.Error
		case status >= 400:
			l.sev = logrules.Warn
		default:
			l.sev = logrules.Info
		}
	}
	return true
}

// parseRule turns plain-text lines into fields: a regular expression with
// named groups, or a pattern with %{TYPE:name} placeholders (grok's
// notation). Groups named message, level and time set those; the rest
// become fields. Service and source, when set, limit the rule to those.
type parseRule struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	Source  string `json:"source"`
	Pattern string `json:"pattern"`

	re *regexp.Regexp
}

// grokTypes are the placeholder types a parse rule can use.
var grokTypes = map[string]string{
	"WORD":              `\w+`,
	"NOTSPACE":          `\S+`,
	"DATA":              `.*?`,
	"GREEDYDATA":        `.*`,
	"INT":               `[+-]?\d+`,
	"NUMBER":            `[+-]?(?:\d+(?:\.\d*)?|\.\d+)`,
	"IP":                `(?:\d{1,3}(?:\.\d{1,3}){3}|[0-9A-Fa-f:]*:[0-9A-Fa-f:.]+)`,
	"UUID":              `[0-9A-Fa-f]{8}-(?:[0-9A-Fa-f]{4}-){3}[0-9A-Fa-f]{12}`,
	"LOGLEVEL":          `(?i:trace|debug|info|notice|warn(?:ing)?|error|err|crit(?:ical)?|fatal|panic|alert|emerg)`,
	"TIMESTAMP_ISO8601": `\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?`,
	"PATH":              `/\S*`,
	"URIPATH":           `/[^\s?#]*`,
	"QUOTEDSTRING":      `"(?:[^"\\]|\\.)*"`,
	"HOSTNAME":          `[A-Za-z0-9.-]+`,
}

var grokToken = regexp.MustCompile(`%\{([A-Z0-9_]+)(?::([A-Za-z_][A-Za-z0-9_.]*))?\}`)

// compile builds the rule's expression; an error names what is wrong.
func (r *parseRule) compile() error {
	src := r.Pattern
	if strings.Contains(src, "%{") {
		var bad string
		src = grokToken.ReplaceAllStringFunc(src, func(tok string) string {
			m := grokToken.FindStringSubmatch(tok)
			re, ok := grokTypes[m[1]]
			if !ok {
				bad = m[1]
				return tok
			}
			if m[2] == "" {
				return "(?:" + re + ")"
			}
			return "(?P<" + strings.ReplaceAll(m[2], ".", "__") + ">" + re + ")"
		})
		if bad != "" {
			return fmt.Errorf("unknown placeholder %%{%s}", bad)
		}
	}
	re, err := regexp.Compile(src)
	if err != nil {
		return fmt.Errorf("the pattern is not a valid expression: %v", err)
	}
	if len(re.SubexpNames()) <= 1 {
		return fmt.Errorf("the pattern has no named fields")
	}
	r.re = re
	return nil
}

// applyParseRules applies the first rule that matches the line.
func applyParseRules(l *logLine, rules []parseRule) bool {
	for i := range rules {
		r := &rules[i]
		if r.re == nil || (r.Service != "" && r.Service != l.service) || (r.Source != "" && r.Source != l.source) {
			continue
		}
		m := r.re.FindStringSubmatch(l.message)
		if m == nil {
			continue
		}
		fields := map[string]any{}
		msg := ""
		for i, name := range r.re.SubexpNames() {
			if name == "" || i >= len(m) {
				continue
			}
			name = strings.ReplaceAll(name, "__", ".")
			if name == "message" || name == "msg" {
				msg = m[i]
				continue
			}
			if n, err := strconv.ParseInt(m[i], 10, 64); err == nil {
				fields[name] = n
			} else {
				fields[name] = m[i]
			}
		}
		takeLogFields(l, fields)
		if msg != "" {
			l.message = msg
		}
		if r.Name != "" {
			l.attrs["parsed_by"] = r.Name
		}
		return true
	}
	return false
}
