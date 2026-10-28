package analytics

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sort"
	"strings"
	"time"
)

// The reports that become PDFs: the weekly digest and the security report,
// each as a reportDoc for renderReportPDF.

// appLabel is the configured app name, or "" while it is the default.
func (s *Server) appLabel() string {
	if a := s.conf("app_name"); a != "the application" {
		return a
	}
	return ""
}

// digestPDF is the digest as a PDF: the email's summary, tiles and
// sections, plus a verdict and the requests per day.
func (s *Server) digestPDF(d *digestData, summary string) ([]byte, error) {
	app := s.appLabel()
	loc := s.digestTZ()
	v := digestEmailData(app, d, summary)
	doc := reportDoc{
		App: app, Kind: "Weekly digest", Title: "Performance and reliability", Period: v.Period,
		TimeZone: tzName(loc), Generated: time.Now().In(loc),
		Verdict: digestVerdict(v), Summary: v.Summary, Stats: v.Stats, Sections: v.Sections,
		Footnote: "This week is compared with the week before. Turn the digest off or change its day in Alerts → Digest.",
	}
	if len(d.Days) > 0 {
		c := &reportChart{Title: "Requests per day", ValueName: "Requests", MarkName: "5xx responses"}
		for _, day := range d.Days {
			t, _ := time.Parse(tsLayout, day.Day)
			c.Labels = append(c.Labels, t.In(loc).Format("Mon 2"))
			c.Values = append(c.Values, float64(day.Requests))
			c.Marks = append(c.Marks, float64(day.Errors))
		}
		doc.Chart = c
	}
	return renderReportPDF(doc)
}

// What a section's flagged rows mean, for the verdict.
var verdictPhrases = map[string]string{
	"Slower than last week": "%d route(s) got slower",
	"Incidents":             "%d critical alert(s) fired",
	"Errors":                "%d new error issue(s)",
	"Uptime":                "%d check(s) below 99.9%% uptime",
	"Machines":              "%d machine(s) peaked above 90%% CPU or memory",
	"Containers":            "%d container(s) were killed for memory",
	"Web Vitals (p75)":      "%d Web Vital(s) rated poor",
	"Logs":                  "%d new error message(s) were logged",
}

// digestVerdict sums up what got worse, from the tones the email uses.
func digestVerdict(v emailView) *reportVerdict {
	var worse []string
	for _, st := range v.Stats {
		if st.Tone == "bad" {
			worse = append(worse, strings.ToLower(st.Label))
		}
	}
	var parts []string
	if len(worse) > 0 {
		parts = append(parts, joinAnd(worse)+" got worse than the week before")
	}
	critical := false
	for _, sec := range v.Sections {
		phrase, ok := verdictPhrases[sec.Title]
		if !ok {
			continue
		}
		bad := 0
		for _, r := range sec.Rows {
			if r.Tone == "bad" {
				bad++
			}
		}
		if bad == 0 {
			continue
		}
		critical = critical || sec.Title == "Incidents"
		parts = append(parts, fmt.Sprintf(phrase, bad))
	}
	text := capitalize(strings.Join(parts, "; ")) + "."
	switch {
	case len(parts) == 0:
		return &reportVerdict{Tone: "good", Title: "A steady week", Text: "Nothing got worse than the week before."}
	case critical:
		return &reportVerdict{Tone: "bad", Title: "Needs attention", Text: text}
	}
	return &reportVerdict{Tone: "warn", Title: "Worth a look", Text: text}
}

// groupNum writes a count with thousands separators: 1,284,512.
func groupNum(x float64) string {
	s := strconv.FormatFloat(math.Round(x), 'f', 0, 64)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}

func joinAnd(xs []string) string {
	if len(xs) < 2 {
		return strings.Join(xs, "")
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Server) handleDigestPDF(w http.ResponseWriter, r *http.Request) {
	d, err := s.digestData(r.Context(), time.Now().UTC())
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	pdf, err := s.digestPDF(d, "")
	if err != nil {
		s.fail(w, err)
		return
	}
	writePDF(w, "visionary-digest-"+time.Now().In(s.digestTZ()).Format("2006-01-02")+".pdf", pdf)
}

func writePDF(w http.ResponseWriter, name string, pdf []byte) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pdf)
}

