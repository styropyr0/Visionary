package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/chromedp"
)

// Each browser run is a trace: the browser sends a W3C traceparent with the
// run's trace id, so a backend instrumented with a Visionary SDK records its
// side under that id and the run links to it. The header only goes to the
// check's trace origins (by default the first page's), since an unexpected
// header on a cross-origin request can fail its CORS preflight.

const browserMaxTraceOrigins = 10

func newTraceIDs() (trace, span string) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	b[0] |= 1 // never all zeros
	b[16] |= 1
	return hex.EncodeToString(b[:16]), hex.EncodeToString(b[16:])
}

// normalizeOrigin reads "https://app.example.com" or "https://*.example.com",
// with or without a trailing slash, into scheme://host[:port].
func normalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "/")
	host, wild := raw, false
	if i := strings.Index(raw, "://*."); i >= 0 {
		host, wild = raw[:i+3]+raw[i+5:], true
	}
	u, err := url.Parse(host)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return "", fmt.Errorf("trace origin %q: use an origin such as https://api.example.com or https://*.example.com", raw)
	}
	h := strings.ToLower(u.Host)
	if wild {
		h = "*." + h
	}
	return u.Scheme + "://" + h, nil
}

// originOf is the origin of an absolute URL, "" when it has none.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + strings.ToLower(u.Host)
}

func (o *BrowserOptions) normalizeTrace() error {
	if len(o.TraceOrigins) > browserMaxTraceOrigins {
		return fmt.Errorf("at most %d trace origins", browserMaxTraceOrigins)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range o.TraceOrigins {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		v, err := normalizeOrigin(raw)
		if err != nil {
			return err
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	o.TraceOrigins = out
	if len(out) == 0 {
		o.TraceOrigins = nil
	}
	return nil
}

// tracePatterns are the Fetch patterns of the origins that get the header.
func (c UptimeCheck) tracePatterns() []*fetch.RequestPattern {
	if c.NoTrace {
		return nil
	}
	origins := c.TraceOrigins
	if len(origins) == 0 && len(c.Steps) > 0 {
		if o := originOf(resolveSecrets(c.Steps[0].Value, c.Secrets)); o != "" {
			origins = []string{o}
		}
	}
	var out []*fetch.RequestPattern
	for _, o := range origins {
		out = append(out, &fetch.RequestPattern{URLPattern: o + "/*"})
	}
	return out
}

// propagateTrace adds traceparent to every request the tab pauses on, until
// ctx ends. Requests are continued from tab, which outlives ctx, so none is
// left hanging.
func propagateTrace(ctx, tab context.Context, wg *sync.WaitGroup, traceparent string) {
	paused := chromedp.Events(ctx, fetch.RequestPaused)
	wg.Add(1)
	go func() {
		defer wg.Done()
		var inflight sync.WaitGroup
		defer inflight.Wait()
		for ev, err := range paused {
			if err != nil {
				return
			}
			params := fetch.ContinueRequestParams{RequestID: ev.RequestID}
			if ev.Request != nil {
				for k, v := range ev.Request.Headers {
					if !strings.EqualFold(k, "traceparent") {
						params.Headers = append(params.Headers, &fetch.HeaderEntry{Name: k, Value: fmt.Sprint(v)})
					}
				}
				params.Headers = append(params.Headers, &fetch.HeaderEntry{Name: "traceparent", Value: traceparent})
			}
			inflight.Add(1)
			go func() {
				defer inflight.Done()
				// It fails when the page cancelled the request meanwhile.
				_, _ = chromedp.Call(tab, fetch.ContinueRequest, params)
			}()
		}
	}()
}

// traceHasRows reports whether a backend recorded anything under the trace.
func (s *Server) traceHasRows(ctx context.Context, traceID string) bool {
	if traceID == "" {
		return false
	}
	for _, t := range []string{"api_logs", "spans"} {
		rows, err := s.db.Query(ctx, `SELECT 1 FROM `+t+` WHERE trace_id = ? LIMIT 1`, traceID)
		if err == nil && len(rows.Rows) > 0 {
			return true
		}
	}
	return false
}
