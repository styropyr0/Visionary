package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
	sdk "github.com/styropyr0/visionary/sdks/go"
	"github.com/styropyr0/visionary/sdks/go/ratelimit"
)

// Blocked addresses are enforced in two places: the host firewall through the
// agent (security_blocks.firewall) and every server SDK through managed
// rate-limit rules (security_blocks.app)

const (
	blockRulePrefix = "visionary-blocks-"
	blockPollS      = 15
)

func (s *Server) securityBlocksSchema() []string {
	id, key, ts := "INTEGER PRIMARY KEY AUTOINCREMENT", "TEXT", "TEXT"
	tail := ""
	switch s.q.d {
	case sdk.Postgres:
		id = "BIGSERIAL PRIMARY KEY"
	case sdk.MySQL:
		id, key, ts = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "VARCHAR(191)", "VARCHAR(32)"
		tail = " CHARACTER SET utf8mb4"
	}
	ddl := `CREATE TABLE IF NOT EXISTS security_blocks (
    id         {id},
    created_at {ts} NOT NULL,
    created_by TEXT,
    address    {key} NOT NULL,
    reason     TEXT,
    finding_id BIGINT,
    firewall   INTEGER NOT NULL DEFAULT 1,
    app        INTEGER NOT NULL DEFAULT 1,
    hosts      TEXT,
    expires_at {ts}
){tail};
CREATE TABLE IF NOT EXISTS security_block_hosts (
    host       {key} NOT NULL PRIMARY KEY,
    version    TEXT,
    backend    TEXT,
    applied    INTEGER NOT NULL DEFAULT 0,
    error      TEXT,
    skipped    TEXT,
    seen_at    {ts} NOT NULL
){tail}`
	return []string{strings.NewReplacer("{id}", id, "{key}", key, "{ts}", ts, "{tail}", tail).Replace(ddl)}
}

func (s *Server) securityBlocksRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/security/blocks", s.authed(s.handleListBlocks))
	mux.Handle("POST /api/security/blocks", s.authed(s.handleCreateBlock))
	mux.Handle("DELETE /api/security/blocks/{id}", s.authed(s.handleDeleteBlock))
	mux.HandleFunc("POST "+agentapi.BlocksPath, s.handleAgentBlocks)
}

type block struct {
	ID        int64    `json:"id"`
	CreatedAt string   `json:"created_at"`
	CreatedBy string   `json:"created_by"`
	Address   string   `json:"address"`
	Reason    string   `json:"reason"`
	FindingID int64    `json:"finding_id,omitempty"`
	Firewall  bool     `json:"firewall"`
	App       bool     `json:"app"`
	Hosts     []string `json:"hosts"`
	ExpiresAt string   `json:"expires_at"`

	prefix netip.Prefix
}

func (b block) covers(a netip.Addr) bool { return b.prefix.IsValid() && b.prefix.Contains(a.Unmap()) }

func (b block) onHost(host string) bool { return len(b.Hosts) == 0 || slices.Contains(b.Hosts, host) }

type blockHost struct {
	Host    string   `json:"host"`
	Version string   `json:"version"`
	Backend string   `json:"backend"`
	Applied int      `json:"applied"`
	Error   string   `json:"error,omitempty"`
	Skipped []string `json:"skipped,omitempty"`
	SeenAt  string   `json:"seen_at"`
	// Current is whether the host applied the list it should have now
	Current bool `json:"current"`
}

// activeBlocks are the blocks not yet expired, newest first
func (s *Server) activeBlocks(ctx context.Context) ([]block, error) {
	res, err := s.db.Query(ctx, `SELECT id, created_at, created_by, address, reason, finding_id, firewall, app, hosts, expires_at
FROM security_blocks WHERE expires_at IS NULL OR expires_at = '' OR expires_at > ? ORDER BY id DESC`, nowTS())
	if err != nil {
		return nil, dbErr(err)
	}
	out := make([]block, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 10 {
			continue
		}
		b := block{ID: toInt(row[0]), CreatedAt: toString(row[1]), CreatedBy: toString(row[2]), Address: toString(row[3]),
			Reason: toString(row[4]), FindingID: toInt(row[5]), Firewall: toInt(row[6]) == 1, App: toInt(row[7]) == 1,
			ExpiresAt: toString(row[9]), Hosts: []string{}}
		if h := toString(row[8]); h != "" {
			_ = json.Unmarshal([]byte(h), &b.Hosts)
		}
		b.prefix, _ = parseIPOrPrefix(b.Address)
		out = append(out, b)
	}
	return out, nil
}

