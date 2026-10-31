package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// API checks play an ordered list of HTTP requests. A request can extract
// values from its response into variables, used by later requests as
// {{var.NAME}}, and assert on its status, JSON body, headers, body text and
// response time. Secrets ({{secret.NAME}}) are kept like browser secrets and
// masked in everything a run records. Every request goes through the same
// address guard as HTTP checks.

// APIConfig is the settings of an API check.
type APIConfig struct {
	Requests []APIRequest `json:"requests"`
}

// APIRequest is one request of an API check.
type APIRequest struct {
	Name      string            `json:"name,omitempty"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	BodyType  string            `json:"body_type,omitempty"`
	Body      string            `json:"body,omitempty"`
	TimeoutMs int               `json:"timeout_ms,omitempty"`
	Extract   []APIExtract      `json:"extract,omitempty"`
	Assert    []APIAssertion    `json:"assert,omitempty"`
}

// APIExtract saves part of a response as a variable. Source is json (Path
// is a JSON path such as $.data.token), header (Path is its name), regex
// (Path is a pattern on the body; its first group, else the whole match) or
// status.
type APIExtract struct {
	Var    string `json:"var"`
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
}

// APIAssertion is a condition on a response. Source is status, json,
// header, body or time.
type APIAssertion struct {
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
	Op     string `json:"op"`
	Value  string `json:"value,omitempty"`
}

const (
	apiMaxRequests   = 10
	apiMaxExtracts   = 10
	apiMaxAssertions = 20
	apiMaxBody       = 64 << 10
	apiExcerpt       = 300
)

var (
	apiVarRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	apiBodyTypes = map[string]string{"": "", "json": "application/json", "text": "text/plain; charset=utf-8", "form": "application/x-www-form-urlencoded"}
	apiOps       = map[string]map[string]bool{
		"status": {"in": true, "equals": true, "not_equals": true, "lt": true, "lte": true, "gt": true, "gte": true},
		"json":   {"equals": true, "not_equals": true, "contains": true, "exists": true, "not_exists": true, "lt": true, "lte": true, "gt": true, "gte": true, "matches": true},
		"header": {"equals": true, "not_equals": true, "contains": true, "exists": true, "not_exists": true, "matches": true},
		"body":   {"contains": true, "not_contains": true, "matches": true},
		"time":   {"lte": true, "lt": true},
	}
	numericOps = map[string]bool{"lt": true, "lte": true, "gt": true, "gte": true}
)

func (c *UptimeCheck) normalizeAPI(prev *UptimeCheck) error {
	var prevSecrets map[string]string
	var prevReqs []APIRequest
	if prev != nil {
		prevSecrets = prev.Secrets
		if prev.API != nil {
			prevReqs = prev.API.Requests
		}
	}
	secrets, err := normalizeSecrets(c.Secrets, prevSecrets)
	if err != nil {
		return err
	}
	c.Secrets = secrets
	reqs := c.API.Requests
	if len(reqs) == 0 || len(reqs) > apiMaxRequests {
		return fmt.Errorf("an API check needs 1–%d requests", apiMaxRequests)
	}
	defined := map[string]bool{}
	for i := range reqs {
		var old *APIRequest
		if i < len(prevReqs) {
			old = &prevReqs[i]
		}
		if err := reqs[i].normalize(old, defined, secrets); err != nil {
			return fmt.Errorf("request %d: %w", i+1, err)
		}
		for _, ex := range reqs[i].Extract {
			defined[ex.Var] = true
		}
	}
	c.URL = reqs[0].URL
	return nil
}

func (rq *APIRequest) normalize(old *APIRequest, defined map[string]bool, secrets map[string]string) error {
	rq.Name = strings.TrimSpace(rq.Name)
	if len(rq.Name) > 120 {
		return errors.New("name is limited to 120 characters")
	}
	rq.Method = strings.ToUpper(strings.TrimSpace(rq.Method))
	if rq.Method == "" {
		rq.Method = "GET"
	}
	if !uptimeMethods[rq.Method] {
		return fmt.Errorf("method %q is not supported", rq.Method)
	}
	rq.URL = strings.TrimSpace(rq.URL)
	if len(rq.URL) > 2048 {
		return errors.New("URL is too long")
	}
	u, err := url.Parse(templateRefRe.ReplaceAllString(rq.URL, "x"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("URL must be an absolute http:// or https:// address")
	}
	if _, ok := apiBodyTypes[rq.BodyType]; !ok {
		return errors.New("body type must be json, text or form")
	}
	if len(rq.Body) > apiMaxBody {
		return errors.New("body is limited to 64 KB")
	}
	if rq.BodyType == "json" && rq.Body != "" && !json.Valid([]byte(templateRefRe.ReplaceAllString(rq.Body, "0"))) {
		return errors.New("body is not valid JSON")
	}
	if rq.TimeoutMs != 0 && (rq.TimeoutMs < 1000 || rq.TimeoutMs > 60000) {
		return errors.New("timeout must be between 1 and 60 seconds")
	}
	if len(rq.Headers) > uptimeMaxHeaders {
		return fmt.Errorf("at most %d headers", uptimeMaxHeaders)
	}
	headers := make(map[string]string, len(rq.Headers))
	for k, v := range rq.Headers {
		k = strings.TrimSpace(k)
		if !headerNameRe.MatchString(k) {
			return fmt.Errorf("header name %q is not valid", k)
		}
		if strings.ContainsAny(v, "\r\n") || len(v) > 4096 {
			return fmt.Errorf("header %s has an invalid value", k)
		}
		if v == uptimeMask {
			prev, ok := "", false
			if old != nil {
				prev, ok = headerValue(old.Headers, k)
			}
			if !ok {
				return fmt.Errorf("enter the value of header %s again", k)
			}
			v = prev
		}
		headers[k] = v
	}
	rq.Headers = headers
	if len(rq.Headers) == 0 {
		rq.Headers = nil
	}
	refs := rq.URL + "\n" + rq.Body
	for _, v := range rq.Headers {
		refs += "\n" + v
	}
	for _, m := range templateRefRe.FindAllStringSubmatch(refs, -1) {
		if _, ok := secrets[m[2]]; m[1] == "secret" && !ok {
			return fmt.Errorf("uses secret %s, which is not defined", m[2])
		}
		if m[1] == "var" && !defined[m[2]] {
			return fmt.Errorf("uses variable %s, which no earlier request extracts", m[2])
		}
	}
	if len(rq.Extract) > apiMaxExtracts {
		return fmt.Errorf("at most %d extractions", apiMaxExtracts)
	}
	for i := range rq.Extract {
		ex := &rq.Extract[i]
		ex.Var, ex.Path = strings.TrimSpace(ex.Var), strings.TrimSpace(ex.Path)
		if !apiVarRe.MatchString(ex.Var) {
			return fmt.Errorf("variable name %q: use letters, digits and _", ex.Var)
		}
		switch ex.Source {
		case "json":
			if _, err := parseJSONPath(ex.Path); err != nil {
				return fmt.Errorf("variable %s: %w", ex.Var, err)
			}
		case "header":
			if !headerNameRe.MatchString(ex.Path) {
				return fmt.Errorf("variable %s: give a header name", ex.Var)
			}
		case "regex":
			if _, err := regexp.Compile(ex.Path); err != nil || ex.Path == "" || len(ex.Path) > 500 {
				return fmt.Errorf("variable %s: the pattern is not a valid regular expression", ex.Var)
			}
		case "status":
			ex.Path = ""
		default:
			return fmt.Errorf("variable %s: extract from json, header, regex or status", ex.Var)
		}
	}
	if len(rq.Assert) > apiMaxAssertions {
		return fmt.Errorf("at most %d assertions", apiMaxAssertions)
	}
	for i := range rq.Assert {
		if err := rq.Assert[i].normalize(); err != nil {
			return fmt.Errorf("assertion %d: %w", i+1, err)
		}
	}
	return nil
}

func (a *APIAssertion) normalize() error {
	a.Path, a.Value = strings.TrimSpace(a.Path), strings.TrimSpace(a.Value)
	ops, ok := apiOps[a.Source]
	if !ok {
		return errors.New("check the status, json, header, body or time")
	}
	if !ops[a.Op] {
		return fmt.Errorf("%q does not apply to %s", a.Op, a.Source)
	}
	if len(a.Path) > 500 || len(a.Value) > 1000 {
		return errors.New("a field is too long")
	}
	switch a.Source {
	case "json":
		if _, err := parseJSONPath(a.Path); err != nil {
			return err
		}
	case "header":
		if !headerNameRe.MatchString(a.Path) {
			return errors.New("give a header name")
		}
	default:
		a.Path = ""
	}
	switch {
	case a.Op == "exists" || a.Op == "not_exists":
		a.Value = ""
	case a.Op == "in":
		if _, err := parseExpectStatus(strings.ReplaceAll(a.Value, " ", "")); err != nil {
			return err
		}
	case a.Op == "matches":
		if _, err := regexp.Compile(a.Value); err != nil || a.Value == "" {
			return errors.New("the pattern is not a valid regular expression")
		}
	case numericOps[a.Op] || a.Source == "status" || a.Source == "time":
		if _, err := strconv.ParseFloat(a.Value, 64); err != nil {
			return errors.New("give a number")
		}
	case a.Op == "contains" || a.Op == "not_contains":
		if a.Value == "" {
			return errors.New("give the text to look for")
		}
	}
	return nil
}

// describe is how a failed assertion reads in results.
func (a APIAssertion) describe() string {
	op := map[string]string{"in": "in", "equals": "=", "not_equals": "≠", "contains": "contains", "not_contains": "does not contain",
		"exists": "exists", "not_exists": "does not exist", "lt": "<", "lte": "≤", "gt": ">", "gte": "≥", "matches": "matches"}[a.Op]
	subject := map[string]string{"status": "status", "body": "body", "time": "response time"}[a.Source]
	if a.Source == "json" {
		subject = a.Path
	} else if a.Source == "header" {
		subject = "header " + a.Path
	}
	out := subject + " " + op
	switch {
	case a.Value == "":
	case a.Source == "time":
		out += " " + a.Value + " ms"
	case a.Source == "status" || numericOps[a.Op]:
		out += " " + a.Value
	default:
		out += " " + strconv.Quote(a.Value)
	}
	return out
}

// --- JSON paths ---

type pathStep struct {
	key   string
	index int
	isIdx bool
}

// parseJSONPath reads $.a.b[0]['c d'] (the $ is optional).
func parseJSONPath(p string) ([]pathStep, error) {
	if p == "" || len(p) > 500 {
		return nil, errors.New("give a JSON path such as $.data.token")
	}
	bad := fmt.Errorf("JSON path %q: use $.field, $.list[0] or $['field']", p)
	if p[0] != '$' {
		p = "$." + strings.TrimPrefix(p, ".")
	}
	var out []pathStep
	i := 1
	for i < len(p) {
		switch p[i] {
		case '.':
			j := i + 1
			for j < len(p) && p[j] != '.' && p[j] != '[' {
				j++
			}
			if j == i+1 {
				return nil, bad
			}
			out = append(out, pathStep{key: p[i+1 : j]})
			i = j
		case '[':
			end := strings.IndexByte(p[i:], ']')
			if end < 0 {
				return nil, bad
			}
			in := p[i+1 : i+end]
			if len(in) >= 2 && (in[0] == '\'' || in[0] == '"') && in[len(in)-1] == in[0] {
				out = append(out, pathStep{key: in[1 : len(in)-1]})
			} else if n, err := strconv.Atoi(in); err == nil {
				out = append(out, pathStep{index: n, isIdx: true})
			} else {
				return nil, bad
			}
			i += end + 1
		default:
			return nil, bad
		}
	}
	return out, nil
}

func jsonPathGet(doc any, steps []pathStep) (any, bool) {
	cur := doc
	for _, st := range steps {
		switch v := cur.(type) {
		case map[string]any:
			if st.isIdx {
				return nil, false
			}
			next, ok := v[st.key]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			if !st.isIdx {
				if st.key == "length" {
					cur = float64(len(v))
					continue
				}
				return nil, false
			}
			i := st.index
			if i < 0 {
				i += len(v)
			}
			if i < 0 || i >= len(v) {
				return nil, false
			}
			cur = v[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func jsonText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return "null"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// --- running ---

// apiResponse is what assertions and extractions read.
type apiResponse struct {
	status int
	header http.Header
	body   []byte
	ms     float64
	doc    any
	parsed bool
	docErr error
}

func (r *apiResponse) json() (any, error) {
	if !r.parsed {
		r.parsed = true
		r.docErr = json.Unmarshal(r.body, &r.doc)
		if r.docErr != nil {
			r.docErr = errors.New("the body is not JSON")
		}
	}
	return r.doc, r.docErr
}

// check returns "" when the assertion holds, else what was found.
func (a APIAssertion) check(r *apiResponse) (bool, string) {
	var got string
	present := true
	switch a.Source {
	case "status":
		got = strconv.Itoa(r.status)
	case "time":
		got = strconv.FormatFloat(float64(int64(r.ms)), 'f', -1, 64)
	case "body":
		got = string(r.body)
	case "header":
		vals := r.header.Values(a.Path)
		present = len(vals) > 0
		got = strings.Join(vals, ", ")
	case "json":
		doc, err := r.json()
		if err != nil {
			return a.Op == "not_exists", err.Error()
		}
		steps, _ := parseJSONPath(a.Path)
		v, ok := jsonPathGet(doc, steps)
		present = ok
		if ok {
			got = jsonText(v)
			if a.Op == "contains" {
				switch t := v.(type) {
				case []any:
					for _, e := range t {
						if jsonText(e) == a.Value {
							return true, ""
						}
					}
					return false, clip(got, 100)
				case map[string]any:
					_, has := t[a.Value]
					return has, clip(got, 100)
				}
			}
		}
	}
	switch a.Op {
	case "exists":
		return present, "missing"
	case "not_exists":
		return !present, "present"
	}
	if !present {
		return false, "missing"
	}
	show := func(ok bool) (bool, string) {
		if a.Source == "body" {
			return ok, ""
		}
		if a.Source == "time" {
			return ok, got + " ms"
		}
		return ok, clip(got, 100)
	}
	switch a.Op {
	case "in":
		rs, _ := parseExpectStatus(strings.ReplaceAll(a.Value, " ", ""))
		return show(statusAccepted(rs, r.status))
	case "equals", "not_equals":
		eq := got == a.Value
		if x, err1 := strconv.ParseFloat(got, 64); err1 == nil {
			if y, err2 := strconv.ParseFloat(a.Value, 64); err2 == nil {
				eq = x == y
			}
		}
		return show(eq == (a.Op == "equals"))
	case "contains":
		return show(strings.Contains(got, a.Value))
	case "not_contains":
		return show(!strings.Contains(got, a.Value))
	case "matches":
		re, err := regexp.Compile(a.Value)
		return show(err == nil && re.MatchString(got))
	case "lt", "lte", "gt", "gte":
		x, err1 := strconv.ParseFloat(got, 64)
		y, err2 := strconv.ParseFloat(a.Value, 64)
		if err1 != nil || err2 != nil {
			return false, clip(got, 100) + " (not a number)"
		}
		ok := map[string]bool{"lt": x < y, "lte": x <= y, "gt": x > y, "gte": x >= y}[a.Op]
		return show(ok)
	}
	return false, ""
}

func (ex APIExtract) extract(r *apiResponse) (string, error) {
	switch ex.Source {
	case "status":
		return strconv.Itoa(r.status), nil
	case "header":
		if v := r.header.Get(ex.Path); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("no header %s", ex.Path)
	case "regex":
		re, err := regexp.Compile(ex.Path)
		if err != nil {
			return "", err
		}
		m := re.FindSubmatch(r.body)
		if m == nil {
			return "", errors.New("the pattern did not match the body")
		}
		if len(m) > 1 {
			return string(m[1]), nil
		}
		return string(m[0]), nil
	}
	doc, err := r.json()
	if err != nil {
		return "", err
	}
	steps, _ := parseJSONPath(ex.Path)
	v, ok := jsonPathGet(doc, steps)
	if !ok {
		return "", fmt.Errorf("%s is not in the response", ex.Path)
	}
	return jsonText(v), nil
}

// apiRequestResult is how one request of a run did. Nothing in it holds a
// secret: URLs and texts are masked, and only a short excerpt of a failed
// response's body is kept.
type apiRequestResult struct {
	Name       string  `json:"name"`
	Method     string  `json:"method"`
	URL        string  `json:"url"`
	State      string  `json:"state"` // passed, failed, skipped
	Status     int     `json:"status,omitempty"`
	DurationMs float64 `json:"duration_ms,omitempty"`
	Size       int     `json:"size,omitempty"`
	Assertion  string  `json:"assertion,omitempty"`
	Got        string  `json:"got,omitempty"`
	Error      string  `json:"error,omitempty"`
	Excerpt    string  `json:"excerpt,omitempty"`
}

type apiDetail struct {
	Requests []apiRequestResult `json:"requests"`
}

func (rq APIRequest) label(i int) string {
	if rq.Name != "" {
		return rq.Name
	}
	return fmt.Sprintf("request %d", i+1)
}

// fill puts variables and secrets into a template.
func fill(s string, vars, secrets map[string]string) (string, error) {
	var missing error
	out := templateRefRe.ReplaceAllStringFunc(s, func(m string) string {
		p := templateRefRe.FindStringSubmatch(m)
		src := vars
		if p[1] == "secret" {
			src = secrets
		}
		v, ok := src[p[2]]
		if !ok && missing == nil {
			missing = fmt.Errorf("%s %s is not set", p[1], p[2])
		}
		return v
	})
	return out, missing
}

func (u *uptimeRunner) probeAPI(ctx context.Context, c UptimeCheck) (res uptimeResult) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutMs)*time.Millisecond)
	defer cancel()
	vars := map[string]string{}
	reqs := c.API.Requests
	detail := apiDetail{Requests: make([]apiRequestResult, len(reqs))}
	failed := -1
	for i, rq := range reqs {
		rr := &detail.Requests[i]
		rr.Name, rr.Method, rr.URL, rr.State = rq.label(i), rq.Method, rq.URL, "skipped"
		if failed >= 0 {
			continue
		}
		why, blocked := u.apiRequest(ctx, c, rq, vars, rr)
		if why != "" {
			failed, rr.State = i, "failed"
			res.Error, res.blocked = fmt.Sprintf("%s: %s", rq.label(i), why), blocked
			continue
		}
		rr.State = "passed"
	}
	// Secrets, and variables whose names look secret, never reach a record.
	vals := make([]string, 0, len(c.Secrets)+len(vars))
	for _, v := range c.Secrets {
		vals = append(vals, v)
	}
	for k, v := range vars {
		if secretHeaderRe.MatchString(k) {
			vals = append(vals, v)
		}
	}
	mask := maskValues(vals)
	for i := range detail.Requests {
		rr := &detail.Requests[i]
		rr.URL = mask(redactURL(rr.URL))
		rr.Got, rr.Error, rr.Excerpt = mask(rr.Got), mask(rr.Error), mask(rr.Excerpt)
		if rr.Status > 0 {
			res.Status = rr.Status
		}
	}
	res.Error = clip(mask(res.Error), 300)
	res.OK = failed < 0
	res.Detail = detail
	res.CreatedAt = start.UTC().Format(tsLayout)
	res.DurationMs = msSince(start)
	res.Degraded = res.OK && c.DegradedMs > 0 && res.DurationMs > float64(c.DegradedMs)
	return res
}

// apiRequest sends one request and checks its response, filling rr. It
// returns why the request failed, or "".
func (u *uptimeRunner) apiRequest(ctx context.Context, c UptimeCheck, rq APIRequest, vars map[string]string, rr *apiRequestResult) (string, bool) {
	target, err := fill(rq.URL, vars, c.Secrets)
	if err != nil {
		return err.Error(), false
	}
	rr.URL = target
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "the URL is not a valid http:// or https:// address", false
	}
	timeout := c.TimeoutMs
	if rq.TimeoutMs > 0 {
		timeout = min(timeout, rq.TimeoutMs)
	}
	if !u.allowPrivate() {
		if err := precheckURL(parsed); err != nil {
			return probeError(err, timeout), true
		}
	}
	body, err := fill(rq.Body, vars, c.Secrets)
	if err != nil {
		return err.Error(), false
	}
	if rq.BodyType == "form" && body != "" && !strings.Contains(body, "&") && strings.Contains(body, "\n") {
		form := url.Values{}
		for _, line := range strings.Split(body, "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				form.Add(k, v)
			}
		}
		body = form.Encode()
	}
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, rq.Method, target, reader)
	if err != nil {
		return "invalid request", false
	}
	req.Header.Set("User-Agent", uptimeUserAgent)
	if ct := apiBodyTypes[rq.BodyType]; ct != "" && body != "" {
		req.Header.Set("Content-Type", ct)
	}
	for k, v := range rq.Headers {
		v, err := fill(v, vars, c.Secrets)
		if err != nil {
			return err.Error(), false
		}
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	cc := c
	cc.TimeoutMs = timeout
	t0 := time.Now()
	resp, err := u.client(cc).Do(req)
	if err != nil {
		rr.DurationMs = msSince(t0)
		return probeError(err, timeout), errors.Is(err, errBlockedAddress)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, uptimeMaxBody))
	r := &apiResponse{status: resp.StatusCode, header: resp.Header, body: raw, ms: msSince(t0)}
	rr.Status, rr.DurationMs, rr.Size = r.status, r.ms, len(raw)
	if readErr != nil {
		return probeError(readErr, timeout), false
	}
	excerpt := func() string { return clip(string(bytes.TrimSpace(raw)), apiExcerpt) }
	hasStatus := false
	for _, a := range rq.Assert {
		hasStatus = hasStatus || a.Source == "status"
		if ok, got := a.check(r); !ok {
			rr.Assertion, rr.Got, rr.Excerpt = a.describe(), got, excerpt()
			msg := "expected " + a.describe()
			if got != "" {
				msg += " (got " + got + ")"
			}
			return msg, false
		}
	}
	if !hasStatus && r.status >= 400 {
		rr.Assertion, rr.Got, rr.Excerpt = "status < 400", strconv.Itoa(r.status), excerpt()
		return fmt.Sprintf("unexpected status %d", r.status), false
	}
	for _, ex := range rq.Extract {
		v, err := ex.extract(r)
		if err != nil {
			rr.Excerpt = excerpt()
			return fmt.Sprintf("could not set %s: %s", ex.Var, err), false
		}
		vars[ex.Var] = v
	}
	return "", false
}
