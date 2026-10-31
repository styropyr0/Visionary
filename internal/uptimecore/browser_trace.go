package uptimecore

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

// Only the check's trace origins get a traceparent, since it can fail
// the CORS preflight of a cross-origin request

const browserMaxTraceOrigins = 10

func newTraceIDs() (trace, span string) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	b[0] |= 1
	b[16] |= 1
	return hex.EncodeToString(b[:16]), hex.EncodeToString(b[16:])
}

func NormalizeOrigin(raw string) (string, error) {
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

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + strings.ToLower(u.Host)
}

func (o *BrowserOptions) NormalizeTrace() error {
	if len(o.TraceOrigins) > browserMaxTraceOrigins {
		return fmt.Errorf("at most %d trace origins", browserMaxTraceOrigins)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range o.TraceOrigins {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		v, err := NormalizeOrigin(raw)
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

func (c Check) tracePatterns() []*fetch.RequestPattern {
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

// Requests are continued from tab, which outlives ctx, so none is left hanging
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
				_, _ = chromedp.Call(tab, fetch.ContinueRequest, params)
			}()
		}
	}()
}
