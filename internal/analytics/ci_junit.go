package analytics

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// JUnit XML uploads: pytest, Jest (jest-junit), Maven Surefire and
// Gradle, go-junit-report, and anything else that writes the format. One
// row per test case per upload, tied to a run, commit, branch and service.

const (
	ciMaxReport     = 32 << 20 // per report, after gunzip
	ciMaxUpload     = 64 << 20 // all reports of one request
	ciMaxCases      = 20000
	ciMaxMessage    = 2000
	ciMaxTestName   = 500
	ciMaxReportsPer = 50
)

var ciTestsTable = &otlpTable{"ci_tests", []string{
	"created_at", "run_key", "provider", "repo", "service", "branch", "commit_sha", "job", "suite", "test", "test_hash", "file", "status", "duration_ms", "message",
}}

type junitIssue struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

type junitCase struct {
	Name          string       `xml:"name,attr"`
	Classname     string       `xml:"classname,attr"`
	File          string       `xml:"file,attr"`
	Time          string       `xml:"time,attr"`
	Failures      []junitIssue `xml:"failure"`
	Errors        []junitIssue `xml:"error"`
	Skipped       *junitIssue  `xml:"skipped"`
	FlakyFailures []junitIssue `xml:"flakyFailure"`
	FlakyErrors   []junitIssue `xml:"flakyError"`
}

// junitNode is <testsuites> or <testsuite>, which may nest.
type junitNode struct {
	XMLName xml.Name
	Name    string      `xml:"name,attr"`
	File    string      `xml:"file,attr"`
	Suites  []junitNode `xml:"testsuite"`
	Cases   []junitCase `xml:"testcase"`
}

// ciTestCase is one result to store.
type ciTestCase struct {
	Suite, Test, File, Status string
	DurationMs                *float64
	Message                   string
}

// parseJUnit reads one report. A test that passed on a rerun (Surefire's
// flakyFailure) is a pass plus a failure on the same commit, which is what
// makes it flaky.
func parseJUnit(data []byte) ([]ciTestCase, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	// Reports declare ISO-8859-1 now and then; their test names are ASCII.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	var root junitNode
	if err := dec.Decode(&root); err != nil {
		return nil, errors.New("not valid XML: " + err.Error())
	}
	if root.XMLName.Local != "testsuites" && root.XMLName.Local != "testsuite" {
		return nil, errors.New("not a JUnit report: the root is <" + root.XMLName.Local + ">")
	}
	var out []ciTestCase
	var walk func(n junitNode, suite, file string)
	walk = func(n junitNode, suite, file string) {
		if n.XMLName.Local == "testsuite" && n.Name != "" {
			suite = n.Name
		}
		if n.File != "" {
			file = n.File
		}
		for _, c := range n.Cases {
			if len(out) >= ciMaxCases {
				return
			}
			tc := ciTestCase{Suite: ciClip(suite, 300), Test: ciClip(junitTestName(c.Classname, c.Name), ciMaxTestName), File: ciClip(fallback(c.File, file), 500), Status: "passed"}
			if tc.Test == "" {
				continue
			}
			if d, ok := junitSeconds(c.Time); ok {
				ms := math.Round(d*1000*100) / 100
				tc.DurationMs = &ms
			}
			switch {
			case len(c.Failures) > 0:
				tc.Status, tc.Message = "failed", junitMessage(c.Failures[0])
			case len(c.Errors) > 0:
				tc.Status, tc.Message = "error", junitMessage(c.Errors[0])
			case c.Skipped != nil:
				tc.Status, tc.Message = "skipped", ciClip(fallback(c.Skipped.Message, c.Skipped.Text), ciMaxMessage)
			}
			out = append(out, tc)
			if tc.Status == "passed" {
				for _, f := range append(append([]junitIssue{}, c.FlakyFailures...), c.FlakyErrors...) {
					retry := tc
					retry.Status, retry.Message, retry.DurationMs = "failed", junitMessage(f), nil
					out = append(out, retry)
				}
			}
		}
		for _, sub := range n.Suites {
			walk(sub, suite, file)
		}
	}
	walk(root, "", "")
	if len(out) == 0 {
		return nil, errors.New("the report has no test cases")
	}
	return out, nil
}

