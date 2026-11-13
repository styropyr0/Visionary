package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	rulesTick        = 30 * time.Second
	ruleGroupsLimit  = 100
	ruleNewLimit     = 500
	ruleLearnLimit   = 1000
	ruleSignalsLimit = 25
	ruleSamples      = 5
	ruleWarmup       = 5
)

type ruleRun struct {
	cursor   time.Time
	lastRun  time.Time
	lastErr  string
	active   map[string]bool
	baseline map[string][]float64
	learned  bool
}

type ruleRunState struct {
	mu   sync.Mutex
	runs map[int64]*ruleRun
}

var ruleRunStates sync.Map

func (s *Server) ruleRuns() *ruleRunState {
	v, _ := ruleRunStates.LoadOrStore(s, &ruleRunState{runs: map[int64]*ruleRun{}})
	return v.(*ruleRunState)
}

func (s *Server) forgetRule(ctx context.Context, id int64) {
	rs := s.ruleRuns()
	rs.mu.Lock()
	delete(rs.runs, id)
	rs.mu.Unlock()
	if err := s.db.Exec(ctx, `DELETE FROM security_known WHERE kind = ?`, ruleKnownKind(id)); err != nil {
		s.logger.Warn("security: forget rule values", "rule", id, "err", err)
	}
}

func ruleKnownKind(id int64) string { return "rule:" + strconv.FormatInt(id, 10) }

func (s *Server) rulesLoop(ctx context.Context) {
	tick := time.NewTicker(rulesTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.runRules(ctx, time.Now().UTC())
		}
	}
}

func (s *Server) runRules(parent context.Context, now time.Time) {
	defer func() {
		if p := recover(); p != nil {
			s.logger.Error("security: rules panicked", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		}
	}()
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	rules, err := s.listDetectionRules(ctx, true)
	if err != nil {
		if !missingTable(err, "security_rules") && ctx.Err() == nil {
			s.logger.Warn("security: load detection rules", "err", err)
		}
		return
	}
	rs := s.ruleRuns()
	for i := range rules {
		r := &rules[i]
		rs.mu.Lock()
		run := rs.runs[r.ID]
		if run == nil {
			run = &ruleRun{active: map[string]bool{}, baseline: map[string][]float64{}}
			rs.runs[r.ID] = run
		}
		due := now.Sub(run.lastRun) >= r.every()-rulesTick/2
		rs.mu.Unlock()
		if !due {
			continue
		}
		to := now.Add(-securityLag)
		cursor := run.cursor
		if cursor.IsZero() || cursor.Before(to.Add(-r.window())) {
			cursor = to.Add(-r.every())
		}
		sigs, err := s.evalRule(ctx, r, run, cursor, to)
		if err == nil {
			err = s.writeSignals(ctx, r, run, sigs, cursor, to, now)
		}
		rs.mu.Lock()
		run.lastRun, run.lastErr = now, ""
		if err != nil {
			run.lastErr = userMessage(err)
		} else {
			run.cursor = to
		}
		rs.mu.Unlock()
		if err != nil && ctx.Err() == nil && !missingTable(err, s.q.ruleSources()[r.Source].table) {
			s.logger.Warn("security: detection rule failed", "rule", r.Name, "err", err)
		}
	}
}

type ruleSignal struct {
	group   map[string]string
	key     string
	value   string
	ip      string
	n, nNew int64
	first   string
	last    string
	extra   map[string]any
	summary string
}

// An empty group value is traffic nobody is known for, such as signed-out requests by user
func hasEmpty(values map[string]string) bool {
	for _, v := range values {
		if v == "" {
			return true
		}
	}
	return false
}

func groupKey(fields []string, values map[string]string) string {
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, f+"="+values[f])
	}
	return strings.Join(parts, " ")
}

func (s *Server) groupExprs(r *detectionRule) []string {
	out := make([]string, len(r.GroupBy))
	for i, g := range r.GroupBy {
		out[i] = s.q.fieldExpr(r.Source, g)
	}
	return out
}

func (s *Server) groupCond(src string, values map[string]string, args *[]any) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		expr := s.q.fieldExpr(src, k)
		if expr == "" || k == "country" {
			continue
		}
		parts = append(parts, expr+" = ?")
		*args = append(*args, values[k])
	}
	if len(parts) == 0 {
		return "1=1"
	}
	return "(" + strings.Join(parts, " AND ") + ")"
}

