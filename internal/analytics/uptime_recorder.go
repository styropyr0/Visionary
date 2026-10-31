package analytics

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// A Chrome DevTools Recorder export (JSON) becomes the steps of a browser
// check. Steps with no equivalent are listed, not dropped silently.

type recorderDoc struct {
	Title string         `json:"title"`
	Steps []recorderStep `json:"steps"`
}

type recorderStep struct {
	Type      string          `json:"type"`
	URL       string          `json:"url"`
	Value     string          `json:"value"`
	Key       string          `json:"key"`
	Target    string          `json:"target"`
	Selectors json.RawMessage `json:"selectors"`
	Frame     []int           `json:"frame"`
	Count     *int            `json:"count"`
	Operator  string          `json:"operator"`
	Visible   *bool           `json:"visible"`
	Expr      string          `json:"expression"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	IsMobile  bool            `json:"isMobile"`
	Asserted  []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"assertedEvents"`
}

type recorderImport struct {
	Title       string        `json:"title"`
	Steps       []BrowserStep `json:"steps"`
	Device      string        `json:"device,omitempty"`
	ViewportW   int           `json:"viewport_w,omitempty"`
	ViewportH   int           `json:"viewport_h,omitempty"`
	Unsupported []string      `json:"unsupported"`
}

var (
	ariaRoleRe  = regexp.MustCompile(`\[role="[^"]*"\]$`)
	selectTagRe = regexp.MustCompile(`(^|[\s>"])select([#.\[:\s"]|$)`)
)

// stableCSS is a CSS selector by id, test id or name, which outlasts
// changes to the page better than an accessible name.
var stableCSS = regexp.MustCompile(`^[a-z]*(#[A-Za-z][\w-]*|\[(data-test(id)?|data-testid|data-cy|name|id)=[^\]]+\])$`)

// pickSelector chooses how a step finds its element: text holds an
// accessible name or visible text, css a CSS selector. A stable CSS
// selector comes first, then the accessible name, the text and any CSS.
// needCSS is for steps that only take a CSS selector.
func pickSelector(raw json.RawMessage, needCSS bool) (css, text string) {
	var groups [][]string
	if err := json.Unmarshal(raw, &groups); err != nil {
		var flat []string
		if json.Unmarshal(raw, &flat) != nil {
			return "", ""
		}
		for _, s := range flat {
			groups = append(groups, []string{s})
		}
	}
	var aria, txt, plain, pierce string
	for _, g := range groups {
		// A chain crosses shadow roots, which a check cannot follow.
		if len(g) != 1 {
			continue
		}
		s := g[0]
		switch {
		case strings.HasPrefix(s, "aria/"):
			if aria == "" {
				aria = strings.TrimSpace(ariaRoleRe.ReplaceAllString(strings.TrimPrefix(s, "aria/"), ""))
			}
		case strings.HasPrefix(s, "text/"):
			if txt == "" {
				txt = strings.TrimPrefix(s, "text/")
			}
		case strings.HasPrefix(s, "pierce/"):
			if pierce == "" {
				pierce = strings.TrimPrefix(s, "pierce/")
			}
		case strings.HasPrefix(s, "xpath/"):
		default:
			if plain == "" {
				plain = s
			}
		}
	}
	if plain == "" {
		plain = pierce
	}
	if needCSS {
		return plain, ""
	}
	switch {
	case stableCSS.MatchString(plain):
		return plain, ""
	case aria != "":
		return "", aria
	case txt != "":
		return "", txt
	}
	return plain, ""
}

func isSelect(raw json.RawMessage) bool {
	s := string(raw)
	return strings.Contains(s, `[role=\"combobox\"]`) || selectTagRe.MatchString(s)
}

func urlPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return ""
	}
	return u.Path
}

