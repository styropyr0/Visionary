package analytics

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// The digest as an HTML email: the same sections as the page, laid out with
// tables and inline styles, which is what mail clients render reliably. It
// travels next to the plain-text lines (multipart/alternative).

type emailStat struct {
	Label, Value, Change string
	Tone                 string // good, bad or ""
}

type emailRow struct {
	Cells []string
	Tone  string // the last cell's color: good, bad or ""
}

type emailSection struct {
	Title   string
	Note    string
	Columns []string
	Rows    []emailRow
}

type emailView struct {
	App, Title, Period string
	Summary            []string
	Stats              []emailStat
	Sections           []emailSection
	Footer             string
}

// digestEmailSrc is the email; "minus" gives the index of a row's last cell.
const digestEmailSrc = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>{{.Title}}</title></head>
<body style="margin:0;padding:0;background:#f4f5f7;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#1d2330;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f4f5f7;"><tr><td align="center" style="padding:24px 12px;">
<table role="presentation" width="640" cellpadding="0" cellspacing="0" style="max-width:640px;width:100%;background:#ffffff;border:1px solid #e3e6eb;border-radius:10px;">
<tr><td style="padding:24px 28px 8px;">
  <div style="font-size:12px;letter-spacing:.06em;text-transform:uppercase;color:#6b7280;">{{.App}} · weekly digest</div>
  <div style="font-size:22px;font-weight:600;margin-top:4px;">{{.Period}}</div>
</td></tr>
{{if .Summary}}<tr><td style="padding:12px 28px 4px;">
  <div style="background:#f0f5ff;border-left:3px solid #3987e5;border-radius:6px;padding:12px 16px;font-size:14px;line-height:1.55;">
  {{range $i, $l := .Summary}}{{if eq $i 0}}<div style="font-weight:600;margin-bottom:4px;">{{$l}}</div>{{else}}<div>{{$l}}</div>{{end}}{{end}}
  </div>
</td></tr>{{end}}
<tr><td style="padding:16px 22px 8px;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr>
  {{range .Stats}}<td width="25%" valign="top" style="padding:6px;">
    <div style="border:1px solid #e3e6eb;border-radius:8px;padding:10px 12px;">
      <div style="font-size:11px;text-transform:uppercase;letter-spacing:.05em;color:#6b7280;">{{.Label}}</div>
      <div style="font-size:19px;font-weight:600;margin-top:2px;">{{.Value}}</div>
      <div style="font-size:12px;margin-top:2px;color:{{if eq .Tone "good"}}#0c7a2c{{else if eq .Tone "bad"}}#c22f2f{{else}}#6b7280{{end}};">{{.Change}}</div>
    </div>
  </td>{{end}}
  </tr></table>