func (s *Server) evalRule(ctx context.Context, r *detectionRule, run *ruleRun, cursor, to time.Time) ([]ruleSignal, error) {
	switch r.Logic {
	case "threshold", "anomaly":
		return s.evalCounts(ctx, r, run, cursor, to)
	case "new_value":
		return s.evalNewValues(ctx, r, run, cursor, to)
	case "sequence":
		return s.evalSequence(ctx, r, cursor, to)
	}
	return nil, nil
}

func (s *Server) countQuery(r *detectionRule, filter map[string]string, from, to, cursor time.Time, having int, limit int) (string, []any, error) {
	src := s.q.ruleSources()[r.Source]
	where, fargs, err := s.filterSQL(r.Source, filter, timeRange{From: from, To: to})
	if err != nil {
		return "", nil, err
	}
	groups := s.groupExprs(r)
	measure := "SUM(" + src.count + ")"
	if r.Distinct != "" {
		measure = "COUNT(DISTINCT " + s.q.fieldExpr(r.Source, r.Distinct) + ")"
	}
	sel := ""
	for i, g := range groups {
		sel += g + " AS g" + strconv.Itoa(i) + ", "
	}
	args := []any{cursor.UTC().Format(tsLayout)}
	args = append(args, spanArgs(from, to)...)
	args = append(args, fargs...)
	sql := `SELECT ` + sel + `SUM(` + src.count + `) AS n, SUM(CASE WHEN created_at >= ? THEN ` + src.count + ` ELSE 0 END) AS n_new, ` + measure + ` AS m,
  MIN(created_at) AS first_at, MAX(created_at) AS last_at
FROM ` + src.table + ` WHERE created_at >= ? AND created_at < ? AND ` + where
	if len(groups) > 0 {
		sql += "\nGROUP BY " + strings.Join(groups, ", ")
		if having > 0 {
			sql += "\nHAVING " + measure + " >= ?"
			args = append(args, having)
		}
		sql += "\nORDER BY m DESC"
	}
	return sql + "\nLIMIT " + strconv.Itoa(limit), args, nil
}

func (s *Server) evalCounts(ctx context.Context, r *detectionRule, run *ruleRun, cursor, to time.Time) ([]ruleSignal, error) {
	w := r.window()
	having := r.Threshold
	sql, args, err := s.countQuery(r, r.Filter, to.Add(-w), to, cursor, having, ruleGroupsLimit)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	ng := len(r.GroupBy)
	samples := map[string]float64{}
	var out []ruleSignal
	for _, row := range res.Rows {
		if len(row) != ng+5 {
			continue
		}
		values := map[string]string{}
		for i, g := range r.GroupBy {
			values[g] = toString(row[i])
		}
		n, nNew, m := toInt(row[ng]), toInt(row[ng+1]), toInt(row[ng+2])
		if n == 0 || m < int64(r.Threshold) || hasEmpty(values) {
			continue
		}
		key := groupKey(r.GroupBy, values)
		sig := ruleSignal{group: values, key: key, n: n, nNew: nNew, first: toString(row[ng+3]), last: toString(row[ng+4]), extra: map[string]any{}}
		who := fallback(key, "everything")
		if r.Logic == "anomaly" {
			samples[key] = float64(m)
			base, warm := run.baselineOf(key)
			if !warm || float64(m) < r.Factor*max(base, 1) {
				continue
			}
			sig.extra["usual"] = base
			sig.summary = fmt.Sprintf("%s %s in %s for %s, %.1f× the usual %.0f.", fnum(float64(m)), s.ruleUnit(r), fmtSpan(w), who, float64(m)/max(base, 1), base)
		} else if r.Distinct != "" {
			sig.extra["distinct"] = m
			sig.summary = fmt.Sprintf("%d different %s across %d %s in %s for %s (threshold %d).", m, r.Distinct, n, s.ruleUnit(r), fmtSpan(w), who, r.Threshold)
		} else {
			sig.summary = fmt.Sprintf("%d %s in %s for %s (threshold %d).", n, s.ruleUnit(r), fmtSpan(w), who, r.Threshold)
		}
		out = append(out, sig)
	}
	if r.Logic == "anomaly" {
		flagged := map[string]bool{}
		for _, sig := range out {
			flagged[sig.key] = true
		}
		run.record(samples, flagged)
	}
	return out, nil
}

