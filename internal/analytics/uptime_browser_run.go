package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// A browser run plays a check's steps in a fresh tab and records how each
// went, what the page logged and which requests failed, the page's Web
// Vitals, and a screenshot when a step fails.

const (
	browserAssertWait  = 5 * time.Second
	browserPollEvery   = 100 * time.Millisecond
	browserMaxShot     = 300 << 10
	browserMaxEntries  = 50
	browserMaxEntryLen = 500
)

// stepResult is how one step went.
type stepResult struct {
	Action     string  `json:"action"`
	Label      string  `json:"label"`
	Status     string  `json:"status"` // passed, failed, skipped
	DurationMs float64 `json:"duration_ms"`
	Error      string  `json:"error,omitempty"`
}

type netFailure struct {
	URL    string `json:"url"`
	Status int64  `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	Type   string `json:"type,omitempty"`
}

// browserRun is the detail of one run, beside its uptime_results row.
type browserRun struct {
	Steps      []stepResult `json:"steps"`
	FailedStep int          `json:"failed_step"` // -1 when every step passed
	Console    []string     `json:"console"`
	Network    []netFailure `json:"network"`
	Screenshot []byte       `json:"-"`
	Final      []byte       `json:"-"`
	TraceID    string       `json:"trace_id,omitempty"`
	Warning    string       `json:"warning,omitempty"`
}

// vitalsScript records LCP and CLS on every page the run opens.
const vitalsScript = `(() => {
  const v = { lcp: null, cls: 0 };
  Object.defineProperty(window, "__visionaryVitals", { value: v, enumerable: false });
  try { new PerformanceObserver((l) => { for (const e of l.getEntries()) v.lcp = e.renderTime || e.loadTime || e.startTime; }).observe({ type: "largest-contentful-paint", buffered: true }); } catch (e) {}
  try { new PerformanceObserver((l) => { for (const e of l.getEntries()) if (!e.hadRecentInput) v.cls += e.value; }).observe({ type: "layout-shift", buffered: true }); } catch (e) {}
})();`

type vitals struct {
	LCP  *float64 `json:"lcp"`
	CLS  *float64 `json:"cls"`
	TTFB *float64 `json:"ttfb"`
}

// runState is what the event listeners collect while the steps play.
type runState struct {
	mu      sync.Mutex
	console []string
	network []netFailure
	urls    map[network.RequestID]string
	navs    int
	mask    func(string) string
	touch   bool
	// navigations seen when the previous step began
	lastDoc int
}

func (st *runState) addConsole(s string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.console) < browserMaxEntries {
		st.console = append(st.console, clip(st.mask(s), browserMaxEntryLen))
	}
}

func (st *runState) addNetwork(f netFailure) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.network) < browserMaxEntries {
		f.URL = clip(st.mask(redactURL(f.URL)), browserMaxEntryLen)
		f.Error = clip(st.mask(f.Error), browserMaxEntryLen)
		st.network = append(st.network, f)
	}
}

func (st *runState) navCount() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.navs
}

// resolveSecrets fills {{secret.NAME}} in a step's value.
func resolveSecrets(v string, secrets map[string]string) string {
	for k, s := range secrets {
		v = strings.ReplaceAll(v, "{{secret."+k+"}}", s)
	}
	return v
}

// masker replaces secret values with the mask in anything a run records.
func masker(secrets map[string]string) func(string) string {
	var vals []string
	for _, v := range secrets {
		if len(v) >= 3 {
			vals = append(vals, v)
		}
	}
	return func(s string) string {
		for _, v := range vals {
			s = strings.ReplaceAll(s, v, uptimeMask)
			if e := url.QueryEscape(v); e != v {
				s = strings.ReplaceAll(s, e, uptimeMask)
			}
		}
		return s
	}
}

// runBrowserCheck plays c in browser b. guard, when set, is the proxy every
// request goes through.
func runBrowserCheck(ctx context.Context, b context.Context, c UptimeCheck, guard *guardProxy) (res uptimeResult, run browserRun) {
	start := time.Now()
	run.FailedStep = -1
	defer func() {
		res.CreatedAt = start.UTC().Format(tsLayout)
		res.DurationMs = float64(time.Since(start).Microseconds()) / 1000
		if res.OK {
			if why := c.overLimit(res); why != "" {
				res.OK, res.Error = false, why
			}
		}
		if res.OK {
			run.Warning = c.warnings(res)
			res.Degraded = run.Warning != ""
		}
	}()
	mask := masker(c.Secrets)
	dev := c.device()
	st := &runState{urls: map[network.RequestID]string{}, mask: mask, touch: dev.Mobile}
	traceID, spanID := newTraceIDs()
	patterns := c.tracePatterns()
	if len(patterns) > 0 {
		run.TraceID = traceID
	}

	proxy := ""
	if guard != nil {
		proxy = guard.addr()
	}
	tab, closeTab := newTab(b, proxy)
	defer closeTab()

	setup := func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := dev.emulate(ctx); err != nil {
			return err
		}
		if _, err := chromedp.Call(ctx, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: vitalsScript}); err != nil {
			return err
		}
		if len(patterns) > 0 {
			_, err := chromedp.Call(ctx, fetch.Enable, fetch.EnableParams{Patterns: patterns})
			return err
		}
		return nil
	}
	// The tab's first action opens it and ties it to tab, not to the run's
	// timeout, so a timed-out run can still be photographed.
	if err := chromedp.Do(tab); err != nil {
		res.Error = "the browser did not open a tab: " + clip(err.Error(), 200)
		return res, run
	}
	listen, stopListening := context.WithCancel(tab)
	var wg sync.WaitGroup
	collect(listen, &wg, st)
	if len(patterns) > 0 {
		propagateTrace(listen, tab, &wg, "00-"+traceID+"-"+spanID+"-01")
	}
	if err := setup(tab); err != nil {
		stopListening()
		wg.Wait()
		res.Error = "the browser did not set up the page: " + clip(err.Error(), 200)
		return res, run
	}

	runCtx, cancel := context.WithTimeout(tab, time.Duration(c.TimeoutMs)*time.Millisecond)
	defer cancel()
	var failure error
	for i, step := range c.Steps {
		sr := stepResult{Action: step.Action, Label: step.describe(), Status: "passed"}
		if failure != nil {
			sr.Status = "skipped"
			run.Steps = append(run.Steps, sr)
			continue
		}
		t0 := time.Now()
		status, err := playStep(runCtx, step, c.Secrets, st, guard)
		sr.DurationMs = float64(time.Since(t0).Microseconds()) / 1000
		if status > 0 {
			res.Status = int(status)
		}
		if err == nil && step.MaxMs > 0 && sr.DurationMs > float64(step.MaxMs) {
			err = fmt.Errorf("took %s, over its %s limit", msText(sr.DurationMs), msText(float64(step.MaxMs)))
		}
		if err != nil {
			if runCtx.Err() != nil && !errors.Is(err, errBlockedAddress) {
				err = fmt.Errorf("timed out after %s", time.Duration(c.TimeoutMs)*time.Millisecond)
			}
			sr.Status, sr.Error = "failed", clip(mask(err.Error()), 300)
			failure = err
			run.FailedStep = i
		}
		run.Steps = append(run.Steps, sr)
	}

	after, done := context.WithTimeout(tab, 10*time.Second)
	defer done()
	if v, err := chromedp.Run(after, chromedp.Evaluate[vitals](`(() => {
  const v = window.__visionaryVitals || {};
  const n = performance.getEntriesByType("navigation")[0];
  return { lcp: v.lcp ?? null, cls: v.lcp == null && !v.cls ? null : v.cls, ttfb: n && n.responseStart > 0 ? n.responseStart : null };
})()`)); err == nil {
		res.LCPMs, res.CLS, res.TTFBMs = roundMs(v.LCP), v.CLS, roundMs(v.TTFB)
	}
	if failure != nil {
		run.Screenshot = screenshot(after, dev.Scale)
	} else if c.FinalScreenshot {
		run.Final = screenshot(after, dev.Scale)
	}
	if len(patterns) > 0 {
		_, _ = chromedp.Call(after, fetch.Disable, cdp.Empty{})
	}
	stopListening()
	wg.Wait()
	st.mu.Lock()
	run.Console, run.Network = st.console, st.network
	st.mu.Unlock()
	if run.Console == nil {
		run.Console = []string{}
	}
	if run.Network == nil {
		run.Network = []netFailure{}
	}

	if failure != nil {
		res.Error = clip(fmt.Sprintf("step %d (%s): %s", run.FailedStep+1, c.Steps[run.FailedStep].describe(), run.Steps[run.FailedStep].Error), 300)
		res.blocked = errors.Is(failure, errBlockedAddress)
		return res, run
	}
	res.OK = true
	return res, run
}

func roundMs(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := float64(int64(*p*10)) / 10
	return &v
}

// collect listens to the tab's console and network until ctx ends.
func collect(ctx context.Context, wg *sync.WaitGroup, st *runState) {
	console := chromedp.Console(ctx)
	sent := chromedp.Events(ctx, network.RequestWillBeSent)
	failed := chromedp.Events(ctx, network.LoadingFailed)
	received := chromedp.Events(ctx, network.ResponseReceived)
	navigated := chromedp.Events(ctx, page.FrameNavigated)
	wg.Add(5)
	go func() {
		defer wg.Done()
		for m, err := range console {
			if err != nil {
				return
			}
			if m.Type == chromedp.ConsoleError || m.IsException() {
				st.addConsole(m.String())
			}
		}
	}()
	go func() {
		defer wg.Done()
		for ev, err := range sent {
			if err != nil {
				return
			}
			if ev.Request != nil {
				st.mu.Lock()
				if len(st.urls) < 2000 {
					st.urls[ev.RequestID] = ev.Request.URL
				}
				st.mu.Unlock()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for ev, err := range failed {
			if err != nil {
				return
			}
			if ev.Canceled {
				continue
			}
			st.mu.Lock()
			u := st.urls[ev.RequestID]
			st.mu.Unlock()
			reason := ev.ErrorText
			if ev.BlockedReason != "" {
				reason += " (" + string(ev.BlockedReason) + ")"
			}
			st.addNetwork(netFailure{URL: u, Error: reason, Type: string(ev.Type)})
		}
	}()
	go func() {
		defer wg.Done()
		for ev, err := range received {
			if err != nil {
				return
			}
			if ev.Response != nil && ev.Response.Status >= 400 {
				st.addNetwork(netFailure{URL: ev.Response.URL, Status: ev.Response.Status, Type: string(ev.Type)})
			}
		}
	}()
	go func() {
		defer wg.Done()
		for ev, err := range navigated {
			if err != nil {
				return
			}
			if ev.Frame != nil && ev.Frame.ParentID == "" {
				st.mu.Lock()
				st.navs++
				st.mu.Unlock()
			}
		}
	}()
}

// screenshot takes a JPEG of the viewport, lowering the quality until it
// fits the size cap; nil when it never does. A device with a pixel ratio
// above 1 is captured at CSS size.
func screenshot(ctx context.Context, scale float64) []byte {
	p := page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatJpeg}
	if scale > 1 {
		if m, err := chromedp.Call(ctx, page.GetLayoutMetrics, cdp.Empty{}); err == nil && m.CSSVisualViewport != nil {
			v := m.CSSVisualViewport
			p.Clip = &page.Viewport{X: v.PageX, Y: v.PageY, Width: v.ClientWidth, Height: v.ClientHeight, Scale: 1 / scale}
		}
	}
	for _, q := range []int64{70, 50, 30} {
		p.Quality = &q
		res, err := chromedp.Call(ctx, page.CaptureScreenshot, p)
		if err != nil {
			return nil
		}
		if len(res.Data) <= browserMaxShot {
			return res.Data
		}
	}
	return nil
}

// jsString quotes s for a script.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// waitJS evaluates expr until it returns a truthy value, across page loads,
// for at most wait (or until ctx ends). It returns the last value.
func waitJS[T any](ctx context.Context, expr string, wait time.Duration, ok func(T) bool) (T, error) {
	var last T
	var lastErr error
	deadline := time.Now().Add(wait)
	for {
		v, err := chromedp.Run(ctx, chromedp.Evaluate[T](expr))
		if err == nil && ok(v) {
			return v, nil
		}
		last, lastErr = v, err
		if ctx.Err() != nil {
			return last, ctx.Err()
		}
		if time.Now().After(deadline) {
			if lastErr != nil && !isTransientEval(lastErr) {
				return last, lastErr
			}
			return last, errStepTimeout
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(browserPollEvery):
		}
	}
}

var errStepTimeout = errors.New("timed out")

// isTransientEval is an evaluation that failed because the page was between
// documents.
func isTransientEval(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "context") || strings.Contains(msg, "navigat") || strings.Contains(msg, "Execution") || strings.Contains(msg, "-32000")
}

// findJS locates the element a step means and scrolls it into view; it
// returns the center of the element, or null.
func findJS(s BrowserStep) string {
	finder := `document.querySelector(` + jsString(s.Selector) + `)`
	if s.Selector == "" {
		finder = `(() => {
  const want = ` + jsString(strings.TrimSpace(s.Text)) + `.toLowerCase();
  const seen = (e) => { const r = e.getBoundingClientRect(); const cs = getComputedStyle(e); return r.width > 0 && r.height > 0 && cs.visibility !== "hidden" && cs.display !== "none"; };
  const text = (e) => ((e.innerText || e.value || e.getAttribute("aria-label") || "") + "").trim().replace(/\s+/g, " ").toLowerCase();
  const pick = (sel) => { const all = [...document.querySelectorAll(sel)].filter(seen); return all.find((e) => text(e) === want) || all.find((e) => text(e).includes(want)); };
  // Controls first, so a button wins over a heading with the same words.
  return pick("button, a, [role=button], [role=link], [role=tab], [role=menuitem], input[type=submit], input[type=button], summary, label") ||
    pick("option, li, span, div, p, td, h1, h2, h3, h4") || null;
})()`
	}
	return `(() => {
  const e = ` + finder + `;
  if (!e) return null;
  const cs = getComputedStyle(e);
  e.scrollIntoView({ block: "center", inline: "center" });
  const r = e.getBoundingClientRect();
  if (r.width === 0 || r.height === 0 || cs.visibility === "hidden" || cs.display === "none") return null;
  return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
})()`
}

type elementAt struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// stepKeys maps the key names a step may use to what chromedp sends.
var stepKeys = map[string]string{
	"enter": kb.Enter, "tab": kb.Tab, "escape": kb.Escape, "backspace": kb.Backspace, "delete": kb.Delete, "space": " ",
	"arrowdown": kb.ArrowDown, "arrowup": kb.ArrowUp, "arrowleft": kb.ArrowLeft, "arrowright": kb.ArrowRight,
	"home": kb.Home, "end": kb.End, "pagedown": kb.PageDown, "pageup": kb.PageUp,
}

// playStep runs one step. It returns the HTTP status of a page it opened.
func playStep(ctx context.Context, s BrowserStep, secrets map[string]string, st *runState, guard *guardProxy) (int64, error) {
	navBefore := st.navCount()
	defer func() {
		st.mu.Lock()
		st.lastDoc = navBefore
		st.mu.Unlock()
	}()
	what := func() string {
		if s.Selector != "" {
			return s.Selector
		}
		return "“" + s.Text + "”"
	}
	notFound := func(err error) error {
		if errors.Is(err, errStepTimeout) {
			return fmt.Errorf("%s was not found or not visible", what())
		}
		return err
	}
	switch s.Action {
	case "goto":
		target := resolveSecrets(s.Value, secrets)
		if guard != nil {
			u, err := url.Parse(target)
			if err != nil {
				return 0, errors.New("invalid URL")
			}
			if err := precheckURL(u); err != nil {
				return 0, err
			}
		}
		resp, err := chromedp.Run(ctx, chromedp.NavigateResponse(target))
		if err != nil {
			if guard != nil {
				if b := guard.refused(); len(b) > 0 {
					return 0, fmt.Errorf("%w%s", errBlockedAddress, strings.TrimPrefix(b[0], errBlockedAddress.Error()))
				}
			}
			return 0, fmt.Errorf("the page did not load: %s", navError(err))
		}
		if resp != nil && resp.Status >= 400 {
			return resp.Status, fmt.Errorf("the page answered HTTP %d", resp.Status)
		}
		if resp != nil {
			return resp.Status, nil
		}
		return 0, nil
	case "click":
		p, err := waitJS[*elementAt](ctx, findJS(s), remaining(ctx), func(p *elementAt) bool { return p != nil })
		if err != nil {
			return 0, notFound(err)
		}
		if st.touch {
			return 0, chromedp.Do(ctx, chromedp.TapXY(p.X, p.Y))
		}
		return 0, chromedp.Do(ctx, chromedp.MouseClickXY(p.X, p.Y))
	case "type":
		focus := `(() => { const e = document.querySelector(` + jsString(s.Selector) + `); if (!e) return false; const r = e.getBoundingClientRect(); if (!r.width || !r.height) return false;
  e.scrollIntoView({ block: "center" }); e.focus(); if ("value" in e) { e.value = ""; e.dispatchEvent(new Event("input", { bubbles: true })); } else if (e.isContentEditable) { e.textContent = ""; } return document.activeElement === e; })()`
		if _, err := waitJS[bool](ctx, focus, remaining(ctx), func(b bool) bool { return b }); err != nil {
			return 0, notFound(err)
		}
		return 0, chromedp.Do(ctx, chromedp.KeyEvent(resolveSecrets(s.Value, secrets)))
	case "press":
		k, ok := stepKeys[strings.ToLower(s.Value)]
		if !ok {
			k = s.Value
		}
		return 0, chromedp.Do(ctx, chromedp.KeyEvent(k))
	case "select":
		js := `(() => { const e = document.querySelector(` + jsString(s.Selector) + `); if (!e || !e.options) return "missing";
  const want = ` + jsString(resolveSecrets(s.Value, secrets)) + `;
  const o = [...e.options].find((o) => o.value === want) || [...e.options].find((o) => o.text.trim() === want);
  if (!o) return "no option";
  e.value = o.value; e.dispatchEvent(new Event("input", { bubbles: true })); e.dispatchEvent(new Event("change", { bubbles: true })); return "ok"; })()`
		v, err := waitJS[string](ctx, js, remaining(ctx), func(v string) bool { return v != "missing" })
		if err != nil {
			return 0, notFound(err)
		}
		if v != "ok" {
			return 0, fmt.Errorf("%s has no option %q", s.Selector, s.Value)
		}
		return 0, nil
	case "wait_selector":
		_, err := waitJS[*elementAt](ctx, findJS(s), remaining(ctx), func(p *elementAt) bool { return p != nil })
		return 0, notFound(err)
	case "wait_text":
		_, err := waitJS[bool](ctx, textJS(s, true), remaining(ctx), func(b bool) bool { return b })
		if errors.Is(err, errStepTimeout) {
			return 0, fmt.Errorf("text “%s” did not appear", s.Text)
		}
		return 0, err
	case "wait_navigation":
		st.mu.Lock()
		since := st.lastDoc
		st.mu.Unlock()
		for st.navCount() <= since {
			select {
			case <-ctx.Done():
				return 0, errors.New("no navigation happened")
			case <-time.After(browserPollEvery):
			}
		}
		_, err := waitJS[bool](ctx, `document.readyState === "complete" && location.href.includes(`+jsString(s.Value)+`)`, remaining(ctx), func(b bool) bool { return b })
		if errors.Is(err, errStepTimeout) {
			return 0, fmt.Errorf("the page did not finish loading at a URL containing %q", s.Value)
		}
		return 0, err
	case "assert_text", "assert_no_text":
		want := s.Action == "assert_text"
		_, err := waitJS[bool](ctx, textJS(s, want), min(browserAssertWait, remaining(ctx)), func(b bool) bool { return b })
		if errors.Is(err, errStepTimeout) {
			if want {
				return 0, fmt.Errorf("text “%s” is not on the page", s.Text)
			}
			return 0, fmt.Errorf("text “%s” is on the page", s.Text)
		}
		return 0, err
	case "assert_url":
		href, err := waitJS[string](ctx, `location.href`, min(browserAssertWait, remaining(ctx)), func(h string) bool { return strings.Contains(h, s.Value) })
		if errors.Is(err, errStepTimeout) {
			return 0, fmt.Errorf("the URL %s does not contain %q", redactURL(href), s.Value)
		}
		return 0, err
	case "assert_count":
		n, err := waitJS[int](ctx, `document.querySelectorAll(`+jsString(s.Selector)+`).length`, min(browserAssertWait, remaining(ctx)), func(n int) bool { return countMatches(s.Op, n, s.Count) })
		if errors.Is(err, errStepTimeout) {
			return 0, fmt.Errorf("found %d of %s, expected %s %d", n, s.Selector, opWord(s.Op), s.Count)
		}
		return 0, err
	case "wait":
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Duration(s.Ms) * time.Millisecond):
			return 0, nil
		}
	case "script":
		body := s.Value
		if !strings.Contains(body, "return") {
			body = "return (" + body + ");"
		}
		v, err := chromedp.Run(ctx, chromedp.Evaluate[json.RawMessage](`(async () => { `+body+` })()`, chromedp.EvalAwaitPromise))
		if err != nil {
			return 0, fmt.Errorf("script failed: %s", clip(err.Error(), 200))
		}
		switch strings.TrimSpace(string(v)) {
		case "", "false", "null", "0", `""`, "undefined":
			return 0, fmt.Errorf("script returned %s", strings.TrimSpace(string(v)))
		}
		return 0, nil
	}
	return 0, fmt.Errorf("unknown step %q", s.Action)
}

func textJS(s BrowserStep, present bool) string {
	scope := `document.body`
	if s.Selector != "" {
		scope = `document.querySelector(` + jsString(s.Selector) + `)`
	}
	has := `((` + scope + `) ? (` + scope + `).innerText : "").includes(` + jsString(s.Text) + `)`
	if present {
		return has
	}
	return `!` + has
}

func remaining(ctx context.Context) time.Duration {
	if d, ok := ctx.Deadline(); ok {
		return time.Until(d)
	}
	return 30 * time.Second
}

func countMatches(op string, n, want int) bool {
	switch op {
	case "gte":
		return n >= want
	case "lte":
		return n <= want
	}
	return n == want
}

func opWord(op string) string {
	switch op {
	case "gte":
		return "at least"
	case "lte":
		return "at most"
	}
	return "exactly"
}

// navError keeps Chrome's net:: code, which says what went wrong.
func navError(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "net::"); i >= 0 {
		return msg[i:]
	}
	return clip(msg, 200)
}
