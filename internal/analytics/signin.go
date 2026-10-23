package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/styropyr0/visionary/internal/authbridge"
)

// Sign-in is configured in Settings → Sign-in and kept in visionary_settings,
// like everything else the server runs on. The only switch left in the
// environment is VISIONARY_ENABLE_AUTH=false, which turns sign-in off for
// anyone locked out by a broken config.
//
// Until sign-in is first saved, the dashboard is open: everyone who reaches
// it is an admin, and a banner says so.

const (
	settingAuthConfig    = "auth.config"
	settingAuthRoleMap   = "auth.role_map"
	settingAuthShared    = "auth.shared_password"
	settingAuthViewer    = "auth.viewer_password"
	settingSessionSecret = "auth.session_secret"
)

// Why sign-in is off.
const (
	openDisabled     = "disabled"     // VISIONARY_ENABLE_AUTH=false
	openUnconfigured = "unconfigured" // nothing saved in Settings → Sign-in yet
)

// signin is the sign-in setup in force. It is replaced, never changed, when
// the settings change.
type signin struct {
	cfg            authbridge.Config
	bridge         *authbridge.Bridge // nil unless cfg is delegated
	roles          *roleMapping
	roleMap        string
	viewerPassword string
	// open, when set, means no sign-in: everyone is the local admin.
	open string
	// broken is a saved setup that no longer loads. Sign-in stays required
	// and nobody can complete it until it is fixed (or turned off).
	broken error
	// key tells setups apart, so an unchanged one keeps its HTTP client.
	key string
}

var openConfig = authbridge.Config{Type: authbridge.TypeNone, Provider: "Visionary", Location: authbridge.LocationNone}

// authEnabled reads VISIONARY_ENABLE_AUTH: on unless it says false.
func authEnabled() bool {
	v := strings.TrimSpace(os.Getenv("VISIONARY_ENABLE_AUTH"))
	if v == "" {
		return true
	}
	b, err := strconv.ParseBool(v)
	return err != nil || b
}

// pendingSignin is in force until the settings are first read: nobody gets
// in, so a database that can't be read never opens the dashboard.
func pendingSignin() *signin {
	if !authEnabled() {
		return &signin{cfg: openConfig, roles: &roleMapping{}, open: openDisabled, key: openDisabled}
	}
	return &signin{cfg: openConfig, roles: &roleMapping{}, broken: errors.New("sign-in is starting; try again in a moment"), key: "pending"}
}

func (s *Server) signin() *signin {
	if sg := s.signinP.Load(); sg != nil {
		return sg
	}
	return pendingSignin()
}

// buildSignin makes the setup from stored settings. prev is reused when
// nothing changed.
func buildSignin(m map[string][2]string, prev *signin) *signin {
	if !authEnabled() {
		return &signin{cfg: openConfig, roles: &roleMapping{}, open: openDisabled, key: openDisabled}
	}
	raw := m[settingAuthConfig][0]
	if strings.TrimSpace(raw) == "" {
		return &signin{cfg: openConfig, roles: &roleMapping{}, open: openUnconfigured, key: openUnconfigured}
	}
	roleMap, shared, viewer := m[settingAuthRoleMap][0], m[settingAuthShared][0], m[settingAuthViewer][0]
	key := strings.Join([]string{raw, roleMap, shared, viewer}, "\x00")
	if prev != nil && prev.key == key {
		return prev
	}
	sg, err := newSignin(raw, roleMap, shared, viewer)
	if err != nil {
		cfg := openConfig
		cfg.Type = "broken"
		return &signin{cfg: cfg, roles: &roleMapping{}, broken: err, key: key}
	}
	sg.key = key
	return sg
}

