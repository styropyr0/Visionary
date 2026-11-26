package visionary

import (
	"strconv"
	"testing"
)

func TestGoVersion(t *testing.T) {
	for in, want := range map[string]string{
		"go1.26.2":                 "1.26.2",
		"go1.26.2 X:nodwarf5":      "1.26.2",
		"go1.27rc1":                "1.27rc1",
		"devel go1.27-abc +cafe":   "",
		"go1.26.2-20260101-abcdef": "1.26.2",
	} {
		if got := goVersion(in); got != want {
			t.Errorf("goVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPackageRows(t *testing.T) {
	pkgs := []modulePackage{{"a", "v1.0.0"}, {"a", "v1.0.0"}, {"b", ""}, {"a", "v1.1.0"}}
	rows := packageRows(pkgs, "2026-01-01T00:00:00.000Z", identity{instance: "i", version: "abc"})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	r := rows[0]
	if len(r) != len(packageColumns) || r[1] != nil || r[2] != "i" || r[3] != "abc" || r[4] != "Go" || r[5] != "a" || r[6] != "v1.0.0" {
		t.Fatalf("row = %v", r)
	}

	many := make([]modulePackage, maxPackages+10)
	for i := range many {
		many[i] = modulePackage{"m", strconv.Itoa(i)}
	}
	if n := len(packageRows(many, "", identity{})); n != maxPackages {
		t.Fatalf("rows = %d, want %d", n, maxPackages)
	}

	found := false
	for _, p := range goPackages() {
		if p.name == "stdlib" {
			found = true
		}
	}
	if !found {
		t.Fatal("no stdlib entry")
	}
}
