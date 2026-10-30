package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Time partitioning on PostgreSQL, opt in (Settings → Data). The tables that
// grow with traffic are range-partitioned by day on created_at. The text
// format sorts as time, so day bounds such as '2026-10-30' work as is.
// Retention then drops whole days instead of deleting rows in chunks, and
// queries over a range read only its days.
//
// Turning it on converts tables that are still empty at once. A table with
// rows is converted only when an admin asks: a copy into a partitioned twin
// in chunks while writes go on, then a short swap under a lock.

var partitionTables = []string{"api_logs", "spans", "app_logs", "metrics", "client_events"}

const (
	settingPartitioning = "server.partitioning"
	// partitionsAhead days are created in advance, so writes never wait on DDL.
	partitionsAhead = 7
	copyChunk       = 50000
	// copySettle bounds the wait for transactions open when the newest id
	// was read; any still open by then is stuck, not writing.
	copySettle = 2 * time.Minute
)

// partitioningOn reports Settings → Data's choice; VISIONARY_PARTITIONING=daily
// sets it before anything is saved, for installs set up as code.
func (l *liveSettings) partitioningOn() bool {
	v := l.Partitioning
	if v == "" {
		v = strings.TrimSpace(os.Getenv("VISIONARY_PARTITIONING"))
	}
	return v == "daily"
}

// conversion is the progress of one table's conversion.
type conversion struct {
	Table   string    `json:"table"`
	Step    string    `json:"step"`
	Copied  int64     `json:"copied"`
	Total   int64     `json:"total"`
	Error   string    `json:"error,omitempty"`
	Done    bool      `json:"done"`
	Started time.Time `json:"started"`
}

type partitionJobs struct {
	mu   sync.Mutex
	runs map[string]*conversion
}

var partitionRuns sync.Map // *Server -> *partitionJobs

func (s *Server) partitionJobs() *partitionJobs {
	v, _ := partitionRuns.LoadOrStore(s, &partitionJobs{runs: map[string]*conversion{}})
	return v.(*partitionJobs)
}

func (j *partitionJobs) get(table string) *conversion {
	j.mu.Lock()
	defer j.mu.Unlock()
	if c := j.runs[table]; c != nil {
		cp := *c
		return &cp
	}
	return nil
}

func (j *partitionJobs) update(table string, f func(c *conversion)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if c := j.runs[table]; c != nil {
		f(c)
	}
}

func (s *Server) partitionsStart(ctx context.Context) {
	if s.q.d != sdk.Postgres {
		return
	}
	s.leased("partitions", func(ctx context.Context) {
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			s.maintainPartitions(ctx)
			timer.Reset(time.Hour)
		}
	})
}

// maintainPartitions keeps days ahead on partitioned tables and, with
// partitioning on, converts tables that have no rows yet.
func (s *Server) maintainPartitions(ctx context.Context) {
	on := s.live().partitioningOn()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, t := range partitionTables {
		kind, err := s.relKind(ctx, t)
		switch {
		case err != nil || kind == "":
			continue
		case kind == "p":
			if err := s.ensurePartitions(ctx, t, t, today.AddDate(0, 0, -1), today.AddDate(0, 0, partitionsAhead)); err != nil && ctx.Err() == nil {
				s.logger.Warn("partitions", "table", t, "err", err)
			}
		case on && s.tableEmpty(ctx, t):
			if err := s.convertTable(ctx, t); err != nil && ctx.Err() == nil {
				s.logger.Warn("partition an empty table", "table", t, "err", err)
			}
		}
	}
}

// relKind is "p" for a partitioned table, "r" for a plain one, "" when it
// does not exist.
func (s *Server) relKind(ctx context.Context, table string) (string, error) {
	res, err := s.db.Query(ctx, `SELECT CAST(relkind AS TEXT) FROM pg_class WHERE oid = to_regclass(CAST(? AS TEXT))`, table)
	if err != nil || len(res.Rows) == 0 {
		return "", err
	}
	return toString(res.Rows[0][0]), nil
}

func (s *Server) tableEmpty(ctx context.Context, table string) bool {
	res, err := s.db.Query(ctx, `SELECT 1 FROM `+table+` LIMIT 1`)
	return err == nil && len(res.Rows) == 0
}

func partitionName(table string, day time.Time) string {
	return table + "_p" + day.Format("20060102")
}

// partitionDay reads the day of a partition this file named, or false.
func partitionDay(table, name string) (time.Time, bool) {
	rest, ok := strings.CutPrefix(name, table+"_p")
	if !ok || len(rest) != 8 {
		return time.Time{}, false
	}
	d, err := time.Parse("20060102", rest)
	return d, err == nil
}