// newSignin checks a setup as Settings → Sign-in saves it.
func newSignin(raw, roleMap, shared, viewer string) (*signin, error) {
	var cfg authbridge.Config
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("the sign-in config isn't valid: %w", err)
	}
	if strings.EqualFold(cfg.Type, authbridge.TypeNone) {
		return nil, errors.New("sign-in can't be turned off here; set VISIONARY_ENABLE_AUTH=false in the server's environment instead")
	}
	cfg.SharedPassword = shared
	if err := cfg.Validate(roleMap); err != nil {
		return nil, err
	}
	roles, err := newRoleMapping(roleMap, cfg.AllowedRoles)
	if err != nil {
		return nil, err
	}
	if cfg.Type == authbridge.TypeAccounts {
		// Each account carries its own role.
		roles, roleMap, viewer = &roleMapping{}, "", ""
	} else if cfg.Type == authbridge.TypeShared {
		roles = &roleMapping{}
		if viewer != "" {
			if len(viewer) < 12 {
				return nil, errors.New("the read-only password must be at least 12 characters")
			}
			if viewer == shared {
				return nil, errors.New("the read-only password must differ from the team password")
			}
		}
	} else {
		viewer = ""
	}
	sg := &signin{cfg: cfg, roles: roles, roleMap: strings.TrimSpace(roleMap), viewerPassword: viewer}
	if cfg.Delegated() {
		sg.bridge = authbridge.New(cfg, UserAgent)
	}
	return sg, nil
}

// applySignin swaps in the setup from m and logs when it changes.
func (s *Server) applySignin(m map[string][2]string) {
	prev := s.signinP.Load()
	next := buildSignin(m, prev)
	if next == prev {
		return
	}
	s.signinP.Store(next)
	switch {
	case next.open == openDisabled:
		s.logger.Warn("VISIONARY_ENABLE_AUTH=false: sign-in is off and anyone who can reach this server is an admin; never leave it like this on a public URL")
	case next.open == openUnconfigured:
		s.logger.Warn("sign-in isn't set up yet: anyone who can reach this server is an admin until it is saved in Settings → Sign-in")
	case next.broken != nil:
		s.logger.Error("sign-in setup in Settings can't be used; nobody can sign in until it is fixed. Set VISIONARY_ENABLE_AUTH=false to get in and fix it", "err", next.broken)
	case next.cfg.Type == authbridge.TypeShared:
		s.logger.Info("sign-in with a shared team password")
	case next.cfg.Type == authbridge.TypeAccounts:
		s.logger.Info("sign-in with Visionary accounts")
	default:
		if next.bridge != nil && !next.bridge.CanRecheck() {
			s.logger.Warn("no profile endpoint; roles are only checked at sign-in")
		}
		s.logger.Info("sign-in delegated", "provider", next.cfg.Provider, "mode", next.cfg.Type,
			"base_url", next.cfg.BaseURL, "location", next.cfg.Location)
	}
	if prev != nil && prev.key != "pending" {
		s.auditRoleMapping(context.Background())
	}
}

// loadSessionSecret sets the cookie key from the stored secret, creating one
// on first start. Two servers starting at once agree: only the first insert
// wins.
func (s *Server) loadSessionSecret(ctx context.Context) {
	m, err := s.readSettings(ctx)
	if err != nil {
		s.logger.Warn("reading the session secret failed; sessions end on restart until it can be read", "err", err)
		return
	}
	secret := m[settingSessionSecret][0]
	if secret == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			s.logger.Warn("creating a session secret failed", "err", err)
			return
		}
		secret = hex.EncodeToString(b)
		if err := s.db.Exec(ctx, s.q.insertSettingIfAbsent(), settingSessionSecret, secret, nowTS()); err != nil {
			s.logger.Warn("storing the session secret failed; sessions end on restart", "err", dbErr(err))
		}
		if m, err = s.readSettings(ctx); err == nil && m[settingSessionSecret][0] != "" {
			secret = m[settingSessionSecret][0]
		}
	}
	if err := s.auth.setSecret([]byte(secret)); err != nil {
		s.logger.Warn("using the session secret failed", "err", err)
	}
}
