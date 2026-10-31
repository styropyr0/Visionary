package skiplist

import "testing"

func TestMatch(t *testing.T) {
	list := Parse(`["/healthz", " /static/* ", ""]`)
	for path, want := range map[string]bool{
		"/healthz":       true,
		"/healthz/deep":  false,
		"/static/app.js": true,
		"/static/":       true,
		"/static":        false,
		"/api/users":     false,
	} {
		if got := Match(list, path); got != want {
			t.Errorf("Match(%q) = %v, want %v", path, got, want)
		}
	}
	if Parse("not json") != nil {
		t.Error("a bad value should skip nothing")
	}
}