func (s *Server) ruleUnit(r *detectionRule) string {
	return map[string]string{"requests": "requests", "logs": "log lines", "events": "events", "audit": "audit entries"}[r.Source]
}

func (run *ruleRun) baselineOf(key string) (float64, bool) {
	h := run.baseline[key]
	if len(h) < ruleWarmup {
		return 0, false
	}
	return median(append([]float64(nil), h...)), true
}

// Flagged groups are left out so an attack doesn't become its own normal
func (run *ruleRun) record(samples map[string]float64, flagged map[string]bool) {
	for k := range samples {
		if _, ok := run.baseline[k]; !ok && !flagged[k] && len(run.baseline) < 2000 {
			run.baseline[k] = nil
		}
	}
	for k, h := range run.baseline {
		if flagged[k] {
			continue
		}
		h = append(h, samples[k])
		if len(h) > baselineSamples {
			h = h[len(h)-baselineSamples:]
		}
		nonzero := false
		for _, v := range h {
			nonzero = nonzero || v != 0
		}
		if !nonzero && len(h) >= ruleWarmup {
			delete(run.baseline, k)
			continue
		}
		run.baseline[k] = h
	}
}

type valuePair struct {
	group       map[string]string
	key, value  string
	ip          string
	n           int64
	first, last string
}

