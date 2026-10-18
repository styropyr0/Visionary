package analytics

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The weekly digest's security section: new findings by severity, the IPs
// behind most of them, failed logins against the week before, hosts with
// updates waiting and notable actions from Visionary's audit log.

type securityDigest struct {
	Findings     map[string]int64 `json:"findings_by_severity"`
	FindingKinds []digestKind     `json:"findings_by_kind"`
	TopIPs       []digestIP       `json:"top_ips"`
	FailedLogins int64            `json:"failed_logins"`
	PrevFailed   int64            `json:"previous_failed_logins"`
	PendingHosts []digestHost     `json:"hosts_with_pending_updates"`
	Audit        []auditCount     `json:"audit_actions"`
}

type digestKind struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

type digestIP struct {
	IP       string `json:"ip"`
	Findings int64  `json:"findings"`
	Kinds    string `json:"kinds"`
}

type digestHost struct {
	Host    string `json:"host"`
	Pending int64  `json:"pending"`
}

// securityDigestData fills the section for [mid, now), with failed logins
// also for the week before. Parts whose tables are missing are left empty.
func (s *Server) securityDigestData(ctx context.Context, mid, now time.Time) *securityDigest {
	d := &securityDigest{Findings: map[string]int64{}}
	span := []any{mid.UTC().Format(tsLayout), now.UTC().Format(tsLayout)}
	warn := func(part string, err error) {
		if err != nil && !missingTable(err, "security_events") && !missingTable(err, "audit_log") {
			s.logger.Info("digest: security "+part+" unavailable", "err", err)
		}
	}

	res, err := s.db.Query(ctx, `SELECT severity, kind, COUNT(*) FROM security_events
WHERE created_at >= ? AND created_at < ? GROUP BY severity, kind`, span...)
	warn("findings", err)
	if err == nil {
		kinds := map[string]int64{}
		for _, row := range res.Rows {
			n := toInt(row[2])
			d.Findings[toString(row[0])] += n
			kinds[toString(row[1])] += n
		}
		for k, n := range kinds {
			d.FindingKinds = append(d.FindingKinds, digestKind{k, n})
		}
		sort.Slice(d.FindingKinds, func(i, j int) bool { return d.FindingKinds[i].Count > d.FindingKinds[j].Count })
	}

	res, err = s.db.Query(ctx, `SELECT ip, COUNT(*) AS n, MIN(kind), MAX(kind) FROM security_events
WHERE created_at >= ? AND created_at < ? AND ip IS NOT NULL AND ip <> '' AND severity IN ('warning', 'critical')
GROUP BY ip ORDER BY n DESC LIMIT 5`, span...)
	warn("ips", err)
	if err == nil {
		for _, row := range res.Rows {
			kinds := toString(row[2])
			if b := toString(row[3]); b != kinds {
				kinds += ", " + b
			}
			d.TopIPs = append(d.TopIPs, digestIP{IP: toString(row[0]), Findings: toInt(row[1]), Kinds: kinds})
		}
	}

	st := &s.security().st
	midTS := "'" + mid.UTC().Format(tsLayout) + "'"
	failed := st.loginCond(s.q) + " AND " + authFailCond(s.q)
	res, err = s.db.Query(ctx, `SELECT `+countIf(failed+" AND created_at >= "+midTS)+`, `+countIf(failed+" AND created_at < "+midTS)+`
FROM api_logs WHERE created_at >= ? AND created_at < ?`, mid.Add(-7*24*time.Hour).UTC().Format(tsLayout), now.UTC().Format(tsLayout))
	warn("failed logins", err)
	if err == nil && len(res.Rows) == 1 {
		d.FailedLogins, d.PrevFailed = toInt(res.Rows[0][0]), toInt(res.Rows[0][1])
	}

	if p, ok := s.security().panels["host_status"]; ok {
		hs, err := s.runTemplate(ctx, p, timeRange{From: mid, To: now, Bucket: 3600}, true)
		warn("hosts", err)
		if err == nil {
			hi, pi := colIndex(hs, "host"), colIndex(hs, "pending_updates")
			for _, row := range hs.Rows {
				if n := toInt(row[pi]); hi >= 0 && pi >= 0 && n > 0 {
					d.PendingHosts = append(d.PendingHosts, digestHost{toString(row[hi]), n})
				}
			}
		}
	}

	_, counts, err := s.sensitiveAudit(ctx, mid, now, 1)
	warn("audit", err)
	d.Audit = counts
	return d
}

// securityDigestLines renders the section, or nothing on a quiet week.
func securityDigestLines(d *securityDigest) []string {
	if d == nil {
		return nil
	}
	total := d.Findings["critical"] + d.Findings["warning"] + d.Findings["info"]
	if total == 0 && d.FailedLogins == 0 && d.PrevFailed == 0 && len(d.PendingHosts) == 0 && len(d.Audit) == 0 {
		return nil
	}
	lines := []string{"Security:"}
	lines = append(lines, fmt.Sprintf("  New findings: %d (%d critical, %d warning, %d info)", total,
		d.Findings["critical"], d.Findings["warning"], d.Findings["info"]))
	if len(d.FindingKinds) > 0 {
		var parts []string
		for _, k := range head(d.FindingKinds, 6) {
			parts = append(parts, fmt.Sprintf("%s %d", strings.ReplaceAll(k.Kind, "_", " "), k.Count))
		}
		lines = append(lines, "  By kind: "+strings.Join(parts, ", "))
	}
	change := ""
	if d.PrevFailed > 0 {
		change = fmt.Sprintf(" (%+.0f%%)", 100*float64(d.FailedLogins-d.PrevFailed)/float64(d.PrevFailed))
	}
	lines = append(lines, fmt.Sprintf("  Failed logins: %d, was %d%s", d.FailedLogins, d.PrevFailed, change))
	if len(d.TopIPs) > 0 {
		lines = append(lines, "  Top offending IPs:")
		for _, ip := range d.TopIPs {
			lines = append(lines, fmt.Sprintf("    %s: %d finding(s), %s", ip.IP, ip.Findings, strings.ReplaceAll(ip.Kinds, "_", " ")))
		}
	}
	if len(d.PendingHosts) > 0 {
		var parts []string
		for _, h := range head(d.PendingHosts, 10) {
			parts = append(parts, fmt.Sprintf("%s (%d)", h.Host, h.Pending))
		}
		lines = append(lines, "  Hosts with pending security updates: "+strings.Join(parts, ", "))
	}
	if len(d.Audit) > 0 {
		var parts []string
		for _, a := range head(d.Audit, 8) {
			parts = append(parts, fmt.Sprintf("%s %d", a.Action, a.Count))
		}
		lines = append(lines, "  Visionary audit: "+strings.Join(parts, ", "))
	}
	return lines
}

// forModel is the digest without IP addresses, which never go to the model.
func (d *digestData) forModel() *digestData {
	if d.Security == nil || len(d.Security.TopIPs) == 0 {
		return d
	}
	c, sec := *d, *d.Security
	sec.TopIPs = nil
	c.Security = &sec
	return &c
}