func (s *Server) partitionsOf(ctx context.Context, parent string) ([]string, error) {
	res, err := s.db.Query(ctx, `SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
WHERE i.inhparent = to_regclass(CAST(? AS TEXT)) ORDER BY c.relname`, parent)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, toString(r[0]))
	}
	return out, nil
}

// ensurePartitions creates parent's default partition and one per day from
// first to last. Partitions are named after table, which differs from parent
// only while a conversion builds its twin.
func (s *Server) ensurePartitions(ctx context.Context, parent, table string, first, last time.Time) error {
	have, err := s.partitionsOf(ctx, parent)
	if err != nil {
		return err
	}
	if !slices.Contains(have, table+"_pdefault") {
		if err := s.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+table+`_pdefault PARTITION OF `+parent+` DEFAULT`); err != nil {
			return err
		}
	}
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		if name := partitionName(table, d); !slices.Contains(have, name) {
			if err := s.createPartition(ctx, parent, table, d); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return nil
}

// createPartition adds one day. Rows of that day already in the default
// partition (clock skew, late events) move into it first.
func (s *Server) createPartition(ctx context.Context, parent, table string, day time.Time) error {
	name := partitionName(table, day)
	bounds := fmt.Sprintf("FROM ('%s') TO ('%s')", day.Format("2006-01-02"), day.AddDate(0, 0, 1).Format("2006-01-02"))
	err := s.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+name+` PARTITION OF `+parent+` FOR VALUES `+bounds)
	if err == nil {
		return nil
	}
	in := fmt.Sprintf("created_at >= '%s' AND created_at < '%s'", day.Format("2006-01-02"), day.AddDate(0, 0, 1).Format("2006-01-02"))
	// Statements sent together run as one transaction.
	return s.db.Exec(ctx, `CREATE TABLE `+name+` (LIKE `+parent+` INCLUDING DEFAULTS);
INSERT INTO `+name+` SELECT * FROM `+table+`_pdefault WHERE `+in+`;
DELETE FROM `+table+`_pdefault WHERE `+in+`;
ALTER TABLE `+parent+` ATTACH PARTITION `+name+` FOR VALUES `+bounds)
}

