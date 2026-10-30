package analytics

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/styropyr0/visionary/internal/authbridge"
)

func (s *Server) ssoRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sso/{id}/start", s.handleSSOStart)
	mux.Handle("GET /api/sso/{id}/test", s.authed(s.handleSSOTest))
	mux.HandleFunc("GET /api/sso/{id}/callback", s.handleOIDCCallback)
	mux.HandleFunc("POST /api/sso/{id}/acs", s.handleSAMLACS)
	mux.HandleFunc("GET /api/sso/{id}/metadata", s.handleSAMLMetadata)
	mux.Handle("GET /api/settings/sso", s.authed(s.handleSSOSettings))
	mux.Handle("PUT /api/settings/sso", s.authed(s.handlePutSSO))
	s.scimRoutes(mux)
}

// handleSSOStart sends someone to the identity provider.
func (s *Server) handleSSOStart(w http.ResponseWriter, r *http.Request) {
	st := s.ssoSetup()
	p := st.provider(r.PathValue("id"))
	if p == nil {
		ssoFail(w, r, "That single sign-on provider isn't turned on.")
		return
	}
	sg := s.signin()
	if sg.open != "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if sg.broken != nil {
		ssoFail(w, r, "Sign-in isn't working: "+sg.broken.Error())
		return
	}
	s.ssoBegin(w, r, st, p, false)
}

// handleSSOTest runs a provider's sign-in without signing anyone in, and
// shows what the provider sent and what Visionary would do with it. It works
// on providers that are still off, and while sign-in is off.
func (s *Server) handleSSOTest(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	st := s.ssoSetup()
	p := st.anyProvider(r.PathValue("id"))
	if p == nil {
		s.ssoTestPage(w, nil, nil, nil, errors.New("save the provider first, then test it"))
		return
	}
	s.ssoBegin(w, r, st, p, true)
}

func (s *Server) ssoBegin(w http.ResponseWriter, r *http.Request, st *ssoSetup, p *ssoProvider, test bool) {
	if p.Kind == ssoSAML {
		s.samlStart(w, r, st, p, test)
		return
	}
	s.oidcStart(w, r, st, p, test)
}

type ssoView struct {
	Config ssoConfig `json:"config"`
	Error  string    `json:"error,omitempty"`
	// SPCert is the SAML certificate to give identity providers that ask.
	SPCert  string `json:"sp_cert,omitempty"`
	SCIMURL string `json:"scim_url,omitempty"`
	// SignInType is the way in from Settings → Sign-in, so the page can say
	// whether single sign-on is the only one.
	SignInType string `json:"sign_in_type"`
}