// ---------- security report ----------

func kindTitle(kind string) string { return capitalize(strings.ReplaceAll(kind, "_", " ")) }

// securityReportPDF is the Security → Report page as a PDF. The audit
// section is only there for admins, as on the page.
func (s *Server) securityReportPDF(ctx context.Context, tr timeRange, admin bool, loc *time.Location) ([]byte, error) {
	panels := s.security().panels
	run := func(name string) []map[string]any {
		sql, ok := panels[name]
		if !ok {
			return nil
		}
		res, err := s.runTemplate(ctx, sql, tr, true)
		if err != nil {
			s.logger.Info("security report: panel skipped", "panel", name, "err", userMessage(err))
			return nil
		}
		return rowMaps(res)
	}
	in := securityReportData{
		App: s.appLabel(), From: tr.From, To: tr.To, Loc: loc, Generated: time.Now(), Summary: map[string]any{},
		Kinds: run("findings_by_kind"), FindingIPs: run("finding_ips"), TopIPs: run("top_ips"), Paths: run("probed_paths"), Hosts: run("host_status"),
	}
	if rows := run("summary"); len(rows) > 0 {
		in.Summary = rows[0]
	}
	if admin {
		a := &securityAudit{}
		a.Entries, a.Counts, a.Err = s.sensitiveAudit(ctx, tr.From, tr.To, 50)
		in.Audit = a
	}
	return renderReportPDF(securityReportDoc(in))
}

// securityReportData is what the security report reads; Audit is nil for
// people who are not admins.
type securityReportData struct {
	App                                     string
	From, To, Generated                     time.Time
	Loc                                     *time.Location
	Summary                                 map[string]any
	Kinds, FindingIPs, TopIPs, Paths, Hosts []map[string]any
	Audit                                   *securityAudit
}

type securityAudit struct {
	Entries []map[string]any
	Counts  []auditCount
	Err     error
}

