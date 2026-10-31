// Package skiplist matches request paths against Settings → Data, kept in
// visionary_settings as requests.skip: a JSON array of exact paths, or
// prefixes ending in "*".
package skiplist

import (
	"encoding/json"
	"strings"
)

// Setting is the visionary_settings name of the list.
const Setting = "requests.skip"

// Parse reads the stored value; anything but a JSON array of strings skips nothing.
func Parse(value string) []string {
	var list []string
	if json.Unmarshal([]byte(value), &list) != nil {
		return nil
	}
	out := list[:0]
	for _, p := range list {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Match reports whether path equals an entry, or starts with one ending in "*".
func Match(list []string, path string) bool {
	for _, p := range list {
		if prefix, ok := strings.CutSuffix(p, "*"); ok {
			if strings.HasPrefix(path, prefix) {
				return true
			}
		} else if path == p {
			return true
		}
	}
	return false
}
