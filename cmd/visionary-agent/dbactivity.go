package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/styropyr0/visionary/internal/agentapi"
	"github.com/styropyr0/visionary/internal/sqlredact"
)

// Database activity: with VISIONARY_AGENT_DATABASES set, the agent connects
// to each listed PostgreSQL or MySQL/MariaDB server with a read-only
// monitoring user, and every db-interval reads its sessions, locks and
// connection counts; once per report it reads pg_stat_statements or
// performance_schema's statement digests and sends what each statement did
// since the previous read. Each server is read on its own goroutine over one
// connection, every query with a short timeout, so a slow or unreachable
// server never holds up the host's report.

const (
	dbQueryTimeout  = 5 * time.Second
	dbMaxSessions   = 200 // per sample
	dbReportSession = 300 // per report
	dbTopStatements = 50  // per statement read
	dbMaxPending    = 240 // samples kept while reports fail
)

// dbTarget is one server from VISIONARY_AGENT_DATABASES.
type dbTarget struct {
	name   string
	engine string // postgres or mysql
	driver string // the database/sql driver
	dsn    string
	secret string // the password, scrubbed from errors
}

var dbNameChars = regexp.MustCompile(`[^A-Za-z0-9._:/@-]+`)

// parseDatabases reads a list of postgres:// and mysql:// URLs separated by
// spaces, newlines or commas.
func parseDatabases(s string) ([]dbTarget, error) {
	var out []dbTarget
	seen := map[string]bool{}
	for _, raw := range splitDSNs(s) {
		t, err := parseDBURL(raw)
		if err != nil {
			return nil, err
		}
		if seen[t.name] {
			return nil, fmt.Errorf("two databases are named %q: add name= to one of them", t.name)
		}
		seen[t.name] = true
		out = append(out, t)
	}
	return out, nil
}

// splitDSNs splits on whitespace, and on a comma only where the next URL
// starts, since a PostgreSQL URL may list hosts with commas.
func splitDSNs(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		for f != "" {
			cut := -1
			for i := strings.IndexByte(f, ','); i >= 0; {
				rest := f[i+1:]
				if strings.HasPrefix(rest, "postgres://") || strings.HasPrefix(rest, "postgresql://") || strings.HasPrefix(rest, "mysql://") || strings.HasPrefix(rest, "mariadb://") {
					cut = i
					break
				}
				j := strings.IndexByte(rest, ',')
				if j < 0 {
					break
				}
				i += j + 1
			}
			if cut < 0 {
				out = append(out, strings.TrimSuffix(f, ","))
				break
			}
			if cut > 0 {
				out = append(out, f[:cut])
			}
			f = f[cut+1:]
		}
	}
	return out
}

func parseDBURL(raw string) (dbTarget, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" && u.Query().Get("host") == "" && u.Query().Get("socket") == "" {
		return dbTarget{}, fmt.Errorf("database %q must be a postgres:// or mysql:// URL", redactDSN(raw))
	}
	q := u.Query()
	name := q.Get("name")
	q.Del("name")
	pw, _ := u.User.Password()
	t := dbTarget{secret: pw}
	dbName := strings.TrimPrefix(u.Path, "/")
	switch u.Scheme {
	case "postgres", "postgresql":
		t.engine, t.driver = "postgres", "pgx"
		// Runtime parameters for the session: named, read-only and quick.
		for k, v := range map[string]string{"application_name": "visionary-agent", "connect_timeout": "5",
			"statement_timeout": "5000", "default_transaction_read_only": "on", "default_query_exec_mode": "exec"} {
			if q.Get(k) == "" {
				q.Set(k, v)
			}
		}
		u.RawQuery = q.Encode()
		t.dsn = u.String()
	case "mysql", "mariadb":
		t.engine, t.driver = "mysql", "mysql"
		cfg := mysql.NewConfig()
		cfg.User = u.User.Username()
		cfg.Passwd = pw
		cfg.DBName = dbName
		if sock := q.Get("socket"); sock != "" {
			cfg.Net, cfg.Addr = "unix", sock
			q.Del("socket")
		} else {
			cfg.Net, cfg.Addr = "tcp", u.Host
			if u.Port() == "" {
				cfg.Addr = net.JoinHostPort(u.Hostname(), "3306")
			}
		}
		if len(q) > 0 {
			// The driver's own parser knows its parameters (tls, charset...);
			// the rest it sets as session variables.
			parsed, err := mysql.ParseDSN(cfg.FormatDSN() + "?" + q.Encode())
			if err != nil {
				return dbTarget{}, fmt.Errorf("database %q: %v", redactDSN(raw), err)
			}
			cfg = parsed
		}
		cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = dbQueryTimeout, 2*dbQueryTimeout, dbQueryTimeout
		t.dsn = cfg.FormatDSN()
	default:
		return dbTarget{}, fmt.Errorf("database %q must be a postgres:// or mysql:// URL", redactDSN(raw))
	}
	if name == "" {
		host := u.Host
		if host == "" {
			host = "localhost"
		}
		name = host
		if dbName != "" {
			name += "/" + dbName
		}
	}
	name = strings.Trim(dbNameChars.ReplaceAllString(name, "-"), "-")
	if len(name) > 128 {
		name = name[:128]
	}
	if name == "" {
		return dbTarget{}, errors.New("a database needs a name: add name= to its URL")
	}
	t.name = name
	return t, nil
}

