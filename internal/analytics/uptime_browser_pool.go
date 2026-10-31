package analytics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Browser checks drive one headless Chrome (or Chromium, or Edge) process,
// started on the first run and closed after a while without any. Each run
// gets a fresh incognito context, so cookies and storage never leak between
// runs or checks. When no browser can be found, browser checks wait instead
// of failing.

const (
	browserIdleClose   = 10 * time.Minute
	browserRetryLaunch = time.Minute
	browserViewportW   = 1280
	browserViewportH   = 800
)

var errNoBrowser = errors.New("no Chrome or Chromium found on the server")

// browserHelp says how to get a browser, for the dashboard and the API.
const browserHelp = "Install Chrome or Chromium on the server (apt install chromium, dnf install chromium, apk add chromium, or Google Chrome from google.com/chrome), " +
	"or point Settings → Server → Other → Browser for browser checks (or VISIONARY_CHROME) at its executable. " +
	"The default Docker image has no browser; build the browser variant with docker build --target browser."

// browserStatus is what the dashboard shows about the browser.
type browserStatus struct {
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Source    string `json:"source,omitempty"` // setting, env or found
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
	Help      string `json:"help,omitempty"`
	Workers   int    `json:"workers"`
}

// findBrowser looks in the setting, then VISIONARY_CHROME, then the usual
// install locations and PATH. A path that is set but missing is an error
// rather than a fallback, so a typo does not go unnoticed.
func findBrowser(setting string) (path, source string, err error) {
	for _, c := range []struct{ v, src string }{{setting, "setting"}, {os.Getenv("VISIONARY_CHROME"), "env"}} {
		v := strings.TrimSpace(c.v)
		if v == "" {
			continue
		}
		if p, err := exec.LookPath(v); err == nil {
			return p, c.src, nil
		}
		return "", c.src, fmt.Errorf("browser not found at %s", v)
	}
	for _, name := range browserLocations() {
		if p, err := exec.LookPath(name); err == nil {
			return p, "found", nil
		}
	}
	return "", "", errNoBrowser
}

func browserLocations() []string {
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		return []string{
			"chrome", "chrome.exe",
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			filepath.Join(local, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(local, `Chromium\Application\chrome.exe`),
			"msedge", "msedge.exe",
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		}
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"chromium", "google-chrome",
		}
	}
	return []string{
		"headless_shell", "headless-shell", "chromium", "chromium-browser",
		"google-chrome", "google-chrome-stable", "chrome",
		"/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/lib/chromium/chromium",
		"/snap/bin/chromium", "/headless-shell/headless-shell", "microsoft-edge",
	}
}

// inContainer is true in Docker and Podman, where Chrome's sandbox usually
// cannot start.
func inContainer() bool {
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

type browserPool struct {
	s   *Server
	sem chan struct{}

	mu        sync.Mutex
	path      string
	version   string
	cancel    context.CancelFunc // ends the browser process
	browser   context.Context
	launchErr error
	launchAt  time.Time
	active    int
	idle      *time.Timer
}

func newBrowserPool(s *Server, workers int) *browserPool {
	return &browserPool{s: s, sem: make(chan struct{}, max(1, workers))}
}

func (p *browserPool) setting() string {
	return p.s.conf("uptime_chrome_path")
}

func (p *browserPool) status() browserStatus {
	st := browserStatus{Workers: cap(p.sem)}
	path, src, err := findBrowser(p.setting())
	st.Path, st.Source = path, src
	if err != nil {
		st.Error, st.Help = err.Error(), browserHelp
		return st
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st.Version = p.version
	if p.launchErr != nil && p.path == path {
		st.Error, st.Help = "the browser did not start: "+p.launchErr.Error(), browserHelp
		return st
	}
	st.Available = true
	return st
}

// available is whether a run could start now: a browser exists and did not
// just fail to launch.
func (p *browserPool) available() bool {
	path, _, err := findBrowser(p.setting())
	if err != nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.launchErr == nil || p.path != path || time.Since(p.launchAt) >= browserRetryLaunch
}

// acquire waits for a free slot and returns the browser context to open a
// tab in; release must follow.
func (p *browserPool) acquire(ctx context.Context) (context.Context, error) {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idle != nil {
		p.idle.Stop()
	}
	b, err := p.ensureLocked()
	if err != nil {
		<-p.sem
		return nil, err
	}
	p.active++
	return b, nil
}

func (p *browserPool) release() {
	p.mu.Lock()
	p.active--
	if p.active == 0 && p.browser != nil {
		p.idle = time.AfterFunc(browserIdleClose, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.active == 0 {
				p.closeLocked()
			}
		})
	}
	p.mu.Unlock()
	<-p.sem
}

func (p *browserPool) ensureLocked() (context.Context, error) {
	path, _, err := findBrowser(p.setting())
	if err != nil {
		return nil, err
	}
	if p.browser != nil && p.browser.Err() == nil && p.path == path {
		if c := chromedp.FromContext(p.browser); c != nil && c.Browser != nil {
			select {
			case <-c.Browser.LostConnection:
			default:
				return p.browser, nil
			}
		}
	}
	p.closeLocked()
	if p.launchErr != nil && p.path == path && time.Since(p.launchAt) < browserRetryLaunch {
		return nil, p.launchErr
	}
	p.path, p.launchAt = path, time.Now()
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(path),
		chromedp.WindowSize(browserViewportW, browserViewportH),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-default-browser-check", true),
		// Cross-site iframes stay in the page's process, so frame steps reach them.
		chromedp.Flag("disable-features", "Translate,BlinkGenPropertyTrees,site-per-process,IsolateOrigins"),
		chromedp.Flag("disable-site-isolation-trials", true),
		// Keeps WebRTC from reaching around the address guard's proxy.
		chromedp.Flag("force-webrtc-ip-handling-policy", "disable_non_proxied_udp"),
	)
	if runtime.GOOS == "linux" && (os.Geteuid() == 0 || inContainer()) {
		opts = append(opts, chromedp.NoSandbox)
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	bctx, cancelBrowser := chromedp.NewContext(alloc)
	// The first Do binds the browser's life to the context it is given, so it
	// gets bctx itself and a timer instead of a timeout context.
	started := make(chan error, 1)
	go func() { started <- chromedp.Do(bctx) }()
	select {
	case err = <-started:
	case <-time.After(30 * time.Second):
		cancelBrowser()
		err = errors.New("timed out starting")
	}
	if err == nil {
		if v, verr := chromedp.CallBrowser(bctx, browser.GetVersion, cdp.Empty{}); verr == nil {
			p.version = v.Product
		}
	}
	if err != nil {
		cancelBrowser()
		cancelAlloc()
		p.launchErr = err
		p.s.logger.Warn("browser for uptime checks did not start", "path", path, "err", err)
		return nil, err
	}
	p.launchErr = nil
	p.browser = bctx
	p.cancel = func() {
		cancelBrowser()
		cancelAlloc()
	}
	return bctx, nil
}

func (p *browserPool) closeLocked() {
	if p.cancel != nil {
		p.cancel()
	}
	p.cancel, p.browser = nil, nil
}

// kill ends the browser process at once, for a run that stopped answering.
// Runs in other tabs fail and are recorded as such.
func (p *browserPool) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.browser != nil {
		if c := chromedp.FromContext(p.browser); c != nil && c.Browser != nil && c.Browser.Process() != nil {
			_ = c.Browser.Process().Kill()
		}
	}
	p.closeLocked()
}

