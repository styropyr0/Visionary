package analytics

import (
	"fmt"
	"html/template"
	"time"
)

// The status page is server-rendered HTML with inline styles (the CSP allows
// them) and a theme that follows the visitor's system setting. It reloads
// itself every minute. Times are written in UTC inside <time> elements;
// /status.js, a same-origin script the CSP allows, rewrites them into the
// visitor's own time zone, and without script they stay readable in UTC.

var statusFuncs = template.FuncMap{
	"pct": func(p *float64) string {
		if p == nil {
			return "—"
		}
		if *p == 100 {
			return "100%"
		}
		return fmt.Sprintf("%.2f%%", *p)
	},
	"day": func(s string) string {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return s
		}
		return t.Format("Jan 2")
	},
	"when": localTime,
	"whenp": func(s *string) template.HTML {
		if s == nil {
			return ""
		}
		return localTime(*s)
	},
	"dur": func(sec int64) string {
		d := time.Duration(sec) * time.Second
		switch {
		case d < time.Minute:
			return fmt.Sprintf("%ds", sec)
		case d < time.Hour:
			return fmt.Sprintf("%dm", int(d.Minutes()))
		case d < 24*time.Hour:
			return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
		}
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	},
	"stateLabel": func(s string) string {
		switch s {
		case "operational":
			return "Operational"
		case "degraded":
			return "Degraded"
		case "outage":
			return "Outage"
		case "paused":
			return "Not monitored"
		}
		return "No data yet"
	},
	"levelLabel": func(l string) string {
		switch l {
		case "ok":
			return "no downtime"
		case "minor":
			return "minor downtime"
		case "major":
			return "major downtime"
		}
		return "no data"
	},
	"ongoing": func(list []statusIncident) []statusIncident {
		var out []statusIncident
		for _, in := range list {
			if in.Ongoing {
				out = append(out, in)
			}
		}
		return out
	},
	"resolved": func(list []statusIncident) []statusIncident {
		var out []statusIncident
		for _, in := range list {
			if !in.Ongoing {
				out = append(out, in)
			}
		}
		return out
	},
}

var statusTemplate = template.Must(template.New("status").Funcs(statusFuncs).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="60">
<meta name="robots" content="noindex">
<script src="/status.js" defer></script>
<title>{{.Title}}</title>
<style>
:root {
  color-scheme: dark;
  --bg: #0e0e0d; --surface-1: #181817; --surface-3: #2b2b29;
  --border: rgba(255,255,255,0.08); --text-1: #f0efec; --text-2: #c3c2b7; --text-3: #898781;
  --good: #0ca30c; --warning: #fab219; --critical: #d03b3b; --muted: #3a3a37;
  --good-text: #4fcf4f; --warning-text: #fab219; --critical-text: #f07c7c;
  --good-wash: rgba(12,163,12,0.12); --warning-wash: rgba(250,178,25,0.12); --critical-wash: rgba(208,59,59,0.14); --muted-wash: rgba(255,255,255,0.04);
}
@media (prefers-color-scheme: light) {
  :root {
    color-scheme: light;
    --bg: #f4f4f2; --surface-1: #ffffff; --surface-3: #e7e6e1;
    --border: rgba(11,11,11,0.09); --text-1: #0b0b0b; --text-2: #4f4e4b; --text-3: #74726c; --muted: #d6d5cf;
    --good-text: #046b04; --warning-text: #8a5a00; --critical-text: #b42c2c;
    --good-wash: rgba(12,163,12,0.09); --warning-wash: rgba(250,178,25,0.14); --critical-wash: rgba(208,59,59,0.09); --muted-wash: rgba(11,11,11,0.03);
  }
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--text-1); font: 14px/1.5 "Inter Variable", Inter, system-ui, -apple-system, "Segoe UI", sans-serif; -webkit-font-smoothing: antialiased; }
main { max-width: 820px; margin: 0 auto; padding: 48px 16px 40px; }
header.top { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 20px; }
header.top h1 { margin: 0; font-size: 20px; font-weight: 650; letter-spacing: -0.01em; }
header.top .updated { color: var(--text-3); font-size: 12.5px; white-space: nowrap; }
h2 { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; margin: 36px 0 12px; font-size: 15px; font-weight: 600; }
h2 small { color: var(--text-3); font-size: 12.5px; font-weight: 400; }
.card { background: var(--surface-1); border: 1px solid var(--border); border-radius: 12px; overflow: hidden; }

