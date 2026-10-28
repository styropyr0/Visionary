package analytics

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Patterns: the pattern job reads lines that have none yet, reduces each
// message to a template (numbers, ids, addresses, quoted text and the like
// become <*>), names the template per service with a 16-hex id, writes the
// id on the lines and keeps a log_patterns row per template: its counts,
// when it was first and last seen, and the release it first appeared in.
// The secret scanner (applogs_secrets.go) reads the same batch.
//
//	GET /api/applogs/patterns         templates in the range, busiest first, with a trend
//	GET /api/applogs/compare          what a release logs that another didn't, and the reverse
//	GET /api/applogs/volume           lines and bytes by service, source, level and code line
//	GET /api/applogs/hidden-failures  errors logged in requests that answered 2xx or 3xx

const (
	patternBatch    = 2000
	patternEvery    = 10 * time.Second
	maxTemplateLen  = 400
	patternsPerPage = 100
)

var (
	tplQuoted = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'`)
	tplUUID   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	tplEmail  = regexp.MustCompile(`[\w.+-]+@[\w-]+(?:\.[\w-]+)+`)
	tplURL    = regexp.MustCompile(`\b(https?://[^\s/?#]+)[^\s]*`)
	tplIP     = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b|\b(?:[0-9a-f]{1,4}:){3,7}[0-9a-f]{1,4}\b`)
	tplHex    = regexp.MustCompile(`(?i)\b(?:0x)?[0-9a-f]{8,}\b`)
	tplTime   = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:[.,]\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?\b|\b\d{2}:\d{2}:\d{2}(?:\.\d+)?\b`)
	tplNum    = regexp.MustCompile(`[-+]?\b\d+(?:[.,]\d+)*(?:ns|us|µs|ms|s|m|h|d|b|kb|mb|gb|tb|%|x)?\b`)
	tplWordID = regexp.MustCompile(`\b[\w-]*\d[\w-]*\b`)
	tplKV     = regexp.MustCompile(`\b([A-Za-z_][\w.]*)=(\S+)`)
	tplStars  = regexp.MustCompile(`<\*>(?:[\s,/:.-]*<\*>)+`)
	tplSpace  = regexp.MustCompile(`\s+`)
)

// templateOf reduces a message to its template.
func templateOf(msg string) string {
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		// A stack trace or a multi-line dump: its first line names it.
		msg = msg[:i]
	}
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	t := tplQuoted.ReplaceAllString(msg, "<*>")
	t = tplURL.ReplaceAllString(t, "$1/<*>")
	for _, re := range []*regexp.Regexp{tplUUID, tplEmail, tplTime, tplIP, tplHex} {
		t = re.ReplaceAllString(t, "<*>")
	}
	t = tplKV.ReplaceAllString(t, "$1=<*>")
	t = tplNum.ReplaceAllString(t, "<*>")
	t = tplWordID.ReplaceAllStringFunc(t, func(w string) string {
		if strings.Contains(w, "<*>") {
			return w
		}
		return "<*>"
	})
	t = tplStars.ReplaceAllString(t, "<*>")
	t = strings.TrimSpace(tplSpace.ReplaceAllString(t, " "))
	if len(t) > maxTemplateLen {
		t = strings.TrimSpace(t[:maxTemplateLen]) + " …"
	}
	if t == "" {
		t = "<*>"
	}
	return t
}

// patternID names a template within a service.
func patternID(service, template string) string {
	h := sha1.Sum([]byte(service + "\n" + template))
	return hex.EncodeToString(h[:8])
}

// patternAgg is a template's share of one batch.
type patternAgg struct {
	id, service, template, sample, version, file string
	line                                           int64
	severity                                       int64
	first, last                                    string
	lines, bytes                                   int64
	ids                                            []int64
}

// applogsPatternsStart runs the job until ctx ends.
func (s *Server) applogsPatternsStart(ctx context.Context) {
	go func() {
		t := time.NewTimer(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			wait := patternEvery
			n, err := s.patternPass(ctx)
			switch {
			case err != nil:
				if ctx.Err() == nil && !missingTable(err, "app_logs") {
					s.logger.Warn("logs: pattern job", "err", err)
				}
				wait = time.Minute
			case n == patternBatch:
				wait = time.Second // more to do
			}
			t.Reset(wait)
		}
	}()
}

