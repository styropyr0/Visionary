package analytics

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The log archive: every finished hour of app_logs is written to an
// S3-compatible bucket (AWS S3, Cloudflare R2, MinIO, Backblaze B2, rclone)
// as gzipped NDJSON, one object per hour, so logs outlive Settings →
// Retention cheaply. Archived hours are listed in log_archives; an admin
// can load a range back into app_logs (rehydrate), where it is searched like
// any other line. Configured in Settings → Server → Log archive.
//
//	GET  /api/applogs/archives            archived hours and the archive's state
//	POST /api/applogs/archives/rehydrate  load {from, to} back

const (
	archiveEvery     = 10 * time.Minute
	archiveHoursRun  = 6
	archivePage      = 5000
	maxRehydrateHrs  = 72
	archiveObjectExt = ".ndjson.gz"
)

func (s *Server) applogsArchiveRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/applogs/archives", s.authed(s.handleLogArchives))
	mux.Handle("POST /api/applogs/archives/rehydrate", s.authed(s.handleRehydrate))
}

// archiveConf is the bucket from Settings; ok is false while it is not set.
type archiveConf struct {
	endpoint, bucket, region, access, secret, prefix string
}

func (s *Server) archiveConf() (archiveConf, bool) {
	c := archiveConf{
		endpoint: strings.TrimRight(strings.TrimSpace(s.conf("logs_archive_endpoint")), "/"),
		bucket:   strings.TrimSpace(s.conf("logs_archive_bucket")),
		region:   strings.TrimSpace(s.conf("logs_archive_region")),
		access:   strings.TrimSpace(s.conf("logs_archive_access_key")),
		secret:   strings.TrimSpace(s.conf("logs_archive_secret_key")),
		prefix:   strings.Trim(strings.TrimSpace(s.conf("logs_archive_prefix")), "/"),
	}
	if c.region == "" {
		c.region = "auto"
	}
	return c, c.endpoint != "" && c.bucket != "" && c.access != "" && c.secret != ""
}

// objectKey names an hour's object: <prefix>/2026/10/28/11.ndjson.gz.
func (c archiveConf) objectKey(hour time.Time) string {
	k := hour.UTC().Format("2006/01/02/15") + archiveObjectExt
	if c.prefix != "" {
		k = c.prefix + "/" + k
	}
	return k
}

// objectURL is the path-style URL of a key, which every S3-compatible
// store accepts.
func (c archiveConf) objectURL(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return c.endpoint + "/" + url.PathEscape(c.bucket) + "/" + strings.Join(parts, "/")
}

// sign adds AWS Signature Version 4 headers to req, with an unsigned payload
// (allowed by S3 and the stores that copy it).
func (c archiveConf) sign(req *http.Request, now time.Time) {
	now = now.UTC()
	date, stamp := now.Format("20060102"), now.Format("20060102T150405Z")
	req.Header.Set("x-amz-date", stamp)
	req.Header.Set("x-amz-content-sha256", "UNSIGNED-PAYLOAD")
	host := req.URL.Host
	signedNames := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	headers := map[string]string{"host": host, "x-amz-content-sha256": "UNSIGNED-PAYLOAD", "x-amz-date": stamp}
	var canonHeaders strings.Builder
	for _, n := range signedNames {
		canonHeaders.WriteString(n + ":" + strings.TrimSpace(headers[n]) + "\n")
	}
	canonical := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		canonicalQuery(req.URL.Query()),
		canonHeaders.String(),
		strings.Join(signedNames, ";"),
		"UNSIGNED-PAYLOAD",
	}, "\n")
	scope := date + "/" + c.region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	key := hmacSHA256([]byte("AWS4"+c.secret), date)
	key = hmacSHA256(key, c.region)
	key = hmacSHA256(key, "s3")
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.access+"/"+scope+", SignedHeaders="+strings.Join(signedNames, ";")+", Signature="+sig)
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// canonicalQuery sorts and escapes the query the way SigV4 wants.
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func awsEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

var archiveClient = &http.Client{Timeout: 10 * time.Minute}

func (c archiveConf) put(ctx context.Context, key string, body io.ReadSeeker, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.objectURL(key), body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	// A .gz file, not gzip content encoding: stores would hand it back
	// decompressed, and clients decompress it on the way.
	req.Header.Set("Content-Type", "application/gzip")
	c.sign(req, time.Now())
	resp, err := archiveClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("put %s: %s %s", key, resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c archiveConf) get(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.objectURL(key), nil)
	if err != nil {
		return nil, err
	}
	// Ask for the bytes as stored, so the HTTP client does not unzip them.
	req.Header.Set("Accept-Encoding", "identity")
	c.sign(req, time.Now())
	resp, err := archiveClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("get %s: %s %s", key, resp.Status, strings.TrimSpace(string(b)))
	}
	return resp.Body, nil
}

