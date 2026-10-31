package analytics

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// The steps beyond clicking and checking: extract a value into a variable
// later steps use as {{var.NAME}}, hover, drag, upload a file stored with
// the check, work inside an iframe, set a cookie or web storage, and grant a
// permission. Steps inside a frame run in an isolated world of that frame,
// which sees its DOM but not the page's own scripts.

// BrowserFile is a small file an upload step sends. The API shows its size
// without its data; sending it back without data keeps the stored file.
type BrowserFile struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	Size int    `json:"size"`
	Data string `json:"data,omitempty"` // base64
}

const (
	browserMaxFiles     = 5
	browserMaxFile      = 256 << 10
	browserMaxFileTotal = 512 << 10
)

var (
	varRefRe      = regexp.MustCompile(`\{\{var\.([A-Za-z0-9_]+)\}\}`)
	cookieNameRe  = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,200}$")
	fileNameRe    = regexp.MustCompile(`^[^/\\:*?"<>|\x00-\x1f]{1,100}$`)
	browserPerms  = map[string][]string{"geolocation": {"geolocation"}, "notifications": {"notifications"}, "clipboard": {"clipboard-read", "clipboard-write"}}
	extractAttrRe = regexp.MustCompile(`^[A-Za-z_:][-A-Za-z0-9_:.]{0,99}$`)
)

// validateMore checks the fields of the newer step types.
func (b *BrowserStep) validateMore(files map[string]bool) error {
	if len(b.Name) > 200 || len(b.Attr) > 100 || len(b.Regex) > 500 || len(b.Target) > 500 || len(b.Scope) > 2048 {
		return errors.New("a field is too long")
	}
	switch b.Action {
	case "extract":
		if b.Selector == "" {
			return errors.New("selector is required")
		}
		if !secretNameRe.MatchString(b.Name) {
			return errors.New("name the variable with letters, digits and _")
		}
		if b.Attr == "" {
			b.Attr = "text"
		}
		if !extractAttrRe.MatchString(b.Attr) {
			return fmt.Errorf("attribute %q is not valid", b.Attr)
		}
		if b.Regex != "" {
			if _, err := regexp.Compile(b.Regex); err != nil {
				return fmt.Errorf("the regex does not compile: %v", err)
			}
		}
	case "hover":
		if b.Selector == "" && strings.TrimSpace(b.Text) == "" {
			return errors.New("give a CSS selector or the element's text")
		}
	case "drag":
		if b.Selector == "" || b.Target == "" {
			return errors.New("give the selector to drag from and the one to drop on")
		}
	case "upload":
		if b.Selector == "" {
			return errors.New("selector is required")
		}
		if !files[b.Name] {
			return fmt.Errorf("no file named %q is stored with the check", b.Name)
		}
	case "frame":
		// No selector goes back to the page itself.
	case "set_cookie":
		if !cookieNameRe.MatchString(b.Name) {
			return errors.New("the cookie needs a name of letters, digits and -_.")
		}
		if strings.ContainsAny(b.Value, ";\r\n") {
			return errors.New("a cookie value cannot hold ; or line breaks")
		}
		if s := strings.TrimSpace(b.Scope); s != "" && strings.Contains(s, "://") {
			if u, err := url.Parse(s); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
				return errors.New("the cookie's URL must be an http:// or https:// address")
			}
		}
	case "set_storage":
		if b.Scope == "" {
			b.Scope = "local"
		}
		if b.Scope != "local" && b.Scope != "session" {
			return errors.New("storage is local or session")
		}
		if strings.TrimSpace(b.Name) == "" {
			return errors.New("key is required")
		}
	case "grant_permission":
		if _, ok := browserPerms[b.Permission]; !ok {
			return errors.New("permission is geolocation, notifications or clipboard")
		}
		if b.Permission == "geolocation" && (b.Lat < -90 || b.Lat > 90 || b.Lon < -180 || b.Lon > 180) {
			return errors.New("latitude is -90 to 90 and longitude -180 to 180")
		}
		if b.Scope != "" {
			if _, err := normalizeOrigin(b.Scope); err != nil || strings.Contains(b.Scope, "*") {
				return errors.New("the permission's origin must look like https://app.example.com")
			}
		}
	}
	return nil
}

