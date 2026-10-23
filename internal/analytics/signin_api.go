package analytics

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/styropyr0/visionary/internal/authbridge"
)

// Settings → Sign-in (admins). Passwords are never sent back, only whether
// they are set; leaving one out of a save keeps it.

type signinView struct {
	// EnvEnabled is false when VISIONARY_ENABLE_AUTH=false turns sign-in off.
	EnvEnabled bool `json:"env_enabled"`
	// Open says why sign-in is off ("disabled", "unconfigured"), if it is.
	Open  string `json:"open,omitempty"`
	Error string `json:"error,omitempty"`
	// Config is the stored setup, null before the first save.
	Config            *authbridge.Config `json:"config"`
	RoleMap           string             `json:"role_map"`
	SharedPasswordSet bool               `json:"shared_password_set"`
	ViewerPasswordSet bool               `json:"viewer_password_set"`
	// Mapping is the role mapping in force, as Members & roles shows it.
	Mapping []roleEntry `json:"mapping"`
}

func (s *Server) handleSigninSettings(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	m, err := s.readSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	sg := s.signin()
	v := signinView{
		EnvEnabled:        authEnabled(),
		Open:              sg.open,
		RoleMap:           m[settingAuthRoleMap][0],
		SharedPasswordSet: m[settingAuthShared][0] != "",
		ViewerPasswordSet: m[settingAuthViewer][0] != "",
		Mapping:           append([]roleEntry{}, sg.roles.entries...),
	}
	if sg.broken != nil {
		v.Error = sg.broken.Error()
	}
	if raw := m[settingAuthConfig][0]; raw != "" {
		var cfg authbridge.Config
		if json.Unmarshal([]byte(raw), &cfg) == nil {
			v.Config = &cfg
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// handlePutSignin checks and saves the sign-in setup. It applies at once on
// this server and within 30 seconds on others.
func (s *Server) handlePutSignin(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		Config         json.RawMessage `json:"config"`
		RoleMap        string          `json:"role_map"`
		SharedPassword *string         `json:"shared_password"`
		ViewerPassword *string         `json:"viewer_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(body.Config) == 0 || string(body.Config) == "null" {
		writeError(w, http.StatusBadRequest, "choose how people sign in")
		return
	}
	m, err := s.readSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	shared, viewer := m[settingAuthShared][0], m[settingAuthViewer][0]
	if body.SharedPassword != nil {
		shared = *body.SharedPassword
	}
	if body.ViewerPassword != nil {
		viewer = *body.ViewerPassword
	}
	roleMap := strings.TrimSpace(body.RoleMap)
	sg, err := newSignin(string(body.Config), roleMap, shared, viewer)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if sg.cfg.Type == authbridge.TypeAccounts {
		// Switching to accounts with nobody able to administer them would
		// lock everyone out.
		n, err := s.enabledAdmins(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		if n == 0 {
			writeError(w, http.StatusBadRequest, "create an admin account first, so someone can sign in")
			return
		}
	}
	// Store what was checked, defaults filled in; secrets apart.
	cfg := sg.cfg
	cfg.SharedPassword = ""
	raw, _ := json.Marshal(cfg)
	kv := map[string]string{settingAuthConfig: string(raw), settingAuthRoleMap: roleMap}
	var reset []string
	if cfg.Type == authbridge.TypeShared {
		kv[settingAuthShared] = shared
		if viewer == "" {
			reset = append(reset, settingAuthViewer)
		} else {
			kv[settingAuthViewer] = viewer
		}
	} else {
		reset = append(reset, settingAuthShared, settingAuthViewer)
	}
	if err := s.writeSettings(r.Context(), kv); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.deleteSettings(r.Context(), reset); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "settings.sign_in", cfg.Type, map[string]any{"mode": cfg.Type, "provider": cfg.Provider, "mapping": sg.roles.entries})
	if err := s.refreshLive(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	s.handleSigninSettings(w, r)
}
