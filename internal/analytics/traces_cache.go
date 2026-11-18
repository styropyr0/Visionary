package analytics

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Trace search and the service map read many rows. Each ask's range is
// snapped to traceCacheGrain so refreshes and tabs a few seconds apart ask the
// same thing; the answer is kept for traceCacheTTL and asks that overlap share
// one run.
const (
	traceCacheGrain = 15 * time.Second
	traceCacheTTL   = 30 * time.Second
	traceCacheMax   = 256
	traceRunLimit   = 2 * time.Minute
)

type traceCache struct {
	runs singleflight.Group
	mu   sync.Mutex
	m    map[string]cachedAnswer
}

type cachedAnswer struct {
	at     time.Time
	status int
	header http.Header
	body   []byte
}

func (s *Server) traceCached(h http.HandlerFunc) http.HandlerFunc {
	c := &s.traceCache
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		snapRange(q)
		r2 := r.Clone(r.Context())
		r2.URL.RawQuery = q.Encode()
		key := scopeFrom(r.Context()).key() + "\x00" + r.URL.Path + "?" + r2.URL.RawQuery
		if a, ok := c.get(key); ok {
			a.write(w)
			return
		}
		v, _, _ := c.runs.Do(key, func() (any, error) {
			// Others may be waiting on this run, so it outlives the asker
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), traceRunLimit)
			defer cancel()
			rec := &answerRecorder{header: http.Header{}, status: http.StatusOK}
			h(rec, r2.WithContext(ctx))
			a := cachedAnswer{at: time.Now(), status: rec.status, header: rec.header, body: rec.body.Bytes()}
			if a.status == http.StatusOK {
				c.put(key, a)
			}
			return a, nil
		})
		v.(cachedAnswer).write(w)
	}
}

// snapRange floors from and to to the grain, unless that would empty the range
func snapRange(q url.Values) {
	from, err1 := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, err2 := time.Parse(time.RFC3339Nano, q.Get("to"))
	if err1 != nil || err2 != nil {
		return
	}
	f, t := from.UTC().Truncate(traceCacheGrain), to.UTC().Truncate(traceCacheGrain)
	if !t.After(f) {
		return
	}
	q.Set("from", f.Format(time.RFC3339Nano))
	q.Set("to", t.Format(time.RFC3339Nano))
}

func (c *traceCache) get(key string) (cachedAnswer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.m[key]
	if !ok || time.Since(a.at) > traceCacheTTL {
		return cachedAnswer{}, false
	}
	return a, true
}

func (c *traceCache) put(key string, a cachedAnswer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cachedAnswer{}
	}
	for k, old := range c.m {
		if time.Since(old.at) > traceCacheTTL {
			delete(c.m, k)
		}
	}
	if len(c.m) >= traceCacheMax {
		return
	}
	c.m[key] = a
}

func (a cachedAnswer) write(w http.ResponseWriter) {
	for k, v := range a.header {
		w.Header()[k] = v
	}
	w.WriteHeader(a.status)
	_, _ = w.Write(a.body)
}

type answerRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *answerRecorder) Header() http.Header         { return r.header }
func (r *answerRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *answerRecorder) WriteHeader(status int)      { r.status = status }