// dropOldPartitions drops the days of table that are past retention. The
// chunked delete that follows clears the rest, such as the default
// partition and app_logs' shorter retention for lower levels.
func (s *Server) dropOldPartitions(ctx context.Context, table string, days int) {
	if kind, err := s.relKind(ctx, table); err != nil || kind != "p" {
		return
	}
	names, err := s.partitionsOf(ctx, table)
	if err != nil {
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	for _, name := range names {
		if d, ok := partitionDay(table, name); ok && !d.AddDate(0, 0, 1).After(cutoff) {
			if err := s.db.Exec(ctx, `DROP TABLE IF EXISTS `+name); err != nil {
				s.logger.Warn("drop partition", "partition", name, "err", err)
				return
			}
			s.logger.Info("retention dropped a partition", "partition", name)
		}
	}
}

// partitionRetention is how long table keeps rows: the longest retention of
// the kinds stored in it, 0 when any keeps them forever.
func (s *Server) partitionRetention(table string) int {
	days := 0
	for _, k := range retentionKinds {
		if !slices.Contains(k.tables, table) {
			continue
		}
		d := s.retentionDays(k.Key)
		if d <= 0 {
			return 0
		}
		days = max(days, d)
	}
	return days
}

// convertTable turns a plain table into a partitioned one with the same
// columns, indexes, rows and id sequence. Writes continue during the copy and
// pause only for the swap.
func (s *Server) convertTable(ctx context.Context, t string) error {
	jobs := s.partitionJobs()
	jobs.mu.Lock()
	if c := jobs.runs[t]; c != nil && !c.Done {
		jobs.mu.Unlock()
		return errors.New("already converting " + t)
	}
	jobs.runs[t] = &conversion{Table: t, Step: "Preparing", Started: time.Now().UTC()}
	jobs.mu.Unlock()
	err := s.convert(ctx, t, func(f func(c *conversion)) { jobs.update(t, f) })
	jobs.update(t, func(c *conversion) {
		c.Done = true
		if err != nil {
			c.Error = err.Error()
			c.Step = "Failed"
		} else {
			c.Step = "Done"
		}
	})
	if err != nil {
		// Leave the plain table as it was.
		_ = s.db.Exec(context.WithoutCancel(ctx), `DROP TABLE IF EXISTS `+t+`_part`)
	} else {
		s.logger.Info("table partitioned by day", "table", t)
	}
	return err
}

func (s *Server) convert(ctx context.Context, t string, progress func(func(c *conversion))) error {
	twin := t + "_part"
	if kind, err := s.relKind(ctx, t); err != nil || kind != "r" {
		return fmt.Errorf("%s is not a plain table", t)
	}
	idx, err := s.db.Query(ctx, `SELECT indexname, indexdef FROM pg_indexes
WHERE schemaname = current_schema() AND tablename = ? AND indexname <> ?`, t, t+"_pkey")
	if err != nil {
		return err
	}
	seq := ""
	if res, err := s.db.Query(ctx, `SELECT pg_get_serial_sequence(CAST(? AS TEXT), 'id')`, t); err == nil && len(res.Rows) > 0 {
		seq = toString(res.Rows[0][0])
	}
	bounds, err := s.db.Query(ctx, `SELECT MIN(created_at), MAX(id) FROM `+t)
	if err != nil {
		return err
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, -1)
	if v, err := time.Parse(tsLayout, toString(bounds.Rows[0][0])); err == nil && v.Before(first) {
		first = v.Truncate(24 * time.Hour)
	}
	total := toInt(bounds.Rows[0][1])
	progress(func(c *conversion) { c.Total, c.Step = total, "Creating partitions" })

	// A twin left by a crash is rebuilt from scratch.
	if err := s.db.Exec(ctx, `DROP TABLE IF EXISTS `+twin); err != nil {
		return err
	}
	if err := s.db.Exec(ctx, `CREATE TABLE `+twin+` (LIKE `+t+` INCLUDING DEFAULTS INCLUDING STORAGE) PARTITION BY RANGE (created_at)`); err != nil {
		return err
	}
	if err := s.ensurePartitions(ctx, twin, t, first, today.AddDate(0, 0, partitionsAhead)); err != nil {
		return err
	}

	// Rows are copied up to the newest id seen once every transaction open
	// at that moment has ended, so none with a smaller id can still appear.
	var last int64
	catchUp := func() error {
		for {
			res, err := s.db.Query(ctx, `SELECT COALESCE(MAX(id), 0), CAST(pg_snapshot_xmax(pg_current_snapshot()) AS TEXT) FROM `+t)
			if err != nil {
				return err
			}
			newest, xmax, seen := toInt(res.Rows[0][0]), toInt(res.Rows[0][1]), time.Now()
			progress(func(c *conversion) { c.Total = newest })
			// What is left goes under the lock.
			if newest-last <= copyChunk {
				return nil
			}
			for {
				if !sleepCtx(ctx, 2*time.Second) {
					return ctx.Err()
				}
				if time.Since(seen) < 5*time.Second {
					continue
				}
				res, err := s.db.Query(ctx, `SELECT CAST(pg_snapshot_xmin(pg_current_snapshot()) AS TEXT)`)
				if err != nil {
					return err
				}
				if toInt(res.Rows[0][0]) >= xmax || time.Since(seen) >= copySettle {
					break
				}
			}
			for last < newest {
				hi := min(last+copyChunk, newest)
				if err := s.db.Exec(ctx, fmt.Sprintf(`INSERT INTO %s SELECT * FROM %s WHERE id > %d AND id <= %d`, twin, t, last, hi)); err != nil {
					return err
				}
				last = hi
				progress(func(c *conversion) { c.Copied = last })
			}
		}
	}
	progress(func(c *conversion) { c.Step = "Copying rows" })
	if err := catchUp(); err != nil {
		return err
	}

	// Indexes are built once the bulk of the rows is in: faster than
	// keeping them up to date row by row.
	progress(func(c *conversion) { c.Step = "Building indexes" })
	if err := s.db.Exec(ctx, `ALTER TABLE `+twin+` ADD CONSTRAINT `+twin+`_pkey PRIMARY KEY (id, created_at)`); err != nil {
		return err
	}
	var renames []string
	for _, r := range idx.Rows {
		name, def := toString(r[0]), toString(r[1])
		if strings.HasPrefix(def, "CREATE UNIQUE") {
			// A unique index must include the partition key; none of these
			// tables has one besides the id.
			continue
		}
		def = strings.Replace(def, " "+name+" ON ", " "+name+"_part ON ", 1)
		def = indexTable.ReplaceAllString(def, " ON "+twin+" USING ")
		if err := s.db.Exec(ctx, def); err != nil {
			return fmt.Errorf("index %s: %w", name, err)
		}
		renames = append(renames, `ALTER INDEX `+name+`_part RENAME TO `+name)
	}
	progress(func(c *conversion) { c.Step = "Copying rows" })
	if err := catchUp(); err != nil {
		return err
	}

	progress(func(c *conversion) { c.Step = "Swapping tables" })
	// Statements sent together run as one transaction.
	swap := []string{
		`LOCK TABLE ` + t + ` IN ACCESS EXCLUSIVE MODE`,
		fmt.Sprintf(`INSERT INTO %s SELECT * FROM %s WHERE id > %d`, twin, t, last),
	}
	if seq != "" {
		swap = append(swap, `ALTER SEQUENCE `+seq+` OWNED BY `+twin+`.id`)
	}
	swap = append(swap,
		`DROP TABLE `+t,
		`ALTER TABLE `+twin+` RENAME TO `+t,
		`ALTER TABLE `+t+` RENAME CONSTRAINT `+twin+`_pkey TO `+t+`_pkey`,
	)
	swap = append(swap, renames...)
	if err := s.db.Exec(ctx, strings.Join(swap, ";\n")); err != nil {
		return err
	}
	progress(func(c *conversion) { c.Copied = c.Total })
	// Fresh statistics, for the planner and the row counts in Settings.
	_ = s.db.Exec(ctx, `ANALYZE `+t)
	s.seenMu.Lock()
	s.seen = nil
	s.seenMu.Unlock()
	return nil
}

// indexTable is the table in an index definition from pg_indexes.
var indexTable = regexp.MustCompile(` ON (?:ONLY )?[A-Za-z0-9_."]+ USING `)

func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// ---------- Settings → Data ----------

func (s *Server) partitionRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/settings/partitions", s.authed(s.handlePartitions))
	mux.Handle("PUT /api/settings/partitions", s.authed(s.handlePutPartitions))
	mux.Handle("POST /api/settings/partitions/{table}/convert", s.authed(s.handleConvertTable))
}

