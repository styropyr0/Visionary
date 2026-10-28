package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

// Client SDKs send logs as events of kind "log" (name is the level); they go to
// app_logs. The X-Request-ID in api event attrs links a session to server requests.

func (s *Server) applogsSessionRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/rum/session-list", s.authed(s.handleSessionList))
	mux.Handle("GET /api/rum/session/{id}", s.authed(s.handleSessionTimeline))
}

func clientLogLines(rows []clientRow) (rest []clientRow, lines []*logLine) {
	for _, r := range rows {
		if r.Kind != "log" {
			rest = append(rest, r)
			continue
		}
		at, _ := time.Parse(tsLayout, r.CreatedAt)
		l := &logLine{
			at: at, service: r.App, version: fallback(r.Version, r.AppVersion), source: clientSource(r.App),
			sev: logrules.ParseLevel(r.Name), message: r.Message, sessionID: r.SessionID, userID: r.UserID,
			attrs: map[string]any{},
		}
		if r.Attrs != "" {
			var a map[string]any
			if json.Unmarshal([]byte(r.Attrs), &a) == nil {
				takeLogFields(l, a)
			}
		}
		for k, v := range map[string]string{"url": r.URL, "device": r.Device, "os": r.OS, "country": r.Country, "app_version": r.AppVersion} {
			if v != "" {
				l.attrs[k] = v
			}
		}
		if l.message == "" {
			l.message = r.Name
		}
		lines = append(lines, l)
	}
	return rest, lines
}

func clientSource(app string) string {
	switch app {
	case "android", "ios":
		return app
	}
	return "browser"
}

func (s *Server) handleSessionList(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	where := "created_at >= ? AND created_at < ? AND session_id IS NOT NULL"
	args := []any{tr.From.Format(tsLayout), tr.To.Format(tsLayout)}
	if app := r.URL.Query().Get("app"); app != "" {
		where += " AND app = ?"
		args = append(args, app)
	}
	if u := strings.TrimSpace(r.URL.Query().Get("user")); u != "" {
		where += " AND user_id = ?"
		args = append(args, u)
	}
	rows, err := s.db.Query(r.Context(), `SELECT session_id, MIN(created_at) AS started, MAX(created_at) AS last, MAX(user_id) AS user_id, MAX(app) AS app,
  MAX(device) AS device, MAX(country) AS country, COUNT(*) AS events, `+countIf("kind = 'error'")+` AS errors, `+countIf("kind = 'api'")+` AS api_calls,
  `+countIf("kind = 'api' AND status >= 500")+` AS failed_calls
FROM client_events WHERE `+where+` GROUP BY session_id ORDER BY last DESC LIMIT 100`, args...)
	if err != nil {
		if missingTable(err, "client_events") {
			writeJSON(w, http.StatusOK, &Result{Columns: []string{}, Rows: [][]any{}})
			return
		}
		s.fail(w, dbErr(err))
		return
	}
	writeJSON(w, http.StatusOK, newResult(rows, 100, "", tr))
}

type timelineItem struct {
	At        string         `json:"at"`
	Kind      string         `json:"kind"` // page_load, screen, api, error, web_vital, custom, log, request, server_log
	Side      string         `json:"side"`
	Name      string         `json:"name,omitempty"`
	Message   string         `json:"message,omitempty"`
	Level     string         `json:"level,omitempty"`
	URL       string         `json:"url,omitempty"`
	Status    any            `json:"status,omitempty"`
	Duration  any            `json:"duration_ms,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	LogID     any            `json:"log_id,omitempty"`
	Service   string         `json:"service,omitempty"`
	Attrs     map[string]any `json:"attrs,omitempty"`
}

func (s *Server) handleSessionTimeline(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || len(id) > 128 {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	ctx := r.Context()
	since := time.Now().UTC().AddDate(0, 0, -s.maxRangeDays()).Format(tsLayout)
	events, err := s.db.Query(ctx, `SELECT created_at, kind, name, url, method, status, duration_ms, value, message, attrs, user_id, app, device, os
FROM client_events WHERE session_id = ? AND created_at >= ? ORDER BY created_at LIMIT 2000`, id, since)
	if err != nil && !missingTable(err, "client_events") {
		s.fail(w, dbErr(err))
		return
	}
	var items []timelineItem
	var requestIDs []any
	info := map[string]any{"session_id": id}
	if events != nil {
		for _, row := range events.Rows {
			it := timelineItem{At: toString(row[0]), Kind: toString(row[1]), Side: "client", Name: toString(row[2]), URL: toString(row[3]),
				Status: row[5], Duration: row[6], Message: toString(row[8])}
			if it.Kind == "web_vital" {
				it.Message = toString(row[7])
			}
			var a map[string]any
			if raw := toString(row[9]); raw != "" && json.Unmarshal([]byte(raw), &a) == nil {
				it.Attrs = a
				if rid, ok := a["request_id"].(string); ok && rid != "" {
					it.RequestID = rid
					requestIDs = append(requestIDs, rid)
				}
			}
			items = append(items, it)
			for i, k := range []string{"user_id", "app", "device", "os"} {
				if v := toString(row[10+i]); v != "" {
					info[k] = v
				}
			}
		}
	}
	logWhere := "session_id = ?"
	logArgs := []any{id}
	if len(requestIDs) > 0 {
		if len(requestIDs) > 500 {
			requestIDs = requestIDs[:500]
		}
		logWhere = "(session_id = ? OR request_id IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(requestIDs)), ", ") + "))"
		logArgs = append(logArgs, requestIDs...)
	}
	q := `SELECT id, created_at, level, message, source, service, request_id FROM app_logs WHERE ` + logWhere + ` AND created_at >= ?`
	logArgs = append(logArgs, since)
	if scope, sargs := s.logScope(r); scope != "" {
		q += " AND " + scope
		logArgs = append(logArgs, sargs...)
	}
	if lines, err := s.db.Query(ctx, q+` ORDER BY created_at, id LIMIT 2000`, logArgs...); err == nil {
		for _, row := range lines.Rows {
			side := "server"
			kind := "server_log"
			switch toString(row[4]) {
			case "browser", "android", "ios":
				side, kind = "client", "log"
			}
			items = append(items, timelineItem{At: toString(row[1]), Kind: kind, Side: side, Level: toString(row[2]), Message: toString(row[3]),
				Service: toString(row[5]), RequestID: toString(row[6]), LogID: row[0]})
		}
	}
	if len(requestIDs) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(requestIDs)), ", ")
		if reqs, err := s.db.Query(ctx, `SELECT id, created_at, method, route, path, status, duration_ms, request_id, service FROM api_logs WHERE request_id IN (`+marks+`)`, requestIDs...); err == nil {
			for _, row := range reqs.Rows {
				route := toString(row[3])
				if route == "" {
					route = toString(row[4])
				}
				items = append(items, timelineItem{At: toString(row[1]), Kind: "request", Side: "server", Name: toString(row[2]) + " " + route,
					Status: row[5], Duration: row[6], RequestID: toString(row[7]), LogID: row[0], Service: toString(row[8])})
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].At < items[j].At })
	if len(items) == 0 {
		writeError(w, http.StatusNotFound, "no events for this session")
		return
	}
	info["started"], info["last"] = items[0].At, items[len(items)-1].At
	writeJSON(w, http.StatusOK, map[string]any{"session": info, "items": items})
}

// ingestClientLogs runs in the background so the response isn't held up.
func (s *Server) ingestClientLogs(lines []*logLine) {
	if len(lines) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := s.ingestLogLines(ctx, lines); err != nil {
			s.logger.Warn("client logs write", "err", err)
		}
	}()
}