// archiveState is the archiver's last outcome, for Settings.
type archiveState struct {
	mu      sync.Mutex
	lastRun time.Time
	lastErr string
}

var archiveStates sync.Map // *Server → *archiveState

func (s *Server) archiveState() *archiveState {
	v, _ := archiveStates.LoadOrStore(s, &archiveState{})
	return v.(*archiveState)
}

// applogsArchiveStart archives finished hours every ten minutes while a
// bucket is set.
func (s *Server) applogsArchiveStart(ctx context.Context) {
	go func() {
		t := time.NewTimer(2 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if _, ok := s.archiveConf(); ok {
				n, err := s.archivePass(ctx, time.Now())
				st := s.archiveState()
				st.mu.Lock()
				st.lastRun = time.Now()
				st.lastErr = ""
				if err != nil {
					st.lastErr = err.Error()
				}
				st.mu.Unlock()
				if err != nil && ctx.Err() == nil {
					s.logger.Warn("logs: archive", "err", err)
				} else if n > 0 {
					s.logger.Info("logs: archived", "hours", n)
				}
			}
			t.Reset(archiveEvery)
		}
	}()
}

// archivePass writes the oldest finished hours not yet archived, at most
// archiveHoursRun of them; it returns how many it wrote.
func (s *Server) archivePass(ctx context.Context, now time.Time) (int, error) {
	c, ok := s.archiveConf()
	if !ok {
		return 0, nil
	}
	done := now.UTC().Truncate(time.Hour) // hours before this have ended
	res, err := s.db.Query(ctx, `SELECT MIN(created_at) FROM app_logs`)
	if err != nil || len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return 0, err
	}
	first, err := time.Parse(tsLayout, toString(res.Rows[0][0]))
	if err != nil {
		return 0, nil
	}
	have := map[string]bool{}
	if ar, err := s.db.Query(ctx, `SELECT hour FROM log_archives WHERE hour >= ?`, first.Truncate(time.Hour).Format(tsLayout)); err == nil {
		for _, row := range ar.Rows {
			have[toString(row[0])] = true
		}
	}
	wrote := 0
	for h := first.UTC().Truncate(time.Hour); h.Before(done) && wrote < archiveHoursRun; h = h.Add(time.Hour) {
		key := h.Format(tsLayout)
		if have[key] {
			continue
		}
		lines, bytes, err := s.archiveHour(ctx, c, h)
		if err != nil {
			return wrote, err
		}
		if err := s.db.Exec(ctx, `INSERT INTO log_archives (hour, object, lines, bytes, created_at) VALUES (?, ?, ?, ?, ?)`,
			key, c.objectKey(h), lines, bytes, time.Now().UTC().Format(tsLayout)); err != nil {
			return wrote, dbErr(err)
		}
		wrote++
	}
	return wrote, nil
}

// archiveColumns are written for each line; rehydrate reads them back.
var archiveColumns = []string{"created_at", "service", "instance", "version", "source", "host", "container", "level", "severity", "logger",
	"message", "attrs", "request_id", "trace_id", "span_id", "user_id", "session_id", "code_file", "code_line", "code_func"}

// archiveHour writes one hour's lines to the bucket, through a temporary
// file so an hour of any size is sent with a known length.
func (s *Server) archiveHour(ctx context.Context, c archiveConf, hour time.Time) (int64, int64, error) {
	f, err := os.CreateTemp("", "visionary-archive-*.ndjson.gz")
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	zw := gzip.NewWriter(f)
	enc := json.NewEncoder(zw)
	from, to := hour.Format(tsLayout), hour.Add(time.Hour).Format(tsLayout)
	var lines int64
	var after int64
	for {
		rows, err := s.db.Query(ctx, `SELECT id, `+strings.Join(archiveColumns, ", ")+` FROM app_logs WHERE created_at >= ? AND created_at < ? AND id > ? ORDER BY id LIMIT `+strconv.Itoa(archivePage),
			from, to, after)
		if err != nil {
			return 0, 0, dbErr(err)
		}
		for _, row := range rows.Rows {
			after = toInt(row[0])
			m := make(map[string]any, len(archiveColumns))
			for i, c := range archiveColumns {
				if v := row[i+1]; v != nil {
					m[c] = v
				}
			}
			if err := enc.Encode(m); err != nil {
				return 0, 0, err
			}
			lines++
		}
		if len(rows.Rows) < archivePage {
			break
		}
	}
	if err := zw.Close(); err != nil {
		return 0, 0, err
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, 0, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, 0, err
	}
	if err := c.put(ctx, c.objectKey(hour), f, size); err != nil {
		return 0, 0, err
	}
	return lines, size, nil
}

