package analytics

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"
)

// Errors grouped into issues by fingerprint. An issue's status compares the
// versions it occurred in with the service's deploy order:
//
//	new        it has only occurred in the version it occurs in now
//	regressed  it occurred in an older version, was absent from the release
//	           before the current one, and is back
//	ongoing    anything else
//
// Without versions, an issue is new when it was first seen inside the range.

const maxIssues = 100

// sparkPoints is about how many buckets an issue's trend line has.
const sparkPoints = 24

type issueVersion struct {
	Version     string `json:"version"`
	FirstSeen   string `json:"first_seen"`
	LastSeen    string `json:"last_seen"`
	Occurrences int64  `json:"occurrences"`
	DeployedAt  string `json:"deployed_at,omitempty"`
}

type issue struct {
	Fingerprint   string         `json:"fingerprint"`
	Type          string         `json:"type"`
	Message       string         `json:"message"`
	Method        string         `json:"method"`
	Route         string         `json:"route"`
	Service       string         `json:"service"`
	Occurrences   int64          `json:"occurrences"`
	Users         int64          `json:"users"`
	Handled       bool           `json:"handled"`
	FirstSeen     string         `json:"first_seen"`
	LastSeen      string         `json:"last_seen"`
	Status        string         `json:"status"`
	StatusVersion string         `json:"status_version,omitempty"`
	Versions      []issueVersion `json:"versions"`
	Trend         []int64        `json:"trend,omitempty"`
}

// issueStatus decides new / regressed / ongoing from the issue's versions
// (all time) and the service's deploy order.
func issueStatus(versions []issueVersion, order []string, firstSeen, from string) (string, string) {
	var known []issueVersion
	for _, v := range versions {
		if v.Version != "" {
			known = append(known, v)
		}
	}
	if len(known) == 0 {
		if firstSeen >= from {
			return "new", ""
		}
		return "ongoing", ""
	}
	cur, earliest := known[0], known[0]
	seen := map[string]bool{}
	for _, v := range known {
		seen[v.Version] = true
		if v.LastSeen > cur.LastSeen {
			cur = v
		}
		if v.FirstSeen < earliest.FirstSeen {
			earliest = v
		}
	}
	if earliest.Version == cur.Version {
		return "new", cur.Version
	}
	pos := -1
	for i, v := range order {
		if v == cur.Version {
			pos = i
		}
	}
	if pos > 0 && !seen[order[pos-1]] {
		for _, v := range order[:pos-1] {
			if seen[v] {
				return "regressed", cur.Version
			}
		}
	}
	return "ongoing", cur.Version
}

// sparkBucket is the bucket size, in seconds, that gives about sparkPoints
// points over the range.
func sparkBucket(tr timeRange) int64 {
	want := int64(tr.To.Sub(tr.From).Seconds()) / sparkPoints
	for _, step := range bucketSteps {
		if step >= want {
			return step
		}
	}
	return bucketSteps[len(bucketSteps)-1]
}

func (s *Server) issuesSummary(ctx context.Context, tr timeRange) (*Result, error) {
	return s.telemetryQuery(ctx, `
SELECT COUNT(*) AS occurrences, COUNT(DISTINCT fingerprint) AS issues,
  COUNT(DISTINCT user_id) AS users, `+countIf("handled = 0")+` AS unhandled
FROM errors WHERE created_at >= {{from}} AND created_at < {{to}}`, tr)
}

