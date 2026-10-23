package analytics

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/styropyr0/visionary/internal/authbridge"
)

func testServer() *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// A stored value wins over the variable, which wins over the default.
func TestConfPrecedence(t *testing.T) {
	s := testServer()
	if got := s.confInt("VISIONARY_MAX_ROWS"); got != 5000 {
		t.Fatalf("default: %d", got)
	}
	t.Setenv("VISIONARY_MAX_ROWS", "200")
	if got := s.confInt("VISIONARY_MAX_ROWS"); got != 200 {
		t.Fatalf("env: %d", got)
	}
	l := defaultLiveSettings()
	l.Config["max_rows"] = "70"
	s.liveSet.p.Store(l)
	if got := s.confInt("VISIONARY_MAX_ROWS"); got != 70 {
		t.Fatalf("stored: %d", got)
	}
	// Stored empty is a value: an SMTP host cleared in Settings stays off.
	t.Setenv("VISIONARY_SMTP_HOST", "smtp.example.com")
	l.Config["smtp_host"] = ""
	if got := s.conf("VISIONARY_SMTP_HOST"); got != "" {
		t.Fatalf("stored empty: %q", got)
	}
	if d := s.confDuration("VISIONARY_SESSION_TTL"); d != 12*time.Hour {
		t.Fatalf("duration default: %v", d)
	}
}

func TestConfNormalize(t *testing.T) {
	cases := []struct {
		env, in, want string
		bad           bool
	}{
		{"VISIONARY_MAX_ROWS", " 250 ", "250", false},
		{"VISIONARY_MAX_ROWS", "0", "", true},
		{"VISIONARY_MAX_ROWS", "lots", "", true},
		{"VISIONARY_TRUST_PROXY", "TRUE", "true", false},
		{"VISIONARY_SESSION_TTL", "90m", "1h30m0s", false},
		{"VISIONARY_SESSION_TTL", "10s", "", true}, // under the 5 minute floor
		{"VISIONARY_SECURE_COOKIE", "Auto", "auto", false},
		{"VISIONARY_SECURE_COOKIE", "maybe", "", true},
		{"VISIONARY_SECURITY_IGNORE_IPS", "10.0.0.0/8, 1.2.3.4", "10.0.0.0/8, 1.2.3.4", false},
		{"VISIONARY_SECURITY_IGNORE_IPS", "not-an-ip", "", true},
		{"VISIONARY_DIGEST_TZ", "Asia/Kolkata", "Asia/Kolkata", false},
		{"VISIONARY_DIGEST_TZ", "Mars/Olympus", "", true},
	}
	for _, c := range cases {
		got, err := confByEnv[c.env].normalize(c.in)
		if (err != nil) != c.bad || (!c.bad && got != c.want) {
			t.Errorf("%s %q: got %q, err %v", c.env, c.in, got, err)
		}
	}
}

// Every setting has a group the dashboard shows, and a default that passes
// its own checks.
func TestConfDefs(t *testing.T) {
	groups := map[string]bool{}
	for _, g := range confGroups {
		groups[g.Key] = true
	}
	for _, d := range confDefs {
		if !groups[d.Group] {
			t.Errorf("%s: unknown group %q", d.Env, d.Group)
		}
		if d.Default != "" {
			if _, err := d.normalize(d.Default); err != nil {
				t.Errorf("%s: default %q: %v", d.Env, d.Default, err)
			}
		}
	}
}

func TestSecretsNeverShown(t *testing.T) {
	s := testServer()
	l := defaultLiveSettings()
	l.Config["smtp_password"] = "hunter2hunter2"
	s.liveSet.p.Store(l)
	for _, v := range s.confView() {
		if v.Kind == confSecret && v.Value != "" {
			t.Fatalf("%s sent back", v.Env)
		}
		if v.Env == "VISIONARY_SMTP_PASSWORD" && !v.Set {
			t.Fatal("a stored secret reports set")
		}
	}
	b, _ := json.Marshal(s.live())
	if strings.Contains(string(b), "hunter2") {
		t.Fatal("live settings serialize stored config")
	}
}

func stored(kv map[string]string) map[string][2]string {
	m := map[string][2]string{}
	for k, v := range kv {
		m[k] = [2]string{v, ""}
	}
	return m
}

func TestBuildSignin(t *testing.T) {
	t.Setenv("VISIONARY_ENABLE_AUTH", "")

	// Nothing saved: open until it is.
	if sg := buildSignin(stored(nil), nil); sg.open != openUnconfigured || sg.broken != nil {
		t.Fatalf("unconfigured: %+v", sg)
	}

	shared := stored(map[string]string{settingAuthConfig: `{"type":"shared"}`, settingAuthShared: "correct horse battery"})
	sg := buildSignin(shared, nil)
	if sg.open != "" || sg.broken != nil || sg.cfg.Type != authbridge.TypeShared {
		t.Fatalf("shared: %+v", sg)
	}
	if again := buildSignin(shared, sg); again != sg {
		t.Fatal("an unchanged setup is reused")
	}

	// A saved setup that no longer validates locks sign-in rather than opening it.
	weak := stored(map[string]string{settingAuthConfig: `{"type":"shared"}`, settingAuthShared: "short"})
	if sg := buildSignin(weak, nil); sg.open != "" || sg.broken == nil {
		t.Fatalf("broken: %+v", sg)
	}

	// The environment switch overrides whatever is saved.
	t.Setenv("VISIONARY_ENABLE_AUTH", "false")
	if sg := buildSignin(weak, nil); sg.open != openDisabled {
		t.Fatalf("disabled: %+v", sg)
	}
	t.Setenv("VISIONARY_ENABLE_AUTH", "true")
	if sg := buildSignin(shared, nil); sg.open != "" {
		t.Fatalf("enabled: %+v", sg)
	}
}

func TestNewSigninDelegated(t *testing.T) {
	cfg := `{"type":"password","base_url":"https://api.example.com","verify":{"method":"POST","path":"/auth/login"},
"token":{"header":"Authorization","prefix":"Bearer "},"fields":{"token":"data.token","role":"data.role"},"allowed_roles":[]}`
	if _, err := newSignin(cfg, "", "", ""); err == nil {
		t.Fatal("no role mapping: nobody could sign in")
	}
	sg, err := newSignin(cfg, "ADMIN=admin,*=viewer", "", "ignored-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	if sg.bridge == nil || sg.viewerPassword != "" || len(sg.roles.entries) != 2 {
		t.Fatalf("delegated: %+v", sg)
	}
	if _, err := newSignin(`{"type":"none"}`, "", "", ""); err == nil {
		t.Fatal("mode none is the environment's switch, not a setting")
	}
	if _, err := newSignin(`{"type":"shared","surprise":1}`, "", "correct horse battery", ""); err == nil {
		t.Fatal("unknown fields are rejected")
	}
}
