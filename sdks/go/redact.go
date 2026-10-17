package visionary

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const redacted = "[REDACTED]"

// Defaults cover the usual credentials; deployments add their own through
// RedactHeaders / RedactFields / RedactRequestFields (VISIONARY_LOG_REDACT_*).
var (
	defaultRedactHeaders = []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "X-Auth-Token"}
	defaultRedactFields  = []string{
		"password", "passwd", "secret", "client_secret", "token", "access_token", "refresh_token", "id_token",
		"api_key", "apikey", "otp", "pin", "card_number", "cvv", "private_key",
	}
)

type redactor struct {
	headerSet map[string]bool
	respJSON  *regexp.Regexp
	reqJSON   *regexp.Regexp // also covers the request-only fields
	form      *regexp.Regexp
}

func newRedactor(extraHeaders, extraFields, requestOnly []string) *redactor {
	h := map[string]bool{}
	for _, name := range append(append([]string{}, defaultRedactHeaders...), extraHeaders...) {
		h[http.CanonicalHeaderKey(strings.TrimSpace(name))] = true
	}
	both := keyPattern(append(append([]string{}, defaultRedactFields...), extraFields...))
	reqOnly := keyPattern(requestOnly)
	reqKeys := both
	if reqOnly != "" {
		reqKeys += "|" + reqOnly
	}
	return &redactor{
		headerSet: h,
		respJSON:  jsonSecrets(both),
		reqJSON:   jsonSecrets(reqKeys),
		form:      regexp.MustCompile(`(?i)(^|&)(` + reqKeys + `)=[^&]*`),
	}
}

func keyPattern(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			quoted = append(quoted, regexp.QuoteMeta(n))
		}
	}
	return strings.Join(quoted, "|")
}

// jsonSecrets matches "key": value pairs. It is regex-based on purpose, so
// bodies cut off by the capture cap (invalid JSON) are still scrubbed.
func jsonSecrets(keys string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)("(?:` + keys + `)"\s*:\s*)("(?:[^"\\]|\\.)*"?|[^,}\]\s]+)`)
}

func (r *redactor) headers(h http.Header) string {
	if len(h) == 0 {
		return ""
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if r.headerSet[http.CanonicalHeaderKey(k)] {
			out[k] = redacted
			continue
		}
		out[k] = strings.Join(v, ", ")
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

func (r *redactor) requestBody(body []byte, contentType string) string {
	if len(body) == 0 {
		return ""
	}
	if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		return r.form.ReplaceAllString(string(body), "${1}${2}="+redacted)
	}
	return r.reqJSON.ReplaceAllString(string(body), `${1}"`+redacted+`"`)
}

func (r *redactor) responseBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return r.respJSON.ReplaceAllString(string(body), `${1}"`+redacted+`"`)
}

// errorFields reads the error code and key from a JSON error response at the
// configured dotted paths. Bodies cut off by the capture cap fall back to a
// pattern match on the last path segment.
func errorFields(body []byte, codePath, keyPath string) (code, key string) {
	var v any
	if json.Unmarshal(body, &v) == nil {
		return lookupString(v, codePath), lookupString(v, keyPath)
	}
	return matchField(body, codePath), matchField(body, keyPath)
}

func lookupString(v any, path string) string {
	if path == "" {
		return ""
	}
	for _, seg := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[seg]
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

func matchField(body []byte, path string) string {
	if path == "" {
		return ""
	}
	last := path[strings.LastIndex(path, ".")+1:]
	re := regexp.MustCompile(`"` + regexp.QuoteMeta(last) + `"\s*:\s*"([^"]*)"`)
	if m := re.FindSubmatch(body); m != nil {
		return string(m[1])
	}
	return ""
}
