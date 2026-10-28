package analytics

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/time/rate"
)

const (
	maxClientBody   = 64 << 10
	maxClientEvents = 100
	// clientClockSkew bounds how far an event's own timestamp may be from
	// the time the server received it.
	clientClockSkew = 24 * time.Hour
)

// handleClientPreflight answers the browser's CORS preflight. Only allowed
// origins get the Access-Control headers; anyone else's browser refuses the
// real request on its own.
func (s *Server) handleClientPreflight(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" || !s.rum().originAllowed(origin) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	h := w.Header()
	setCORS(h, origin)
	h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	h.Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
}

func setCORS(h http.Header, origin string) {
	h.Set("Access-Control-Allow-Origin", origin)
	h.Add("Vary", "Origin")
}

// handleClientIngest takes a batch from a browser or mobile SDK. Answers:
// 204 stored (or queued), 400 bad payload, 401 bad key, 403 origin not
// allowed, 413 over 64 KB, 429 too many requests, 503 queue full.
func (s *Server) handleClientIngest(w http.ResponseWriter, r *http.Request) {
	st := s.rum()
	if origin := r.Header.Get("Origin"); origin != "" {
		if !st.originAllowed(origin) {
			writeError(w, http.StatusForbidden, "origin not allowed")
			return
		}
		setCORS(w.Header(), origin)
	}
	// Per-IP first, so guessing keys is throttled too.
	if !st.limits.allowIP(s.auth.clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	key := clientKey(r)
	if !s.ingestKeyValid(key, "client") {
		writeError(w, http.StatusUnauthorized, "invalid client key")
		return
	}
	if !st.limits.allowKey(key) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || (mt != "application/json" && mt != "text/plain") {
		writeError(w, http.StatusBadRequest, "expected application/json or text/plain")
		return
	}
	if r.ContentLength > maxClientBody {
		writeError(w, http.StatusRequestEntityTooLarge, "payload over 64 KB")
		return
	}
	body, err := readClientBody(w, r)
	if err != nil {
		if errors.Is(err, errClientTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload over 64 KB")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	rows, err := parseClientBatch(body, now, clientCountry(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Log lines go to app_logs with the server's other logs.
	rows, lines := clientLogLines(rows)
	s.ingestClientLogs(lines)
	if len(rows) > 0 && !st.writer.enqueue(rows) {
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, "ingest queue full")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errClientTooLarge = errors.New("payload over 64 KB")

// readClientBody reads at most 64 KB, after gzip decompression when the body
// is compressed. Both the compressed and the decompressed reads are capped,
// so a small body that inflates hugely (a decompression bomb) stops at the
// limit instead of filling memory.
func readClientBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	var src io.Reader = http.MaxBytesReader(w, r.Body, maxClientBody)
	switch enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
	case "gzip", "x-gzip":
		gz, err := gzip.NewReader(src)
		if err != nil {
			return nil, tooLargeOr(err, "invalid gzip body")
		}
		defer gz.Close()
		src = gz
	default:
		return nil, errors.New("unsupported Content-Encoding " + enc)
	}
	body, err := io.ReadAll(io.LimitReader(src, maxClientBody+1))
	if err != nil {
		return nil, tooLargeOr(err, "could not read body")
	}
	if len(body) > maxClientBody {
		return nil, errClientTooLarge
	}
	return body, nil
}

func tooLargeOr(err error, msg string) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return errClientTooLarge
	}
	return errors.New(msg)
}

// clientKey reads the key from Authorization or, for sendBeacon and other
// clients that cannot set headers, from ?key=.
func clientKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		scheme, token, ok := strings.Cut(auth, " ")
		if ok && strings.EqualFold(scheme, "Bearer") {
			return strings.TrimSpace(token)
		}
		return ""
	}
	return r.URL.Query().Get("key")
}

// rumOrigins is the allowed browser origins setting, raw and parsed.
type rumOrigins struct {
	raw  string
	list []string // empty allows any
}