</td></tr>
{{range .Sections}}<tr><td style="padding:14px 28px 4px;">
  <div style="font-size:15px;font-weight:600;">{{.Title}}</div>
  {{if .Note}}<div style="font-size:12.5px;color:#6b7280;margin-top:2px;">{{.Note}}</div>{{end}}
  {{if .Rows}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="margin-top:8px;font-size:13px;border-collapse:collapse;">
    {{if .Columns}}<tr>{{range $i, $c := .Columns}}<td style="padding:6px 8px;border-bottom:1px solid #e3e6eb;color:#6b7280;font-size:11px;text-transform:uppercase;letter-spacing:.04em;{{if $i}}text-align:right;{{end}}">{{$c}}</td>{{end}}</tr>{{end}}
    {{range .Rows}}{{$tone := .Tone}}{{$n := len .Cells}}<tr>{{range $i, $c := .Cells}}<td style="padding:7px 8px;border-bottom:1px solid #f0f1f4;{{if $i}}text-align:right;white-space:nowrap;{{else}}word-break:break-word;{{end}}{{if and (eq $i (minus $n)) (eq $tone "bad")}}color:#c22f2f;font-weight:600;{{else if and (eq $i (minus $n)) (eq $tone "good")}}color:#0c7a2c;font-weight:600;{{end}}">{{$c}}</td>{{end}}</tr>{{end}}
  </table>{{end}}
</td></tr>{{end}}
<tr><td style="padding:18px 28px 24px;font-size:12px;color:#9aa1ad;">{{.Footer}}</td></tr>
</table>
</td></tr></table>
</body></html>`

var digestEmailTmpl = template.Must(template.New("digest").Funcs(template.FuncMap{"minus": func(n int) int { return n - 1 }}).Parse(digestEmailSrc))

// digestHTML renders the digest email. summary is the analyst's text, if any.
func (s *Server) digestHTML(d *digestData, summary string) string {
	v := digestEmailData(s.conf("app_name"), d, summary)
	var b bytes.Buffer
	if err := digestEmailTmpl.Execute(&b, v); err != nil {
		s.logger.Warn("digest: html render failed", "err", err)
		return ""
	}
	return b.String()
}

// change is "+12% vs 1,204" with a tone; upIsBad says which way is bad.
func change(cur, prev float64, fmtv func(float64) string, upIsBad bool) (string, string) {
	if prev == 0 {
		return "no data the week before", ""
	}
	pct := 100 * (cur - prev) / prev
	tone := ""
	if pct >= 0.5 || pct <= -0.5 {
		if (pct > 0) == upIsBad {
			tone = "bad"
		} else {
			tone = "good"
		}
	}
	return fmt.Sprintf("%+.0f%% vs %s", pct, fmtv(prev)), tone
}

func digestEmailData(app string, d *digestData, summary string) emailView {
	from, _ := time.Parse(tsLayout, d.From)
	to, _ := time.Parse(tsLayout, d.To)
	v := emailView{App: app, Title: "Weekly digest · " + app, Period: from.Format("Jan 2") + " – " + to.Format("Jan 2, 2006"),
		Footer: "Sent by Visionary. Turn the digest off or change its day in Alerts → Digest."}
	for _, l := range strings.Split(summary, "\n") {
		if l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- ")); l != "" {
			v.Summary = append(v.Summary, l)
		}
	}
	count := groupNum
	ms := func(x float64) string { return formatUnit(x, "ms") }
	pct := func(x float64) string { return formatUnit(x, "percent") }
	opt := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}
	stat := func(label string, cur, prev float64, has bool, f func(float64) string, upIsBad bool) emailStat {
		if !has {
			return emailStat{Label: label, Value: "—"}
		}
		c, t := change(cur, prev, f, upIsBad)
		return emailStat{Label: label, Value: f(cur), Change: c, Tone: t}
	}
	v.Stats = []emailStat{
		stat("Requests", float64(d.This.Requests), float64(d.Last.Requests), true, count, false),
		stat("p95 latency", opt(d.This.P95), opt(d.Last.P95), d.This.P95 != nil, ms, true),
		stat("5xx rate", opt(d.This.ErrorRate), opt(d.Last.ErrorRate), d.This.ErrorRate != nil, pct, true),
		stat("Alerts fired", float64(d.This.Incidents), float64(d.Last.Incidents), true, count, true),
	}
	add := func(sec emailSection) {
		if len(sec.Rows) > 0 || sec.Note != "" {
			v.Sections = append(v.Sections, sec)
		}
	}
	routeRows := func(rs []digestChange, tone string) []emailRow {
		var out []emailRow
		for _, r := range rs {
			out = append(out, emailRow{Cells: []string{r.Route, ms(r.PrevP95), ms(r.P95), fmt.Sprintf("%+.0f%%", r.ChangePct)}, Tone: tone})
		}
		return out
	}
	add(emailSection{Title: "Slower than last week", Columns: []string{"Route", "Was", "p95", "Change"}, Rows: routeRows(d.Regressed, "bad")})
	add(emailSection{Title: "Faster than last week", Columns: []string{"Route", "Was", "p95", "Change"}, Rows: routeRows(d.Improved, "good")})
	var slow []emailRow
	for _, r := range d.Slowest {
		slow = append(slow, emailRow{Cells: []string{r.Route, count(float64(r.Requests)), ms(r.P95)}})
	}
	add(emailSection{Title: "Slowest routes", Columns: []string{"Route", "Requests", "p95"}, Rows: slow})
	var inc []emailRow
	for _, i := range d.Incidents {
		tone := ""
		if i.Severity == "critical" {
			tone = "bad"
		}
		inc = append(inc, emailRow{Cells: []string{i.Rule, i.Severity, fmt.Sprintf("%d×", i.Count)}, Tone: tone})
	}
	add(emailSection{Title: "Incidents", Columns: []string{"Rule", "Severity", "Fired"}, Rows: inc})
	if e := d.Errors; e != nil {
		sec := emailSection{Title: "Errors", Note: fmt.Sprintf("%s errors, was %s; %d new issue(s).", count(float64(e.Total)), count(float64(e.PrevTotal)), e.NewIssues),
			Columns: []string{"Issue", "Count"}}
		for _, i := range e.Top {
			name := i.Type + ": " + truncate(i.Message, 90)
			tone := ""
			if i.New {
				name, tone = "NEW · "+name, "bad"
			}
			sec.Rows = append(sec.Rows, emailRow{Cells: []string{name, count(float64(i.Count))}, Tone: tone})
		}
		add(sec)
	}
	var up []emailRow
	for _, c := range d.Uptime {
		tone := ""
		if c.Availability < 99.9 {
			tone = "bad"
		}
		up = append(up, emailRow{Cells: []string{c.Check, count(float64(c.Failures)), ms(c.AvgMs), fmt.Sprintf("%.2f%%", c.Availability)}, Tone: tone})
	}
	add(emailSection{Title: "Uptime", Columns: []string{"Check", "Failed", "Avg", "Up"}, Rows: up})
	var mach []emailRow
	for _, m := range d.Machines {
		tone := ""
		if opt(m.PeakCPU) >= 90 || opt(m.PeakMem) >= 90 {
			tone = "bad"
		}
		mach = append(mach, emailRow{Cells: []string{m.Instance, pctOrNA(m.AvgCPU), pctOrNA(m.PeakCPU), pctOrNA(m.PeakMem)}, Tone: tone})
	}
	add(emailSection{Title: "Machines", Columns: []string{"Instance", "Avg CPU", "Peak CPU", "Peak memory"}, Rows: mach})
	if c := d.Containers; c != nil {
		sec := emailSection{Title: "Containers", Note: fmt.Sprintf("%d restart(s), %d out-of-memory kill(s), %d exit(s), %d failed health check(s).", c.Restarts, c.OOMKills, c.Exits, c.Unhealthy),
			Columns: []string{"Container", "Restarts", "Unhealthy", "OOM kills"}}
		for _, w := range c.Worst {
			tone := ""
			if w.OOMKills > 0 {
				tone = "bad"
			}
			sec.Rows = append(sec.Rows, emailRow{Cells: []string{w.Container, count(float64(w.Restarts)), count(float64(w.Unhealthy)), count(float64(w.OOMKills))}, Tone: tone})
		}
		add(sec)
	}
	var vit []emailRow
	for _, x := range d.Vitals {
		was := "—"
		if x.PrevP75 != nil {
			was = vitalText(x.Name, *x.PrevP75)
		}
		tone := map[string]string{"poor": "bad", "good": "good"}[x.Rating]
		vit = append(vit, emailRow{Cells: []string{x.Name, was, vitalText(x.Name, x.P75), strings.ReplaceAll(x.Rating, "_", " ")}, Tone: tone})
	}
	add(emailSection{Title: "Web Vitals (p75)", Columns: []string{"Metric", "Was", "Now", "Rating"}, Rows: vit})
	if len(d.Releases) > 0 {
		var rel []emailRow
		for _, r := range d.Releases {
			at, _ := time.Parse(tsLayout, r.FirstSeen)
			rel = append(rel, emailRow{Cells: []string{strings.TrimSpace(r.Service + " " + r.Version), at.Format("Mon Jan 2")}})
		}
		add(emailSection{Title: "New versions", Columns: []string{"Version", "First seen"}, Rows: rel})
	}
	if sec := d.Security; sec != nil {
		total := int64(0)
		for _, n := range sec.Findings {
			total += n
		}
		if total > 0 || sec.FailedLogins > 0 || len(sec.PendingHosts) > 0 {
			s := emailSection{Title: "Security", Note: fmt.Sprintf("%d finding(s): %d critical, %d warning. Failed logins: %d, was %d.",
				total, sec.Findings["critical"], sec.Findings["warning"], sec.FailedLogins, sec.PrevFailed), Columns: []string{"Finding", "Count"}}
			for _, k := range head(sec.FindingKinds, 5) {
				s.Rows = append(s.Rows, emailRow{Cells: []string{strings.ReplaceAll(k.Kind, "_", " "), count(float64(k.Count))}})
			}
			add(s)
		}
	}
	return v
}