// junitTestName joins class and name, unless the name already starts with
// the class (jest-junit repeats it by default).
func junitTestName(class, name string) string {
	class, name = strings.TrimSpace(class), strings.TrimSpace(name)
	switch {
	case name == "":
		return class
	case class == "" || strings.HasPrefix(name, class):
		return name
	}
	return class + "." + name
}

// junitSeconds reads a time attribute; Surefire writes "1,234.5" in some locales.
func junitSeconds(v string) (float64, bool) {
	v = strings.ReplaceAll(strings.TrimSpace(v), ",", "")
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// junitMessage is the failure's message and the start of its output.
func junitMessage(i junitIssue) string {
	msg, text := strings.TrimSpace(i.Message), strings.TrimSpace(i.Text)
	switch {
	case msg == "" && text == "":
		return strings.TrimSpace(i.Type)
	case msg == "":
		return ciClip(text, ciMaxMessage)
	case text == "" || strings.HasPrefix(text, msg):
		return ciClip(fallback(text, msg), ciMaxMessage)
	}
	return ciClip(msg+"\n"+text, ciMaxMessage)
}

// gunzipMaybe inflates gzip data (by its magic bytes) up to limit.
func gunzipMaybe(b []byte, limit int64) ([]byte, error) {
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		return b, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("the gzip data is invalid")
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, limit+1))
	if err != nil {
		return nil, errors.New("the gzip data is invalid")
	}
	if int64(len(out)) > limit {
		return nil, errors.New("a report is larger than 32 MB")
	}
	return out, nil
}

// readJUnitUpload returns the reports and the fields that came with them:
// query parameters, plus form fields of a multipart upload.
func readJUnitUpload(w http.ResponseWriter, r *http.Request) ([][]byte, url.Values, int, error) {
	fields := r.URL.Query()
	mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "multipart/form-data" {
		body, status, err := otlpReadBody(w, r)
		if err != nil {
			return nil, nil, status, err
		}
		body, err = gunzipMaybe(body, ciMaxReport)
		if err != nil {
			return nil, nil, http.StatusBadRequest, err
		}
		return [][]byte{body}, fields, 0, nil
	}
	mr, err := r.MultipartReader()
	if err != nil || params["boundary"] == "" {
		return nil, nil, http.StatusBadRequest, errors.New("the multipart body is invalid")
	}
	r.Body = http.MaxBytesReader(w, r.Body, ciMaxUpload)
	var reports [][]byte
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, otlpReadStatus(err), errors.New("the multipart body could not be read")
		}
		if part.FileName() == "" {
			v, _ := io.ReadAll(io.LimitReader(part, 4096))
			if name := part.FormName(); name != "" && fields.Get(name) == "" {
				fields.Set(name, strings.TrimSpace(string(v)))
			}
			continue
		}
		if len(reports) == ciMaxReportsPer {
			return nil, nil, http.StatusBadRequest, errors.New("at most 50 reports per upload")
		}
		data, err := io.ReadAll(io.LimitReader(part, ciMaxReport+1))
		if err != nil {
			return nil, nil, otlpReadStatus(err), errors.New("a report could not be read")
		}
		if data, err = gunzipMaybe(data, ciMaxReport); err != nil {
			return nil, nil, http.StatusBadRequest, err
		}
		if len(data) > ciMaxReport {
			return nil, nil, http.StatusRequestEntityTooLarge, errors.New("a report is larger than 32 MB")
		}
		reports = append(reports, data)
	}
	if len(reports) == 0 {
		return nil, nil, http.StatusBadRequest, errors.New("attach the JUnit XML as a file, e.g. -F file=@junit.xml")
	}
	return reports, fields, 0, nil
}

