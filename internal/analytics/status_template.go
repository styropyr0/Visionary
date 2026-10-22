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
  --bg: #111110; --surface-1: #1a1a19; --surface-2: #222221; --surface-3: #2b2b29;
  --border: rgba(255,255,255,0.08); --text-1: #f0efec; --text-2: #c3c2b7; --text-3: #898781;
  --good: #0ca30c; --warning: #fab219; --critical: #d03b3b;
  --good-text: #3fbf3f; --warning-text: #fab219; --critical-text: #f07c7c; --accent: #3987e5;
}
@media (prefers-color-scheme: light) {
  :root {
    color-scheme: light;
    --bg: #f3f3f1; --surface-1: #fcfcfb; --surface-2: #f3f3f0; --surface-3: #e9e8e3;
    --border: rgba(11,11,11,0.1); --text-1: #0b0b0b; --text-2: #52514e; --text-3: #75736d;
    --good-text: #006300; --warning-text: #8a5a00; --critical-text: #b42c2c; --accent: #2a78d6;
  }
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--text-1); font: 14px/1.5 "Inter Variable", system-ui, -apple-system, "Segoe UI", sans-serif; -webkit-font-smoothing: antialiased; }
main { max-width: 860px; margin: 0 auto; padding: 40px 16px 32px; }
h1 { margin: 0 0 20px; font-size: 22px; font-weight: 600; letter-spacing: -0.01em; }
h2 { margin: 32px 0 10px; font-size: 13px; font-weight: 600; color: var(--text-2); text-transform: uppercase; letter-spacing: 0.04em; }
.card { background: var(--surface-1); border: 1px solid var(--border); border-radius: 10px; }
.banner { display: flex; align-items: center; gap: 12px; padding: 16px 18px; font-size: 16px; font-weight: 600; }
.banner .sub { margin-left: auto; font-size: 12px; font-weight: 400; color: var(--text-3); }
.icon { display: grid; place-items: center; width: 28px; height: 28px; border-radius: 50%; color: #fff; flex: none; }
.icon svg { width: 16px; height: 16px; }
.operational .icon { background: var(--good); }
.degraded .icon { background: var(--warning); color: #111; }
.partial_outage .icon, .major_outage .icon { background: var(--critical); }
.unknown .icon { background: var(--surface-3); color: var(--text-2); }
.check { padding: 16px 18px; border-top: 1px solid var(--border); }
.check:first-child { border-top: 0; }
.row { display: flex; align-items: baseline; gap: 12px; }
.name { font-weight: 600; }
.url { color: var(--text-3); font-size: 12px; overflow-wrap: anywhere; }
.state { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 500; white-space: nowrap; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--surface-3); }
.state.operational { color: var(--good-text); } .state.operational .dot { background: var(--good); }
.state.degraded { color: var(--warning-text); } .state.degraded .dot { background: var(--warning); }
.state.outage { color: var(--critical-text); } .state.outage .dot { background: var(--critical); }
.state.paused, .state.unknown { color: var(--text-3); }
.bars { display: flex; gap: 2px; height: 30px; margin: 12px 0 6px; }
.bars span { flex: 1; min-width: 2px; border-radius: 2px; background: var(--surface-3); }
.bars .ok { background: var(--good); }
.bars .minor { background: var(--warning); }
.bars .major { background: var(--critical); }
.legend { display: flex; justify-content: space-between; color: var(--text-3); font-size: 12px; }
.legend b { color: var(--text-2); font-weight: 500; }
.incident { display: flex; gap: 12px; align-items: baseline; padding: 12px 18px; border-top: 1px solid var(--border); }
.incident:first-child { border-top: 0; }
.incident .what { font-weight: 500; }
.incident .meta { margin-left: auto; color: var(--text-3); font-size: 12.5px; text-align: right; }
.tag { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 10px; font-size: 11.5px; font-weight: 600; }
.tag.live { background: var(--critical); color: #fff; }
.tag.done { background: var(--surface-3); color: var(--text-2); }
.empty { padding: 16px 18px; color: var(--text-3); }
footer { margin-top: 32px; color: var(--text-3); font-size: 12px; display: flex; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
@media (max-width: 560px) { .bars span:nth-child(-n+60) { display: none; } .legend .from90 { display: none; } .legend .from30 { display: inline; } }
.legend .from30 { display: none; }
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<section class="card banner {{.State}}" aria-live="polite">
  <span class="icon" aria-hidden="true">{{if eq .State "operational"}}<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 8.5l3 3 6-7"/></svg>{{else if eq .State "unknown"}}<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><path d="M4 8h8"/></svg>{{else}}<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><path d="M8 4v5"/><path d="M8 12v.01"/></svg>{{end}}</span>
  <span>{{.Label}}</span>
  <span class="sub">Updated {{when .UpdatedAt}}</span>
</section>

{{with ongoing .Incidents}}
<h2>Current incidents</h2>
<section class="card">
{{range .}}  <div class="incident"><span class="tag live">Ongoing</span><span class="what">{{.Check}} is not responding</span><span class="meta">since {{when .StartedAt}}<br>{{dur .DurationS}} so far</span></div>
{{end}}</section>
{{end}}

<h2>Services</h2>
<section class="card">
{{range .Checks}}  <div class="check">
    <div class="row">
      <div><div class="name">{{.Name}}</div>{{if .URL}}<div class="url">{{.URL}}</div>{{end}}</div>
      <span class="state {{.State}}"><span class="dot"></span>{{stateLabel .State}}</span>
    </div>
    <div class="bars" role="img" aria-label="Daily uptime for the last 90 days">{{range .Days}}<span class="{{.Level}}" title="{{day .Day}}: {{if .Uptime}}{{pct .Uptime}} up, {{end}}{{levelLabel .Level}}"></span>{{end}}</div>
    <div class="legend"><span class="from90">90 days ago</span><span class="from30">30 days ago</span><span><b>{{pct .Uptime90}}</b> uptime</span><span>Today</span></div>
  </div>
{{else}}  <div class="empty">No services are published on this page yet.</div>
{{end}}</section>

<h2>Past 7 days</h2>
<section class="card">
{{range resolved .Incidents}}  <div class="incident"><span class="tag done">Resolved</span><span class="what">{{.Check}} was unavailable</span><span class="meta">{{when .StartedAt}} to {{whenp .EndedAt}}<br>{{dur .DurationS}}</span></div>
{{else}}  <div class="empty">No incidents in the past 7 days.</div>
{{end}}</section>

<footer><span>Times in UTC. This page refreshes every minute.</span><span>Monitored by Visionary</span></footer>
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
