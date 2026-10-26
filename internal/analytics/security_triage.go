package analytics

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Working a finding, and following an account or the audit log:
//
//   - status and note: open, acknowledged, resolved or false positive, with
//     who changed it.
//   - suppressions: "don't flag this again" rules by kind and IP (or CIDR) or
//     account, optionally until a date. Kind "*" with an IP ignores the
//     address for every detector; prepareFindings applies them all.
//   - actions: Block IP and End sessions POST a signed JSON call to the
//     action webhook, for a firewall, gateway or the app to act on.
//   - an account's timeline: its sign-ins with place and device, findings,
//     and what it signed in from before.
//   - a tamper-evident audit export: every line hashes the one before it and
//     the last line signs the chain with a key from the session secret, so a
//     changed, dropped or reordered line shows.

var findingStatuses = map[string]bool{"open": true, "acknowledged": true, "resolved": true, "false_positive": true}

// suppression is one "don't flag this again" rule.
type suppression struct {
	ID        int64  `json:"id"`
	CreatedAt string `json:"created_at"`
	CreatedBy string `json:"created_by"`
	Kind      string `json:"kind"` // "*" for every kind
	IP        string `json:"ip,omitempty"`
	Actor     string `json:"actor,omitempty"`
	Note      string `json:"note,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	prefix    netip.Prefix
}

// suppressionCache keeps the active rules for a few seconds, since every
// detector run reads them.
type suppressionCache struct {
	mu    sync.Mutex
	at    time.Time
	rules []suppression
}

var suppressionCaches sync.Map // *Server -> *suppressionCache

func (s *Server) suppressionCache() *suppressionCache {
	v, _ := suppressionCaches.LoadOrStore(s, &suppressionCache{})
	return v.(*suppressionCache)
}

func (s *Server) listSuppressions(ctx context.Context) ([]suppression, error) {
	res, err := s.db.Query(ctx, `SELECT id, created_at, created_by, kind, ip, actor, note, expires_at FROM security_suppressions ORDER BY id DESC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	out := make([]suppression, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 8 {
			continue
		}
		sp := suppression{ID: toInt(row[0]), CreatedAt: toString(row[1]), CreatedBy: toString(row[2]), Kind: toString(row[3]),
			IP: toString(row[4]), Actor: toString(row[5]), Note: toString(row[6]), ExpiresAt: toString(row[7])}
		if sp.IP != "" {
			sp.prefix, _ = parseIPOrPrefix(sp.IP)
		}
		out = append(out, sp)
	}
	return out, nil
}

// activeSuppressions are the rules not yet expired.
func (s *Server) activeSuppressions(ctx context.Context) ([]suppression, error) {
	c := s.suppressionCache()
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) < 10*time.Second {
		return c.rules, nil
	}
	all, err := s.listSuppressions(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(tsLayout)
	c.rules = c.rules[:0]
	for _, sp := range all {
		if sp.ExpiresAt == "" || sp.ExpiresAt > now {
			c.rules = append(c.rules, sp)
		}
	}
	c.at = time.Now()
	return c.rules, nil
}

func (s *Server) dropSuppressionCache() {
	c := s.suppressionCache()
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
}

// suppressed reports a finding one of the rules covers.
func suppressed(rules []suppression, f finding) bool {
	for _, sp := range rules {
		if sp.Kind != "*" && sp.Kind != f.kind {
			continue
		}
		if sp.IP != "" {
			a, err := netip.ParseAddr(f.ip)
			if err != nil || !sp.prefix.IsValid() || !sp.prefix.Contains(a.Unmap()) {
				continue
			}
		}
		if sp.Actor != "" && !strings.EqualFold(sp.Actor, f.actor) {
			continue
		}
		if sp.IP == "" && sp.Actor == "" && sp.Kind == "*" {
			continue // never mute everything
		}
		return true
	}
	return false
}