// allowedOrigins follows Settings → Server, parsing only when it changed.
func (st *rumState) allowedOrigins() []string {
	raw := st.s.conf("client_origins")
	if p := st.origins.Load(); p != nil && p.raw == raw {
		return p.list
	}
	p := &rumOrigins{raw: raw, list: parseOrigins(raw)}
	st.origins.Store(p)
	return p.list
}

func (st *rumState) originAllowed(origin string) bool {
	origins := st.allowedOrigins()
	if len(origins) == 0 {
		return true
	}
	origin = strings.ToLower(strings.TrimRight(origin, "/"))
	for _, o := range origins {
		if o == "*" || o == origin {
			return true
		}
		// "https://*.example.com" allows any subdomain over that scheme.
		if scheme, host, ok := strings.Cut(o, "://*."); ok && strings.HasPrefix(origin, scheme+"://") && strings.HasSuffix(origin, "."+host) {
			return true
		}
	}
	return false
}

// countryHeaders are set by CDNs and edge platforms from the client's IP.
// They are only as trustworthy as the rest of the payload (a client could
// send them directly), which is fine for an analytics breakdown.
var countryHeaders = []string{"CF-IPCountry", "CloudFront-Viewer-Country", "X-Vercel-IP-Country", "Fastly-Geo-Country", "X-AppEngine-Country", "X-Country-Code"}

func clientCountry(r *http.Request) string {
	for _, h := range countryHeaders {
		v := strings.ToUpper(strings.TrimSpace(r.Header.Get(h)))
		if len(v) == 2 && v[0] >= 'A' && v[0] <= 'Z' && v[1] >= 'A' && v[1] <= 'Z' && v != "XX" {
			return v
		}
	}
	return ""
}

// clientRow is one client_events row; empty strings and nil pointers are NULL.
type clientRow struct {
	CreatedAt, ReceivedAt                                   string
	App, AppVersion, Version, SessionID, UserID, Device, OS string
	Country                                                 string
	Kind, Name, URL, Method                                 string
	Status                                                  *int64
	DurationMs, Value                                       *float64
	Message, Stack, Fingerprint, Attrs                      string
}

type clientBatch struct {
	App        string            `json:"app"`
	AppVersion string            `json:"app_version"`
	Version    string            `json:"version"`
	SessionID  string            `json:"session_id"`
	UserID     json.RawMessage   `json:"user_id"`
	Device     string            `json:"device"`
	OS         string            `json:"os"`
	Events     []json.RawMessage `json:"events"`
}

type clientEvent struct {
	TS         string          `json:"ts"`
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	URL        string          `json:"url"`
	Method     string          `json:"method"`
	Status     *float64        `json:"status"`
	DurationMs *float64        `json:"duration_ms"`
	Value      *float64        `json:"value"`
	Message    string          `json:"message"`
	Stack      string          `json:"stack"`
	Attrs      json.RawMessage `json:"attrs"`
}

var (
	clientKinds = map[string]bool{"page_load": true, "web_vital": true, "screen": true, "api": true, "error": true, "custom": true, "log": true}
	appName     = regexp.MustCompile(`^[a-z0-9._-]{1,32}$`)
	methodName  = regexp.MustCompile(`^[A-Z]{1,16}$`)
)

