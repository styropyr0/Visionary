package analytics

import (
	"fmt"
	"html/template"
	"io/fs"
	"sync"
	"time"
)

// The status page is server-rendered HTML with inline styles (the CSP allows
// them), drawn with the dashboard's own tokens and Inter, and a theme that
// follows the visitor's system setting. It reloads itself every minute. Times
// are written in UTC inside <time> elements; /status.js, a same-origin script
// the CSP allows, rewrites them into the visitor's own time zone, and without
// script they stay readable in UTC.

// statusView is what the template sees: the cached page plus where the
// dashboard's font lives in this build.
type statusView struct {
	*statusPage
	Font string
}

var statusFont struct {
	once sync.Once
	path string
}

// statusFontPath finds the dashboard's Latin Inter file in the embedded build;
// its name carries a content hash that changes between builds.
func (s *Server) statusFontPath() string {
	statusFont.once.Do(func() {
		if s.static == nil {
			return
		}
		if m, _ := fs.Glob(s.static, "assets/inter-latin-wght-normal-*.woff2"); len(m) > 0 {
			statusFont.path = "/" + m[0]
		}
	})
	return statusFont.path
}

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
			return "Down"
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
	// avgUptime is the mean 90-day uptime of the checks that have one.
	"avgUptime": func(checks []statusCheck) *float64 {
		var sum float64
		n := 0
		for _, c := range checks {
			if c.Uptime90 != nil {
				sum += *c.Uptime90
				n++
			}
		}
		if n == 0 {
			return nil
		}
		v := sum / float64(n)
		return &v
	},
	"countState": func(checks []statusCheck, state string) int {
		n := 0
		for _, c := range checks {
			if c.State == state {
				n++
			}
		}
		return n
	},
}

