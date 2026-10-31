package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Each replica writes a heartbeat to visionary_replicas each lease round,
// listed in Settings → Server. Edits other replicas cache (alert rules,
// silences, API keys, uptime checks, settings, sign-outs) bump a row in
// visionary_changes, so the others drop their copy within a round.

type replicaState struct {
	id      string
	started string

	mu       sync.Mutex
	seen     map[string]int64
	prunedAt time.Time
}

var replicaStates sync.Map

func (s *Server) replica() *replicaState {
	if v, ok := replicaStates.Load(s); ok {
		return v.(*replicaState)
	}
	id := strings.TrimSpace(os.Getenv("VISIONARY_REPLICA_ID"))
	if id == "" {
		host, _ := os.Hostname()
		port := s.cfg.Addr
		if i := strings.LastIndex(port, ":"); i >= 0 {
			port = port[i+1:]
		}
		id = host + ":" + port
	}
	v, _ := replicaStates.LoadOrStore(s, &replicaState{id: id, started: nowTS()})
	return v.(*replicaState)
}

func (s *Server) replicaID() string { return s.replica().id }

// heartbeat records this replica and returns the database's clock and how
// many replicas are alive.
func (s *Server) heartbeat(ctx context.Context) (nowMs int64, live int, err error) {
	rs := s.replica()
	now := s.q.dbNowMs()
	version, commit := buildInfo()
	if version == "" && len(commit) >= 7 {
		version = commit[:7]
	}
	if err = s.db.Exec(ctx, s.q.upsertRow("visionary_replicas", "id", "id, version, addr, started_at, heartbeat_ms", "?, ?, ?, ?, "+now,
		[]string{"version", "addr", "started_at", "heartbeat_ms"}), rs.id, version, s.cfg.Addr, rs.started); err != nil {
		return 0, 0, err
	}
	res, err := s.db.Query(ctx, `SELECT `+now+`, COUNT(*) FROM visionary_replicas WHERE heartbeat_ms > `+now+` - `+ms(leaseTTL))
	if err != nil || len(res.Rows) != 1 || len(res.Rows[0]) != 2 {
		return 0, 0, firstErr(err, errNoRows)
	}
	nowMs, live = toInt(res.Rows[0][0]), int(toInt(res.Rows[0][1]))
	rs.mu.Lock()
	prune := time.Since(rs.prunedAt) > time.Hour
	if prune {
		rs.prunedAt = time.Now()
	}
	rs.mu.Unlock()
	if prune {
		_ = s.db.Exec(ctx, `DELETE FROM visionary_replicas WHERE heartbeat_ms < ?`, nowMs-(24*time.Hour).Milliseconds())
		_ = s.db.Exec(ctx, `DELETE FROM visionary_once WHERE expires_ms < ?`, time.Now().UnixMilli())
	}
	return nowMs, live, nil
}

var errNoRows = errors.New("the heartbeat read no rows")

// announce tells the other replicas that kind changed here. Alone, there is
// nobody to tell.
func (s *Server) announce(kind string) {
	ls := s.leases()
	ls.mu.Lock()
	alone := ls.live <= 1
	ls.mu.Unlock()
	if alone {
		return
	}
	go s.bump(kind)
}

func (s *Server) bump(kind string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.db.Exec(ctx, s.q.upsertRow("visionary_changes", "name", "name, changed_ms", "?, "+s.q.dbNowMs(), []string{"changed_ms"}), kind); err != nil {
		s.logger.Warn("replicas: announce change", "kind", kind, "err", err)
	}
}

func (s *Server) followChanges(ctx context.Context) {
	res, err := s.db.Query(ctx, `SELECT name, changed_ms FROM visionary_changes`)
	if err != nil {
		return
	}
	rs := s.replica()
	rs.mu.Lock()
	first := rs.seen == nil
	if first {
		rs.seen = map[string]int64{}
	}
	var changed []string
	for _, r := range res.Rows {
		if len(r) != 2 {
			continue
		}
		name, at := toString(r[0]), toInt(r[1])
		if prev, ok := rs.seen[name]; !first && (!ok || at > prev) {
			changed = append(changed, name)
		}
		rs.seen[name] = at
	}
	rs.mu.Unlock()
	for _, kind := range changed {
		switch kind {
		case "alerts":
			e := s.alertEngine()
			e.mu.Lock()
			e.rulesAt, e.silencesAt = time.Time{}, time.Time{}
			e.mu.Unlock()
		case "uptime":
			wakeUptimeHere(s)
		case "keys":
			ks := &s.team().keys
			ks.mu.Lock()
			ks.loaded = time.Time{}
			ks.mu.Unlock()
		case "status":
			dropStatusCacheHere(s)
		case "settings":
			_ = s.refreshLive(ctx)
		case "signout":
			s.pullSignOuts(ctx)
		}
	}
}