func (s *Server) newValueRows(ctx context.Context, r *detectionRule, from, to time.Time, limit int) ([]valuePair, error) {
	src := s.q.ruleSources()[r.Source]
	where, fargs, err := s.filterSQL(r.Source, r.Filter, timeRange{From: from, To: to})
	if err != nil {
		return nil, err
	}
	groups := s.groupExprs(r)
	fexpr := s.q.fieldExpr(r.Source, r.Field)
	sel := ""
	for i, g := range groups {
		sel += g + " AS g" + strconv.Itoa(i) + ", "
	}
	args := append(spanArgs(from, to), fargs...)
	res, err := s.db.Query(ctx, `SELECT `+sel+fexpr+` AS v, SUM(`+src.count+`) AS n, MIN(created_at) AS first_at, MAX(created_at) AS last_at
FROM `+src.table+` WHERE created_at >= ? AND created_at < ? AND `+where+` AND `+fexpr+` <> ''
GROUP BY `+strings.Join(append(groups, fexpr), ", ")+`
ORDER BY first_at
LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		return nil, err
	}
	ng := len(r.GroupBy)
	byKey := map[string]*valuePair{}
	var out []*valuePair
	for _, row := range res.Rows {
		if len(row) != ng+4 {
			continue
		}
		values := map[string]string{}
		for i, g := range r.GroupBy {
			values[g] = toString(row[i])
		}
		v, ip := toString(row[ng]), ""
		if r.Field == "country" {
			ip = v
			if v = s.lookupIP(ip).Country; v == "" {
				continue
			}
		}
		key := groupKey(r.GroupBy, values)
		p := &valuePair{group: values, key: key, value: v, ip: ip, n: toInt(row[ng+1]), first: toString(row[ng+2]), last: toString(row[ng+3])}
		if old := byKey[key+"\x00"+v]; old != nil {
			old.n += p.n
			old.last = max(old.last, p.last)
			continue
		}
		byKey[key+"\x00"+v] = p
		out = append(out, p)
	}
	list := make([]valuePair, len(out))
	for i, p := range out {
		list[i] = *p
	}
	return list, nil
}

func (s *Server) evalNewValues(ctx context.Context, r *detectionRule, run *ruleRun, cursor, to time.Time) ([]ruleSignal, error) {
	if r.Field == "country" && !s.geoEnabled() {
		return nil, badInput("watching countries needs a location database (Settings → Server → Threat intelligence)")
	}
	kind := ruleKnownKind(r.ID)
	pairs, err := s.newValueRows(ctx, r, cursor, to, ruleNewLimit)
	if err != nil {
		return nil, err
	}
	if !run.learned {
		res, err := s.db.Query(ctx, `SELECT 1 FROM security_known WHERE kind = ? AND account = '~' LIMIT 1`, kind)
		if err != nil {
			return nil, err
		}
		if len(res.Rows) == 0 {
			// First run learns each group's values over the learning period. With no history at all,
			// this run's values are learned too instead of all flagged, and with none yet the next run tries again
			past, err := s.newValueRows(ctx, r, cursor.Add(-r.learn()), cursor, ruleLearnLimit)
			if err != nil {
				return nil, err
			}
			if len(past) == 0 {
				past = pairs
			}
			if len(past) == 0 {
				return nil, nil
			}
			for _, p := range past {
				if err := s.knowValue(ctx, kind, p.key, p.value, p.last); err != nil {
					return nil, err
				}
			}
			if err := s.knowValue(ctx, kind, "~", "~", to.Format(tsLayout)); err != nil {
				return nil, err
			}
		}
		run.learned = true
	}
	known, err := s.knownValues(ctx, kind, pairs)
	if err != nil {
		return nil, err
	}
	var out []ruleSignal
	for _, p := range pairs {
		id := clip(fallback(p.key, "*"), 191) + "\x00" + clip(p.value, 191)
		if known[id] {
			continue
		}
		known[id] = true
		if err := s.knowValue(ctx, kind, p.key, p.value, p.first); err != nil {
			return nil, err
		}
		who := ""
		if p.key != "" {
			who = " for " + p.key
		}
		sig := ruleSignal{group: p.group, key: p.key, value: p.value, ip: p.ip, n: p.n, nNew: p.n, first: p.first, last: p.last,
			extra:   map[string]any{"field": r.Field, "new_value": p.value},
			summary: fmt.Sprintf("First %s %q%s, not seen in the %s before.", r.Field, p.value, who, fmtSpan(r.learn()))}
		out = append(out, sig)
	}
	return out, nil
}

func (s *Server) knowValue(ctx context.Context, kind, account, value, at string) error {
	err := s.db.Exec(ctx, `INSERT INTO security_known (account, kind, value, first_seen, last_seen, hits) VALUES (?, ?, ?, ?, ?, 1)`,
		clip(fallback(account, "*"), 191), kind, clip(value, 191), at, at)
	if err != nil && !isDuplicateKey(err) {
		return err
	}
	return nil
}

func (s *Server) knownValues(ctx context.Context, kind string, pairs []valuePair) (map[string]bool, error) {
	out := map[string]bool{}
	seen := map[string]bool{}
	var accounts []string
	for _, p := range pairs {
		a := clip(fallback(p.key, "*"), 191)
		if !seen[a] {
			seen[a] = true
			accounts = append(accounts, a)
		}
	}
	for len(accounts) > 0 {
		batch := head(accounts, maxInArgs)
		accounts = accounts[len(batch):]
		args := []any{kind}
		for _, a := range batch {
			args = append(args, a)
		}
		res, err := s.db.Query(ctx, `SELECT account, value FROM security_known WHERE kind = ? AND account IN (`+placeholders(len(batch))+`) LIMIT 5000`, args...)
		if err != nil {
			return nil, err
		}
		for _, row := range res.Rows {
			if len(row) == 2 {
				out[toString(row[0])+"\x00"+toString(row[1])] = true
			}
		}
	}
	return out, nil
}

type stepTime struct {
	at time.Time
	n  int64
}

func (s *Server) evalSequence(ctx context.Context, r *detectionRule, cursor, to time.Time) ([]ruleSignal, error) {
	w := r.window()
	from := cursor.Add(-w)
	sql, args, err := s.countQuery(r, r.Filter, from, to, cursor, r.Threshold, ruleGroupsLimit)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	ng := len(r.GroupBy)
	var cands []map[string]string
	for _, row := range res.Rows {
		if len(row) != ng+5 {
			continue
		}
		values := map[string]string{}
		for i, g := range r.GroupBy {
			values[g] = toString(row[i])
		}
		if hasEmpty(values) {
			continue
		}
		cands = append(cands, values)
	}
	if len(cands) == 0 {
		return nil, nil
	}
	cands = head(cands, 50)
	thenRows, err := s.stepTimes(ctx, r, r.Then, cands, cursor, to, 500)
	if err != nil || len(thenRows) == 0 {
		return nil, err
	}
	var both []map[string]string
	for _, c := range cands {
		if len(thenRows[groupKey(r.GroupBy, c)]) > 0 {
			both = append(both, c)
		}
	}
	firstRows, err := s.stepTimes(ctx, r, r.Filter, head(both, 20), from, to, 2000)
	if err != nil {
		return nil, err
	}
	var out []ruleSignal
	for _, c := range head(both, 20) {
		key := groupKey(r.GroupBy, c)
		firsts := firstRows[key]
		for _, b := range thenRows[key] {
			var n int64
			var earliest time.Time
			for _, a := range firsts {
				if !a.at.Before(b.at.Add(-w)) && a.at.Before(b.at) {
					n += a.n
					if earliest.IsZero() {
						earliest = a.at
					}
				}
			}
			if n < int64(r.Threshold) {
				continue
			}
			out = append(out, ruleSignal{group: c, key: key, n: n + b.n, nNew: n + b.n,
				first: earliest.Format(tsLayout), last: b.at.Format(tsLayout),
				extra:   map[string]any{"first_step": n, "then_at": b.at.Format(tsLayout), "took": fmtSpan(b.at.Sub(earliest).Round(time.Second))},
				summary: fmt.Sprintf("%d matches of the first step, then the second step %s later, for %s (within %s).", n, fmtSpan(b.at.Sub(earliest).Round(time.Second)), key, fmtSpan(w))})
			break
		}
	}
	return out, nil
}

func (s *Server) stepTimes(ctx context.Context, r *detectionRule, filter map[string]string, groups []map[string]string, from, to time.Time, limit int) (map[string][]stepTime, error) {
	out := map[string][]stepTime{}
	if len(groups) == 0 {
		return out, nil
	}
	src := s.q.ruleSources()[r.Source]
	where, fargs, err := s.filterSQL(r.Source, filter, timeRange{From: from, To: to})
	if err != nil {
		return nil, err
	}
	args := append(spanArgs(from, to), fargs...)
	ors := make([]string, len(groups))
	for i, g := range groups {
		ors[i] = s.groupCond(r.Source, g, &args)
	}
	exprs := s.groupExprs(r)
	res, err := s.db.Query(ctx, `SELECT `+strings.Join(exprs, ", ")+`, created_at, `+src.count+` AS n FROM `+src.table+`
WHERE created_at >= ? AND created_at < ? AND `+where+` AND (`+strings.Join(ors, " OR ")+`)
ORDER BY created_at LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		return nil, err
	}
	ng := len(exprs)
	for _, row := range res.Rows {
		if len(row) != ng+2 {
			continue
		}
		values := map[string]string{}
		for i, g := range r.GroupBy {
			values[g] = toString(row[i])
		}
		at, err := time.Parse(tsLayout, toString(row[ng]))
		if err != nil {
			continue
		}
		key := groupKey(r.GroupBy, values)
		out[key] = append(out[key], stepTime{at, max(toInt(row[ng+1]), 1)})
	}
	return out, nil
}