func parseIPOrPrefix(v string) (netip.Prefix, error) {
	v = strings.TrimSpace(v)
	if p, err := netip.ParsePrefix(v); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(v)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func (s *Server) securityTriageRoutes(mux *http.ServeMux) {
	mux.Handle("PATCH /api/security/findings/{id}", s.authed(s.handleUpdateFinding))
	mux.Handle("POST /api/security/findings/{id}/actions", s.authed(s.handleFindingAction))
	mux.Handle("GET /api/security/suppressions", s.authed(s.handleListSuppressions))
	mux.Handle("POST /api/security/suppressions", s.authed(s.handleCreateSuppression))
	mux.Handle("DELETE /api/security/suppressions/{id}", s.authed(s.handleDeleteSuppression))
	mux.Handle("GET /api/security/accounts/{account}", s.authed(s.handleAccountTimeline))
	mux.Handle("GET /api/security/ip/{ip}", s.authed(s.handleIPInfo))
	mux.Handle("GET /api/security/intel", s.authed(s.handleIntelStatus))
	mux.Handle("GET /api/security/audit/export", s.authed(s.handleAuditExport))
	mux.Handle("POST /api/security/audit/verify", s.authed(s.handleAuditVerify))
}

// loadFinding is one finding's view, or nil.
func (s *Server) loadFinding(ctx context.Context, id int64) (map[string]any, error) {
	res, err := s.db.Query(ctx, `SELECT `+findingColumns+` FROM security_events WHERE id = ?`, id)
	if err != nil || len(res.Rows) == 0 {
		return nil, err
	}
	return findingView(res.Rows[0]), nil
}

// handleUpdateFinding sets a finding's status and note.
func (s *Server) handleUpdateFinding(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid finding id")
		return
	}
	var body struct {
		Status *string `json:"status"`
		Note   *string `json:"note"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := s.loadFinding(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if f == nil {
		writeError(w, http.StatusNotFound, "finding not found")
		return
	}
	status, note := f["status"].(string), f["note"].(string)
	if body.Status != nil {
		if !findingStatuses[*body.Status] {
			writeError(w, http.StatusBadRequest, "status must be open, acknowledged, resolved or false_positive")
			return
		}
		status = *body.Status
	}
	if body.Note != nil {
		note = clip(strings.TrimSpace(*body.Note), 2000)
	}
	stored := status
	if stored == "open" {
		stored = ""
	}
	sess := sessionFrom(r)
	by := ""
	if sess != nil {
		by = fallback(sess.Name, sess.Handle)
	}
	if err := s.db.Exec(r.Context(), `UPDATE security_events SET status = ?, note = ?, status_by = ?, status_at = ? WHERE id = ?`,
		nullable(stored), nullable(note), nullable(by), nowTS(), id); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "security.finding_updated", strconv.FormatInt(id, 10), map[string]any{"status": status, "kind": f["kind"]})
	f, err = s.loadFinding(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// handleFindingAction runs one action on a finding: block_ip and
// end_sessions call the action webhook; ignore_ip and suppress add a rule.
func (s *Server) handleFindingAction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid finding id")
		return
	}
	var body struct {
		Action  string `json:"action"`
		Account string `json:"account"` // end_sessions: overrides the finding's account
		Scope   string `json:"scope"`   // suppress: "ip" or "actor"
		Days    int    `json:"days"`    // suppress / ignore_ip: 0 is forever
		Note    string `json:"note"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	need := "admin"
	if body.Action == "suppress" {
		need = "edit"
	}
	if !s.require(w, r, need) {
		return
	}
	f, err := s.loadFinding(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if f == nil {
		writeError(w, http.StatusNotFound, "finding not found")
		return
	}
	ip, actor := f["ip"].(string), f["actor"].(string)
	switch body.Action {
	case "block_ip", "end_sessions":
		target := ip
		if body.Action == "end_sessions" {
			target = fallback(strings.TrimSpace(body.Account), actor)
		}
		if target == "" {
			writeError(w, http.StatusBadRequest, "this finding has no "+map[string]string{"block_ip": "IP address", "end_sessions": "account"}[body.Action])
			return
		}
		status, err := s.sendAction(r, body.Action, target, f)
		s.audit(r, "security.action", target, map[string]any{"action": body.Action, "finding": id, "status": status, "error": errText(err)})
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
	case "ignore_ip", "suppress":
		sp := suppression{Kind: f["kind"].(string), Note: clip(strings.TrimSpace(body.Note), 500)}
		switch {
		case body.Action == "ignore_ip":
			sp.Kind, sp.IP = "*", ip
		case body.Scope == "actor":
			sp.Actor = actor
		default:
			sp.IP = ip
		}
		if sp.IP == "" && sp.Actor == "" {
			writeError(w, http.StatusBadRequest, "this finding has no IP address or account to match")
			return
		}
		if body.Days > 0 {
			sp.ExpiresAt = time.Now().UTC().AddDate(0, 0, min(body.Days, 3650)).Format(tsLayout)
		}
		created, err := s.insertSuppression(r, sp)
		if err != nil {
			s.fail(w, err)
			return
		}
		if body.Action == "suppress" {
			_ = s.db.Exec(r.Context(), `UPDATE security_events SET status = 'false_positive', status_by = ?, status_at = ? WHERE id = ?`,
				nullable(created.CreatedBy), nowTS(), id)
		}
		writeJSON(w, http.StatusOK, created)
	default:
		writeError(w, http.StatusBadRequest, "action must be block_ip, end_sessions, ignore_ip or suppress")
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// sendAction POSTs one action to the action webhook, signed with the secret
// when one is set. It returns the HTTP status the webhook answered.
func (s *Server) sendAction(r *http.Request, action, target string, f map[string]any) (int, error) {
	hook := strings.TrimSpace(s.conf("security_action_webhook"))
	if hook == "" {
		return 0, errors.New("set an action webhook in Settings → Server → Response first")
	}
	if u, err := url.Parse(hook); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return 0, errors.New("the action webhook is not an http(s) URL")
	}
	sess := sessionFrom(r)
	by := ""
	if sess != nil {
		by = fallback(sess.Name, sess.Handle)
	}
	payload := map[string]any{
		"action": action, "at": time.Now().UTC().Format(time.RFC3339), "requested_by": by,
		"finding": map[string]any{"id": f["id"], "kind": f["kind"], "severity": f["severity"], "summary": f["summary"], "ip": f["ip"], "account": f["actor"]},
	}
	if action == "block_ip" {
		payload["ip"] = target
	} else {
		payload["account"] = target
	}
	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Visionary security actions")
	if secret := s.conf("security_action_secret"); secret != "" {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write(body)
		req.Header.Set("X-Visionary-Signature", "sha256="+hex.EncodeToString(m.Sum(nil)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("the action webhook didn't answer: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("the action webhook answered %s", resp.Status)
	}
	return resp.StatusCode, nil
}

func (s *Server) insertSuppression(r *http.Request, sp suppression) (suppression, error) {
	sess := sessionFrom(r)
	if sess != nil {
		sp.CreatedBy = fallback(sess.Name, sess.Handle)
	}
	sp.CreatedAt = nowTS()
	if err := s.db.Exec(r.Context(), `INSERT INTO security_suppressions (created_at, created_by, kind, ip, actor, note, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sp.CreatedAt, sp.CreatedBy, sp.Kind, nullable(sp.IP), nullable(sp.Actor), nullable(sp.Note), nullable(sp.ExpiresAt)); err != nil {
		return sp, err
	}
	s.dropSuppressionCache()
	s.audit(r, "security.suppression_created", sp.Kind, map[string]any{"ip": sp.IP, "actor": sp.Actor, "expires_at": sp.ExpiresAt})
	return sp, nil
}

func (s *Server) handleListSuppressions(w http.ResponseWriter, r *http.Request) {
	list, err := s.listSuppressions(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suppressions": list})
}

func (s *Server) handleCreateSuppression(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	var body struct {
		Kind  string `json:"kind"`
		IP    string `json:"ip"`
		Actor string `json:"actor"`
		Note  string `json:"note"`
		Days  int    `json:"days"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sp := suppression{Kind: strings.TrimSpace(body.Kind), IP: strings.TrimSpace(body.IP), Actor: clip(strings.TrimSpace(body.Actor), 255), Note: clip(strings.TrimSpace(body.Note), 500)}
	if sp.Kind != "*" && !findingKind.MatchString(sp.Kind) {
		writeError(w, http.StatusBadRequest, "kind must be a finding kind, or * for every kind")
		return
	}
	if sp.IP != "" {
		p, err := parseIPOrPrefix(sp.IP)
		if err != nil {
			writeError(w, http.StatusBadRequest, "ip must be an address or CIDR range")
			return
		}
		sp.IP = p.String()
		if p.IsSingleIP() {
			sp.IP = p.Addr().String()
		}
	}
	if sp.IP == "" && sp.Actor == "" {
		writeError(w, http.StatusBadRequest, "match an IP address or an account")
		return
	}
	if body.Days > 0 {
		sp.ExpiresAt = time.Now().UTC().AddDate(0, 0, min(body.Days, 3650)).Format(tsLayout)
	}
	created, err := s.insertSuppression(r, sp)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, created)
}

func (s *Server) handleDeleteSuppression(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.db.Exec(r.Context(), `DELETE FROM security_suppressions WHERE id = ?`, id); err != nil {
		s.fail(w, err)
		return
	}
	s.dropSuppressionCache()
	s.audit(r, "security.suppression_deleted", strconv.FormatInt(id, 10), nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAccountTimeline is one account's security picture over days (30 by
// default, at most the request log's range): sign-ins, findings, and what it
// signed in from before.
func (s *Server) handleAccountTimeline(w http.ResponseWriter, r *http.Request) {
	account := strings.TrimSpace(r.PathValue("account"))
	if account == "" || len(account) > 255 {
		writeError(w, http.StatusBadRequest, "invalid account")
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}
	days = min(days, max(s.maxRangeDays(), 1))
	ctx := r.Context()
	since := time.Now().UTC().AddDate(0, 0, -days).Format(tsLayout)
	st := &s.security().st
	login, fail := st.loginCond(s.q), authFailCond(s.q)
	match := "(login_account = ? OR user_handle = ? OR " + s.q.text("user_id") + " = ?)"
	res, err := s.db.Query(ctx, `SELECT created_at, ip, COALESCE(user_agent, ''), status, method, COALESCE(route, path), `+fail+` AS failed
FROM api_logs WHERE created_at >= ? AND `+login+` AND `+match+`
ORDER BY created_at DESC LIMIT 300`, since, account, account, account)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	var logins []map[string]any
	var ok, failed int
	ips := map[string]bool{}
	for _, row := range res.Rows {
		if len(row) != 7 {
			continue
		}
		ip := toString(row[1])
		bad := toInt(row[6]) == 1 || toString(row[6]) == "true"
		if bad {
			failed++
		} else if toInt(row[3]) < 400 {
			ok++
		}
		ips[ip] = true
		logins = append(logins, map[string]any{"at": toString(row[0]), "ip": ip, "place": s.lookupIP(ip).place(),
			"device": deviceOf(toString(row[2])), "status": toInt(row[3]), "route": toString(row[4]) + " " + maskPath(toString(row[5])), "failed": bad})
	}
	fres, err := s.db.Query(ctx, `SELECT `+findingColumns+` FROM security_events WHERE created_at >= ? AND (actor = ? OR target = ?)
ORDER BY id DESC LIMIT 100`, since, account, account)
	if err != nil {
		s.fail(w, err)
		return
	}
	var findings []map[string]any
	for _, row := range fres.Rows {
		if f := findingView(row); f != nil {
			findings = append(findings, f)
		}
	}
	kres, err := s.db.Query(ctx, `SELECT kind, value, first_seen, last_seen, hits FROM security_known WHERE account = ? AND kind <> 'last' ORDER BY last_seen DESC LIMIT 100`, strings.ToLower(account))
	if err != nil {
		s.fail(w, err)
		return
	}
	var known []map[string]any
	for _, row := range kres.Rows {
		if len(row) == 5 {
			known = append(known, map[string]any{"kind": toString(row[0]), "value": toString(row[1]), "first_seen": toString(row[2]), "last_seen": toString(row[3]), "hits": toInt(row[4])})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account": account, "days": days, "sign_ins": ok, "failed_sign_ins": failed, "ips": len(ips),
		"logins": logins, "findings": findings, "known": known, "geo": s.geoEnabled(),
	})
}

// findingAudit adds the audit entries behind an audit-sourced finding.
func (s *Server) findingAudit(ctx context.Context, out, f map[string]any, tr timeRange) error {
	var cond string
	var args []any
	detail, _ := f["detail"].(map[string]any)
	switch {
	case detail["audit_id"] != nil:
		cond, args = "id = ?", []any{int64(toFloat(detail["audit_id"]))}
	case f["kind"] == "portal_account_targeted":
		cond, args = "target = ? AND action IN ('auth.sign_in_failed', 'auth.sign_in')", []any{f["actor"]}
	case f["ip"] != "":
		cond, args = "ip = ? AND action IN ('auth.sign_in_failed', 'auth.sign_in')", []any{f["ip"]}
	default:
		return nil
	}
	res, err := s.db.Query(ctx, `SELECT id, created_at, user_name, role, action, target, detail, ip FROM audit_log
WHERE created_at >= ? AND created_at < ? AND `+cond+` ORDER BY id DESC LIMIT 100`,
		append([]any{tr.From.Format(tsLayout), tr.To.Add(time.Hour).Format(tsLayout)}, args...)...)
	if err != nil {
		return err
	}
	out["audit"] = newResult(res, 100, "", tr)
	out["match"] = "audit"
	return nil
}

// auditExportKey signs exports: derived from the session secret, so only
// this installation can produce or check a signature.
func (s *Server) auditExportKey(ctx context.Context) ([]byte, error) {
	m, err := s.readSettings(ctx)
	if err != nil {
		return nil, err
	}
	secret := m[settingSessionSecret][0]
	if secret == "" {
		return nil, errors.New("no session secret is stored yet")
	}
	sum := sha256.Sum256([]byte("visionary/audit-export/v1:" + secret))
	return sum[:], nil
}

// auditLine is one exported entry, in a fixed field order.
type auditLine struct {
	Seq       int             `json:"seq"`
	ID        int64           `json:"id"`
	CreatedAt string          `json:"created_at"`
	UserID    string          `json:"user_id"`
	UserName  string          `json:"user_name"`
	Role      string          `json:"role"`
	Action    string          `json:"action"`
	Target    string          `json:"target"`
	Detail    json.RawMessage `json:"detail"`
	IP        string          `json:"ip"`
	Prev      string          `json:"prev"`
	Hash      string          `json:"hash,omitempty"`
}

// hashAuditLine hashes a line without its own hash; prev chains it to the
// line before.
func hashAuditLine(l auditLine) string {
	l.Hash = ""
	b, _ := json.Marshal(l)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type auditSignature struct {
	Type       string `json:"type"` // "signature"
	Entries    int    `json:"entries"`
	From       string `json:"from"`
	To         string `json:"to"`
	ExportedAt string `json:"exported_at"`
	ExportedBy string `json:"exported_by"`
	Head       string `json:"head"`
	HMAC       string `json:"hmac"`
}

func (sig auditSignature) mac(key []byte) string {
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "%s|%d|%s|%s|%s|%s", sig.Head, sig.Entries, sig.From, sig.To, sig.ExportedAt, sig.ExportedBy)
	return hex.EncodeToString(m.Sum(nil))
}

// handleAuditExport streams the audit log in a range as signed JSON lines.
func (s *Server) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	tr, err := parseRange(r.URL.Query(), 3650)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	key, err := s.auditExportKey(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	res, err := s.db.Query(r.Context(), `SELECT id, created_at, user_id, user_name, role, action, target, detail, ip FROM audit_log
WHERE created_at >= ? AND created_at < ? ORDER BY id LIMIT 500000`, tr.From.Format(tsLayout), tr.To.Format(tsLayout))
	if err != nil {
		s.fail(w, err)
		return
	}
	sess := sessionFrom(r)
	by := ""
	if sess != nil {
		by = fallback(sess.Name, sess.Handle)
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="visionary-audit-`+tr.From.Format("20060102")+`-`+tr.To.Format("20060102")+`.jsonl"`)
	w.Header().Set("Cache-Control", "no-store")
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	prev := strings.Repeat("0", 64)
	n := 0
	for _, row := range res.Rows {
		if len(row) != 9 {
			continue
		}
		detail := json.RawMessage(toString(row[7]))
		if !json.Valid(detail) {
			detail, _ = json.Marshal(toString(row[7]))
		}
		var compact bytes.Buffer
		if json.Compact(&compact, detail) == nil {
			detail = compact.Bytes()
		}
		n++
		l := auditLine{Seq: n, ID: toInt(row[0]), CreatedAt: toString(row[1]), UserID: toString(row[2]), UserName: toString(row[3]),
			Role: toString(row[4]), Action: toString(row[5]), Target: toString(row[6]), Detail: detail, IP: toString(row[8]), Prev: prev}
		l.Hash = hashAuditLine(l)
		prev = l.Hash
		_ = enc.Encode(l)
	}
	sig := auditSignature{Type: "signature", Entries: n, From: tr.From.Format(tsLayout), To: tr.To.Format(tsLayout),
		ExportedAt: time.Now().UTC().Format(tsLayout), ExportedBy: by, Head: prev}
	sig.HMAC = sig.mac(key)
	_ = enc.Encode(sig)
	_ = bw.Flush()
	s.audit(r, "security.audit_exported", "", map[string]any{"from": sig.From, "to": sig.To, "entries": n})
}

// handleAuditVerify checks an export: every hash, the chain, and the
// signature. The file comes as {"file": "<contents>"}.
func (s *Server) handleAuditVerify(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		File string `json:"file"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "send the export as {\"file\": \"...\"}")
		return
	}
	key, err := s.auditExportKey(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, verifyAuditExport(body.File, key))
}

// verifyAuditExport reports whether an export is intact and where it breaks.
func verifyAuditExport(file string, key []byte) map[string]any {
	fail := func(line int, why string) map[string]any {
		return map[string]any{"valid": false, "line": line, "error": why}
	}
	prev := strings.Repeat("0", 64)
	sc := bufio.NewScanner(strings.NewReader(file))
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	line, entries := 0, 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		if strings.Contains(text, `"type":"signature"`) {
			var sig auditSignature
			if json.Unmarshal([]byte(text), &sig) != nil {
				return fail(line, "the signature line is not valid JSON")
			}
			if sig.Head != prev || sig.Entries != entries {
				return fail(line, "the signature doesn't match the entries above it: lines were added, removed or changed")
			}
			if !hmac.Equal([]byte(sig.mac(key)), []byte(sig.HMAC)) {
				return fail(line, "the signature is not this installation's, or the signature line was changed")
			}
			return map[string]any{"valid": true, "entries": entries, "from": sig.From, "to": sig.To, "exported_at": sig.ExportedAt, "exported_by": sig.ExportedBy}
		}
		var l auditLine
		if json.Unmarshal([]byte(text), &l) != nil {
			return fail(line, "not valid JSON")
		}
		entries++
		if l.Seq != entries || l.Prev != prev {
			return fail(line, "out of order, or a line before it is missing")
		}
		if hashAuditLine(l) != l.Hash {
			return fail(line, "this entry was changed after export")
		}
		prev = l.Hash
	}
	return fail(line, "the signature line is missing")
}