// shareSignOut makes a logout end the session on every replica, not only
// the one that answered it.
func (s *Server) shareSignOut(sess *session) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := s.db.Exec(ctx, s.q.insertIgnore("visionary_once", "name, holder, expires_ms", "?, '', ?"),
			"signout:"+tokenKey(sess.Token), sess.expiresAt().UnixMilli())
		if err != nil {
			s.logger.Warn("replicas: share sign-out", "err", err)
			return
		}
		s.bump("signout")
	}()
}

func (s *Server) pullSignOuts(ctx context.Context) {
	res, err := s.db.Query(ctx, `SELECT name, expires_ms FROM visionary_once WHERE name LIKE 'signout:%' AND expires_ms > ?`, time.Now().UnixMilli())
	if err != nil {
		return
	}
	for _, r := range res.Rows {
		if len(r) == 2 {
			s.auth.addSignedOut(strings.TrimPrefix(toString(r[0]), "signout:"), time.UnixMilli(toInt(r[1])))
		}
	}
}

// claimOnce reports whether key was not claimed before by any replica. A
// claim is kept for ttl.
func (s *Server) claimOnce(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	if err := s.db.Exec(ctx, s.q.insertIgnore("visionary_once", "name, holder, expires_ms", "?, ?, ?"),
		key, token, time.Now().Add(ttl).UnixMilli()); err != nil {
		return false, err
	}
	res, err := s.db.Query(ctx, `SELECT holder FROM visionary_once WHERE name = ?`, key)
	if err != nil {
		return false, err
	}
	return len(res.Rows) == 1 && toString(res.Rows[0][0]) == token, nil
}

// firstUse is claimOnce for checks this replica has already passed: when the
// database can't answer, its own answer stands.
func (s *Server) firstUse(ctx context.Context, key string, ttl time.Duration) bool {
	ok, err := s.claimOnce(ctx, key, ttl)
	if err != nil {
		s.logger.Warn("replicas: replay check", "err", err)
		return true
	}
	return ok
}

func (s *Server) replicasRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/readyz", s.handleReadyz)
	mux.Handle("GET /api/settings/replicas", s.authed(s.handleReplicas))
}

// handleReadyz is the readiness probe: ready once the tables exist and the
// database answers. /api/healthz only says the process is up.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.schemaPending.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "starting", "reason": "the tables are not all created yet"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if _, err := s.db.Query(ctx, `SELECT 1`); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "reason": "the database does not answer"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "replica": s.replicaID()})
}

type replicaView struct {
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Addr        string   `json:"addr"`
	StartedAt   string   `json:"started_at"`
	HeartbeatAt string   `json:"heartbeat_at"`
	SeenAgoS    int64    `json:"seen_ago_s"`
	Live        bool     `json:"live"`
	Self        bool     `json:"self"`
	Leases      []string `json:"leases"`
}

func (s *Server) handleReplicas(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	now := s.q.dbNowMs()
	res, err := s.db.Query(r.Context(), `SELECT id, version, addr, started_at, heartbeat_ms, `+now+` FROM visionary_replicas ORDER BY started_at`)
	if err != nil {
		s.fail(w, err)
		return
	}
	leases, err := s.readLeases(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	held := map[string][]string{}
	var nowMs int64
	out := []replicaView{}
	for _, row := range res.Rows {
		if len(row) != 6 {
			continue
		}
		nowMs = toInt(row[5])
		hb := toInt(row[4])
		out = append(out, replicaView{
			ID: toString(row[0]), Version: toString(row[1]), Addr: toString(row[2]), StartedAt: toString(row[3]),
			HeartbeatAt: time.UnixMilli(hb).UTC().Format(tsLayout), SeenAgoS: max(0, (nowMs-hb)/1000),
			Live: nowMs-hb < leaseTTL.Milliseconds(), Self: toString(row[0]) == s.replicaID(),
		})
	}
	var free []string
	for name, l := range leases {
		if l.holder != "" && l.expiresMs > nowMs {
			held[l.holder] = append(held[l.holder], name)
		} else {
			free = append(free, name)
		}
	}
	for i := range out {
		out[i].Leases = held[out[i].ID]
		if out[i].Leases == nil {
			out[i].Leases = []string{}
		}
		sort.Strings(out[i].Leases)
	}
	sort.Strings(free)
	if free == nil {
		free = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"replicas": out, "self": s.replicaID(), "unheld": free,
		"lease_ttl_s": int(leaseTTL / time.Second), "renew_s": int(leaseRenew / time.Second)})
}