/* The overall state: one tinted card in the colour of the worst state. */
.hero { display: flex; align-items: flex-start; gap: 16px; padding: 22px; border-radius: 14px; border: 1px solid var(--border); background: var(--muted-wash); }
.hero.operational { background: var(--good-wash); }
.hero.degraded { background: var(--warning-wash); }
.hero.partial_outage, .hero.major_outage { background: var(--critical-wash); }
.hero .icon { display: grid; place-items: center; width: 40px; height: 40px; border-radius: 50%; color: #fff; flex: none; background: var(--muted); }
.hero .icon svg { width: 20px; height: 20px; }
.hero.operational .icon { background: var(--good); }
.hero.degraded .icon { background: var(--warning); color: #111; }
.hero.partial_outage .icon, .hero.major_outage .icon { background: var(--critical); }
.hero .label { font-size: 19px; font-weight: 650; line-height: 1.3; margin-top: 1px; }
.hero .sub { margin-top: 4px; color: var(--text-2); font-size: 13.5px; }

.notice { padding: 18px 20px; border-top: 1px solid var(--border); }
.notice:first-child { border-top: 0; }
.notice .head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.notice .title { font-weight: 600; font-size: 15px; }
.notice .since { width: 100%; color: var(--text-3); font-size: 12.5px; }
.updates { list-style: none; margin: 14px 0 0; padding: 0; display: grid; gap: 12px; }
.updates li { display: grid; grid-template-columns: 12px 1fr; gap: 10px; }
.updates .mark { width: 8px; height: 8px; margin-top: 7px; border-radius: 50%; background: var(--text-3); }
.updates li:first-child .mark { background: var(--text-1); }
.updates .st { font-weight: 600; }
.updates .when { display: block; color: var(--text-3); font-size: 12.5px; }

.tag { display: inline-flex; align-items: center; height: 22px; padding: 0 9px; border-radius: 11px; font-size: 12px; font-weight: 600; white-space: nowrap; }
.tag.live { background: var(--critical-wash); color: var(--critical-text); }
.tag.watch { background: var(--warning-wash); color: var(--warning-text); }
.tag.done { background: var(--surface-3); color: var(--text-2); }

.check { padding: 18px 20px 16px; border-top: 1px solid var(--border); }
.check:first-child { border-top: 0; }
.check .head { display: flex; align-items: center; gap: 12px; }
.check .name { font-weight: 600; }
.check .url { color: var(--text-3); font-size: 12px; overflow-wrap: anywhere; }
.check .right { margin-left: auto; display: flex; align-items: center; gap: 14px; white-space: nowrap; }
.check .uptime { color: var(--text-2); font-size: 13px; font-variant-numeric: tabular-nums; }
.state { display: inline-flex; align-items: center; gap: 7px; font-size: 13px; font-weight: 550; }
.state .dot { width: 9px; height: 9px; border-radius: 50%; background: var(--muted); }
.state.operational { color: var(--good-text); } .state.operational .dot { background: var(--good); }
.state.degraded { color: var(--warning-text); } .state.degraded .dot { background: var(--warning); }
.state.outage { color: var(--critical-text); } .state.outage .dot { background: var(--critical); }
.state.paused, .state.unknown { color: var(--text-3); }
.bars { display: flex; gap: 2px; height: 34px; margin: 14px 0 8px; }
.bars span { flex: 1; min-width: 2px; border-radius: 3px; background: var(--muted); }
.bars span:hover { opacity: 0.75; }
.bars .ok { background: var(--good); }
.bars .minor { background: var(--warning); }
.bars .major { background: var(--critical); }
.scale { display: flex; justify-content: space-between; color: var(--text-3); font-size: 12px; }
.scale .from30 { display: none; }

.past { display: grid; grid-template-columns: 1fr auto; gap: 4px 16px; padding: 14px 20px; border-top: 1px solid var(--border); }
.past:first-child { border-top: 0; }
.past .what { font-weight: 550; }
.past .when { color: var(--text-3); font-size: 12.5px; grid-column: 1; }
.past .dur { color: var(--text-2); font-size: 13px; grid-row: 1; grid-column: 2; font-variant-numeric: tabular-nums; }
.empty { padding: 18px 20px; color: var(--text-3); }

footer { margin-top: 36px; padding-top: 18px; border-top: 1px solid var(--border); color: var(--text-3); font-size: 12px; display: flex; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
footer a { color: var(--text-2); text-decoration: none; }
footer a:hover { text-decoration: underline; }

@media (max-width: 600px) {
  main { padding-top: 28px; }
  header.top { flex-direction: column; align-items: flex-start; gap: 4px; }
  .bars span:nth-child(-n+60) { display: none; }
  .scale .from90 { display: none; }
  .scale .from30 { display: inline; }
  .check .right { flex-direction: column; align-items: flex-end; gap: 2px; }
}
</style>
</head>
<body>
<main>
<header class="top">
  <h1>{{.Title}}</h1>
  <span class="updated">Updated {{when .UpdatedAt}}</span>
</header>

<section class="hero {{.State}}" aria-live="polite">
  <span class="icon" aria-hidden="true">{{if eq .State "operational"}}<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 8.5l3 3 6-7"/></svg>{{else if eq .State "unknown"}}<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><path d="M4 8h8"/></svg>{{else}}<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><path d="M8 4v5"/><path d="M8 12v.01"/></svg>{{end}}</span>
  <div>
    <div class="label">{{.Label}}</div>
    <div class="sub">{{if .Checks}}{{len .Checks}} service{{if gt (len .Checks) 1}}s{{end}} monitored. {{end}}{{with ongoing .Incidents}}{{len .}} not responding right now.{{else}}{{if eq $.State "unknown"}}Nothing has been published here yet.{{else}}Checked around the clock.{{end}}{{end}}</div>
  </div>
</section>

{{with .Notices}}
<h2>Incidents</h2>
<section class="card">
{{range .}}  <article class="notice">
    <div class="head"><span class="tag {{if .ResolvedAt}}done{{else if eq .Status "monitoring"}}watch{{else}}live{{end}}">{{.StatusLabel}}</span><span class="title">{{.Title}}</span><span class="since">Started {{when .StartedAt}}{{if .ResolvedAt}} · resolved {{when .ResolvedAt}}{{end}}</span></div>
{{with .Updates}}    <ol class="updates">
{{range .}}      <li><span class="mark" aria-hidden="true"></span><span><span class="st">{{.Status}}</span>{{if .Body}} · {{.Body}}{{end}}<span class="when">{{when .At}}</span></span></li>
{{end}}    </ol>
{{end}}  </article>
{{end}}</section>
{{end}}

{{with ongoing .Incidents}}
<h2>Not responding now</h2>
<section class="card">
{{range .}}  <div class="past"><span class="what">{{.Check}}</span><span class="dur"><span class="tag live">{{dur .DurationS}} so far</span></span><span class="when">Since {{when .StartedAt}}</span></div>
{{end}}</section>
{{end}}

<h2>Services <small>Last 90 days</small></h2>
<section class="card">
{{range .Checks}}  <div class="check">
    <div class="head">
      <div><div class="name">{{.Name}}</div>{{if .URL}}<div class="url">{{.URL}}</div>{{end}}</div>
      <div class="right"><span class="uptime">{{pct .Uptime90}} uptime</span><span class="state {{.State}}"><span class="dot"></span>{{stateLabel .State}}</span></div>
    </div>
    <div class="bars" role="img" aria-label="Daily uptime for the last 90 days">{{range .Days}}<span class="{{.Level}}" title="{{day .Day}}: {{if .Uptime}}{{pct .Uptime}} up, {{end}}{{levelLabel .Level}}"></span>{{end}}</div>
    <div class="scale"><span class="from90">90 days ago</span><span class="from30">30 days ago</span><span>Today</span></div>
  </div>
{{else}}  <div class="empty">No services are listed on this page yet.</div>
{{end}}</section>

<h2>Past incidents <small>Last 7 days</small></h2>
<section class="card">
{{range resolved .Incidents}}  <div class="past"><span class="what">{{.Check}} was unavailable</span><span class="dur">{{dur .DurationS}}</span><span class="when">{{when .StartedAt}} to {{whenp .EndedAt}}</span></div>
{{else}}  <div class="empty">No incidents in the last 7 days.</div>
{{end}}</section>

<footer><span>Times are shown in your time zone. This page refreshes every minute.</span><span><a href="/status.json">JSON feed</a> · Monitored by Visionary</span></footer>
</main>
</body>
</html>
`))

// localTime renders a stored timestamp as a <time> element in UTC, for
// /status.js to show in the visitor's zone. Both parts come from the parsed
// time, so nothing from the database reaches the HTML unescaped.
func localTime(s string) template.HTML {
	t, err := time.Parse(tsLayout, s)
	if err != nil {
		return template.HTML(template.HTMLEscapeString(s))
	}
	return template.HTML(`<time datetime="` + t.UTC().Format(time.RFC3339) + `">` + t.UTC().Format("Jan 2, 15:04 UTC") + `</time>`)
}

// statusScript shows the page's times in the visitor's time zone.
const statusScript = `document.querySelectorAll("time[datetime]").forEach(function (el) {
  var d = new Date(el.getAttribute("datetime"));
  if (isNaN(d)) return;
  el.textContent = new Intl.DateTimeFormat(undefined, {
    month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", timeZoneName: "short"
  }).format(d);
});
`