// redactDSN hides a URL's password for messages.
func redactDSN(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
		return u.String()
	}
	return "(unreadable URL)"
}

// dbEngine reads one kind of server. Readings carry raw query text; the
// monitor redacts it.
type dbEngine interface {
	init(ctx context.Context, db *sql.DB) (version string, err error)
	sample(ctx context.Context, db *sql.DB, now time.Time) (dbReading, error)
	statements(ctx context.Context, db *sql.DB) ([]stmtRow, error)
	texts(ctx context.Context, db *sql.DB, ids []string) (map[string]string, error)
}

type dbReading struct {
	values   map[string]float64 // gauges
	counters map[string]float64 // cumulative: commits, rollbacks, deadlocks
	sessions []agentapi.DBSession
}

// noteSet holds what a monitor could not read, by topic, until it can.
type noteSet struct {
	mu sync.Mutex
	m  map[string]string
}

func (n *noteSet) set(key, msg string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.m == nil {
		n.m = map[string]string{}
	}
	n.m[key] = msg
}

func (n *noteSet) clear(key string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.m, key)
}

func (n *noteSet) list() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	keys := make([]string, 0, len(n.m))
	for k := range n.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, n.m[k])
	}
	return out
}

// dbCollector runs one monitor per server.
type dbCollector struct {
	every     time.Duration
	stmtEvery time.Duration
	mons      []*dbMonitor
}

func newDBCollector(a *agent, targets []dbTarget) *dbCollector {
	c := &dbCollector{every: a.cfg.dbInterval, stmtEvery: max(a.cfg.report, 30*time.Second)}
	for _, t := range targets {
		m := &dbMonitor{a: a, t: t, notes: &noteSet{}, sessions: map[string]*agentapi.DBSession{}, stmts: newStmtTracker()}
		if t.engine == "postgres" {
			m.eng = &pgEngine{notes: m.notes}
		} else {
			m.eng = &mysqlEngine{notes: m.notes}
		}
		c.mons = append(c.mons, m)
	}
	return c
}

func (c *dbCollector) run(ctx context.Context) {
	for _, m := range c.mons {
		go m.run(ctx, c.every, c.stmtEvery)
	}
}

func (c *dbCollector) take() []agentapi.Database {
	var out []agentapi.Database
	for _, m := range c.mons {
		out = append(out, m.take())
	}
	return out
}

// dbMonitor samples one server. Its goroutine writes the pending state and
// the report loop takes it, under mu.
type dbMonitor struct {
	a     *agent
	t     dbTarget
	eng   dbEngine
	db    *sql.DB
	notes *noteSet
	stmts *stmtTracker

	prevCounters map[string]float64
	lastStmt     time.Time

	mu         sync.Mutex
	version    string
	samples    []agentapi.DBSample
	sessions   map[string]*agentapi.DBSession
	latest     string // the newest sample's time
	statements []agentapi.DBStatement
}

func (m *dbMonitor) run(ctx context.Context, every, stmtEvery time.Duration) {
	defer func() {
		if m.db != nil {
			m.db.Close()
		}
	}()
	t := time.NewTicker(every)
	defer t.Stop()
	m.poll(ctx, stmtEvery)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.poll(ctx, stmtEvery)
		}
	}
}