// parseClientBatch validates a payload and turns it into rows. A malformed
// envelope fails the whole batch; a malformed event is dropped on its own so
// one bad event does not lose the others.
func parseClientBatch(body []byte, now time.Time, country string) ([]clientRow, error) {
	var b clientBatch
	if err := json.Unmarshal(bytes.TrimSpace(body), &b); err != nil {
		return nil, errors.New("invalid JSON body")
	}
	app := strings.ToLower(strings.TrimSpace(b.App))
	if !appName.MatchString(app) {
		return nil, errors.New("app is required: web, android or ios")
	}
	if len(b.Events) == 0 {
		return nil, errors.New("events is empty")
	}
	if len(b.Events) > maxClientEvents {
		return nil, errors.New("at most 100 events per request")
	}
	received := now.Format(tsLayout)
	base := clientRow{
		ReceivedAt: received,
		App:        app,
		AppVersion: clip(b.AppVersion, 64),
		Version:    clip(b.Version, 64),
		SessionID:  clip(b.SessionID, 128),
		UserID:     clip(jsonScalar(b.UserID), 128),
		Device:     clip(b.Device, 128),
		OS:         clip(b.OS, 64),
		Country:    country,
	}
	rows := make([]clientRow, 0, len(b.Events))
	for _, raw := range b.Events {
		var e clientEvent
		if json.Unmarshal(raw, &e) != nil || !clientKinds[e.Kind] {
			continue
		}
		rows = append(rows, base.withEvent(e, now))
	}
	if len(rows) == 0 {
		return nil, errors.New("no valid events")
	}
	return rows, nil
}

func (base clientRow) withEvent(e clientEvent, now time.Time) clientRow {
	row := base
	row.CreatedAt = eventTime(e.TS, now)
	row.Kind = e.Kind
	row.Name = clip(e.Name, 256)
	row.URL = clip(stripQuery(e.URL), 2048)
	if m := strings.ToUpper(strings.TrimSpace(e.Method)); methodName.MatchString(m) {
		row.Method = m
	}
	if e.Status != nil && *e.Status >= 0 && *e.Status <= 999 {
		st := int64(*e.Status)
		row.Status = &st
	}
	row.DurationMs = boundedFloat(e.DurationMs, 0, 24*3600*1000)
	row.Value = boundedFloat(e.Value, -1e12, 1e12)
	row.Message = clip(e.Message, 1000)
	if e.Kind == "log" {
		row.Message = clip(e.Message, 8<<10)
	}
	row.Stack = clip(e.Stack, 16<<10)
	row.Attrs = flatAttrs(e.Attrs)
	switch e.Kind {
	case "api":
		// Ids in paths would make every call its own endpoint; the SDKs
		// normalize, and this holds for clients that do not.
		if verb, path, ok := strings.Cut(row.Name, " "); ok && strings.HasPrefix(path, "/") {
			row.Name = verb + " " + normalizePath(path)
		}
	case "page_load", "web_vital":
		if strings.HasPrefix(row.Name, "/") {
			row.Name = normalizePath(row.Name)
		}
		// Pages are grouped by URL, so /trips/41 and /trips/42 are one page.
		row.URL = normalizeURLPath(row.URL)
	case "error":
		if row.Name == "" {
			row.Name = "Error"
		}
		row.Fingerprint = clientFingerprint(row.Name, row.Message, row.Stack)
	}
	return row
}

// eventTime is the event's own time, clamped to a day either side of
// receipt; a missing or unreadable time means "now".
func eventTime(ts string, now time.Time) string {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(ts))
	if err != nil {
		return now.Format(tsLayout)
	}
	t = t.UTC()
	if lo := now.Add(-clientClockSkew); t.Before(lo) {
		t = lo
	} else if hi := now.Add(clientClockSkew); t.After(hi) {
		t = hi
	}
	return t.Format(tsLayout)
}

func boundedFloat(v *float64, lo, hi float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v < lo || *v > hi {
		return nil
	}
	x := math.Round(*v*1000) / 1000
	return &x
}

// clip trims s and cuts it to at most n bytes on a rune boundary, dropping
// invalid UTF-8 and control characters other than newlines and tabs.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !utf8.ValidString(s) || strings.ContainsFunc(s, isBadControl) {
		s = strings.Map(func(r rune) rune {
			if r == utf8.RuneError || isBadControl(r) {
				return -1
			}
			return r
		}, s)
	}
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func isBadControl(r rune) bool { return r < 0x20 && r != '\n' && r != '\t' || r == 0x7f }

