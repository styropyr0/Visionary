package analytics

import (
	"strconv"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// sqlDialect writes the few expressions that differ between SQLite (and D1),
// PostgreSQL and MySQL. Everything else in the panels is common SQL.
type sqlDialect struct{ d sdk.Dialect }

// epoch is created_at (an ISO-8601 UTC string) as integer epoch seconds.
func (q sqlDialect) epoch() string {
	switch q.d {
	case sdk.Postgres:
		return "CAST(FLOOR(EXTRACT(EPOCH FROM CAST(created_at AS TIMESTAMPTZ))) AS BIGINT)"
	case sdk.MySQL:
		return "TIMESTAMPDIFF(SECOND, '1970-01-01 00:00:00', STR_TO_DATE(created_at, '%Y-%m-%dT%H:%i:%s.%fZ'))"
	}
	return "CAST(strftime('%s', created_at) AS INTEGER)"
}

// bucket is created_at floored to {{bucket}} seconds, as epoch seconds.
func (q sqlDialect) bucket() string { return q.bucketOf("{{bucket}}") }

func (q sqlDialect) bucketOf(seconds string) string {
	if q.d == sdk.MySQL {
		return "(" + q.epoch() + " DIV " + seconds + ") * " + seconds
	}
	return "(" + q.epoch() + " / " + seconds + ") * " + seconds
}

// round rounds to n decimals. PostgreSQL only rounds NUMERIC to a precision.
func (q sqlDialect) round(expr string, n int) string {
	if q.d == sdk.Postgres {
		return "CAST(ROUND(CAST(" + expr + " AS NUMERIC), " + strconv.Itoa(n) + ") AS DOUBLE PRECISION)"
	}
	return "ROUND(" + expr + ", " + strconv.Itoa(n) + ")"
}

func (q sqlDialect) text(expr string) string {
	if q.d == sdk.MySQL {
		return "CAST(" + expr + " AS CHAR)"
	}
	return "CAST(" + expr + " AS TEXT)"
}

// countIf counts rows matching cond; PostgreSQL cannot SUM a boolean.
func countIf(cond string) string { return "SUM(CASE WHEN " + cond + " THEN 1 ELSE 0 END)" }

// pct is the share of rows matching cond, in percent with two decimals.
func (q sqlDialect) pct(cond string) string {
	return q.round("100.0 * "+countIf(cond)+" / COUNT(*)", 2)
}

func (q sqlDialect) panelsSchema() string {
	switch q.d {
	case sdk.Postgres:
		return `CREATE TABLE IF NOT EXISTS analytics_panels (
    id          BIGSERIAL PRIMARY KEY,
    title       TEXT NOT NULL,
    description TEXT NOT NULL,
    prompt      TEXT NOT NULL,
    query       TEXT NOT NULL,
    viz         TEXT NOT NULL,
    created_at  TEXT NOT NULL
)`
	case sdk.MySQL:
		return `CREATE TABLE IF NOT EXISTS analytics_panels (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    title       VARCHAR(255) NOT NULL,
    description TEXT NOT NULL,
    prompt      TEXT NOT NULL,
    query       MEDIUMTEXT NOT NULL,
    viz         TEXT NOT NULL,
    created_at  VARCHAR(32) NOT NULL
) CHARACTER SET utf8mb4`
	}
	return `CREATE TABLE IF NOT EXISTS analytics_panels (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    title       TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    prompt      TEXT    NOT NULL DEFAULT '',
    query       TEXT    NOT NULL,
    viz         TEXT    NOT NULL,
    created_at  TEXT    NOT NULL
)`
}

// promptRules is the dialect-specific part of the analyst's SQL rules.
func (q sqlDialect) promptRules() string {
	switch q.d {
	case sdk.Postgres:
		return `- Dialect: PostgreSQL. created_at is TEXT; compare it as text, or CAST(created_at AS TIMESTAMPTZ) for date functions.
- Percentiles: percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) works, or the rank method below.
- Round with ROUND(CAST(x AS NUMERIC), 2). Booleans cannot be summed: count with SUM(CASE WHEN cond THEN 1 ELSE 0 END) or COUNT(*) FILTER (WHERE cond).`
	case sdk.MySQL:
		return `- Dialect: MySQL 8. created_at is VARCHAR; compare it as text, or STR_TO_DATE(created_at, '%Y-%m-%dT%H:%i:%s.%fZ') for date functions.
- No percentile function: use the rank method below. Integer division is DIV.
- Round with ROUND(x, 2). Count a condition with SUM(cond) or SUM(CASE WHEN cond THEN 1 ELSE 0 END). Every derived table needs an alias.`
	}
	return `- Dialect: SQLite. created_at is TEXT; compare it as text, or use strftime/julianday for date functions. Never wrap a bound in datetime() or date(): they return '2026-10-22 05:00:00' (a space, no Z), which never matches the stored text. Write created_at >= '2026-10-22T05:00:00.000Z'.
- No percentile function: use the rank method below.
- Round with ROUND(x, 2). Count a condition with SUM(CASE WHEN cond THEN 1 ELSE 0 END).`
}