func (s *Server) handleLogArchives(w http.ResponseWriter, r *http.Request) {
	c, ok := s.archiveConf()
	st := s.archiveState()
	st.mu.Lock()
	last, lastErr := st.lastRun, st.lastErr
	st.mu.Unlock()
	out := map[string]any{"configured": ok, "bucket": c.bucket, "endpoint": c.endpoint, "prefix": c.prefix, "last_error": lastErr, "hours": []any{}}
	if !last.IsZero() {
		out["last_run"] = last.UTC().Format(tsLayout)
	}
	rows, err := s.db.Query(r.Context(), `SELECT hour, object, lines, bytes, created_at FROM log_archives ORDER BY hour DESC LIMIT 500`)
	if err == nil {
		out["hours"] = newResult(rows, 500, "", timeRange{})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRehydrate loads archived hours back into app_logs. Hours that
// still have lines are skipped, so nothing is loaded twice.
func (s *Server) handleRehydrate(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	c, ok := s.archiveConf()
	if !ok {
		writeError(w, http.StatusBadRequest, "set up the log archive in Settings → Server first")
		return
	}
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	from, err1 := time.Parse(time.RFC3339, body.From)
	to, err2 := time.Parse(time.RFC3339, body.To)
	if err1 != nil || err2 != nil || !to.After(from) {
		writeError(w, http.StatusBadRequest, "from and to are RFC 3339 times, from before to")
		return
	}
	from, to = from.UTC().Truncate(time.Hour), to.UTC()
	if to.Sub(from) > maxRehydrateHrs*time.Hour {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("load at most %d hours at a time", maxRehydrateHrs))
		return
	}
	ctx := r.Context()
	rows, err := s.db.Query(ctx, `SELECT hour, object FROM log_archives WHERE hour >= ? AND hour < ? ORDER BY hour`, from.Format(tsLayout), to.Format(tsLayout))
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	type result struct {
		Hour    string `json:"hour"`
		Lines   int    `json:"lines"`
		Skipped string `json:"skipped,omitempty"`
	}
	var out []result
	total := 0
	for _, row := range rows.Rows {
		hour, object := toString(row[0]), toString(row[1])
		h, _ := time.Parse(tsLayout, hour)
		n, err := s.db.Query(ctx, `SELECT COUNT(*) FROM app_logs WHERE created_at >= ? AND created_at < ?`, hour, h.Add(time.Hour).Format(tsLayout))
		if err == nil && len(n.Rows) > 0 && toInt(n.Rows[0][0]) > 0 {
			out = append(out, result{Hour: hour, Skipped: "its lines are still stored"})
			continue
		}
		count, err := s.rehydrateObject(ctx, c, object)
		if err != nil {
			s.logger.Warn("logs: rehydrate", "object", object, "err", err)
			out = append(out, result{Hour: hour, Skipped: err.Error()})
			continue
		}
		total += count
		out = append(out, result{Hour: hour, Lines: count})
	}
	s.audit(r, "logs.rehydrate", "", map[string]any{"from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339), "lines": total})
	writeJSON(w, http.StatusOK, map[string]any{"lines": total, "hours": out})
}

// rehydrateObject reads one archived hour back into app_logs, as written:
// the lines already passed the pipeline once.
func (s *Server) rehydrateObject(ctx context.Context, c archiveConf, key string) (int, error) {
	body, err := c.get(ctx, key)
	if err != nil {
		return 0, err
	}
	defer body.Close()
	// Gzipped as written; plain when a store or proxy unzipped it.
	br := bufio.NewReader(body)
	var src io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return 0, errors.New("the archived object is damaged")
		}
		src = zr
	}
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	t := &otlpTable{"app_logs", append(append([]string{}, archiveColumns...), "size")}
	rows := &otlpRows{}
	n := 0
	flush := func() error {
		if rows.empty() {
			return nil
		}
		err := s.otlp().write(ctx, rows)
		rows = &otlpRows{}
		return err
	}
	for sc.Scan() {
		dec := json.NewDecoder(strings.NewReader(sc.Text()))
		dec.UseNumber()
		var m map[string]any
		if dec.Decode(&m) != nil {
			continue
		}
		row := make([]any, 0, len(t.cols))
		size := 0
		for _, col := range archiveColumns {
			v := m[col]
			if num, ok := v.(json.Number); ok {
				if i, err := num.Int64(); err == nil {
					v = i
				}
			}
			if str, ok := v.(string); ok && (col == "message" || col == "attrs") {
				size += len(str)
			}
			row = append(row, v)
		}
		row = append(row, size)
		rows.add(t, row)
		n++
		if n%1000 == 0 {
			if err := flush(); err != nil {
				return n, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return n, err
	}
	return n, flush()
}
