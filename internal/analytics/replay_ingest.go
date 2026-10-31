package analytics

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

const (
	// A chunk as sent; base64 makes it about 680 KB, under D1's row limit.
	maxReplayBody = 512 << 10
	// A chunk once unzipped, so a small body can't inflate without bound.
	maxReplayJSON = 16 << 20
)

var replaySessionID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type replayChunk struct {
	sessionID, app, userID, url, reason string
	seq                                 int64
	start, end                          time.Time
	events                              int64
	gz                                  []byte
}

// handleReplayIngest takes one chunk of a recording. Answers: 204 stored,
// 400 bad chunk, 401 bad key, 403 replays off or origin not allowed, 413 too
// large, 429 too many requests, 507 the storage cap is reached.
func (s *Server) handleReplayIngest(w http.ResponseWriter, r *http.Request) {
	st := s.rum()
	if origin := r.Header.Get("Origin"); origin != "" {
		if !st.originAllowed(origin) {
			writeError(w, http.StatusForbidden, "origin not allowed")
			return
		}
		setCORS(w.Header(), origin)
	}
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
	if !s.confBool("replay") {
		writeError(w, http.StatusForbidden, "session replay is off; turn it on in Settings → Server → Ingest")
		return
	}
	if !st.limits.allowKey(key) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	if capMB := s.confInt("replay_max_mb"); capMB > 0 && s.replay().bytes.Load() > int64(capMB)<<20*11/10 {
		writeError(w, http.StatusInsufficientStorage, "replay storage is full")
		return
	}
	if r.ContentLength > maxReplayBody {
		writeError(w, http.StatusRequestEntityTooLarge, "chunk over 512 KB")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxReplayBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "chunk over 512 KB")
		return
	}
	c, err := parseReplayChunk(r.URL.Query(), body, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	if err := s.storeReplayChunk(ctx, c); err != nil {
		s.logger.Warn("replay chunk", "err", err)
		writeError(w, http.StatusServiceUnavailable, "could not store the chunk")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseReplayChunk checks the chunk is a JSON array of events, unzipping it
// when the SDK compressed it and zipping it when it could not.
func parseReplayChunk(q map[string][]string, body []byte, now time.Time) (replayChunk, error) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	c := replayChunk{
		sessionID: get("sid"),
		app:       strings.ToLower(strings.TrimSpace(get("app"))),
		userID:    clip(get("user"), 128),
		url:       clip(stripQuery(get("url")), 2048),
		reason:    "sample",
	}
	if !replaySessionID.MatchString(c.sessionID) {
		return c, errors.New("sid is required")
	}
	if !appName.MatchString(c.app) {
		c.app = "web"
	}
	if get("reason") == "error" {
		c.reason = "error"
	}
	var err error
	if c.seq, err = strconv.ParseInt(get("seq"), 10, 64); err != nil || c.seq < 0 {
		return c, errors.New("seq must be a number")
	}
	start, err1 := strconv.ParseInt(get("start"), 10, 64)
	end, err2 := strconv.ParseInt(get("end"), 10, 64)
	if err1 != nil || err2 != nil || end < start {
		return c, errors.New("start and end must be epoch milliseconds")
	}
	c.start = clampTime(time.UnixMilli(start).UTC(), now)
	c.end = clampTime(time.UnixMilli(end).UTC(), now)
	var raw []byte
	switch get("enc") {
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return c, errors.New("invalid gzip body")
		}
		raw, err = io.ReadAll(io.LimitReader(zr, maxReplayJSON+1))
		if err != nil {
			return c, errors.New("invalid gzip body")
		}
		c.gz = body
	case "json", "":
		raw = body
	default:
		return c, errors.New("enc must be gzip or json")
	}
	if len(raw) > maxReplayJSON {
		return c, errors.New("chunk over 16 MB unzipped")
	}
	var events []json.RawMessage
	if err := json.Unmarshal(raw, &events); err != nil || len(events) == 0 {
		return c, errors.New("the body must be a JSON array of events")
	}
	c.events = int64(len(events))
	if c.gz == nil {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		c.gz = buf.Bytes()
		if len(c.gz) > maxReplayBody {
			return c, errors.New("chunk over 512 KB")
		}
	}
	return c, nil
}

// clampTime keeps a client's clock within a day of the server's.
func clampTime(t, now time.Time) time.Time {
	if lo := now.Add(-clientClockSkew); t.Before(lo) {
		return lo
	}
	if hi := now.Add(clientClockSkew); t.After(hi) {
		return hi
	}
	return t
}

func (s *Server) storeReplayChunk(ctx context.Context, c replayChunk) error {
	payload := base64.StdEncoding.EncodeToString(c.gz)
	size := int64(len(c.gz))
	start, end := c.start.Format(tsLayout), c.end.Format(tsLayout)
	insert := `INSERT INTO replay_chunks (session_id, seq, created_at, ended_at, events, bytes, payload) VALUES (?, ?, ?, ?, ?, ?, ?)`
	switch s.q.d {
	case sdk.Postgres:
		insert += ` ON CONFLICT (session_id, seq) DO NOTHING`
	case sdk.MySQL:
		insert = strings.Replace(insert, "INSERT INTO", "INSERT IGNORE INTO", 1)
	default:
		insert = strings.Replace(insert, "INSERT INTO", "INSERT OR IGNORE INTO", 1)
	}
	if err := s.db.Exec(ctx, insert, c.sessionID, c.seq, start, end, c.events, size, payload); err != nil {
		return dbErr(err)
	}
	// created_at follows the last chunk, so retention and deletes go by the
	// session's last activity; archived goes back to 0 so it is archived again.
	upsert := `INSERT INTO replay_sessions (session_id, started_at, created_at, app, user_id, url, reason, chunks, bytes, archived) VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, 1, ?, 0)`
	if s.q.d == sdk.MySQL {
		upsert += ` ON DUPLICATE KEY UPDATE started_at = LEAST(started_at, VALUES(started_at)), created_at = GREATEST(created_at, VALUES(created_at)),
  user_id = COALESCE(VALUES(user_id), user_id), chunks = chunks + 1, bytes = bytes + VALUES(bytes), archived = 0`
	} else {
		upsert += ` ON CONFLICT (session_id) DO UPDATE SET
  started_at = CASE WHEN excluded.started_at < replay_sessions.started_at THEN excluded.started_at ELSE replay_sessions.started_at END,
  created_at = CASE WHEN excluded.created_at > replay_sessions.created_at THEN excluded.created_at ELSE replay_sessions.created_at END,
  user_id = COALESCE(excluded.user_id, replay_sessions.user_id), chunks = replay_sessions.chunks + 1, bytes = replay_sessions.bytes + excluded.bytes, archived = 0`
	}
	if err := s.db.Exec(ctx, upsert, c.sessionID, start, end, c.app, c.userID, c.url, c.reason, size); err != nil {
		return dbErr(err)
	}
	s.replay().bytes.Add(size)
	return nil
}