// blockedBy is the active block covering ip, if any
func (s *Server) blockedBy(ctx context.Context, ip string) (*block, error) {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return nil, nil
	}
	list, err := s.activeBlocks(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].covers(a) {
			return &list[i], nil
		}
	}
	return nil, nil
}

// hostAddresses is the sorted firewall list for one host and its version
func hostAddresses(list []block, host string) ([]string, string) {
	seen := map[string]bool{}
	var out []string
	for _, b := range list {
		if b.Firewall && b.onHost(host) && b.prefix.IsValid() && !seen[b.prefix.String()] {
			seen[b.prefix.String()] = true
			out = append(out, blockText(b.prefix))
		}
	}
	slices.Sort(out)
	if len(out) > agentapi.MaxBlocks {
		out = out[:agentapi.MaxBlocks]
	}
	sum := sha256.Sum256([]byte(strings.Join(out, "\n")))
	return out, hex.EncodeToString(sum[:8])
}

// blockText writes a single address without its /32 or /128
func blockText(p netip.Prefix) string {
	if p.IsSingleIP() {
		return p.Addr().String()
	}
	return p.String()
}

// checkBlockable refuses addresses that should never be blocked: private and
// special ranges, very wide ranges, the person's own address and addresses on
// the detectors' Ignore list
func (s *Server) checkBlockable(r *http.Request, raw string) (netip.Prefix, error) {
	p, err := parseIPOrPrefix(strings.TrimSpace(raw))
	if err != nil {
		return p, errors.New("enter an IP address or a CIDR range such as 203.0.113.0/24")
	}
	p = p.Masked()
	a := p.Addr()
	if !publicAddr(a) || a.Is4() && a.As4()[0] == 100 && a.As4()[1]&0xc0 == 64 {
		return p, errors.New("private, loopback and other non-public addresses can't be blocked")
	}
	if a.Is4() && p.Bits() < 8 || a.Is6() && p.Bits() < 32 {
		return p, errors.New("the range is too wide: at most a /8 for IPv4 or a /32 for IPv6")
	}
	if own, err := netip.ParseAddr(s.auth.clientIP(r)); err == nil && p.Contains(own.Unmap()) {
		return p, errors.New("that would block your own address")
	}
	if s.security().st.ignored(a.String()) {
		return p, errors.New("that address is on the detectors' Ignore list (Settings → Server → Security detectors)")
	}
	return p, nil
}

func (s *Server) handleListBlocks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.activeBlocks(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	hosts, err := s.blockHosts(ctx, list)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocks": list, "hosts": hosts, "can_edit": s.can(r, "admin") && scopeFrom(ctx) == nil,
		"webhook": strings.TrimSpace(s.conf("security_action_webhook")) != ""})
}

// blockHosts are the hosts that polled for blocks in the last day, and agent
// hosts that never did (an old agent, or the firewall off)
func (s *Server) blockHosts(ctx context.Context, list []block) ([]blockHost, error) {
	res, err := s.db.Query(ctx, `SELECT host, version, backend, applied, error, skipped, seen_at FROM security_block_hosts WHERE seen_at >= ? ORDER BY host`,
		time.Now().UTC().Add(-24*time.Hour).Format(tsLayout))
	if err != nil {
		return nil, dbErr(err)
	}
	out := []blockHost{}
	seen := map[string]bool{}
	for _, row := range res.Rows {
		if len(row) != 7 {
			continue
		}
		h := blockHost{Host: toString(row[0]), Version: toString(row[1]), Backend: toString(row[2]), Applied: int(toInt(row[3])),
			Error: toString(row[4]), SeenAt: toString(row[6])}
		if sk := toString(row[5]); sk != "" {
			_ = json.Unmarshal([]byte(sk), &h.Skipped)
		}
		_, want := hostAddresses(list, h.Host)
		h.Current = h.Version == want
		seen[h.Host] = true
		out = append(out, h)
	}
	agents, err := s.db.Query(ctx, `SELECT host, last_seen FROM host_inventory WHERE last_seen >= ? ORDER BY host`, time.Now().UTC().Add(-24*time.Hour).Format(tsLayout))
	if err == nil {
		for _, row := range agents.Rows {
			if h := toString(row[0]); h != "" && !seen[h] {
				out = append(out, blockHost{Host: h, Backend: "never", SeenAt: toString(row[1])})
			}
		}
	}
	return out, nil
}