func (s *Server) handleSSOSettings(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	st := s.ssoSetup()
	v := ssoView{Config: st.cfg, SPCert: spCertPEM(st.spPEM), SignInType: s.signin().cfg.Type}
	if st.err != nil {
		v.Error = st.err.Error()
	}
	v.Config.Providers = make([]ssoProvider, len(st.cfg.Providers))
	for i, p := range st.cfg.Providers {
		p.SecretSet = st.secrets[p.ID] != ""
		p.urls(st.cfg.PublicURL)
		v.Config.Providers[i] = p
	}
	if st.cfg.PublicURL != "" {
		v.SCIMURL = strings.TrimRight(st.cfg.PublicURL, "/") + "/scim/v2"
	}
	writeJSON(w, http.StatusOK, v)
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// handlePutSSO checks and saves the providers. Turned-on providers are
// reached first (discovery, metadata), so a setup that can't work isn't
// saved.
func (s *Server) handlePutSSO(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		ssoConfig
		// Secrets sets client secrets by provider id; null removes one and
		// a provider left out keeps its own.
		Secrets map[string]*string `json:"secrets"`
		// SPKeyPair replaces the SAML key: a PEM private key and certificate.
		SPKeyPair string `json:"sp_key_pair"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := body.ssoConfig
	if len(cfg.Providers) > maxSSOProvs {
		writeError(w, http.StatusBadRequest, "at most "+strconv.Itoa(maxSSOProvs)+" providers")
		return
	}
	cfg.PublicURL = strings.TrimRight(strings.TrimSpace(cfg.PublicURL), "/")
	if len(cfg.Providers) > 0 {
		u, err := url.Parse(cfg.PublicURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") {
			writeError(w, http.StatusBadRequest, "the public URL is the address people open Visionary at, such as https://visionary.example.com")
			return
		}
	}
	seen := map[string]bool{}
	samlUsed := false
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		p.SecretSet, p.RedirectURL, p.EntityID, p.ACSURL = false, "", "", ""
		if p.ID == "" {
			base := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(p.Name), "-"), "-")
			base = clip(fallback(base, p.Kind), 24)
			p.ID = base
			for n := 2; seen[p.ID]; n++ {
				p.ID = base + "-" + strconv.Itoa(n)
			}
		}
		if seen[p.ID] {
			writeError(w, http.StatusBadRequest, "two providers have the id "+p.ID)
			return
		}
		seen[p.ID] = true
		if err := p.check(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		samlUsed = samlUsed || p.Kind == ssoSAML
	}
	cfg.SCIM.RoleMap = strings.TrimSpace(cfg.SCIM.RoleMap)
	if _, err := newRoleMapping(cfg.SCIM.RoleMap, nil); err != nil {
		writeError(w, http.StatusBadRequest, "SCIM groups: "+err.Error())
		return
	}
	if cfg.SCIM.DefaultRole != "" && parseRole(cfg.SCIM.DefaultRole) == roleNone {
		writeError(w, http.StatusBadRequest, "the SCIM default role is viewer, editor, admin or none")
		return
	}
	cur := s.ssoSetup()
	secrets := map[string]string{}
	for id, v := range cur.secrets {
		if seen[id] {
			secrets[id] = v
		}
	}
	for id, v := range body.Secrets {
		switch {
		case !seen[id]:
		case v == nil || *v == "":
			delete(secrets, id)
		default:
			secrets[id] = strings.TrimSpace(*v)
		}
	}
	ctx := r.Context()
	if pair := strings.TrimSpace(body.SPKeyPair); pair != "" {
		if _, _, err := parseSPKeyPair(pair); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.writeSettings(ctx, map[string]string{settingSAMLSP: pair + "\n"}); err != nil {
			s.fail(w, err)
			return
		}
	} else if samlUsed {
		if err := s.ensureSPKeyPair(ctx); err != nil {
			s.fail(w, err)
			return
		}
	}
	m, err := s.readSettings(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	enabled := 0
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if !p.Enabled {
			continue
		}
		enabled++
		if p.Kind == ssoOIDC {
			_, err = newOIDCClient(ctx, p, secrets[p.ID], cfg.PublicURL)
		} else {
			_, err = newSAMLClient(ctx, p, m[settingSAMLSP][0], cfg.PublicURL)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if enabled == 0 && s.signin().cfg.Type == authbridge.TypeSSO {
		writeError(w, http.StatusBadRequest, "single sign-on is the only way in; keep a provider on, or choose another way in under How people sign in first")
		return
	}
	raw, _ := json.Marshal(cfg)
	sec, _ := json.Marshal(secrets)
	if err := s.writeSettings(ctx, map[string]string{settingSSO: string(raw), settingSSOSecrets: string(sec)}); err != nil {
		s.fail(w, err)
		return
	}
	summary := make([]map[string]any, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		summary = append(summary, map[string]any{"id": p.ID, "kind": p.Kind, "name": p.Name, "enabled": p.Enabled,
			"role_map": p.RoleMap, "default_role": p.DefaultRole, "domains": p.AllowedDomains, "create_users": p.CreateUsers})
	}
	s.audit(r, "settings.sso", "", map[string]any{"providers": summary, "scim": cfg.SCIM, "public_url": cfg.PublicURL})
	if err := s.refreshLive(ctx); err != nil {
		s.fail(w, err)
		return
	}
	s.handleSSOSettings(w, r)
}
