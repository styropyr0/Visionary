package provider

import "testing"

func TestKindProblems(t *testing.T) {
	check := func(kind string, set ...string) map[string]string {
		on := map[string]bool{}
		for _, s := range set {
			on[s] = true
		}
		return kindProblems(kind, func(n string) bool { return on[n] })
	}
	if p := check("http", "url"); len(p) != 0 {
		t.Fatalf("valid http check refused: %v", p)
	}
	if p := check("tcp", "tcp", "interval_s"); len(p) != 0 {
		t.Fatalf("valid tcp check refused: %v", p)
	}
	if p := check("api", "api", "secrets"); len(p) != 0 {
		t.Fatalf("valid api check refused: %v", p)
	}
	if p := check("dns"); p["dns"] == "" {
		t.Fatal("dns needs its block")
	}
	if p := check("ping", "ping", "ssl", "url"); p["ssl"] == "" || p["url"] == "" {
		t.Fatalf("ping with another block and a url: %v", p)
	}
	if p := check("ssl", "ssl", "secrets"); p["secrets"] == "" {
		t.Fatal("ssl takes no secrets")
	}
	if p := check("heartbeat", "heartbeat", "interval_s"); p["interval_s"] == "" {
		t.Fatal("heartbeat takes no interval")
	}
	if p := check("http", "url", "tcp"); p["tcp"] == "" {
		t.Fatal("http takes no tcp block")
	}
	if p := check("browser", "steps", "secrets"); len(p) != 0 {
		t.Fatalf("valid browser check refused: %v", p)
	}
}
