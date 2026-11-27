package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// What needs you is read over the last needsWindow whatever range the page
// shows. Reading that much takes a while, so the answer is kept: within
// needsFresh it is served as is, after that it is served while a new one is
// read in the background. Triage and blocks drop it.
const (
	needsWindow = 30 * 24 * time.Hour
	needsFresh  = time.Minute
	needsStale  = 24 * time.Hour
	needsLimit  = 3 * time.Minute
)

type needsAnswer struct {
	Through        []*secIncident `json:"through"`
	Exposed        []postureItem  `json:"exposed"`
	Tried          int            `json:"tried"`
	Blocked        int            `json:"blocked"`
	NoiseAddresses int            `json:"noise_addresses"`
	From           string         `json:"from"`
	At             string         `json:"at"`
}

type needsEntry struct {
	at   time.Time
	body []byte
}

type needsCache struct {
	runs singleflight.Group
	mu   sync.Mutex
	gen  int
	m    map[string]needsEntry
}

var needsCaches sync.Map // *Server -> *needsCache

func (s *Server) needsCache() *needsCache {
	v, _ := needsCaches.LoadOrStore(s, &needsCache{m: map[string]needsEntry{}})
	return v.(*needsCache)
}

func (c *needsCache) drop() {
	c.mu.Lock()
	c.gen++
	clear(c.m)
	c.mu.Unlock()
}

func (s *Server) handleSecurityNeeds(w http.ResponseWriter, r *http.Request) {
	c := s.needsCache()
	key := scopeFrom(r.Context()).key()
	c.mu.Lock()
	e, ok := c.m[key]
	c.mu.Unlock()
	age := time.Since(e.at)
	if ok && age < needsStale {
		if age >= needsFresh {
			go func() { _, _, _ = c.runs.Do(key, func() (any, error) { return s.readNeeds(r.Context(), key) }) }()
		}
		writeNeeds(w, e.body)
		return
	}
	v, err, _ := c.runs.Do(key, func() (any, error) { return s.readNeeds(r.Context(), key) })
	if err != nil {
		s.fail(w, err)
		return
	}
	writeNeeds(w, v.([]byte))
}

func writeNeeds(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// readNeeds outlives the request that started it, since others may wait on it
func (s *Server) readNeeds(rctx context.Context, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(rctx), needsLimit)
	defer cancel()
	c := s.needsCache()
	c.mu.Lock()
	gen := c.gen
	c.mu.Unlock()
	to := time.Now().UTC()
	tr := timeRange{From: to.Add(-needsWindow), To: to}
	list, noise, err := s.incidents(ctx, tr)
	if err != nil {
		return nil, err
	}
	posture, err := s.posture(ctx)
	if err != nil {
		return nil, err
	}
	a := needsAnswer{Through: []*secIncident{}, Exposed: []postureItem{}, From: tr.From.Format(time.RFC3339), At: to.Format(time.RFC3339)}
	for _, inc := range list {
		switch inc.Outcome {
		case "succeeded":
			if inc.Open {
				a.Through = append(a.Through, inc)
			}
		case "attempted":
			a.Tried++
		case "blocked":
			a.Blocked++
		}
	}
	for _, p := range posture {
		if p.Severity == "critical" {
			a.Exposed = append(a.Exposed, p)
		}
	}
	a.NoiseAddresses, _ = noise["addresses"].(int)
	body, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	// a drop while reading means this answer may already be out of date
	if c.gen == gen {
		c.m[key] = needsEntry{at: to, body: body}
	}
	c.mu.Unlock()
	return body, nil
}

// dropNeedsOn clears what needs you after triage, blocks and unblocks
func (s *Server) dropNeedsOn(action string) {
	if strings.HasPrefix(action, "security.") {
		s.needsCache().drop()
	}
}