func (s *Server) ruleEvents(ctx context.Context, r *detectionRule, values map[string]string, from, to time.Time, limit int) (*Result, error) {
	src := s.q.ruleSources()[r.Source]
	steps := []map[string]string{r.Filter}
	if r.Logic == "sequence" {
		steps = append(steps, r.Then)
	}
	var out *Result
	for i, f := range steps {
		where, fargs, err := s.filterSQL(r.Source, f, timeRange{From: from, To: to})
		if err != nil {
			return nil, err
		}
		args := append(spanArgs(from, to), fargs...)
		cond := s.groupCond(r.Source, values, &args)
		res, err := s.db.Query(ctx, `SELECT `+strconv.Itoa(i+1)+` AS step, `+src.sample+` FROM `+src.table+`
WHERE created_at >= ? AND created_at < ? AND `+where+` AND `+cond+`
ORDER BY created_at DESC LIMIT `+strconv.Itoa(limit), args...)
		if err != nil {
			return nil, err
		}
		page := newResult(res, limit, "", timeRange{From: from, To: to})
		if out == nil {
			out = page
			continue
		}
		out.Rows = append(out.Rows, page.Rows...)
		out.Truncated = out.Truncated || page.Truncated
	}
	if out != nil && len(steps) > 1 {
		sort.SliceStable(out.Rows, func(i, j int) bool { return toString(out.Rows[i][1]) > toString(out.Rows[j][1]) })
	}
	if out != nil && out.Rows == nil {
		out.Rows = [][]any{}
	}
	return out, nil
}