type blockRequest struct {
	Address   string   `json:"address"`
	Reason    string   `json:"reason"`
	FindingID int64    `json:"finding_id"`
	Firewall  *bool    `json:"firewall"`
	App       *bool    `json:"app"`
	Hosts     []string `json:"hosts"`
	Hours     int      `json:"hours"`
}

func (s *Server) handleCreateBlock(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") || !s.requireAllServices(w, r) {
		return
	}
	var body blockRequest
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, status, err := s.createBlock(r, body)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) createBlock(r *http.Request, body blockRequest) (block, int, error) {
	ctx := r.Context()
	p, err := s.checkBlockable(r, body.Address)
	if err != nil {
		return block{}, http.StatusBadRequest, err
	}
	b := block{Address: blockText(p), Reason: clip(strings.TrimSpace(body.Reason), 500), FindingID: body.FindingID,
		Firewall: body.Firewall == nil || *body.Firewall, App: body.App == nil || *body.App, Hosts: []string{}, prefix: p}
	if !b.Firewall && !b.App {
		return b, http.StatusBadRequest, errors.New("choose the firewall, your apps or both")
	}
	for _, h := range body.Hosts {
		if h = strings.TrimSpace(h); h != "" && !slices.Contains(b.Hosts, h) {
			b.Hosts = append(b.Hosts, h)
		}
	}
	if body.Hours > 0 {
		b.ExpiresAt = time.Now().UTC().Add(time.Duration(min(body.Hours, 24*3650)) * time.Hour).Format(tsLayout)
	}
	list, err := s.activeBlocks(ctx)
	if err != nil {
		return b, 0, err
	}
	if len(list) >= agentapi.MaxBlocks {
		return b, http.StatusBadRequest, fmt.Errorf("at most %d blocked addresses: remove some first", agentapi.MaxBlocks)
	}
	if b.App && countApp(list) >= ratelimit.MaxListEntries*blockRuleSlots() {
		return b, http.StatusBadRequest, fmt.Errorf("your apps can refuse at most %d addresses: block this one in the firewall only", ratelimit.MaxListEntries*blockRuleSlots())
	}
	for _, o := range list {
		if o.Address == b.Address {
			if err := s.db.Exec(ctx, `DELETE FROM security_blocks WHERE id = ?`, o.ID); err != nil {
				return b, 0, dbErr(err)
			}
		}
	}
	if sess := sessionFrom(r); sess != nil {
		b.CreatedBy = fallback(sess.Name, sess.Handle)
	}
	b.CreatedAt = nowTS()
	hosts := ""
	if len(b.Hosts) > 0 {
		raw, _ := json.Marshal(b.Hosts)
		hosts = string(raw)
	}
	if err := s.db.Exec(ctx, `INSERT INTO security_blocks (created_at, created_by, address, reason, finding_id, firewall, app, hosts, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.CreatedAt, nullable(b.CreatedBy), b.Address, nullable(b.Reason), nullableID(b.FindingID), boolInt(b.Firewall), boolInt(b.App), nullable(hosts), nullable(b.ExpiresAt)); err != nil {
		return b, 0, dbErr(err)
	}
	if res, err := s.db.Query(ctx, `SELECT MAX(id) FROM security_blocks WHERE address = ?`, b.Address); err == nil && len(res.Rows) == 1 {
		b.ID = toInt(res.Rows[0][0])
	}
	s.audit(r, "security.ip_blocked", b.Address, map[string]any{"firewall": b.Firewall, "app": b.App, "hosts": b.Hosts, "expires_at": b.ExpiresAt, "finding": b.FindingID})
	if err := s.syncAppBlocks(ctx); err != nil {
		s.logger.Warn("update the app block rules", "err", err)
	}
	return b, http.StatusOK, nil
}

func (s *Server) handleDeleteBlock(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") || !s.requireAllServices(w, r) {
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid block id")
		return
	}
	ctx := r.Context()
	res, err := s.db.Query(ctx, `SELECT address FROM security_blocks WHERE id = ?`, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(res.Rows) == 0 {
		writeError(w, http.StatusNotFound, "block not found")
		return
	}
	if err := s.db.Exec(ctx, `DELETE FROM security_blocks WHERE id = ?`, id); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "security.ip_unblocked", toString(res.Rows[0][0]), nil)
	if err := s.syncAppBlocks(ctx); err != nil {
		s.logger.Warn("update the app block rules", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func countApp(list []block) int {
	n := 0
	for _, b := range list {
		if b.App {
			n++
		}
	}
	return n
}

// blockRuleSlots is how many managed rules app blocks may take
func blockRuleSlots() int { return 5 }

// syncAppBlocks rewrites the managed block rules at the front of the rate
// limit config when the app list changed. No app blocks means no managed
// rules, since a block rule with no IPs would match every request.
func (s *Server) syncAppBlocks(ctx context.Context) error {
	list, err := s.activeBlocks(ctx)
	if err != nil {
		return err
	}
	var ips []string
	for _, b := range list {
		if b.App && b.prefix.IsValid() {
			ips = append(ips, blockText(b.prefix))
		}
	}
	slices.Sort(ips)
	ips = slices.Compact(ips)
	ips = ips[:min(len(ips), ratelimit.MaxListEntries*blockRuleSlots())]
	cfg, _, _, err := s.loadRateLimits(ctx)
	if err != nil {
		return err
	}
	var had []string
	for _, rule := range cfg.Rules {
		if isBlockRule(rule.ID) {
			had = append(had, rule.IPs...)
		}
	}
	slices.Sort(had)
	if slices.Equal(had, ips) {
		return nil
	}
	cfg.Rules = withBlockRules(cfg.Rules, ips)
	if err := ratelimit.Validate(cfg); err != nil {
		return err
	}
	raw, _ := json.Marshal(cfg)
	return s.writeSettings(ctx, map[string]string{ratelimit.SettingConfig: string(raw), ratelimit.SettingVersion: newRateLimitVersion()})
}

func isBlockRule(id string) bool { return strings.HasPrefix(id, blockRulePrefix) }

// withBlockRules replaces the managed rules in rules with ones for ips, first
// so no allow rule can exempt a blocked address
func withBlockRules(rules []ratelimit.Rule, ips []string) []ratelimit.Rule {
	var out []ratelimit.Rule
	for i := 0; i*ratelimit.MaxListEntries < len(ips); i++ {
		chunk := ips[i*ratelimit.MaxListEntries : min(len(ips), (i+1)*ratelimit.MaxListEntries)]
		out = append(out, ratelimit.Rule{ID: blockRulePrefix + strconv.Itoa(i+1), Name: "Blocked in Security", Action: "block", IPs: slices.Clone(chunk)})
	}
	for _, rule := range rules {
		if !isBlockRule(rule.ID) {
			out = append(out, rule)
		}
	}
	return out
}

// blocksLoop drops app rules for blocks that expired
func (s *Server) blocksLoop(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if err := s.syncAppBlocks(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn("update the app block rules", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// handleAgentBlocks answers an agent's poll with the host's firewall list and
// records what the host last applied
func (s *Server) handleAgentBlocks(w http.ResponseWriter, r *http.Request) {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	if !s.ingestKeyValid(otlpKey(r), "server") {
		writeError(w, http.StatusUnauthorized, "missing or invalid ingest key")
		return
	}
	var poll agentapi.BlocksPoll
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&poll); err != nil {
		writeError(w, http.StatusBadRequest, "the body is not a valid blocks poll")
		return
	}
	poll.Host = strings.TrimSpace(poll.Host)
	if !instanceName.MatchString(poll.Host) {
		writeError(w, http.StatusBadRequest, "host must be 1-128 letters, digits, '.', '_', ':' or '-'")
		return
	}
	ctx := r.Context()
	list, err := s.activeBlocks(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	addrs, version := hostAddresses(list, poll.Host)
	skipped := ""
	if len(poll.Skipped) > 0 {
		raw, _ := json.Marshal(poll.Skipped[:min(len(poll.Skipped), 50)])
		skipped = string(raw)
	}
	if err := s.db.Exec(ctx, s.q.upsertRow("security_block_hosts", "host", "host, version, backend, applied, error, skipped, seen_at", "?, ?, ?, ?, ?, ?, ?",
		[]string{"version", "backend", "applied", "error", "skipped", "seen_at"}),
		poll.Host, clip(poll.Version, 64), clip(poll.Backend, 32), poll.Applied, nullable(clip(poll.Error, 500)), nullable(skipped), nowTS()); err != nil {
		s.fail(w, err)
		return
	}
	if addrs == nil {
		addrs = []string{}
	}
	writeJSON(w, http.StatusOK, agentapi.Blocks{Version: version, Addresses: addrs, PollS: blockPollS})
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