func (m *dbMonitor) poll(ctx context.Context, stmtEvery time.Duration) {
	defer func() {
		if r := recover(); r != nil {
			m.a.log.Error("database sample panicked", "database", m.t.name, "panic", r)
		}
	}()
	if m.db == nil {
		db, err := sql.Open(m.t.driver, m.t.dsn)
		if err != nil {
			m.notes.set("connect", "Cannot connect: "+m.scrub(err))
			return
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		db.SetConnMaxLifetime(30 * time.Minute)
		cctx, cancel := context.WithTimeout(ctx, dbQueryTimeout)
		version, err := m.eng.init(cctx, db)
		cancel()
		if err != nil {
			db.Close()
			m.notes.set("connect", "Cannot connect: "+m.scrub(err))
			m.a.once("db-connect-"+m.t.name, "database not readable", "database", m.t.name, "err", m.scrub(err))
			return
		}
		m.notes.clear("connect")
		m.db = db
		m.mu.Lock()
		m.version = version
		m.mu.Unlock()
	}
	now := time.Now().UTC()
	sctx, cancel := context.WithTimeout(ctx, 2*dbQueryTimeout)
	r, err := m.eng.sample(sctx, m.db, now)
	cancel()
	if err != nil {
		m.notes.set("sample", "Sampling failed: "+m.scrub(err))
		return
	}
	m.notes.clear("sample")
	at := now.Format(time.RFC3339Nano)
	values := r.values
	for k, v := range counterDeltas(m.prevCounters, r.counters) {
		values[k] = v
	}
	m.prevCounters = r.counters
	for i := range r.sessions {
		r.sessions[i].Query = sqlredact.Redact(m.t.engine, r.sessions[i].Query)
	}

	var stmts []agentapi.DBStatement
	if now.Sub(m.lastStmt) >= stmtEvery-time.Second {
		m.lastStmt = now
		stmts = m.readStatements(ctx, now)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.samples = append(m.samples, agentapi.DBSample{At: at, Values: values})
	if len(m.samples) > dbMaxPending {
		m.samples = m.samples[len(m.samples)-dbMaxPending:]
	}
	mergeSessions(m.sessions, r.sessions, at)
	m.latest = at
	m.statements = append(m.statements, stmts...)
	if len(m.statements) > 4*dbTopStatements {
		m.statements = m.statements[len(m.statements)-4*dbTopStatements:]
	}
}

// readStatements reads the statement counters and returns what the busiest
// statements did since the previous read, with their redacted text.
func (m *dbMonitor) readStatements(ctx context.Context, now time.Time) []agentapi.DBStatement {
	sctx, cancel := context.WithTimeout(ctx, 2*dbQueryTimeout)
	defer cancel()
	rows, err := m.eng.statements(sctx, m.db)
	if err != nil {
		return nil // the engine notes why
	}
	deltas, interval := m.stmts.update(rows, now, dbTopStatements)
	if len(deltas) == 0 {
		return nil
	}
	var missing []string
	for _, d := range deltas {
		if _, ok := m.stmts.texts[d.id]; !ok {
			missing = append(missing, d.id)
		}
	}
	if len(missing) > 0 {
		texts, err := m.eng.texts(sctx, m.db, missing)
		if err == nil {
			if len(m.stmts.texts) > 5000 {
				m.stmts.texts = map[string]string{}
			}
			for id, q := range texts {
				m.stmts.texts[id] = sqlredact.Redact(m.t.engine, q)
			}
		}
	}
	at := now.Format(time.RFC3339Nano)
	out := make([]agentapi.DBStatement, 0, len(deltas))
	for _, d := range deltas {
		st := agentapi.DBStatement{At: at, IntervalS: round(interval.Seconds(), 1), ID: d.id, Database: d.db, User: d.user,
			Query: m.stmts.texts[d.id], Calls: int64(d.c.calls), TotalMs: round(d.c.totalMs, 3), Rows: int64(d.c.rows), MaxMs: round(d.c.maxMs, 3)}
		if d.mysql {
			ex, er := int64(d.c.examined), int64(d.c.errors)
			st.RowsExamined, st.Errors = &ex, &er
		}
		out = append(out, st)
	}
	return out
}

// scrub is an error's text without the password.
func (m *dbMonitor) scrub(err error) string {
	msg := err.Error()
	if m.t.secret != "" {
		msg = strings.ReplaceAll(msg, m.t.secret, "xxxxx")
	}
	return clipRunes(msg, 300)
}

func (m *dbMonitor) take() agentapi.Database {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := agentapi.Database{Server: m.t.name, Engine: m.t.engine, Version: m.version, Notes: m.notes.list(),
		Samples: m.samples, Statements: m.statements, Sessions: reportSessions(m.sessions, m.latest, dbReportSession)}
	m.samples, m.statements = nil, nil
	m.sessions = map[string]*agentapi.DBSession{}
	return d
}

// counterDeltas is cur minus prev per counter; a counter that went down (a
// restart or reset) or is new has no delta.
func counterDeltas(prev, cur map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k, v := range cur {
		p, ok := prev[k]
		if ok && v >= p {
			out[k] = v - p
		}
	}
	return out
}

// mergeSessions folds one sample's sessions into those seen since the last
// report: one entry per statement execution, keeping the latest reading. The
// same pid running the same text again, started later, is a new execution.
func mergeSessions(seen map[string]*agentapi.DBSession, cur []agentapi.DBSession, at string) {
	for i := range cur {
		s := cur[i]
		s.SeenAt = at
		key := strconv.FormatInt(s.PID, 10) + "|" + s.State + "|" + s.Query
		if old, ok := seen[key]; ok && !sameStart(old.QueryStart, s.QueryStart) {
			seen[key+"|"+old.QueryStart] = old
		}
		seen[key] = &s
	}
}

// sameStart allows the second or two MySQL's whole-second TIME moves by.
func sameStart(a, b string) bool {
	if a == b {
		return true
	}
	ta, err1 := time.Parse(time.RFC3339Nano, a)
	tb, err2 := time.Parse(time.RFC3339Nano, b)
	if err1 != nil || err2 != nil {
		return false
	}
	d := ta.Sub(tb)
	return d < 2*time.Second && d > -2*time.Second
}

// reportSessions lists the sessions to send: every one running at the latest
// sample, then the longest of the rest, n in all.
func reportSessions(seen map[string]*agentapi.DBSession, latest string, n int) []agentapi.DBSession {
	out := make([]agentapi.DBSession, 0, len(seen))
	for _, s := range seen {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := out[i].SeenAt == latest, out[j].SeenAt == latest
		if li != lj {
			return li
		}
		if out[i].ElapsedS != out[j].ElapsedS {
			return out[i].ElapsedS > out[j].ElapsedS
		}
		return out[i].PID < out[j].PID
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// stmtCounters are a statement's cumulative counters, or their deltas.
type stmtCounters struct {
	calls, totalMs, rows, examined, errors, maxMs float64
}

// stmtRow is one row of pg_stat_statements or the digest summary.
type stmtRow struct {
	id, db, user string
	mysql        bool
	c            stmtCounters
}

func (r stmtRow) key() string { return r.id + "|" + r.db + "|" + r.user }

type stmtDelta = stmtRow

// stmtTracker turns cumulative statement counters into per-read deltas.
type stmtTracker struct {
	prev  map[string]stmtCounters
	at    time.Time
	texts map[string]string // redacted text by id
}

func newStmtTracker() *stmtTracker {
	return &stmtTracker{texts: map[string]string{}}
}

// update takes a full read of the counters and returns the top statements
// by time spent since the previous read, and the time between the reads. The
// first read only sets the baseline. A statement new since the previous read
// counts in full; one whose counters went down was reset and counts from
// zero.
func (t *stmtTracker) update(rows []stmtRow, now time.Time, top int) ([]stmtDelta, time.Duration) {
	cur := make(map[string]stmtRow, len(rows))
	order := make([]string, 0, len(rows))
	for _, r := range rows {
		k := r.key()
		if c, dup := cur[k]; dup { // PostgreSQL's toplevel and nested rows
			r.c.calls += c.c.calls
			r.c.totalMs += c.c.totalMs
			r.c.rows += c.c.rows
			r.c.examined += c.c.examined
			r.c.errors += c.c.errors
			r.c.maxMs = max(r.c.maxMs, c.c.maxMs)
		} else {
			order = append(order, k)
		}
		cur[k] = r
	}
	first := t.prev == nil
	interval := now.Sub(t.at)
	next := make(map[string]stmtCounters, len(cur))
	var out []stmtDelta
	for _, k := range order {
		r := cur[k]
		c := r.c
		next[k] = c
		if first {
			continue
		}
		d := c
		if p, seen := t.prev[k]; seen && c.calls >= p.calls && c.totalMs >= p.totalMs {
			d = stmtCounters{calls: c.calls - p.calls, totalMs: c.totalMs - p.totalMs, rows: max(c.rows-p.rows, 0),
				examined: max(c.examined-p.examined, 0), errors: max(c.errors-p.errors, 0), maxMs: c.maxMs}
		}
		if d.calls <= 0 {
			continue
		}
		r.c = d
		out = append(out, r)
	}
	t.prev, t.at = next, now
	sort.SliceStable(out, func(i, j int) bool { return out[i].c.totalMs > out[j].c.totalMs })
	if len(out) > top {
		out = out[:top]
	}
	return out, interval
}
