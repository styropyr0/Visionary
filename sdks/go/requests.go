package visionary

import (
	"context"
	"net/http"
	"strings"
	"time"
)

const tsLayout = "2006-01-02T15:04:05.000Z"

// requestColumns is the insert order; requestParams builds rows in it.
var requestColumns = func() []string {
	cols := []string{
		"request_id", "created_at", "method", "path", "route", "query", "status", "duration_ms", "ip", "user_agent",
		"user_id", "user_handle", "user_role", "request_headers", "request_content_type", "request_body",
		"request_body_truncated", "request_size", "response_content_type", "response_body",
		"response_body_truncated", "response_size", "error_code", "error_key",
	}
	for _, c := range AddedRequestColumns {
		cols = append(cols, c.Name)
	}
	return cols
}()

// entry is the raw capture of one request. Redaction and user lookup happen
// on the writer goroutine, off the request path.
type entry struct {
	RequestID           string
	StartedAt           time.Time
	Method              string
	Path                string
	Route               string
	Query               string
	Status              int
	Duration            time.Duration
	IP                  string
	UserAgent           string
	User                User
	LoginAccount        string
	RequestHeaders      http.Header
	RequestContentType  string
	RequestBody         []byte
	RequestTruncated    bool
	RequestSize         int64
	ResponseContentType string
	ResponseBody        []byte
	ResponseTruncated   bool
	ResponseSize        int64

	// Totals is nil when span capture is off; Spans are the rows to store
	// with the request, at most SpanMax of them.
	Totals *spanTotals
	Spans  []*spanRecord
}

// lookupUsers resolves each distinct user missing a handle or role once.
func (w *writer) lookupUsers(ctx context.Context, batch []any) map[string]User {
	out := map[string]User{}
	if w.cfg.LookupUser == nil {
		return out
	}
	for _, row := range batch {
		e, ok := row.(*entry)
		if !ok {
			continue
		}
		id := e.User.ID
		if id == "" || (e.User.Handle != "" && e.User.Role != "") {
			continue
		}
		if _, seen := out[id]; seen {
			continue
		}
		if u, ok := w.cfg.LookupUser(ctx, id); ok {
			out[id] = u
		} else {
			out[id] = User{}
		}
	}
	return out
}

// requestParams returns the row in requestColumns order, typed, with nil for
// NULL.
func (w *writer) requestParams(e *entry, users map[string]User) []any {
	u := e.User
	if full, ok := users[u.ID]; ok {
		if u.Handle == "" {
			u.Handle = full.Handle
		}
		if u.Role == "" {
			u.Role = full.Role
		}
	}
	var errCode, errKey string
	if e.Status >= 400 {
		errCode, errKey = errorFields(e.ResponseBody, w.cfg.ErrorCodeField, w.cfg.ErrorKeyField)
	}
	reqBody, respBody := "", ""
	if w.cfg.CaptureBodies {
		reqBody = w.redact.requestBody(e.RequestBody, e.RequestContentType)
		respBody = w.redact.responseBody(e.ResponseBody)
	}
	dbCount, dbMs, httpCount, httpMs := e.Totals.columns()
	return []any{
		e.RequestID,
		e.StartedAt.UTC().Format(tsLayout),
		e.Method,
		e.Path,
		nullable(e.Route),
		nullable(e.Query),
		e.Status,
		millis(e.Duration),
		nullable(e.IP),
		nullable(e.UserAgent),
		nullable(u.ID),
		nullable(u.Handle),
		nullable(u.Role),
		nullable(w.redact.headers(e.RequestHeaders)),
		nullable(e.RequestContentType),
		nullable(reqBody),
		boolInt(e.RequestTruncated),
		e.RequestSize,
		nullable(e.ResponseContentType),
		nullable(respBody),
		boolInt(e.ResponseTruncated),
		e.ResponseSize,
		nullable(errCode),
		nullable(errKey),
		nullable(w.ident.service),
		nullable(w.ident.version),
		dbCount, dbMs, httpCount, httpMs,
		nullable(clipLogin(e.LoginAccount)),
	}
}

// clipLogin trims a login identifier to what the column holds.
func clipLogin(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 255 {
		s = strings.ToValidUTF8(s[:255], "")
	}
	return s
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