func compactRows(res *Result, n int) []map[string]any {
	if res == nil {
		return nil
	}
	var out []map[string]any
	for _, row := range head(res.Rows, n) {
		m := map[string]any{}
		for i, c := range res.Columns {
			if i >= len(row) || row[i] == nil || c == "id" {
				continue
			}
			if v, ok := row[i].(string); ok {
				if v == "" {
					continue
				}
				if c == "path" {
					v = maskPath(v)
				}
				m[c] = truncate(v, 160)
				continue
			}
			m[c] = row[i]
		}
		out = append(out, m)
	}
	return out
}

func (r *detectionRule) signalFinding(sig ruleSignal) finding {
	f := finding{source: "rule", kind: r.Kind, severity: r.Severity, n: sig.n, nNew: sig.nNew,
		ip: sig.group["ip"], actor: fallback(sig.group["user"], sig.group["account"]), host: sig.group["host"], service: sig.group["service"]}
	if r.Field == "ip" {
		f.ip = sig.value
	}
	if f.ip == "" {
		f.ip = sig.ip
	}
	f.target = fallback(sig.key, "all")
	if sig.value != "" {
		f.target += " · " + r.Field + "=" + sig.value
	}
	f.target = clip(f.target, 255)
	group := map[string]any{}
	for k, v := range sig.group {
		group[k] = v
	}
	f.detail = map[string]any{"summary": sig.summary, "rule_id": r.ID, "rule": r.Name, "logic": r.Logic, "window": fmtSpan(r.window()),
		"group": group, "first": sig.first, "last": sig.last}
	if r.Tactic != "" {
		f.detail["tactic"] = r.Tactic
	}
	if r.Technique != "" {
		f.detail["technique"] = r.Technique
	}
	for k, v := range sig.extra {
		f.detail[k] = v
	}
	return f
}

func detailJSON(d map[string]any) string {
	b, err := json.Marshal(d)
	for err == nil && len(b) > maxDetailBytes {
		samples, _ := d["samples"].([]map[string]any)
		if len(samples) == 0 {
			break
		}
		d["samples"] = samples[:len(samples)-1]
		b, err = json.Marshal(d)
	}
	if err != nil || len(b) > maxDetailBytes {
		return "{}"
	}
	return string(b)
}

