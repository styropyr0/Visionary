package uptimecore

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

// Steps in a frame run in an isolated world of that frame, which sees its DOM
// but not the page's own scripts

// The API shows a file's size without its data, and sending it back without data keeps the stored file
type BrowserFile struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	Size int    `json:"size"`
	Data string `json:"data,omitempty"`
}

const (
	browserMaxFiles     = 5
	browserMaxFile      = 256 << 10
	browserMaxFileTotal = 512 << 10
)

var (
	varRefRe     = regexp.MustCompile(`\{\{var\.([A-Za-z0-9_]+)\}\}`)
	fileNameRe   = regexp.MustCompile(`^[^/\\:*?"<>|\x00-\x1f]{1,100}$`)
	browserPerms = map[string][]string{"geolocation": {"geolocation"}, "notifications": {"notifications"}, "clipboard": {"clipboard-read", "clipboard-write"}}
)

func (o *BrowserOptions) NormalizeFiles(prev []BrowserFile) error {
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
			for _, p := range prev {
				if p.Name == f.Name && p.Data != "" {
					f.Data, found = p.Data, true
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

func (st *runState) resolve(s BrowserStep, secrets map[string]string) BrowserStep {
	vars := func(v string) string {
		return varRefRe.ReplaceAllStringFunc(v, func(m string) string { return st.vars[varRefRe.FindStringSubmatch(m)[1]] })
	}
	s.Value = vars(resolveSecrets(s.Value, secrets))
	s.Selector, s.Text, s.Target, s.Scope, s.Name = vars(s.Selector), vars(s.Text), vars(s.Target), vars(resolveSecrets(s.Scope, secrets)), vars(s.Name)
	return s
}

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
		st.frameCtx = nil
	}
	return v, err
}

func inContext(id runtime.ExecutionContextID) chromedp.EvaluateOption {
	return func(p *runtime.EvaluateParams) { p.ContextID = id }
}

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

// viewport
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