// handleErrorIssues lists the issues seen in the range, most frequent first.
func (s *Server) handleErrorIssues(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	spark := sparkBucket(tr)
	var (
		list, versions, trend, timeline, cur, prev *Result
		releases                                   []release
	)
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) {
		list, err = s.telemetryQuery(ctx, `
WITH e AS (
  SELECT fingerprint, type, message, method, route, service, user_id, handled, created_at,
    ROW_NUMBER() OVER (PARTITION BY fingerprint ORDER BY created_at DESC, id DESC) AS rn
  FROM errors WHERE created_at >= {{from}} AND created_at < {{to}}
)
SELECT fingerprint,
  MAX(CASE WHEN rn = 1 THEN type END) AS type,
  MAX(CASE WHEN rn = 1 THEN message END) AS message,
  MAX(CASE WHEN rn = 1 THEN method END) AS method,
  MAX(CASE WHEN rn = 1 THEN route END) AS route,
  MAX(CASE WHEN rn = 1 THEN service END) AS service,
  COUNT(*) AS occurrences,
  COUNT(DISTINCT user_id) AS users,
  MIN(handled) AS handled,
  MAX(created_at) AS last_seen
FROM e
GROUP BY fingerprint
ORDER BY occurrences DESC, last_seen DESC
LIMIT `+strconv.Itoa(maxIssues), tr)
		return
	})
	// Every version each issue ever occurred in, for first seen and status.
	g.Go(func() (err error) {
		versions, err = s.telemetryQuery(ctx, `
SELECT fingerprint, COALESCE(version, '') AS version, MIN(created_at) AS first_seen,
  MAX(created_at) AS last_seen, COUNT(*) AS occurrences
FROM errors
WHERE fingerprint IN (SELECT fingerprint FROM errors WHERE created_at >= {{from}} AND created_at < {{to}})
GROUP BY fingerprint, COALESCE(version, '')`, tr)
		return
	})
	g.Go(func() (err error) {
		trend, err = s.telemetryQuery(ctx, `
SELECT fingerprint, `+s.q.bucketOf(strconv.FormatInt(spark, 10))+` AS t, COUNT(*) AS n
FROM errors WHERE created_at >= {{from}} AND created_at < {{to}}
GROUP BY fingerprint, t`, tr)
		return
	})
	g.Go(func() (err error) {
		timeline, err = s.telemetryQuery(ctx, `
SELECT {{t}} AS t, type AS series, COUNT(*) AS value
FROM errors WHERE created_at >= {{from}} AND created_at < {{to}}
GROUP BY 1, 2 ORDER BY 1`, tr)
		return
	})
	g.Go(func() (err error) { cur, err = s.issuesSummary(ctx, tr); return })
	g.Go(func() (err error) { prev, err = s.issuesSummary(ctx, tr.previous()); return })
	g.Go(func() (err error) { releases, err = s.allReleases(ctx); return })
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}

	byFP := map[string][]issueVersion{}
	for _, m := range rowMaps(versions) {
		fp := toString(m["fingerprint"])
		byFP[fp] = append(byFP[fp], issueVersion{
			Version:     toString(m["version"]),
			FirstSeen:   toString(m["first_seen"]),
			LastSeen:    toString(m["last_seen"]),
			Occurrences: toInt(m["occurrences"]),
		})
	}
	start := tr.From.Unix() / spark * spark
	slots := int((tr.To.Unix()-start)/spark) + 1
	trends := map[string][]int64{}
	for _, m := range rowMaps(trend) {
		fp := toString(m["fingerprint"])
		if trends[fp] == nil {
			trends[fp] = make([]int64, slots)
		}
		if i := int((toInt(m["t"]) - start) / spark); i >= 0 && i < slots {
			trends[fp][i] += toInt(m["n"])
		}
	}

	from := tr.From.Format(tsLayout)
	issues := make([]issue, 0, len(list.Rows))
	counts := map[string]int{}
	for _, m := range rowMaps(list) {
		is := issue{
			Fingerprint: toString(m["fingerprint"]),
			Type:        toString(m["type"]),
			Message:     toString(m["message"]),
			Method:      toString(m["method"]),
			Route:       toString(m["route"]),
			Service:     toString(m["service"]),
			Occurrences: toInt(m["occurrences"]),
			Users:       toInt(m["users"]),
			Handled:     toInt(m["handled"]) == 1,
			LastSeen:    toString(m["last_seen"]),
			Trend:       trends[toString(m["fingerprint"])],
		}
		is.Versions, is.FirstSeen = withDeploys(byFP[is.Fingerprint], releases, is.Service)
		is.Status, is.StatusVersion = issueStatus(is.Versions, releaseOrder(releases, is.Service), is.FirstSeen, from)
		counts[is.Status]++
		issues = append(issues, is)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issues":   issues,
		"counts":   counts,
		"summary":  map[string]any{"current": cur, "previous": prev},
		"timeline": timeline,
		"spark":    map[string]any{"bucket": spark, "from": time.Unix(start, 0).UTC().Format(time.RFC3339)},
		"sql":      list.SQL,
	})
}

// withDeploys sorts an issue's versions by first occurrence, adds each one's
// deploy time and returns the issue's first sighting over all versions.
func withDeploys(vs []issueVersion, releases []release, service string) ([]issueVersion, string) {
	deployed := map[string]string{}
	for _, rel := range releases {
		if rel.Service == service {
			deployed[rel.Version] = rel.DeployedAt
		}
	}
	out := make([]issueVersion, 0, len(vs))
	first := ""
	for _, v := range vs {
		if first == "" || v.FirstSeen < first {
			first = v.FirstSeen
		}
		v.DeployedAt = deployed[v.Version]
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstSeen < out[j].FirstSeen })
	return out, first
}