func securityReportDoc(in securityReportData) reportDoc {
	loc, sum, kinds, findingIPs, topIPs, paths, hosts := in.Loc, in.Summary, in.Kinds, in.FindingIPs, in.TopIPs, in.Paths, in.Hosts
	n := func(k string) int64 { return toInt(sum[k]) }
	count := func(v any) string { return groupNum(float64(toInt(v))) }
	when := func(v any) string {
		t, err := time.Parse(tsLayout, toString(v))
		if err != nil {
			return "—"
		}
		return t.In(loc).Format("Jan 2, 15:04")
	}
	doc := reportDoc{
		App: in.App, Kind: "Security report", Title: "Security posture", TimeZone: tzName(loc), Generated: in.Generated.In(loc),
		Period:   in.From.In(loc).Format("Jan 2, 2006 15:04") + " – " + in.To.In(loc).Format("Jan 2, 2006 15:04"),
		Footnote: "Findings come from Visionary's detectors over request logs and, where installed, the host agent. Tables show the top rows only. This report contains IP addresses and account names: share it on a need-to-know basis.",
	}

	var answered []map[string]any
	for _, p := range paths {
		if toInt(p["answered_2xx"]) > 0 {
			answered = append(answered, p)
		}
	}
	var pending []string
	var sshTotal int64
	for _, h := range hosts {
		if toInt(h["pending_updates"]) > 0 {
			pending = append(pending, toString(h["host"]))
		}
		sshTotal += toInt(h["ssh_failures"])
	}
	switch {
	case n("critical") > 0 || len(answered) > 0:
		text := fmt.Sprintf("%d critical finding(s) in this period.", n("critical"))
		if n("critical") == 0 {
			text = "Probe paths were answered with a success response."
		}
		doc.Verdict = &reportVerdict{Tone: "bad", Title: "Action needed", Text: text}
	case n("warning") > 0 || len(pending) > 0:
		doc.Verdict = &reportVerdict{Tone: "warn", Title: "Worth a look", Text: "No critical findings, but there are warnings or pending security updates."}
	default:
		doc.Verdict = &reportVerdict{Tone: "good", Title: "All clear", Text: "No critical or warning findings in this period."}
	}

	total := n("findings")
	if total == 0 {
		doc.Summary = append(doc.Summary, "No security findings in this period.")
	} else {
		doc.Summary = append(doc.Summary, fmt.Sprintf("%s security finding(s): %s critical, %s warning and %s info.", count(total), count(n("critical")), count(n("warning")), count(n("info"))))
	}
	if len(kinds) > 0 {
		doc.Summary = append(doc.Summary, fmt.Sprintf("Most frequent: %s (%s finding(s)).", kindTitle(toString(kinds[0]["kind"])), count(kinds[0]["findings"])))
	}
	doc.Summary = append(doc.Summary, fmt.Sprintf("%s failed login(s); %s of %s requests were blocked or forbidden (401, 403, 429).", count(n("failed_logins")), formatUnit(toFloat(sum["blocked_rate"]), "percent"), count(n("requests"))))
	if n("hosts") > 0 {
		doc.Summary = append(doc.Summary, fmt.Sprintf("%s host(s) monitored: %s failed SSH login(s), %d with security updates pending.", count(n("hosts")), count(sshTotal), len(pending)))
	} else {
		doc.Summary = append(doc.Summary, "No host agent reported, so host security is not covered.")
	}

	tone := func(bad bool) string {
		if bad {
			return "bad"
		}
		return ""
	}
	doc.Stats = []emailStat{
		{Label: "Critical", Value: groupNum(float64(n("critical"))), Tone: tone(n("critical") > 0), Change: "findings"},
		{Label: "Warning", Value: groupNum(float64(n("warning"))), Change: "findings"},
		{Label: "Failed logins", Value: groupNum(float64(n("failed_logins")))},
		{Label: "Scanner IPs", Value: groupNum(float64(n("scanner_ips")))},
	}

	// Recommended actions, most urgent first.
	var actions []emailRow
	act := func(text string) { actions = append(actions, emailRow{Cells: []string{fmt.Sprintf("%d. %s", len(actions)+1, text)}}) }
	if n("critical") > 0 {
		act(fmt.Sprintf("Review the %d critical finding(s) under Security → Findings and mark each one handled or a false positive.", n("critical")))
	}
	if len(answered) > 0 {
		var names []string
		for _, p := range head(answered, 3) {
			names = append(names, toString(p["path"]))
		}
		act(fmt.Sprintf("%d probed path(s) returned a success response (%s). Make sure none of them expose files, admin pages or configuration.", len(answered), strings.Join(names, ", ")))
	}
	if len(pending) > 0 {
		act("Apply security updates on " + strings.Join(head(pending, 5), ", ") + ".")
	}
	if sshTotal > 0 {
		act(fmt.Sprintf("%d failed SSH login(s): allow key-only SSH and limit who can reach port 22.", sshTotal))
	}
	sort.SliceStable(topIPs, func(i, j int) bool { return toInt(topIPs[i]["probes"]) > toInt(topIPs[j]["probes"]) })
	if len(topIPs) > 0 && toInt(topIPs[0]["probes"]) > 0 {
		act(fmt.Sprintf("Consider blocking %s, behind %s probe(s).", toString(topIPs[0]["ip"]), count(topIPs[0]["probes"])))
	}
	if n("failed_logins") > 50 {
		act(fmt.Sprintf("%d failed logins: check that sign-in routes are rate limited.", n("failed_logins")))
	}
	if n("hosts") == 0 {
		act("Install the Visionary host agent on your servers to cover SSH, open ports and pending updates.")
	}
	if len(actions) > 0 {
		doc.Sections = append(doc.Sections, emailSection{Title: "Recommended actions", Rows: actions})
	}

	sec := emailSection{Title: "Findings by kind", Columns: []string{"Kind", "Findings", "Occurrences", "Critical", "Last seen"}}
	for _, k := range head(kinds, 20) {
		sec.Rows = append(sec.Rows, emailRow{Cells: []string{kindTitle(toString(k["kind"])), count(k["findings"]), count(k["occurrences"]), count(k["critical"]), when(k["last_seen"])}})
	}
	if len(sec.Rows) == 0 {
		sec.Note = "No findings."
	}
	doc.Sections = append(doc.Sections, sec)

	sec = emailSection{Title: "IPs behind the most findings", Columns: []string{"IP", "Findings", "Critical", "Kinds", "Last seen"}}
	for _, ip := range head(findingIPs, 15) {
		sec.Rows = append(sec.Rows, emailRow{Cells: []string{toString(ip["ip"]), count(ip["findings"]), count(ip["critical"]), count(ip["kinds"]), when(ip["last_seen"])}, Tone: ""})
	}
	if len(sec.Rows) > 0 {
		doc.Sections = append(doc.Sections, sec)
	}

	sec = emailSection{Title: "IPs with the most failed logins and probes", Columns: []string{"IP", "Requests", "Failed logins", "401", "403", "Probes"}}
	for _, ip := range head(topIPs, 10) {
		sec.Rows = append(sec.Rows, emailRow{Cells: []string{toString(ip["ip"]), count(ip["requests"]), count(ip["failed_logins"]), count(ip["unauthorized"]), count(ip["forbidden"]), count(ip["probes"])}})
	}
	if len(sec.Rows) > 0 {
		doc.Sections = append(doc.Sections, sec)
	}

	sec = emailSection{Title: "Most probed paths", Columns: []string{"Path", "Hits", "IPs", "Answered 2xx"}}
	for _, p := range head(paths, 15) {
		sec.Rows = append(sec.Rows, emailRow{Cells: []string{toString(p["path"]), count(p["hits"]), count(p["ips"]), count(p["answered_2xx"])}, Tone: tone(toInt(p["answered_2xx"]) > 0)})
	}
	if len(sec.Rows) > 0 {
		doc.Sections = append(doc.Sections, sec)
	}

	sec = emailSection{Title: "Host security", Columns: []string{"Host", "SSH failures", "SSH logins", "sudo", "Ports opened", "Pending updates"}}
	for _, h := range hosts {
		sec.Rows = append(sec.Rows, emailRow{Cells: []string{toString(h["host"]), count(h["ssh_failures"]), count(h["ssh_logins"]), count(h["sudo"]), count(h["ports_opened"]), count(h["pending_updates"])}, Tone: tone(toInt(h["pending_updates"]) > 0)})
	}
	if len(sec.Rows) == 0 {
		sec.Note = "No host agent reported in this period. Install the Visionary host agent on your servers to see SSH logins, open ports and pending updates."
	}
	doc.Sections = append(doc.Sections, sec)

	if a := in.Audit; a != nil {
		sec = emailSection{Title: "Visionary audit", Columns: []string{"Time", "Action", "User", "IP"}}
		switch {
		case a.Err != nil:
			sec.Note = "The audit log could not be read."
		case len(a.Entries) == 0:
			sec.Note = "No sensitive actions in this period."
		default:
			var c []string
			for _, x := range a.Counts {
				c = append(c, fmt.Sprintf("%s: %d", x.Action, x.Count))
			}
			sec.Note = strings.Join(c, " · ")
			for _, e := range a.Entries {
				user := toString(e["user_name"])
				if user == "" {
					user = toString(e["user_id"])
				}
				sec.Rows = append(sec.Rows, emailRow{Cells: []string{when(e["created_at"]), toString(e["action"]), user, toString(e["ip"])}})
			}
		}
		doc.Sections = append(doc.Sections, sec)
	}
	return doc
}

func (s *Server) handleSecurityReportPDF(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	loc := s.digestTZ()
	if tz := r.URL.Query().Get("tz"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	admin := s.can(r, "admin")
	pdf, err := s.securityReportPDF(r.Context(), tr, admin, loc)
	if err != nil {
		s.fail(w, err)
		return
	}
	writePDF(w, "visionary-security-report-"+tr.To.In(loc).Format("2006-01-02")+".pdf", pdf)
}