func (s *Server) writeSignals(ctx context.Context, r *detectionRule, run *ruleRun, sigs []ruleSignal, cursor, to, now time.Time) error {
	active := map[string]bool{}
	if len(sigs) == 0 {
		run.active = active
		return nil
	}
	res, err := s.db.Query(ctx, `SELECT id, target, severity FROM security_events WHERE source = 'rule' AND kind = ? AND created_at >= ? ORDER BY id LIMIT 1000`,
		r.Kind, now.Add(-r.dedupe()).Format(tsLayout))
	if err != nil {
		return err
	}
	open := map[string]int64{}
	for _, row := range res.Rows {
		if len(row) == 3 {
			open[toString(row[1])] = toInt(row[0])
		}
	}
	var found []finding
	var kept []ruleSignal
	for _, sig := range sigs {
		values := map[string]string{}
		for k, v := range sig.group {
			values[k] = v
		}
		if sig.value != "" {
			values[r.Field] = sig.value
		}
		if r.suppressedBy(values, now) != nil {
			continue
		}
		found = append(found, r.signalFinding(sig))
		kept = append(kept, sig)
	}
	before := len(found)
	found, err = s.prepareFindings(ctx, found)
	if err != nil {
		return err
	}
	if len(found) != before {
		keep := map[string]bool{}
		for _, f := range found {
			keep[f.target] = true
		}
		var k2 []ruleSignal
		for _, sig := range kept {
			if keep[r.signalFinding(sig).target] {
				k2 = append(k2, sig)
			}
		}
		kept = k2
	}
	var fresh []finding
	var freshIDs []int64
	written := 0
	for i, f := range found {
		sig := kept[i]
		active[f.target] = true
		if written >= ruleSignalsLimit {
			continue
		}
		id, isOpen := open[f.target]
		if isOpen && (sig.nNew <= 0 || r.Logic == "new_value") {
			continue
		}
		count := f.n
		if !isOpen && run.active[f.target] {
			count = f.nNew
		}
		if count <= 0 {
			continue
		}
		written++
		if ev, err := s.ruleEvents(ctx, r, sig.group, to.Add(-r.window()), to, ruleSamples); err == nil {
			f.detail["samples"] = compactRows(ev, ruleSamples)
		}
		detail := detailJSON(f.detail)
		if isOpen {
			if err := s.db.Exec(ctx, `UPDATE security_events SET count = count + ?, detail = ? WHERE id = ?`, sig.nNew, detail, id); err != nil {
				return err
			}
			continue
		}
		newID, err := s.db.InsertID(ctx, `INSERT INTO security_events (created_at, source, kind, severity, host, service, actor, ip, target, detail, count)
VALUES (?, 'rule', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			now.Format(tsLayout), f.kind, f.severity, nullable(f.host), nullable(f.service), nullable(clip(f.actor, 255)), nullable(f.ip), f.target, detail, count)
		if err != nil {
			return err
		}
		s.logger.Info("security signal", "rule", r.Name, "kind", r.Kind, "group", f.target, "count", count)
		fresh = append(fresh, f)
		freshIDs = append(freshIDs, newID)
	}
	run.active = active
	if len(fresh) > 0 && len(r.Channels) > 0 {
		n := s.signalNotice(r, fresh, freshIDs)
		chans := append([]int64(nil), r.Channels...)
		go func() {
			nctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			s.deliver(nctx, 0, nil, chans, n)
		}()
	}
	return nil
}

func (s *Server) signalNotice(r *detectionRule, fresh []finding, ids []int64) notice {
	title := "[SECURITY] " + r.Name
	if len(fresh) == 1 {
		title += " · " + fresh[0].target
	} else {
		title += fmt.Sprintf(" · %d groups", len(fresh))
	}
	n := notice{Event: "firing", Title: title, Severity: r.Severity, At: time.Now().UTC(), Runbook: r.Runbook,
		Key: "security-rule:" + strconv.FormatInt(r.ID, 10)}
	var list []map[string]any
	for i, f := range head(fresh, 10) {
		summary, _ := f.detail["summary"].(string)
		n.Lines = append(n.Lines, summary)
		list = append(list, map[string]any{"id": ids[i], "group": f.detail["group"], "target": f.target, "severity": f.severity, "summary": summary,
			"new_value": f.detail["new_value"], "count": f.n})
	}
	if len(fresh) > 10 {
		n.Lines = append(n.Lines, fmt.Sprintf("And %d more.", len(fresh)-10))
	}
	n.Fields = append(n.Fields, [2]string{"Severity", r.Severity})
	if r.Tactic != "" {
		n.Fields = append(n.Fields, [2]string{"MITRE tactic", mitreName(r.Tactic)})
	}
	if r.Technique != "" {
		n.Fields = append(n.Fields, [2]string{"MITRE technique", mitreName(r.Technique)})
	}
	n.Payload = map[string]any{
		"event":     "security.signal",
		"rule":      map[string]any{"id": r.ID, "name": r.Name, "kind": r.Kind, "logic": r.Logic, "source": r.Source},
		"severity":  r.Severity,
		"tactic":    r.Tactic,
		"technique": r.Technique,
		"signals":   list,
		"at":        n.At.Format(time.RFC3339),
	}
	return n
}

func (s *Server) findingRule(ctx context.Context, out, f map[string]any, at time.Time) error {
	out["match"] = "rule"
	res, err := s.db.Query(ctx, `SELECT id, name, kind, enabled, spec, created_at, created_by, updated_at FROM security_rules WHERE kind = ?`, f["kind"])
	if err != nil || len(res.Rows) == 0 {
		return err
	}
	rule, ok := scanDetectionRule(res.Rows[0])
	if !ok {
		return nil
	}
	out["rule"] = rule
	values := map[string]string{}
	if d, ok := f["detail"].(map[string]any); ok {
		if g, ok := d["group"].(map[string]any); ok {
			for k, v := range g {
				values[k] = toString(v)
			}
		}
	}
	from := at.Add(-rule.window() - time.Minute)
	to := at.Add(rule.dedupe() + time.Minute)
	if now := time.Now().UTC(); to.After(now) {
		to = now
	}
	out["from"], out["to"] = from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano)
	ev, err := s.ruleEvents(ctx, &rule, values, from, to, 200)
	if err != nil {
		return err
	}
	out["events"] = ev
	return nil
}