func (s *Server) handleJUnit(w http.ResponseWriter, r *http.Request) {
	if !s.ingestKeyValid(logIngestKey(r), "server") {
		writeError(w, http.StatusUnauthorized, "missing or invalid ingest key")
		return
	}
	reports, f, status, err := readJUnitUpload(w, r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	var cases []ciTestCase
	for i, rep := range reports {
		got, err := parseJUnit(rep)
		if err != nil {
			msg := err.Error()
			if len(reports) > 1 {
				msg = "report " + strconv.Itoa(i+1) + ": " + msg
			}
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		cases = append(cases, got...)
	}
	if len(cases) > ciMaxCases {
		cases = cases[:ciMaxCases]
	}
	up := ciUpload{
		provider: strings.ToLower(fallback(strings.TrimSpace(f.Get("provider")), "ci")), repo: strings.TrimSpace(f.Get("repo")),
		runID: strings.TrimSpace(f.Get("run_id")), branch: strings.TrimSpace(f.Get("branch")), commit: strings.ToLower(strings.TrimSpace(f.Get("commit"))),
		service: strings.TrimSpace(f.Get("service")), job: ciClip(f.Get("job"), 300), at: time.Now().UTC(),
	}
	up.attempt, _ = strconv.ParseInt(f.Get("attempt"), 10, 64)
	if v := f.Get("at"); v != "" {
		if up.at = ciTime(v); up.at.IsZero() {
			writeError(w, http.StatusBadRequest, "at must be an RFC 3339 time")
			return
		}
	}
	if len(up.provider) > 40 || len(up.repo) > 200 || len(up.runID) > 100 || len(up.branch) > 300 || len(up.commit) > 64 || len(up.service) > 200 {
		writeError(w, http.StatusBadRequest, "a field is too long")
		return
	}
	counts, err := s.saveTests(r.Context(), up, cases)
	if err != nil {
		s.logger.Warn("ci junit", "err", err)
		writeError(w, http.StatusServiceUnavailable, "the test results could not be stored")
		return
	}
	writeJSON(w, http.StatusAccepted, counts)
}

type ciUpload struct {
	provider, repo, runID, branch, commit, service, job string
	attempt                                             int64
	at                                                  time.Time
}

// saveTests stores the cases, filling the commit, branch and service from
// the run when it is known, then looks for newly flaky tests among them.
func (s *Server) saveTests(ctx context.Context, up ciUpload, cases []ciTestCase) (map[string]int, error) {
	runKey := ""
	if up.runID != "" {
		runKey = ciRunKey(up.provider, up.repo, up.runID, up.attempt)
		res, err := s.db.Query(ctx, `SELECT commit_sha, branch, service FROM ci_pipelines WHERE run_key = ?`, runKey)
		if err == nil && len(res.Rows) > 0 {
			row := res.Rows[0]
			up.commit = fallback(toString(row[0]), up.commit)
			up.branch = fallback(toString(row[1]), up.branch)
			up.service = fallback(up.service, toString(row[2]))
		}
	}
	scope := fallback(up.service, up.repo)
	counts := map[string]int{"tests": 0, "passed": 0, "failed": 0, "error": 0, "skipped": 0}
	var rows otlpRows
	hashes := map[string]string{}
	at := up.at.Format(tsLayout)
	for _, c := range cases {
		h := ciTestHash(scope, c.Test)
		hashes[h] = c.Test
		var dur any
		if c.DurationMs != nil {
			dur = *c.DurationMs
		}
		rows.add(ciTestsTable, []any{at, otlpNullStr(runKey), otlpNullStr(up.provider), otlpNullStr(up.repo), otlpNullStr(up.service),
			otlpNullStr(up.branch), otlpNullStr(up.commit), otlpNullStr(up.job), otlpNullStr(c.Suite), c.Test, h, otlpNullStr(c.File),
			c.Status, dur, otlpNullStr(c.Message)})
		counts["tests"]++
		counts[c.Status]++
	}
	if err := s.otlp().write(ctx, &rows); err != nil {
		return nil, err
	}
	if err := s.noteFlaky(ctx, hashes, up); err != nil {
		s.logger.Warn("ci flaky tests", "err", err)
	}
	return counts, nil
}