func stripQuery(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}

// jsonScalar accepts a user id sent as a string or a number.
func jsonScalar(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// flatAttrs keeps a flat object of string, number and boolean values (at
// most 32 keys, short values) and returns it as JSON text.
func flatAttrs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil || len(in) == 0 {
		return ""
	}
	out := make(map[string]any, min(len(in), 32))
	for k, v := range in {
		if len(out) == 32 {
			break
		}
		k = clip(k, 64)
		if k == "" {
			continue
		}
		switch x := v.(type) {
		case string:
			out[k] = clip(x, 256)
		case float64:
			if !math.IsNaN(x) && !math.IsInf(x, 0) {
				out[k] = x
			}
		case bool:
			out[k] = x
		}
	}
	if len(out) == 0 {
		return ""
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

var idSegment = regexp.MustCompile(`^(\d+|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[0-9a-fA-F]{16,})$`)

// normalizePath replaces id-like segments with :id, by the SPEC rule: all
// digits, a UUID, 16 or more hex characters, or 20 or more characters
// containing a digit.
func normalizePath(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if seg != "" && (idSegment.MatchString(seg) || utf8.RuneCountInString(seg) >= 20 && strings.ContainsAny(seg, "0123456789")) {
			parts[i] = ":id"
		}
	}
	return strings.Join(parts, "/")
}

// normalizeURLPath applies normalizePath to the path of an absolute or
// relative URL.
func normalizeURLPath(u string) string {
	start := 0
	if i := strings.Index(u, "://"); i >= 0 {
		j := strings.IndexByte(u[i+3:], '/')
		if j < 0 {
			return u
		}
		start = i + 3 + j
	}
	return u[:start] + normalizePath(u[start:])
}

// rumLimiter is a token bucket per client IP and per key, in requests per
// minute; 0 turns a limit off.
// rumLimiter limits events per IP and per key, at the rates in Settings →
// Server, read on each event so a change applies without a restart.
type rumLimiter struct {
	s         *Server
	mu        sync.Mutex
	ips, keys     map[string]*seenLimiter
}

type seenLimiter struct {
	lim  *rate.Limiter
	seen time.Time
}

func newRUMLimiter(s *Server) *rumLimiter {
	return &rumLimiter{s: s, ips: map[string]*seenLimiter{}, keys: map[string]*seenLimiter{}}
}

func (l *rumLimiter) allowIP(ip string) bool { return l.allow(l.ips, ip, l.s.confInt("client_rate_per_ip")) }

// allowKey limits by the key's hash, so the map never holds keys themselves.
func (l *rumLimiter) allowKey(key string) bool {
	sum := sha256.Sum256([]byte(key))
	return l.allow(l.keys, hex.EncodeToString(sum[:8]), l.s.confInt("client_rate_per_key"))
}

func (l *rumLimiter) allow(m map[string]*seenLimiter, id string, perMinute int) bool {
	if perMinute <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := m[id]
	if !ok {
		// A burst of half a minute's allowance absorbs page-unload flushes.
		e = &seenLimiter{lim: rate.NewLimiter(rate.Limit(float64(perMinute)/60), max(1, perMinute/2))}
		m[id] = e
	} else if r := rate.Limit(float64(perMinute) / 60); e.lim.Limit() != r {
		e.lim.SetLimit(r)
		e.lim.SetBurst(max(1, perMinute/2))
	}
	e.seen = time.Now()
	return e.lim.Allow()
}

// sweep forgets clients idle for 10 minutes, so the maps stay small.
func (l *rumLimiter) sweep(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			l.mu.Lock()
			for _, m := range []map[string]*seenLimiter{l.ips, l.keys} {
				for k, e := range m {
					if now.Sub(e.seen) > 10*time.Minute {
						delete(m, k)
					}
				}
			}
			l.mu.Unlock()
		}
	}
}