var statusTemplate = template.Must(template.New("status").Funcs(statusFuncs).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="60">
<meta name="robots" content="noindex">
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<script src="/status.js" defer></script>
<title>{{.Title}}</title>
<style>
{{with .Font}}@font-face { font-family: "Inter Variable"; font-style: normal; font-display: swap; font-weight: 100 900; src: url("{{.}}") format("woff2-variations"); }
{{end}}:root {
  color-scheme: dark;
  --font-sans: "Inter Variable", system-ui, -apple-system, "Segoe UI", sans-serif;
  --radius: 6px; --radius-lg: 10px;
  --good: #0ca30c; --warning: #fab219; --critical: #d03b3b;
  --bg: #111110; --surface-1: #1a1a19; --surface-2: #222221; --surface-3: #2b2b29;
  --border: rgba(255, 255, 255, 0.08); --border-strong: rgba(255, 255, 255, 0.15);
  --text-1: #f0efec; --text-2: #c3c2b7; --text-3: #898781; --accent: #3987e5;
  --hover-wash: rgba(255, 255, 255, 0.04); --button-wash: rgba(255, 255, 255, 0.07);
  --good-text: #3fbf3f; --critical-text: #f07c7c; --warning-text: #fab219;
  --good-wash: rgba(12, 163, 12, 0.1); --warning-wash: rgba(250, 178, 25, 0.1); --critical-wash: rgba(208, 59, 59, 0.12);
}
@media (prefers-color-scheme: light) {
  :root {
    color-scheme: light;
    --bg: #f3f3f1; --surface-1: #fcfcfb; --surface-2: #f3f3f0; --surface-3: #e9e8e3;
    --border: rgba(11, 11, 11, 0.1); --border-strong: rgba(11, 11, 11, 0.18);
    --text-1: #0b0b0b; --text-2: #52514e; --text-3: #75736d; --accent: #2a78d6;
    --hover-wash: rgba(11, 11, 11, 0.035); --button-wash: rgba(11, 11, 11, 0.055);
    --good-text: #006300; --critical-text: #b42c2c; --warning-text: #8a5a00;
    --good-wash: rgba(12, 163, 12, 0.08); --warning-wash: rgba(250, 178, 25, 0.12); --critical-wash: rgba(208, 59, 59, 0.08);
  }
}
* { box-sizing: border-box; }
html { background: var(--bg); }
body { margin: 0; background: var(--bg); color: var(--text-1); font-family: var(--font-sans); font-size: 13px; line-height: 1.45; -webkit-font-smoothing: antialiased; font-feature-settings: "cv11", "ss01"; }
svg { flex: none; }

.topbar { position: sticky; top: 0; z-index: 2; height: 52px; border-bottom: 1px solid var(--border); background: color-mix(in srgb, var(--bg) 88%, transparent); backdrop-filter: blur(8px); }
.topbar .in { display: flex; align-items: center; gap: 12px; max-width: 1040px; height: 100%; margin: 0 auto; padding: 0 16px; }
.crumbs { display: flex; align-items: center; gap: 8px; min-width: 0; font-size: 14px; }
.crumbs .sep, .crumbs .sec { color: var(--text-3); }
.crumbs b { font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.pill { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 12px; border-radius: var(--radius); background: var(--button-wash); color: var(--text-2); font-size: 12.5px; white-space: nowrap; }
.pill svg { width: 14px; height: 14px; color: var(--text-3); }

.content { max-width: 1040px; margin: 0 auto; padding: 16px; display: grid; gap: 12px; }
.panel { border: 1px solid var(--border); border-radius: var(--radius); background: var(--surface-1); min-width: 0; }
.panel-head { display: flex; align-items: center; gap: 6px; min-height: 40px; padding: 6px 14px 0; }
.panel-title { margin: 0; font-size: 13px; font-weight: 600; }
.panel-head .aside { margin-left: auto; color: var(--text-3); font-size: 12px; }
.panel-body { padding: 4px 14px 14px; }

.banner { display: flex; align-items: flex-start; gap: 12px; padding: 14px 16px; border: 1px solid var(--border); border-radius: var(--radius); background: var(--surface-1); }
.banner.operational { background: var(--good-wash); }
.banner.degraded { background: var(--warning-wash); }
.banner.partial_outage, .banner.major_outage { background: var(--critical-wash); }
.banner svg { width: 20px; height: 20px; margin-top: 1px; color: var(--text-3); }
.banner.operational svg { color: var(--good-text); }
.banner.degraded svg { color: var(--warning-text); }
.banner.partial_outage svg, .banner.major_outage svg { color: var(--critical-text); }
.banner .label { font-size: 15px; font-weight: 600; }
.banner .sub { margin-top: 2px; color: var(--text-2); }

.tiles { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.stat { display: flex; flex-direction: column; gap: 2px; padding: 12px 14px 10px; min-height: 100px; }
.stat-label { font-size: 12px; font-weight: 500; color: var(--text-2); }
.stat-value { font-size: 26px; font-weight: 600; letter-spacing: -0.02em; line-height: 1.25; font-variant-numeric: tabular-nums; }
.stat-unit { margin-left: 3px; font-size: 14px; font-weight: 500; color: var(--text-3); }
.stat-foot { margin-top: auto; font-size: 12px; color: var(--text-3); }
.stat-value.good { color: var(--good-text); } .stat-value.warn { color: var(--warning-text); } .stat-value.bad { color: var(--critical-text); }

table { width: 100%; border-collapse: collapse; font-size: 12.5px; }
th { height: 32px; padding: 0 10px; border-bottom: 1px solid var(--border-strong); color: var(--text-3); font-size: 11.5px; font-weight: 500; text-align: left; white-space: nowrap; }
td { height: 40px; padding: 8px 10px; border-bottom: 1px solid var(--border); vertical-align: middle; }
tr:last-child td { border-bottom: 0; }
tbody tr:hover td { background: var(--hover-wash); }
th.num, td.num { text-align: right; font-variant-numeric: tabular-nums; white-space: nowrap; }
.svc { font-weight: 500; }
.url { color: var(--text-3); font-family: ui-monospace, "SF Mono", "Cascadia Mono", Menlo, Consolas, monospace; font-size: 11.5px; overflow-wrap: anywhere; }
.state { display: inline-flex; align-items: center; gap: 6px; font-weight: 500; white-space: nowrap; }
.state svg { width: 14px; height: 14px; }
.state.operational { color: var(--good-text); } .state.degraded { color: var(--warning-text); } .state.outage { color: var(--critical-text); } .state.paused, .state.unknown { color: var(--text-3); }
.bars-cell { width: 46%; min-width: 200px; }
.bars { display: flex; gap: 2px; height: 22px; }
.bars span { flex: 1; min-width: 1px; border-radius: 1.5px; background: var(--surface-3); }
.bars span:hover { opacity: 0.7; }
.bars .ok { background: var(--good); } .bars .minor { background: var(--warning); } .bars .major { background: var(--critical); }
.axis { display: flex; justify-content: space-between; margin-top: 4px; font-size: 11px; color: var(--text-3); }
.axis .d30 { display: none; }
.legend { display: flex; gap: 14px; flex-wrap: wrap; padding: 10px 10px 0; color: var(--text-3); font-size: 12px; }
.legend i { display: inline-block; width: 8px; height: 8px; margin-right: 6px; border-radius: 2px; background: var(--surface-3); }
.legend .ok { background: var(--good); } .legend .minor { background: var(--warning); } .legend .major { background: var(--critical); }

.tag { display: inline-flex; align-items: center; height: 20px; padding: 0 7px; border-radius: 4px; font-size: 11.5px; font-weight: 600; white-space: nowrap; }
.tag.live { background: var(--critical-wash); color: var(--critical-text); }
.tag.watch { background: var(--warning-wash); color: var(--warning-text); }
.tag.done { background: var(--surface-3); color: var(--text-2); }
.notice + .notice { margin-top: 14px; padding-top: 14px; border-top: 1px solid var(--border); }
.notice .head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.notice .title { font-size: 13.5px; font-weight: 600; }
.notice .since { margin-left: auto; color: var(--text-3); font-size: 12px; }
.updates { list-style: none; margin: 10px 0 0; padding: 0; display: grid; gap: 8px; }
.updates li { display: grid; grid-template-columns: 150px 1fr; gap: 12px; }
.updates .when { color: var(--text-3); font-size: 12px; }
.updates b { font-weight: 600; }
.empty { padding: 18px 10px; color: var(--text-3); }

footer { max-width: 1040px; margin: 8px auto 0; padding: 14px 16px 32px; display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; color: var(--text-3); font-size: 12px; }
footer .brand { display: inline-flex; align-items: center; gap: 8px; color: var(--text-2); text-decoration: none; }
footer .brand img { width: 18px; height: 18px; }
footer a { color: var(--text-2); }

@media (max-width: 760px) {
  .tiles { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .bars span:nth-child(-n+60) { display: none; }
  .axis .d90 { display: none; } .axis .d30 { display: inline; }
  .hide-sm { display: none; }
  .bars-cell { min-width: 110px; }
  .updates li { grid-template-columns: 1fr; gap: 0; }
  .notice .since { margin-left: 0; width: 100%; }
}
</style>
</head>
<body>
<header class="topbar"><div class="in">
  <nav class="crumbs" aria-label="Page"><b>{{.Title}}</b></nav>
  <span class="pill"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M3 21v-5h5"/><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/></svg>Updated {{when .UpdatedAt}}</span>
</div></header>

<main class="content">
<section class="banner {{.State}}" aria-live="polite">
  {{if eq .State "operational"}}<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><path d="m9 12 2 2 4-4"/></svg>{{else if eq .State "unknown"}}<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><path d="M8 12h8"/></svg>{{else}}<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>{{end}}
  <div>
    <div class="label">{{.Label}}</div>
    <div class="sub">{{with ongoing .Incidents}}{{len .}} service{{if gt (len .) 1}}s{{end}} not responding right now.{{else}}{{if eq $.State "unknown"}}Nothing has been published on this page yet.{{else}}Every service is checked around the clock.{{end}}{{end}}</div>
  </div>
</section>

{{if .Checks}}<section class="tiles">
  <div class="panel stat"><span class="stat-label">Services</span><span class="stat-value">{{len .Checks}}</span><span class="stat-foot">on this page</span></div>
  <div class="panel stat"><span class="stat-label">Operational</span>{{$up := countState .Checks "operational"}}<span class="stat-value {{if eq $up (len .Checks)}}good{{else}}bad{{end}}">{{$up}}<span class="stat-unit">/ {{len .Checks}}</span></span><span class="stat-foot">{{countState .Checks "outage"}} down · {{countState .Checks "degraded"}} degraded</span></div>
  <div class="panel stat"><span class="stat-label">Uptime</span><span class="stat-value">{{pct (avgUptime .Checks)}}</span><span class="stat-foot">average over 90 days</span></div>
  <div class="panel stat"><span class="stat-label">Incidents</span><span class="stat-value">{{len .Incidents}}</span><span class="stat-foot">in the last 7 days</span></div>
</section>{{end}}

{{with .Notices}}<section class="panel">
  <div class="panel-head"><h2 class="panel-title">Incidents</h2></div>
  <div class="panel-body">
{{range .}}    <article class="notice">
      <div class="head"><span class="tag {{if .ResolvedAt}}done{{else if eq .Status "monitoring"}}watch{{else}}live{{end}}">{{.StatusLabel}}</span><span class="title">{{.Title}}</span><span class="since">Started {{when .StartedAt}}{{if .ResolvedAt}} · resolved {{when .ResolvedAt}}{{end}}</span></div>
{{with .Updates}}      <ol class="updates">
{{range .}}        <li><span class="when">{{when .At}}</span><span><b>{{.Status}}</b>{{if .Body}} · {{.Body}}{{end}}</span></li>
{{end}}      </ol>
{{end}}    </article>
{{end}}  </div>
</section>{{end}}

<section class="panel">
  <div class="panel-head"><h2 class="panel-title">Services</h2><span class="aside">Last 90 days</span></div>
  <div class="panel-body">
{{if .Checks}}    <table>
      <thead><tr><th>State</th><th>Service</th><th class="bars-cell">Daily uptime</th><th class="num hide-sm">Uptime</th></tr></thead>
      <tbody>
{{range .Checks}}        <tr>
          <td><span class="state {{.State}}">{{if eq .State "operational"}}<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><path d="m9 12 2 2 4-4"/></svg>{{else if eq .State "degraded"}}<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>{{else if eq .State "outage"}}<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><path d="m15 9-6 6"/><path d="m9 9 6 6"/></svg>{{else}}<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><path d="M8 12h8"/></svg>{{end}}{{stateLabel .State}}</span></td>
          <td><div class="svc">{{.Name}}</div>{{if .URL}}<div class="url">{{.URL}}</div>{{end}}</td>
          <td class="bars-cell"><div class="bars" role="img" aria-label="Daily uptime for the last 90 days">{{range .Days}}<span class="{{.Level}}" title="{{day .Day}}: {{if .Uptime}}{{pct .Uptime}} up, {{end}}{{levelLabel .Level}}"></span>{{end}}</div><div class="axis"><span class="d90">90 days ago</span><span class="d30">30 days ago</span><span>Today</span></div></td>
          <td class="num hide-sm">{{pct .Uptime90}}</td>
        </tr>
{{end}}      </tbody>
    </table>
    <div class="legend"><span><i class="ok"></i>No downtime</span><span><i class="minor"></i>Some downtime</span><span><i class="major"></i>Under 99% up</span><span><i></i>No data</span></div>
{{else}}    <div class="empty">No services are listed on this page yet.</div>
{{end}}  </div>
</section>

<section class="panel">
  <div class="panel-head"><h2 class="panel-title">Downtime</h2><span class="aside">Last 7 days</span></div>
  <div class="panel-body">
{{if .Incidents}}    <table>
      <thead><tr><th>Service</th><th>Started</th><th class="hide-sm">Ended</th><th class="num">Duration</th></tr></thead>
      <tbody>
{{range .Incidents}}        <tr>
          <td><span class="svc">{{.Check}}</span> {{if .Ongoing}}<span class="tag live">Ongoing</span>{{end}}</td>
          <td>{{when .StartedAt}}</td>
          <td class="hide-sm">{{if .Ongoing}}<span class="url">still down</span>{{else}}{{whenp .EndedAt}}{{end}}</td>
          <td class="num">{{dur .DurationS}}{{if .Ongoing}} so far{{end}}</td>
        </tr>
{{end}}      </tbody>
    </table>
{{else}}    <div class="empty">No downtime in the last 7 days.</div>
{{end}}  </div>
</section>
</main>

<footer>
  <span>Times are in your time zone. This page refreshes every minute · <a href="/status.json">JSON</a></span>
  <span class="brand"><img src="/favicon.svg" alt="" width="18" height="18">Monitored by Visionary</span>
</footer>
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
