package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The audit log records who changed what. Writes go through a buffered
// channel to one background writer, so a slow or failing database never
// holds up or fails the request being audited.

type auditEntry struct {
	at                         time.Time
	userID, userName, role, ip string
	action, target, detailJSON string
}

const (
	auditQueue     = 1024
	maxAuditTarget = 255
	maxAuditDetail = 8 << 10
)

// audit records who did what. action is a short verb phrase such as
// "alert.create"; target names the object; detail is any JSON value.
func (s *Server) audit(r *http.Request, action, target string, detail any) {
	s.auditAs(r, sessionFrom(r), action, target, detail)
}

// auditAs is audit for requests without a session in their context yet
// (sign-in and sign-out).
func (s *Server) auditAs(r *http.Request, sess *session, action, target string, detail any) {
	e := auditEntry{at: time.Now().UTC(), action: clip(action, 64), target: clip(target, maxAuditTarget), ip: s.auth.clientIP(r)}
	if sess != nil {
		e.userID, e.userName, e.role = clip(sess.UserID, 255), clip(fallback(sess.Name, sess.Handle), 255), s.roleOf(sess).String()
	}
	e.detailJSON = "{}"
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil && len(b) <= maxAuditDetail {
			e.detailJSON = string(b)
		}
	}
	select {
	case s.team().audits <- e:
	default:
		s.logger.Warn("audit queue full, entry dropped", "action", action, "target", target)
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// auditWriter drains the queue until ctx ends, then writes what is left.
func (s *Server) auditWriter(ctx context.Context) {
	for {
		select {
		case e := <-s.team().audits:
			s.writeAudit(e)
		case <-ctx.Done():
			for {
				select {
				case e := <-s.team().audits:
					s.writeAudit(e)
				default:
					return
				}
			}
		}
	}
}

func (s *Server) writeAudit(e auditEntry) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := s.db.Exec(ctx, `INSERT INTO audit_log (created_at, user_id, user_name, role, action, target, detail, ip)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, e.at.Format(tsLayout), e.userID, e.userName, e.role, e.action, e.target, e.detailJSON, e.ip)
	if err != nil {
		s.logger.Error("write audit entry", "action", e.action, "err", err)
	}
}

// auditRetention deletes entries older than VISIONARY_AUDIT_RETENTION_DAYS,
// at startup and every six hours.
func (s *Server) auditRetention(ctx context.Context) {
	tick := time.NewTicker(6 * time.Hour)
	defer tick.Stop()
	for {
		cutoff := time.Now().UTC().AddDate(0, 0, -s.team().retentionDays).Format(tsLayout)
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		if err := s.db.Exec(cctx, `DELETE FROM audit_log WHERE created_at < ?`, cutoff); err != nil && ctx.Err() == nil {
			s.logger.Warn("audit retention", "err", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// handleAudit lists entries newest first. Filters: action ("panel" matches
// panel.*, "panel.create" only that), user (id or name), from/to, and before
// (an id, for paging).
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	q := r.URL.Query()
	where := []string{"1 = 1"}
	var args []any
	if a := strings.TrimSpace(q.Get("action")); a != "" {
		if strings.Contains(a, ".") {
			where, args = append(where, "action = ?"), append(args, a)
		} else {
			// A text range is a prefix match that needs no LIKE escaping.
			where, args = append(where, "action >= ? AND action < ?"), append(args, a+".", a+"/")
		}
	}
	if u := strings.TrimSpace(q.Get("user")); u != "" {
		where, args = append(where, "(user_id = ? OR user_name = ?)"), append(args, u, u)
	}
	for _, f := range []struct{ param, op string }{{"from", ">="}, {"to", "<"}} {
		if v := q.Get(f.param); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				writeError(w, http.StatusBadRequest, f.param+" must be an RFC 3339 timestamp")
				return
			}
			where, args = append(where, "created_at "+f.op+" ?"), append(args, t.UTC().Format(tsLayout))
		}
	}
	if v := q.Get("before"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before must be an id")
			return
		}
		where, args = append(where, "id < ?"), append(args, id)
	}
	limit := 100
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, created_at, user_id, user_name, role, action, target, detail, ip
FROM audit_log WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	entries := make([]map[string]any, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		if len(row) != 9 {
			continue
		}
		var detail any
		if json.Unmarshal([]byte(toString(row[7])), &detail) != nil {
			detail = map[string]any{}
		}
		entries = append(entries, map[string]any{
			"id": toInt(row[0]), "created_at": toString(row[1]), "user_id": toString(row[2]), "user_name": toString(row[3]),
			"role": toString(row[4]), "action": toString(row[5]), "target": toString(row[6]), "detail": detail, "ip": toString(row[8]),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "retention_days": s.team().retentionDays, "more": len(entries) == limit})
}