type partitionTable struct {
	Table       string      `json:"table"`
	Exists      bool        `json:"exists"`
	Partitioned bool        `json:"partitioned"`
	Partitions  int         `json:"partitions"`
	Rows        int64       `json:"rows"` // the planner's estimate
	Bytes       int64       `json:"bytes"`
	Conversion  *conversion `json:"conversion,omitempty"`
}

func (s *Server) handlePartitions(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"supported": s.q.d == sdk.Postgres,
		"enabled":   s.live().partitioningOn(),
		"can_admin": s.can(r, "admin"),
		"tables":    []partitionTable{},
	}
	if s.q.d != sdk.Postgres {
		writeJSON(w, http.StatusOK, out)
		return
	}
	jobs := s.partitionJobs()
	var tables []partitionTable
	for _, t := range partitionTables {
		pt := partitionTable{Table: t, Conversion: jobs.get(t)}
		kind, err := s.relKind(r.Context(), t)
		if err != nil {
			s.fail(w, err)
			return
		}
		pt.Exists, pt.Partitioned = kind != "", kind == "p"
		if pt.Exists {
			// Partitioned parents hold nothing themselves; sum their partitions.
			res, err := s.db.Query(r.Context(), `SELECT COUNT(*), COALESCE(SUM(GREATEST(c.reltuples, 0)), 0), COALESCE(SUM(pg_total_relation_size(c.oid)), 0)
FROM pg_class c WHERE (c.oid = to_regclass(CAST(? AS TEXT)) AND c.relkind <> 'p')
   OR c.oid IN (SELECT inhrelid FROM pg_inherits WHERE inhparent = to_regclass(CAST(? AS TEXT)))`, t, t)
			if err == nil && len(res.Rows) > 0 {
				pt.Rows, pt.Bytes = toInt(res.Rows[0][1]), toInt(res.Rows[0][2])
				if pt.Partitioned {
					pt.Partitions = int(toInt(res.Rows[0][0]))
				}
			}
		}
		tables = append(tables, pt)
	}
	out["tables"] = tables
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePutPartitions(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.q.d != sdk.Postgres {
		writeError(w, http.StatusBadRequest, "time partitioning needs PostgreSQL")
		return
	}
	v := "off"
	if body.Enabled {
		v = "daily"
	}
	if err := s.writeSettings(r.Context(), map[string]string{settingPartitioning: v}); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "settings.partitioning", "", map[string]any{"enabled": body.Enabled})
	if err := s.refreshLive(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	if body.Enabled {
		go s.maintainPartitions(context.WithoutCancel(r.Context()))
	}
	s.handlePartitions(w, r)
}

func (s *Server) handleConvertTable(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	t := r.PathValue("table")
	if !slices.Contains(partitionTables, t) {
		writeError(w, http.StatusNotFound, "unknown table")
		return
	}
	if s.q.d != sdk.Postgres || !s.live().partitioningOn() {
		writeError(w, http.StatusBadRequest, "turn time partitioning on first")
		return
	}
	if c := s.partitionJobs().get(t); c != nil && !c.Done {
		writeError(w, http.StatusConflict, t+" is already being converted")
		return
	}
	s.audit(r, "settings.partitioning.convert", t, nil)
	go func() {
		if err := s.convertTable(context.WithoutCancel(r.Context()), t); err != nil {
			s.logger.Warn("partition a table", "table", t, "err", err)
		}
	}()
	// Let the job register before the page reads its progress.
	time.Sleep(50 * time.Millisecond)
	s.handlePartitions(w, r)
}
