package analytics

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !replaySessionID.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	ctx := r.Context()
	sess, err := s.db.Query(ctx, `SELECT session_id, started_at, created_at, app, user_id, url, reason, chunks, bytes FROM replay_sessions WHERE session_id = ?`, id)
	if err != nil && !missingTable(err, "replay_sessions") {
		s.fail(w, dbErr(err))
		return
	}
	if sess == nil || len(sess.Rows) == 0 {
		writeError(w, http.StatusNotFound, "no replay for this session")
		return
	}
	info := map[string]any{}
	for i, c := range sess.Columns {
		info[c] = sess.Rows[0][i]
	}
	chunks, err := s.db.Query(ctx, `SELECT seq, created_at, ended_at, events, bytes FROM replay_chunks WHERE session_id = ? ORDER BY seq LIMIT 5000`, id)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	list := make([]map[string]any, 0, len(chunks.Rows))
	for _, row := range chunks.Rows {
		list = append(list, map[string]any{"seq": toInt(row[0]), "started": toString(row[1]), "ended": toString(row[2]), "events": toInt(row[3]), "bytes": toInt(row[4])})
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": info, "chunks": list})
}

// handleReplayChunk sends a chunk's events as stored, gzipped; the browser
// unzips them on the way in.
func (s *Server) handleReplayChunk(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if !replaySessionID.MatchString(id) || err != nil {
		writeError(w, http.StatusBadRequest, "invalid chunk")
		return
	}
	res, err := s.db.Query(r.Context(), `SELECT payload FROM replay_chunks WHERE session_id = ? AND seq = ?`, id, seq)
	if err != nil && !missingTable(err, "replay_chunks") {
		s.fail(w, dbErr(err))
		return
	}
	if res == nil || len(res.Rows) == 0 {
		writeError(w, http.StatusNotFound, "no such chunk")
		return
	}
	gz, err := base64.StdEncoding.DecodeString(toString(res.Rows[0][0]))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the stored chunk is damaged")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Length", strconv.Itoa(len(gz)))
	_, _ = w.Write(gz)
}

// replayFrameCSP lets a replay load the recorded site's styles, images and
// fonts but never run a script, submit a form or leave the dashboard.
const replayFrameCSP = "default-src 'none'; script-src 'none'; style-src * data: 'unsafe-inline'; img-src * data: blob:; font-src * data:; media-src *; form-action 'none'; frame-ancestors 'self'"

// handleReplayFrame is the empty page the player draws into. It has its own
// policy: about:blank would inherit the dashboard's, which blocks other sites.
func (s *Server) handleReplayFrame(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", replayFrameCSP)
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte("<!DOCTYPE html><html><head></head><body></body></html>"))
}

func (s *Server) replaySessions(ctx context.Context, ids []any) map[string]bool {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out
	}
	res, err := s.db.Query(ctx, `SELECT session_id FROM replay_sessions WHERE session_id IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")+`)`, ids...)
	if err != nil {
		return out
	}
	for _, row := range res.Rows {
		out[toString(row[0])] = true
	}
	return out
}