// handleErrorIssue is one issue: its latest stack, occurrences over time by
// version, routes, users, versions and the most recent occurrences with a
// link to their request log entry.
func (s *Server) handleErrorIssue(w http.ResponseWriter, r *http.Request) {
	fp := r.PathValue("fingerprint")
	if !errorFingerprint.MatchString(fp) {
		writeError(w, http.StatusBadRequest, "invalid fingerprint")
		return
	}
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	var (
		latest, stats, timeline, routes, users, versions, recent *Result
		releases                                                 []release
	)
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) {
		latest, err = s.telemetryQuery(ctx, `
SELECT id, created_at, type, message, stack, handled, runtime, service, instance, version, method, route, path, request_id
FROM errors WHERE fingerprint = ?
ORDER BY created_at DESC, id DESC
LIMIT 1`, tr, fp)
		return
	})
	g.Go(func() (err error) {
		stats, err = s.telemetryQuery(ctx, `
SELECT COUNT(*) AS occurrences, COUNT(DISTINCT user_id) AS users, `+countIf("handled = 0")+` AS unhandled,
  COUNT(DISTINCT instance) AS instances
FROM errors WHERE fingerprint = ? AND created_at >= {{from}} AND created_at < {{to}}`, tr, fp)
		return
	})
	g.Go(func() (err error) {
		timeline, err = s.telemetryQuery(ctx, `
SELECT {{t}} AS t, COALESCE(version, 'unknown') AS series, COUNT(*) AS value
FROM errors WHERE fingerprint = ? AND created_at >= {{from}} AND created_at < {{to}}
GROUP BY 1, 2 ORDER BY 1`, tr, fp)
		return
	})
	g.Go(func() (err error) {
		routes, err = s.telemetryQuery(ctx, `
SELECT method, COALESCE(route, path) AS route, COUNT(*) AS occurrences,
  COUNT(DISTINCT user_id) AS users, MAX(created_at) AS last_seen
FROM errors WHERE fingerprint = ? AND created_at >= {{from}} AND created_at < {{to}}
GROUP BY method, COALESCE(route, path)
ORDER BY occurrences DESC
LIMIT 20`, tr, fp)
		return
	})
	g.Go(func() (err error) {
		users, err = s.telemetryQuery(ctx, `
SELECT e.user_id, MAX(a.user_handle) AS handle, COUNT(*) AS occurrences, MAX(e.created_at) AS last_seen
FROM errors e LEFT JOIN api_logs a ON a.request_id = e.request_id
WHERE e.fingerprint = ? AND e.created_at >= {{from}} AND e.created_at < {{to}} AND e.user_id IS NOT NULL
GROUP BY e.user_id
ORDER BY occurrences DESC
LIMIT 20`, tr, fp)
		return
	})
	g.Go(func() (err error) {
		versions, err = s.telemetryQuery(ctx, `
SELECT COALESCE(version, '') AS version, MIN(created_at) AS first_seen, MAX(created_at) AS last_seen,
  COUNT(*) AS occurrences
FROM errors WHERE fingerprint = ?
GROUP BY COALESCE(version, '')`, tr, fp)
		return
	})
	g.Go(func() (err error) {
		recent, err = s.telemetryQuery(ctx, `
SELECT e.id, e.created_at, e.method, COALESCE(e.route, e.path) AS route, e.path, e.user_id, e.version,
  e.instance, e.handled, e.message, e.request_id,
  (SELECT MAX(a.id) FROM api_logs a WHERE a.request_id = e.request_id) AS log_id
FROM errors e
WHERE e.fingerprint = ? AND e.created_at >= {{from}} AND e.created_at < {{to}}
ORDER BY e.created_at DESC, e.id DESC
LIMIT 50`, tr, fp)
		return
	})
	g.Go(func() (err error) { releases, err = s.allReleases(ctx); return })
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}
	if len(latest.Rows) == 0 {
		writeError(w, http.StatusNotFound, "no errors with this fingerprint")
		return
	}
	last := rowMaps(latest)[0]
	service := toString(last["service"])
	var vs []issueVersion
	for _, m := range rowMaps(versions) {
		vs = append(vs, issueVersion{
			Version:     toString(m["version"]),
			FirstSeen:   toString(m["first_seen"]),
			LastSeen:    toString(m["last_seen"]),
			Occurrences: toInt(m["occurrences"]),
		})
	}
	vs, first := withDeploys(vs, releases, service)
	status, statusVersion := issueStatus(vs, releaseOrder(releases, service), first, tr.From.Format(tsLayout))
	writeJSON(w, http.StatusOK, map[string]any{
		"fingerprint":    fp,
		"latest":         last,
		"first_seen":     first,
		"status":         status,
		"status_version": statusVersion,
		"versions":       vs,
		"stats":          stats,
		"timeline":       timeline,
		"routes":         routes,
		"users":          users,
		"recent":         recent,
	})
}