// refs are the variables a step uses.
func (b BrowserStep) refs() []string {
	var out []string
	for _, f := range []string{b.Selector, b.Text, b.Value, b.Target, b.Scope, b.Name} {
		for _, m := range varRefRe.FindAllStringSubmatch(f, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// normalizeFiles keeps the stored data of files sent back without it.
func (o *BrowserOptions) normalizeFiles(prev *UptimeCheck) error {
	if len(o.Files) > browserMaxFiles {
		return fmt.Errorf("at most %d files", browserMaxFiles)
	}
	total, seen := 0, map[string]bool{}
	for i := range o.Files {
		f := &o.Files[i]
		f.Name = strings.TrimSpace(f.Name)
		if !fileNameRe.MatchString(f.Name) || seen[f.Name] {
			return fmt.Errorf("file name %q is not valid or used twice", f.Name)
		}
		seen[f.Name] = true
		if f.Data == "" {
			found := false
			if prev != nil {
				for _, p := range prev.Files {
					if p.Name == f.Name && p.Data != "" {
						f.Data, found = p.Data, true
					}
				}
			}
			if !found {
				return fmt.Errorf("add file %s again", f.Name)
			}
		}
		raw, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			return fmt.Errorf("file %s is not base64", f.Name)
		}
		if len(raw) > browserMaxFile {
			return fmt.Errorf("file %s is over %d KB", f.Name, browserMaxFile>>10)
		}
		f.Size, total = len(raw), total+len(raw)
		if len(f.Type) > 100 {
			f.Type = ""
		}
	}
	if total > browserMaxFileTotal {
		return fmt.Errorf("files may hold %d KB in all", browserMaxFileTotal>>10)
	}
	if len(o.Files) == 0 {
		o.Files = nil
	}
	return nil
}

// resolve fills secrets and variables into the fields of a step.
func (st *runState) resolve(s BrowserStep, secrets map[string]string) BrowserStep {
	vars := func(v string) string {
		return varRefRe.ReplaceAllStringFunc(v, func(m string) string { return st.vars[varRefRe.FindStringSubmatch(m)[1]] })
	}
	s.Value = vars(resolveSecrets(s.Value, secrets))
	s.Selector, s.Text, s.Target, s.Scope, s.Name = vars(s.Selector), vars(s.Text), vars(s.Target), vars(resolveSecrets(s.Scope, secrets)), vars(s.Name)
	return s
}

// evalJS evaluates expr in the frame the run is in, or in the page when st
// is nil or in no frame.
func evalJS[T any](ctx context.Context, st *runState, expr string, opts ...chromedp.EvaluateOption) (T, error) {
	if st == nil || len(st.frames) == 0 {
		return chromedp.Run(ctx, chromedp.Evaluate[T](expr, opts...))
	}
	var zero T
	ids, err := st.frameContexts(ctx)
	if err != nil {
		return zero, err
	}
	id := ids[len(ids)-1]
	v, err := chromedp.Run(ctx, chromedp.Evaluate[T](expr, append(opts, inContext(id))...))
	if err != nil && isTransientEval(err) {
		st.frameCtx = nil // the frame navigated; find it again next time
	}
	return v, err
}

func inContext(id runtime.ExecutionContextID) chromedp.EvaluateOption {
	return func(p *runtime.EvaluateParams) { p.ContextID = id }
}

// frameContexts are the execution contexts of each frame of the chain, an
// isolated world in each, found again after a frame navigates.
func (st *runState) frameContexts(ctx context.Context) ([]runtime.ExecutionContextID, error) {
	if len(st.frameCtx) == len(st.frames) {
		return st.frameCtx, nil
	}
	var ids []runtime.ExecutionContextID
	for i, sel := range st.frames {
		opts := []chromedp.EvaluateOption{}
		if i > 0 {
			opts = append(opts, inContext(ids[i-1]))
		}
		obj, err := chromedp.Run(ctx, chromedp.Evaluate[*runtime.RemoteObject](`document.querySelector(`+jsString(sel)+`)`, opts...))
		if err != nil {
			return nil, err
		}
		if obj == nil || obj.ObjectID == "" {
			return nil, fmt.Errorf("frame %s was not found", sel)
		}
		desc, err := chromedp.Call(ctx, dom.DescribeNode, dom.DescribeNodeParams{ObjectID: obj.ObjectID})
		_, _ = chromedp.Call(ctx, runtime.ReleaseObject, runtime.ReleaseObjectParams{ObjectID: obj.ObjectID})
		if err != nil {
			return nil, err
		}
		if desc.Node == nil || desc.Node.FrameID == "" {
			return nil, fmt.Errorf("%s is not a frame", sel)
		}
		w, err := chromedp.Call(ctx, page.CreateIsolatedWorld, page.CreateIsolatedWorldParams{FrameID: desc.Node.FrameID, WorldName: "visionary"})
		if err != nil {
			return nil, fmt.Errorf("frame %s is not ready: %s", sel, clip(err.Error(), 100))
		}
		ids = append(ids, w.ExecutionContextID)
	}
	st.frameCtx = ids
	return ids, nil
}

// frameOffset is where the current frame's content starts in the viewport,
// to turn a point inside it into one the mouse can go to.
func (st *runState) frameOffset(ctx context.Context) (elementAt, error) {
	var off elementAt
	if len(st.frames) == 0 {
		return off, nil
	}
	ids, err := st.frameContexts(ctx)
	if err != nil {
		return off, err
	}
	for i, sel := range st.frames {
		opts := []chromedp.EvaluateOption{}
		if i > 0 {
			opts = append(opts, inContext(ids[i-1]))
		}
		p, err := chromedp.Run(ctx, chromedp.Evaluate[*elementAt](`(() => { const f = document.querySelector(`+jsString(sel)+`); if (!f) return null;
  const r = f.getBoundingClientRect(), cs = getComputedStyle(f);
  return { x: r.left + f.clientLeft + parseFloat(cs.paddingLeft), y: r.top + f.clientTop + parseFloat(cs.paddingTop) }; })()`, opts...))
		if err != nil {
			return off, err
		}
		if p == nil {
			return off, fmt.Errorf("frame %s was not found", sel)
		}
		off.X, off.Y = off.X+p.X, off.Y+p.Y
	}
	return off, nil
}

// locate waits for the element a step means and returns its center in the
// viewport.
func (st *runState) locate(ctx context.Context, s BrowserStep) (elementAt, error) {
	p, err := waitJS[*elementAt](ctx, st, findJS(s), remaining(ctx), func(p *elementAt) bool { return p != nil })
	if err != nil {
		return elementAt{}, err
	}
	off, err := st.frameOffset(ctx)
	if err != nil {
		return elementAt{}, err
	}
	return elementAt{X: p.X + off.X, Y: p.Y + off.Y}, nil
}

// pageURL is the address of the page itself.
func pageURL(ctx context.Context) (*url.URL, error) {
	href, err := evalJS[string](ctx, nil, `location.href`)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(href)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("open a page first")
	}
	return u, nil
}

// playMore runs the newer step types; detail is what the step found.
func playMore(ctx context.Context, s BrowserStep, st *runState, notFound func(error) error) (detail string, err error) {
	switch s.Action {
	case "extract":
		js := `(() => { const e = document.querySelector(` + jsString(s.Selector) + `); if (!e) return null; const a = ` + jsString(s.Attr) + `;
  if (a === "text") return ((e.innerText ?? e.textContent) || "").trim();
  if (a === "value") return "value" in e ? String(e.value) : null;
  const v = e.getAttribute(a); return v === null ? { missing: true } : v; })()`
		raw, err := waitJS[any](ctx, st, js, remaining(ctx), func(v any) bool { return v != nil })
		if err != nil {
			return "", notFound(err)
		}
		v, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("%s has no %s", s.Selector, s.Attr)
		}
		if s.Regex != "" {
			m := regexp.MustCompile(s.Regex).FindStringSubmatch(v)
			if m == nil {
				return "", fmt.Errorf("%q does not match the regex", clip(v, 80))
			}
			v = m[0]
			if len(m) > 1 {
				v = m[1]
			}
		}
		st.vars[s.Name] = v
		return s.Name + " = " + clip(v, 80), nil
	case "hover":
		p, err := st.locate(ctx, s)
		if err != nil {
			return "", notFound(err)
		}
		return "", chromedp.Do(ctx, chromedp.MouseEvent(chromedp.MouseMoved, p.X, p.Y))
	case "drag":
		from, err := st.locate(ctx, s)
		if err != nil {
			return "", notFound(err)
		}
		to, err := st.locate(ctx, BrowserStep{Selector: s.Target})
		if err != nil {
			if errors.Is(err, errStepTimeout) {
				return "", fmt.Errorf("%s was not found or not visible", s.Target)
			}
			return "", err
		}
		return "", chromedp.Do(ctx, chromedp.DragAndDropXY(from.X, from.Y, to.X, to.Y))
	case "upload":
		f, ok := st.files[s.Name]
		if !ok {
			return "", fmt.Errorf("no file named %q", s.Name)
		}
		path, err := st.writeFile(f)
		if err != nil {
			return "", err
		}
		var obj *runtime.RemoteObject
		_, err = waitJS[bool](ctx, st, `!!document.querySelector(`+jsString(s.Selector)+`)`, remaining(ctx), func(b bool) bool { return b })
		if err == nil {
			obj, err = evalJS[*runtime.RemoteObject](ctx, st, `document.querySelector(`+jsString(s.Selector)+`)`)
		}
		if err != nil {
			return "", notFound(err)
		}
		if obj == nil || obj.ObjectID == "" {
			return "", fmt.Errorf("%s was not found", s.Selector)
		}
		defer func() {
			_, _ = chromedp.Call(ctx, runtime.ReleaseObject, runtime.ReleaseObjectParams{ObjectID: obj.ObjectID})
		}()
		if _, err := chromedp.Call(ctx, dom.SetFileInputFiles, dom.SetFileInputFilesParams{Files: []string{path}, ObjectID: obj.ObjectID}); err != nil {
			return "", fmt.Errorf("%s did not take the file: %s", s.Selector, clip(err.Error(), 120))
		}
		return "", nil
	case "frame":
		if s.Selector == "" {
			st.frames, st.frameCtx = nil, nil
			return "", nil
		}
		st.frames, st.frameCtx = append(append([]string(nil), st.frames...), s.Selector), nil
		for {
			_, err := st.frameContexts(ctx)
			if err == nil {
				return "", nil
			}
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("frame %s did not load: %s", s.Selector, clip(err.Error(), 120))
			case <-time.After(browserPollEvery):
			}
		}
	case "set_cookie":
		p := network.SetCookieParams{Name: s.Name, Value: s.Value}
		switch scope := strings.TrimSpace(s.Scope); {
		case strings.Contains(scope, "://"):
			p.URL = scope
		case scope != "":
			p.Domain, p.Path = scope, "/"
		default:
			u, err := pageURL(ctx)
			if err != nil {
				return "", err
			}
			p.URL = u.String()
		}
		_, err := chromedp.Call(ctx, network.SetCookie, p)
		return "", err
	case "set_storage":
		js := `(() => { ` + s.Scope + `Storage.setItem(` + jsString(s.Name) + `, ` + jsString(s.Value) + `); return true; })()`
		_, err := evalJS[bool](ctx, st, js)
		return "", err
	case "grant_permission":
		origin := s.Scope
		if origin == "" {
			u, err := pageURL(ctx)
			if err != nil {
				return "", err
			}
			origin = u.Scheme + "://" + u.Host
		}
		bc := chromedp.FromContext(ctx).BrowserContextID
		for _, name := range browserPerms[s.Permission] {
			if _, err := chromedp.CallBrowser(ctx, browser.SetPermission, browser.SetPermissionParams{
				Permission: &browser.PermissionDescriptor{Name: name, AllowWithoutSanitization: true}, Setting: browser.PermissionSettingGranted,
				Origin: origin, BrowserContextID: bc}); err != nil {
				return "", fmt.Errorf("the browser did not grant %s: %s", s.Permission, clip(err.Error(), 120))
			}
		}
		if s.Permission == "geolocation" {
			acc := 50.0
			if _, err := chromedp.Call(ctx, emulation.SetGeolocationOverride, emulation.SetGeolocationOverrideParams{Latitude: &s.Lat, Longitude: &s.Lon, Accuracy: &acc}); err != nil {
				return "", err
			}
		}
		return "", nil
	}
	return "", fmt.Errorf("unknown step %q", s.Action)
}

// writeFile puts an upload's file in the run's own folder, for the browser
// to read.
func (st *runState) writeFile(f BrowserFile) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(f.Data)
	if err != nil {
		return "", err
	}
	if st.tmpDir == "" {
		if st.tmpDir, err = os.MkdirTemp("", "visionary-upload-"); err != nil {
			return "", err
		}
	}
	path := filepath.Join(st.tmpDir, filepath.Base(f.Name))
	return path, os.WriteFile(path, raw, 0o600)
}