var patternMu sync.Map // *Server → *sync.Mutex: one pass at a time

// patternPass names the patterns of up to patternBatch lines that have
// none; it returns how many it read.
func (s *Server) patternPass(ctx context.Context) (int, error) {
	v, _ := patternMu.LoadOrStore(s, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	rows, err := s.db.Query(ctx, `SELECT id, created_at, service, version, message, attrs, severity, code_file, code_line, size, logger, host FROM app_logs WHERE pattern IS NULL ORDER BY id LIMIT `+strconv.Itoa(patternBatch))
	if err != nil {
		return 0, err
	}
	if len(rows.Rows) == 0 {
		return 0, nil
	}
	aggs := map[string]*patternAgg{}
	var order []string
	for _, r := range rows.Rows {
		service := toString(r[2])
		msg := toString(r[4])
		tpl := templateOf(msg)
		id := patternID(service, tpl)
		a := aggs[id]
		if a == nil {
			a = &patternAgg{id: id, service: service, template: tpl, sample: clip(msg, 1000), version: toString(r[3]),
				file: toString(r[7]), line: toInt(r[8]), first: toString(r[1]), last: toString(r[1])}
			aggs[id] = a
			order = append(order, id)
		}
		at := toString(r[1])
		if at < a.first {
			a.first, a.version = at, toString(r[3])
		}
		if at > a.last {
			a.last = at
		}
		a.severity = max(a.severity, toInt(r[6]))
		a.lines++
		a.bytes += toInt(r[9])
		a.ids = append(a.ids, toInt(r[0]))
	}
	s.scanSecrets(ctx, rows)
	for _, id := range order {
		a := aggs[id]
		if err := s.upsertPattern(ctx, a); err != nil {
			return 0, err
		}
		for start := 0; start < len(a.ids); start += 400 {
			chunk := a.ids[start:min(start+400, len(a.ids))]
			args := []any{a.id}
			for _, x := range chunk {
				args = append(args, x)
			}
			marks := strings.TrimSuffix(strings.Repeat("?, ", len(chunk)), ", ")
			if err := s.db.Exec(ctx, `UPDATE app_logs SET pattern = ? WHERE id IN (`+marks+`)`, args...); err != nil {
				return 0, err
			}
		}
	}
	return len(rows.Rows), nil
}

func (s *Server) upsertPattern(ctx context.Context, a *patternAgg) error {
	cols := `(id, service, template, sample, level, severity, first_seen, last_seen, first_version, code_file, code_line, lines, bytes)`
	vals := `VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	var line any
	if a.line > 0 {
		line = a.line
	}
	args := []any{a.id, otlpNullStr(a.service), a.template, a.sample, levelNameOf(a.severity), a.severity, a.first, a.last,
		otlpNullStr(a.version), otlpNullStr(a.file), line, a.lines, a.bytes}
	var q string
	switch s.q.d {
	case sdk.MySQL:
		q = `INSERT INTO log_patterns ` + cols + ` ` + vals + ` ON DUPLICATE KEY UPDATE
  lines = lines + VALUES(lines), bytes = bytes + VALUES(bytes),
  last_seen = GREATEST(last_seen, VALUES(last_seen)),
  severity = GREATEST(severity, VALUES(severity)),
  level = IF(VALUES(severity) > severity, VALUES(level), level)`
	case sdk.Postgres:
		q = `INSERT INTO log_patterns ` + cols + ` ` + vals + ` ON CONFLICT (id) DO UPDATE SET
  lines = log_patterns.lines + excluded.lines, bytes = log_patterns.bytes + excluded.bytes,
  last_seen = GREATEST(log_patterns.last_seen, excluded.last_seen),
  level = CASE WHEN excluded.severity > log_patterns.severity THEN excluded.level ELSE log_patterns.level END,
  severity = GREATEST(log_patterns.severity, excluded.severity)`
	default:
		q = `INSERT INTO log_patterns ` + cols + ` ` + vals + ` ON CONFLICT (id) DO UPDATE SET
  lines = log_patterns.lines + excluded.lines, bytes = log_patterns.bytes + excluded.bytes,
  last_seen = max(log_patterns.last_seen, excluded.last_seen),
  level = CASE WHEN excluded.severity > log_patterns.severity THEN excluded.level ELSE log_patterns.level END,
  severity = max(log_patterns.severity, excluded.severity)`
	}
	return s.db.Exec(ctx, q, args...)
}

func levelNameOf(sev int64) string { return levelNames[min(max(sev, 1), 6)] }

var levelNames = [...]string{"", "trace", "debug", "info", "warn", "error", "fatal"}

// handleLogPatterns lists the templates of the matching lines in the range,
// busiest first, with a count per bucket for a trend line.
func (s *Server) handleLogPatterns(w http.ResponseWriter, r *http.Request) {
	ls, err := s.logSearchOf(r)
	if err != nil {
		s.logFail(w, err)
		return
	}
	ctx := r.Context()
	q := `SELECT pattern, COUNT(*) AS n, MAX(severity) AS sev, MIN(created_at) AS first, MAX(created_at) AS last, SUM(size) AS bytes, MAX(id) AS sample_id
FROM app_logs WHERE ` + ls.sql() + ` AND pattern IS NOT NULL GROUP BY pattern ORDER BY n DESC LIMIT ` + strconv.Itoa(patternsPerPage)
	rows, err := s.db.Query(ctx, q, ls.args...)
	if err != nil {
		s.logFail(w, dbErr(err))
		return
	}
	var total, pending int64
	if res, err := s.db.Query(ctx, `SELECT COUNT(*), SUM(CASE WHEN pattern IS NULL THEN 1 ELSE 0 END) FROM app_logs WHERE `+ls.sql(), ls.args...); err == nil && len(res.Rows) > 0 {
		total, pending = toInt(res.Rows[0][0]), toInt(res.Rows[0][1])
	}
	ids := make([]any, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		ids = append(ids, row[0])
	}
	info := map[string]map[string]any{}
	if len(ids) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
		pr, err := s.db.Query(ctx, `SELECT id, service, template, first_seen, first_version, code_file, code_line, lines, viewed_at FROM log_patterns WHERE id IN (`+marks+`)`, ids...)
		if err == nil {
			for i := range pr.Rows {
				m := rowMap(&Result{Columns: pr.Columns, Rows: pr.Rows}, i)
				info[toString(m["id"])] = m
			}
		}
	}
	// Trend: counts per bucket for the patterns on this page.
	trend := map[string]map[int64]int64{}
	if len(ids) > 0 {
		bucket := strconv.FormatInt(ls.tr.Bucket, 10)
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
		tq := `SELECT pattern, ` + s.q.bucketOf(bucket) + ` AS t, COUNT(*) FROM app_logs WHERE ` + ls.sql() + ` AND pattern IN (` + marks + `) GROUP BY pattern, 2`
		if tr, err := s.db.Query(ctx, tq, append(append([]any{}, ls.args...), ids...)...); err == nil {
			for _, row := range tr.Rows {
				p := toString(row[0])
				if trend[p] == nil {
					trend[p] = map[int64]int64{}
				}
				trend[p][toInt(row[1])] = toInt(row[2])
			}
		}
	}
	from := ls.tr.From.Unix() / ls.tr.Bucket * ls.tr.Bucket
	steps := int((ls.tr.To.Unix()-from)/ls.tr.Bucket) + 1
	out := []map[string]any{}
	for _, row := range rows.Rows {
		id := toString(row[0])
		points := make([]int64, steps)
		for t, n := range trend[id] {
			if i := int((t - from) / ls.tr.Bucket); i >= 0 && i < steps {
				points[i] = n
			}
		}
		p := map[string]any{
			"id": id, "count": toInt(row[1]), "level": levelNameOf(toInt(row[2])), "first_in_range": row[3], "last_in_range": row[4],
			"bytes": toInt(row[5]), "sample_id": toInt(row[6]), "trend": points,
		}
		if in := info[id]; in != nil {
			for _, k := range []string{"service", "template", "first_seen", "first_version", "code_file", "code_line", "lines", "viewed_at"} {
				p[k] = in[k]
			}
			p["new"] = toString(in["first_seen"]) >= ls.tr.From.Format(tsLayout)
		}
		out = append(out, p)
	}
	// pending lines have not been through the pattern job yet.
	writeJSON(w, http.StatusOK, map[string]any{"patterns": out, "total": total, "pending": pending, "bucket": ls.tr.Bucket, "from": from})
}

// handleLogCompare lists the templates one release logs that the other
// doesn't (and those whose rate changed most), for a service: what a
// deploy changed in the logs. a and b are versions; each is read over its
// own lifetime within the range.
func (s *Server) handleLogCompare(w http.ResponseWriter, r *http.Request) {
	ls, err := s.logSearchOf(r)
	if err != nil {
		s.logFail(w, err)
		return
	}
	qs := r.URL.Query()
	a, b, service := qs.Get("a"), qs.Get("b"), qs.Get("service")
	if a == "" || b == "" || a == b {
		writeError(w, http.StatusBadRequest, "pick two different versions to compare")
		return
	}
	where := ls.sql() + ` AND pattern IS NOT NULL`
	args := append([]any{}, ls.args...)
	if service != "" {
		where += ` AND service = ?`
		args = append(args, service)
	}
	counts := func(version string) (map[string]int64, map[string]int64, float64, error) {
		res, err := s.db.Query(r.Context(), `SELECT pattern, COUNT(*), MAX(severity) FROM app_logs WHERE `+where+` AND version = ? GROUP BY pattern`, append(append([]any{}, args...), version)...)
		if err != nil {
			return nil, nil, 0, err
		}
		n, sev := map[string]int64{}, map[string]int64{}
		var total float64
		for _, row := range res.Rows {
			n[toString(row[0])] = toInt(row[1])
			sev[toString(row[0])] = toInt(row[2])
			total += float64(toInt(row[1]))
		}
		return n, sev, total, nil
	}
	na, sevA, ta, err := counts(a)
	if err != nil {
		s.logFail(w, dbErr(err))
		return
	}
	nb, sevB, tb, err := counts(b)
	if err != nil {
		s.logFail(w, dbErr(err))
		return
	}
	type change struct {
		ID     string  `json:"id"`
		A      int64   `json:"a"`
		B      int64   `json:"b"`
		ShareA float64 `json:"share_a"`
		ShareB float64 `json:"share_b"`
		Level  string  `json:"level"`
		Kind   string  `json:"kind"` // new, gone, more, less
	}
	var out []change
	share := func(n int64, t float64) float64 {
		if t == 0 {
			return 0
		}
		return float64(n) / t
	}
	for id, n := range nb {
		c := change{ID: id, A: na[id], B: n, ShareA: share(na[id], ta), ShareB: share(n, tb), Level: levelNameOf(sevB[id])}
		switch {
		case na[id] == 0:
			c.Kind = "new"
		case c.ShareB > c.ShareA*2 && n >= 10:
			c.Kind = "more"
		case c.ShareB*2 < c.ShareA && na[id] >= 10:
			c.Kind = "less"
		default:
			continue
		}
		out = append(out, c)
	}
	for id, n := range na {
		if nb[id] == 0 {
			out = append(out, change{ID: id, A: n, ShareA: share(n, ta), Level: levelNameOf(sevA[id]), Kind: "gone"})
		}
	}
	rank := map[string]int{"new": 0, "more": 1, "less": 2, "gone": 3}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Kind] != rank[out[j].Kind] {
			return rank[out[i].Kind] < rank[out[j].Kind]
		}
		return out[i].A+out[i].B > out[j].A+out[j].B
	})
	if len(out) > 200 {
		out = out[:200]
	}
	ids := make([]string, len(out))
	for i, c := range out {
		ids[i] = c.ID
	}
	templates := s.patternTemplates(r.Context(), ids)
	rows := make([]map[string]any, 0, len(out))
	for _, c := range out {
		rows = append(rows, map[string]any{"id": c.ID, "a": c.A, "b": c.B, "share_a": c.ShareA, "share_b": c.ShareB, "level": c.Level, "kind": c.Kind, "template": templates[c.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"a": a, "b": b, "lines_a": ta, "lines_b": tb, "changes": rows})
}

// patternTemplates reads the templates of patterns by id.
func (s *Server) patternTemplates(ctx context.Context, ids []string) map[string]string {
	out := map[string]string{}
	for start := 0; start < len(ids); start += 400 {
		chunk := ids[start:min(start+400, len(ids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(chunk)), ", ")
		res, err := s.db.Query(ctx, `SELECT id, template FROM log_patterns WHERE id IN (`+marks+`)`, args...)
		if err != nil {
			continue
		}
		for _, row := range res.Rows {
			out[toString(row[0])] = toString(row[1])
		}
	}
	return out
}