func (p *browserPool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idle != nil {
		p.idle.Stop()
	}
	p.closeLocked()
}

// newTab opens an incognito context in the browser, behind the guard proxy
// when there is one.
func newTab(b context.Context, proxy string) (context.Context, context.CancelFunc) {
	return chromedp.NewContext(b, chromedp.WithNewBrowserContext(func(p *target.CreateBrowserContextParams) {
		if proxy != "" {
			p.ProxyServer = proxy
			// Chrome skips the proxy for loopback unless told not to.
			p.ProxyBypassList = "<-loopback>"
		}
	}))
}

// guardProxy is a small HTTP proxy on loopback that the browser sends all
// its traffic through, so the address guard sees every connection after DNS
// resolution, as it does for HTTP checks. One proxy serves one run, which
// lets it tell the run what it refused.
type guardProxy struct {
	ln  net.Listener
	srv *http.Server

	mu      sync.Mutex
	blocked []string
}

func startGuardProxy() (*guardProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	g := &guardProxy{ln: ln}
	g.srv = &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = g.srv.Serve(ln) }()
	return g, nil
}

func (g *guardProxy) addr() string { return "http://" + g.ln.Addr().String() }

func (g *guardProxy) close() { _ = g.srv.Close() }

func (g *guardProxy) refused() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.blocked...)
}

func (g *guardProxy) dial(ctx context.Context, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 15 * time.Second, Control: dialGuard}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil && errors.Is(err, errBlockedAddress) {
		g.mu.Lock()
		if len(g.blocked) < 20 {
			g.blocked = append(g.blocked, probeError(err, 0))
		}
		g.mu.Unlock()
	}
	return c, err
}

func (g *guardProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		g.tunnel(w, r)
		return
	}
	if r.URL.Host == "" {
		http.Error(w, "not a proxy request", http.StatusBadRequest)
		return
	}
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) { return g.dial(ctx, addr) }, DisableKeepAlives: true}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive", "Te", "Trailer", "Upgrade"} {
		out.Header.Del(h)
	}
	resp, err := tr.RoundTrip(out)
	if err != nil {
		http.Error(w, probeError(err, 0), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (g *guardProxy) tunnel(w http.ResponseWriter, r *http.Request) {
	up, err := g.dial(r.Context(), r.Host)
	if err != nil {
		http.Error(w, probeError(err, 0), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "tunnel not supported", http.StatusInternalServerError)
		return
	}
	down, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	_, _ = down.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	go func() {
		if buf.Reader.Buffered() > 0 {
			_, _ = io.CopyN(up, buf, int64(buf.Reader.Buffered()))
		}
		_, _ = io.Copy(up, down)
		up.Close()
	}()
	_, _ = io.Copy(down, up)
	down.Close()
}
