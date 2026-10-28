package analytics

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The _bulk and Loki endpoints let Filebeat, Fluent Bit, Vector, Logstash and
// Promtail ship here with their stock Elasticsearch or Loki outputs.

func (s *Server) applogsIngestRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/ingest/logs", s.logIngestHandler(s.ingestNative))
	mux.HandleFunc("POST /api/ingest/logs/_bulk", s.logIngestHandler(s.ingestBulk))
	mux.HandleFunc("POST /api/ingest/logs/{index}/_bulk", s.logIngestHandler(s.ingestBulk))
	mux.HandleFunc("GET /api/ingest/logs", s.handleBulkInfo)
	mux.HandleFunc("HEAD /api/ingest/logs", s.handleBulkInfo)
	mux.HandleFunc("POST /loki/api/v1/push", s.logIngestHandler(s.ingestLoki))
}

func logIngestKey(r *http.Request) string {
	if k := otlpKey(r); k != "" {
		return k
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "apikey ") {
		k := strings.TrimSpace(auth[7:])
		// Elasticsearch API keys are base64("id:key"); accept either form.
		if b, err := base64.StdEncoding.DecodeString(k); err == nil {
			if _, key, ok := strings.Cut(string(b), ":"); ok {
				return key
			}
		}
		return k
	}
	if _, pass, ok := r.BasicAuth(); ok && pass != "" {
		return pass
	}
	return strings.TrimSpace(r.URL.Query().Get("key"))
}

type logIngestFunc func(w http.ResponseWriter, r *http.Request, body []byte) ([]*logLine, func(kept int), error)

// errLogBadBody means the body can't be parsed; senders shouldn't retry it.
var errLogBadBody = errors.New("bad body")

func (s *Server) logIngestHandler(parse logIngestFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ingestKeyValid(logIngestKey(r), "server") {
			writeError(w, http.StatusUnauthorized, "missing or invalid ingest key")
			return
		}
		o := s.otlp()
		select {
		case o.inflight <- struct{}{}:
			defer func() { <-o.inflight }()
		default:
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "too many concurrent requests")
			return
		}
		syncLimit(o.limiter, o.s.confInt("otlp_rate"))
		if !o.limiter.Allow() {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate exceeded")
			return
		}
		body, status, err := otlpReadBody(w, r)
		if err != nil {
			writeError(w, status, err.Error())
			return
		}
		lines, answer, err := parse(w, r, body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		q := r.URL.Query()
		for _, l := range lines {
			if l.service == "" {
				l.service = q.Get("service")
			}
			if l.source == "" {
				l.source = q.Get("source")
			}
			if l.host == "" {
				l.host = q.Get("host")
			}
		}
		kept, err := s.ingestLogLines(r.Context(), lines)
		if err != nil {
			s.logger.Warn("log ingest write", "path", r.URL.Path, "err", err)
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusServiceUnavailable, "the database is unavailable; retry later")
			return
		}
		answer(kept)
	}
}

func (s *Server) ingestNative(w http.ResponseWriter, r *http.Request, body []byte) ([]*logLine, func(int), error) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	trimmed := bytes.TrimSpace(body)
	var lines []*logLine
	switch {
	case len(trimmed) == 0:
	case ct == "application/json" && trimmed[0] == '[':
		var objs []map[string]any
		if err := decodeNumbers(trimmed, &objs); err != nil {
			return nil, nil, errors.New("the body is not a JSON array of objects")
		}
		for _, o := range objs {
			lines = append(lines, lineFromObject(o, "http"))
		}
	case ct == "application/json" && trimmed[0] == '{':
		var env struct {
			Logs []map[string]any `json:"logs"`
		}
		var obj map[string]any
		if err := decodeNumbers(trimmed, &obj); err != nil {
			return nil, nil, errors.New("the body is not valid JSON")
		}
		if raw, ok := obj["logs"]; ok && len(obj) == 1 {
			if b, err := json.Marshal(raw); err == nil && decodeNumbers(b, &env.Logs) == nil {
				for _, o := range env.Logs {
					lines = append(lines, lineFromObject(o, "http"))
				}
				break
			}
		}
		lines = append(lines, lineFromObject(obj, "http"))
	default:
		sc := bufio.NewScanner(bytes.NewReader(trimmed))
		sc.Buffer(make([]byte, 64<<10), otlpMaxBody)
		for sc.Scan() {
			text := strings.TrimRight(sc.Text(), "\r")
			if strings.TrimSpace(text) == "" {
				continue
			}
			var obj map[string]any
			if strings.HasPrefix(strings.TrimSpace(text), "{") && decodeNumbers([]byte(text), &obj) == nil {
				lines = append(lines, lineFromObject(obj, "http"))
				continue
			}
			lines = append(lines, &logLine{message: text, source: "http"})
		}
	}
	return lines, func(kept int) {
		writeJSON(w, http.StatusOK, map[string]int{"accepted": kept, "dropped": len(lines) - kept})
	}, nil
}

