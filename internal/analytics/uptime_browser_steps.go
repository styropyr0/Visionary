package analytics

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/styropyr0/visionary/internal/uptimecore"
)

// Validation of the steps beyond clicking and checking; they run in
// internal/uptimecore (browser_steps.go).

var (
	varRefRe      = regexp.MustCompile(`\{\{var\.([A-Za-z0-9_]+)\}\}`)
	cookieNameRe  = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,200}$")
	extractAttrRe = regexp.MustCompile(`^[A-Za-z_:][-A-Za-z0-9_:.]{0,99}$`)
	browserPerms  = map[string]bool{"geolocation": true, "notifications": true, "clipboard": true}
)

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
		if !browserPerms[b.Permission] {
			return errors.New("permission is geolocation, notifications or clipboard")
		}
		if b.Permission == "geolocation" && (b.Lat < -90 || b.Lat > 90 || b.Lon < -180 || b.Lon > 180) {
			return errors.New("latitude is -90 to 90 and longitude -180 to 180")
		}
		if b.Scope != "" {
			if _, err := uptimecore.NormalizeOrigin(b.Scope); err != nil || strings.Contains(b.Scope, "*") {
				return errors.New("the permission's origin must look like https://app.example.com")
			}
		}
	}
	return nil
}

func prevFiles(prev *UptimeCheck) []BrowserFile {
	if prev == nil {
		return nil
	}
	return prev.Files
}

func (b BrowserStep) refs() []string {
	var out []string
	for _, f := range []string{b.Selector, b.Text, b.Value, b.Target, b.Scope, b.Name} {
		for _, m := range varRefRe.FindAllStringSubmatch(f, -1) {
			out = append(out, m[1])
		}
	}
	return out
}