func convertRecording(data []byte) (recorderImport, error) {
	var doc recorderDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return recorderImport{}, fmt.Errorf("not a Recorder JSON export: %v", err)
	}
	if len(doc.Steps) == 0 {
		return recorderImport{}, fmt.Errorf("the recording has no steps")
	}
	out := recorderImport{Title: strings.TrimSpace(doc.Title), Steps: []BrowserStep{}, Unsupported: []string{}}
	skip := func(i int, typ, why string) {
		out.Unsupported = append(out.Unsupported, fmt.Sprintf("step %d (%s): %s", i+1, typ, why))
	}
	var frame []int
	enter := func(want []int) {
		if fmt.Sprint(want) == fmt.Sprint(frame) {
			return
		}
		if len(frame) > 0 {
			out.Steps = append(out.Steps, BrowserStep{Action: "frame"})
		}
		for _, n := range want {
			out.Steps = append(out.Steps, BrowserStep{Action: "frame", Selector: "iframe:nth-of-type(" + strconv.Itoa(n+1) + ")"})
		}
		frame = want
	}
	for i, s := range doc.Steps {
		if s.Target != "" && s.Target != "main" {
			skip(i, s.Type, "it happens in another tab or window")
			continue
		}
		var step *BrowserStep
		switch s.Type {
		case "setViewport":
			switch {
			case s.IsMobile:
				out.Device = "phone"
			case s.Width == 1280 && s.Height == 800:
				out.Device = "desktop"
			case s.Width == 1440 && s.Height == 900:
				out.Device = "laptop"
			case s.Width == 768 && s.Height == 1024:
				out.Device = "tablet"
			case s.Width >= 320 && s.Width <= 3840 && s.Height >= 320 && s.Height <= 2160:
				out.Device, out.ViewportW, out.ViewportH = "custom", s.Width, s.Height
			default:
				skip(i, s.Type, fmt.Sprintf("%d × %d is outside the sizes a check can use", s.Width, s.Height))
			}
			continue
		case "navigate":
			enter(nil)
			out.Steps = append(out.Steps, BrowserStep{Action: "goto", Value: s.URL})
			continue
		case "click", "hover":
			css, text := pickSelector(s.Selectors, false)
			if css == "" && text == "" {
				skip(i, s.Type, "Chrome gave only selectors a check cannot use (XPath or shadow DOM)")
				continue
			}
			step = &BrowserStep{Action: s.Type, Selector: css, Text: text}
		case "change":
			css, _ := pickSelector(s.Selectors, true)
			if css == "" {
				skip(i, s.Type, "the field has no CSS selector")
				continue
			}
			step = &BrowserStep{Action: "type", Selector: css, Value: s.Value}
			if isSelect(s.Selectors) {
				step.Action = "select"
			}
		case "keyDown":
			switch s.Key {
			case "Shift", "Control", "Alt", "Meta":
				skip(i, s.Type, "key combinations are not supported")
				continue
			}
			step = &BrowserStep{Action: "press", Value: s.Key}
		case "keyUp":
			continue
		case "waitForElement":
			css, text := pickSelector(s.Selectors, false)
			if s.Count != nil || (s.Visible != nil && !*s.Visible) {
				if css, _ = pickSelector(s.Selectors, true); css == "" {
					skip(i, s.Type, "counting needs a CSS selector")
					continue
				}
				n, op := 1, "gte"
				if s.Count != nil {
					n = *s.Count
				}
				switch s.Operator {
				case "==":
					op = "eq"
				case "<=":
					op = "lte"
				}
				if s.Visible != nil && !*s.Visible && s.Count == nil {
					n, op = 0, "eq"
				}
				step = &BrowserStep{Action: "assert_count", Selector: css, Count: n, Op: op}
				break
			}
			if css == "" && text == "" {
				skip(i, s.Type, "Chrome gave only selectors a check cannot use (XPath or shadow DOM)")
				continue
			}
			step = &BrowserStep{Action: "wait_selector", Selector: css, Text: text}
		case "waitForExpression":
			step = &BrowserStep{Action: "script", Value: "for (let i = 0; i < 100; i++) { if (await (" + s.Expr + ")) return true; await new Promise((r) => setTimeout(r, 100)); } return false;"}
		case "scroll":
			skip(i, s.Type, "not needed: steps scroll to their element")
			continue
		case "doubleClick":
			skip(i, s.Type, "double clicks are not supported")
			continue
		default:
			skip(i, s.Type, "no matching step")
			continue
		}
		enter(s.Frame)
		out.Steps = append(out.Steps, *step)
		for _, ev := range s.Asserted {
			if ev.Type == "navigation" {
				out.Steps = append(out.Steps, BrowserStep{Action: "wait_navigation", Value: urlPath(ev.URL)})
				break
			}
		}
	}
	if len(out.Steps) > browserMaxSteps {
		out.Unsupported = append(out.Unsupported, fmt.Sprintf("steps after the %dth were left out: a check has at most %d", browserMaxSteps, browserMaxSteps))
		out.Steps = out.Steps[:browserMaxSteps]
	}
	return out, nil
}

func (s *Server) handleRecorderImport(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the recording")
		return
	}
	out, err := convertRecording(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