func decodeNumbers(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}

func lineFromObject(obj map[string]any, source string) *logLine {
	l := &logLine{source: source, attrs: map[string]any{}}
	fields := map[string]any{}
	flatten(fields, "", obj)
	if s, ok := fields["source"].(string); ok && s != "" {
		l.source = s
		delete(fields, "source")
	}
	takeLogFields(l, fields)
	if l.message == "" {
		l.message = summarizeFields(l.attrs)
	}
	return l
}

// ingestBulk acknowledges delete and update actions but ignores them.
func (s *Server) ingestBulk(w http.ResponseWriter, r *http.Request, body []byte) ([]*logLine, func(int), error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64<<10), otlpMaxBody)
	var lines []*logLine
	var items []map[string]any
	expectDoc, op := false, ""
	for sc.Scan() {
		text := bytes.TrimSpace(sc.Bytes())
		if len(text) == 0 {
			continue
		}
		var obj map[string]any
		if err := decodeNumbers(text, &obj); err != nil {
			return nil, nil, errors.New("the bulk body has a line that is not JSON")
		}
		if !expectDoc {
			for k := range obj {
				op = k
			}
			switch op {
			case "index", "create":
				expectDoc = true
			case "update":
				expectDoc = true
			case "delete":
				items = append(items, map[string]any{op: map[string]any{"status": 200, "result": "not_found"}})
			default:
				return nil, nil, errors.New("unknown bulk action " + strconv.Quote(op))
			}
			continue
		}
		expectDoc = false
		if op == "update" {
			items = append(items, map[string]any{op: map[string]any{"status": 200, "result": "noop"}})
			continue
		}
		lines = append(lines, lineFromObject(obj, "bulk"))
		items = append(items, map[string]any{op: map[string]any{"status": 201, "result": "created", "_index": r.PathValue("index")}})
	}
	return lines, func(int) {
		writeJSON(w, http.StatusOK, map[string]any{"took": 0, "errors": false, "items": items})
	}, nil
}

// handleBulkInfo answers the version check Elasticsearch clients make first.
func (s *Server) handleBulkInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Elastic-Product", "Elasticsearch")
	writeJSON(w, http.StatusOK, map[string]any{
		"name":         "visionary",
		"cluster_name": "visionary",
		"version":      map[string]any{"number": "8.11.0", "build_flavor": "default", "lucene_version": "9.8.0"},
		"tagline":      "Logs stored by Visionary",
	})
}

func (s *Server) ingestLoki(w http.ResponseWriter, r *http.Request, body []byte) ([]*logLine, func(int), error) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "application/x-protobuf" {
		return nil, nil, errors.New("send Loki pushes as JSON (Content-Type: application/json); protobuf is not supported")
	}
	var push struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][]any           `json:"values"`
		} `json:"streams"`
	}
	if err := decodeNumbers(body, &push); err != nil {
		return nil, nil, errors.New("the body is not a Loki JSON push")
	}
	var lines []*logLine
	for _, st := range push.Streams {
		for _, v := range st.Values {
			if len(v) < 2 {
				continue
			}
			l := &logLine{source: "loki", attrs: map[string]any{}}
			fields := map[string]any{}
			for k, x := range st.Stream {
				fields[k] = x
			}
			if len(v) > 2 {
				if meta, ok := v[2].(map[string]any); ok {
					flatten(fields, "", meta)
				}
			}
			l.message = fieldString(v[1])
			if ns, err := strconv.ParseInt(fieldString(v[0]), 10, 64); err == nil {
				l.at = time.Unix(0, ns)
			}
			takeLogFields(l, fields)
			lines = append(lines, l)
		}
	}
	return lines, func(int) { w.WriteHeader(http.StatusNoContent) }, nil
}
