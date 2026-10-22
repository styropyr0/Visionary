package visionary

import (
	"net/http"
	"strconv"
	"sync"
)

// Transport returns an http.RoundTripper that records each outbound request
// as an http span ("POST api.stripe.com"): the time until the response
// headers arrive, and an error on a transport failure or a 5xx. A nil base
// means http.DefaultTransport. Query strings are never stored.
//
//	client := &http.Client{Timeout: 10 * time.Second, Transport: visionary.Transport(nil)}
//
// Requests must carry the handler's context (http.NewRequestWithContext) to
// be linked to the request being served.
func Transport(base http.RoundTripper) http.RoundTripper {
	httpSeen.Store(true)
	if base == nil {
		base = http.DefaultTransport
	}
	if t, ok := base.(*tracedTransport); ok {
		return t
	}
	return &tracedTransport{base: base}
}

var defaultClientOnce sync.Once

// instrumentDefaultClient makes http.Get, http.Post and other calls through
// http.DefaultClient record spans. http.DefaultTransport itself is left
// alone: code often asserts it is an *http.Transport to clone it.
func instrumentDefaultClient() {
	defaultClientOnce.Do(func() {
		if http.DefaultClient.Transport == nil {
			http.DefaultClient.Transport = Transport(nil)
		}
	})
}

type tracedTransport struct{ base http.RoundTripper }

func (t *tracedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s := beginSpan(req.Context(), KindHTTP)
	if s == nil {
		return t.base.RoundTrip(req)
	}
	resp, err := t.base.RoundTrip(req)
	host := req.URL.Host
	s.rec.name = req.Method + " " + host
	s.rec.target = host
	s.rec.detail = req.Method + " " + req.URL.Scheme + "://" + host + req.URL.EscapedPath()
	if err == nil && resp.StatusCode >= 500 {
		s.fail("HTTP " + strconv.Itoa(resp.StatusCode))
	}
	s.end(err)
	return resp, err
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the base.
func (t *tracedTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
