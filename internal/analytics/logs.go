package analytics

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

const (
	logsPageSize = 100
	maxSearchLen = 200
)

// likeEscaper escapes with "!": a backslash is itself an escape inside MySQL
// string literals, so it cannot be the LIKE escape character portably.
var likeEscaper = strings.NewReplacer(`!`, `!!`, `%`, `!%`, `_`, `!_`)

// listLogs pages the request log newest-first, filtered by the explorer's
// controls. Every filter is a bound parameter.
func (s *Server) listLogs(ctx context.Context, r timeRange, q url.Values) (*Result, error) {
	where := []string{"created_at >= ?", "created_at < ?"}
	args := []any{r.From.Format(tsLayout), r.To.Format(tsLayout)}

	switch status := strings.TrimSpace(q.Get("status")); status {
	case "":
	case "2xx", "3xx", "4xx", "5xx":
		lo := int(status[0]-'0') * 100
		where = append(where, "status >= ? AND status < ?")
		args = append(args, lo, lo+100)
	default:
		code, err := strconv.Atoi(status)
		if err != nil || code < 100 || code > 599 {
			return nil, &queryError{errors.New("status must be a code like 404 or a class like 4xx")}
		}
		where = append(where, "status = ?")
		args = append(args, code)
	}
	if m := strings.ToUpper(strings.TrimSpace(q.Get("method"))); m != "" {
		where = append(where, "method = ?")
		args = append(args, m)
	}
	if route := strings.TrimSpace(q.Get("route")); route != "" {
		where = append(where, "route = ?")
		args = append(args, route)
	}
	if user := strings.TrimSpace(q.Get("user")); user != "" {
		if id, err := strconv.ParseInt(user, 10, 64); err == nil {
			where = append(where, "user_id = ?")
			args = append(args, strconv.FormatInt(id, 10))
		} else {
			where = append(where, "user_handle = ?")
			args = append(args, strings.TrimPrefix(user, "@"))
		}
	}
	if search := strings.TrimSpace(q.Get("search")); search != "" {
		if len(search) > maxSearchLen {
			return nil, &queryError{errors.New("search is too long")}
		}
		where = append(where, `(request_id = ? OR path LIKE ? ESCAPE '!')`)
		args = append(args, search, "%"+likeEscaper.Replace(search)+"%")
	}
	if before := strings.TrimSpace(q.Get("before")); before != "" {
		id, err := strconv.ParseInt(before, 10, 64)
		if err != nil {
			return nil, &queryError{errors.New("before must be a log id")}
		}
		where = append(where, "id < ?")
		args = append(args, id)
	}

	sql := `SELECT id, created_at, method, path, route, status, duration_ms, user_id, user_handle, ip,
  error_key, response_size
FROM api_logs
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY id DESC
LIMIT ` + strconv.Itoa(logsPageSize+1)

	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	return newResult(rows, logsPageSize, sql, r), nil
}

func (s *Server) getLog(ctx context.Context, id int64) (*Result, error) {
	rows, err := s.db.Query(ctx, `SELECT * FROM api_logs WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	return &Result{Columns: rows.Columns, Rows: rows.Rows, RowsRead: rows.RowsRead, DurationMs: rows.DurationMs}, nil
}
