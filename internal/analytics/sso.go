package analytics

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Single sign-on through OpenID Connect or SAML 2.0, with SCIM for accounts.
// Providers live in visionary_settings, so a change applies within 30 seconds
// on every server. People who sign in get a passwordless visionary_users row.

const (
	settingSSO        = "auth.sso"
	settingSSOSecrets = "auth.sso_secrets"
	// settingSAMLSP holds the SAML key and certificate as PEM in one value,
	// so two servers creating them at once can't end up with halves of each.
	settingSAMLSP = "auth.saml_sp"

	ssoPrefix   = "sso:"
	ssoOIDC     = "oidc"
	ssoSAML     = "saml"
	maxSSOProvs = 10
)

type ssoConfig struct {
	// PublicURL is where people reach Visionary; redirect and ACS URLs are
	// built from it, so they match what is registered at the provider.
	PublicURL string        `json:"public_url"`
	Providers []ssoProvider `json:"providers"`
	SCIM      scimConfig    `json:"scim"`
}

type ssoProvider struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`

	Issuer   string   `json:"issuer,omitempty"`
	ClientID string   `json:"client_id,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`

	MetadataURL string `json:"metadata_url,omitempty"`
	MetadataXML string `json:"metadata_xml,omitempty"`
	EmailAttr   string `json:"email_attr,omitempty"`
	NameAttr    string `json:"name_attr,omitempty"`

	// GroupsClaim is the ID token claim (OIDC) or attribute (SAML) that
	// lists the person's groups or roles.
	GroupsClaim    string   `json:"groups_claim,omitempty"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	RoleMap        string   `json:"role_map,omitempty"`
	DefaultRole    string   `json:"default_role,omitempty"`
	CreateUsers    bool     `json:"create_users"`

	SecretSet   bool   `json:"client_secret_set,omitempty"`
	RedirectURL string `json:"redirect_url,omitempty"`
	EntityID    string `json:"entity_id,omitempty"`
	ACSURL      string `json:"acs_url,omitempty"`
}

// scimConfig maps SCIM groups to roles. People in no mapped group get the
// default role, or no access when it is empty.
type scimConfig struct {
	RoleMap     string `json:"role_map"`
	DefaultRole string `json:"default_role"`
}

// ssoSetup is the configuration in force. It is replaced, never changed.
type ssoSetup struct {
	cfg     ssoConfig
	secrets map[string]string
	spPEM   string
	// err is a saved setup that can't be read; SSO is off until it is fixed.
	err error
	key string
}

type ssoState struct {
	setup atomic.Pointer[ssoSetup]

	mu   sync.Mutex
	oidc map[string]*oidcClient
	saml map[string]*samlClient
	// touched holds when this server changed a user (SCIM, Settings), so
	// their session is re-checked at once instead of within the minute.
	touched map[string]time.Time
	// used holds the SAML requests already answered, against replays.
	used map[string]time.Time
}

var ssoStates sync.Map

func (s *Server) sso() *ssoState {
	v, _ := ssoStates.LoadOrStore(s, &ssoState{oidc: map[string]*oidcClient{}, saml: map[string]*samlClient{},
		touched: map[string]time.Time{}, used: map[string]time.Time{}})
	return v.(*ssoState)
}

func (s *Server) ssoSetup() *ssoSetup {
	if st := s.sso().setup.Load(); st != nil {
		return st
	}
	return &ssoSetup{key: "pending"}
}

func (s *Server) applySSO(m map[string][2]string) {
	raw, secrets := m[settingSSO][0], m[settingSSOSecrets][0]
	key := strings.Join([]string{raw, secrets, m[settingSAMLSP][0]}, "\x00")
	st := s.sso()
	if prev := st.setup.Load(); prev != nil && prev.key == key {
		return
	}
	next := &ssoSetup{key: key, secrets: map[string]string{}, spPEM: m[settingSAMLSP][0]}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &next.cfg); err != nil {
			next.err = fmt.Errorf("the single sign-on setup can't be read: %w", err)
			s.logger.Error("single sign-on is off", "err", next.err)
		}
	}
	if secrets != "" {
		_ = json.Unmarshal([]byte(secrets), &next.secrets)
	}
	st.setup.Store(next)
	st.mu.Lock()
	clear(st.oidc)
	clear(st.saml)
	st.mu.Unlock()
}

func (st *ssoSetup) provider(id string) *ssoProvider {
	if st.err != nil {
		return nil
	}
	for i := range st.cfg.Providers {
		if p := &st.cfg.Providers[i]; p.ID == id && p.Enabled {
			return p
		}
	}
	return nil
}

// anyProvider finds a provider whether or not it is on, for a test sign-in.
func (st *ssoSetup) anyProvider(id string) *ssoProvider {
	if st.err != nil {
		return nil
	}
	for i := range st.cfg.Providers {
		if p := &st.cfg.Providers[i]; p.ID == id {
			return p
		}
	}
	return nil
}

func (st *ssoSetup) enabled() []ssoProvider {
	var out []ssoProvider
	if st.err != nil {
		return nil
	}
	for _, p := range st.cfg.Providers {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out
}

type ssoButton struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func (s *Server) ssoButtons() []ssoButton {
	out := []ssoButton{}
	for _, p := range s.ssoSetup().enabled() {
		out = append(out, ssoButton{ID: p.ID, Name: p.Name, Kind: p.Kind})
	}
	return out
}

// urls fills in the addresses the provider must be told about.
func (p *ssoProvider) urls(publicURL string) {
	base := strings.TrimRight(publicURL, "/") + "/api/sso/" + p.ID
	p.RedirectURL, p.EntityID, p.ACSURL = "", "", ""
	if p.Kind == ssoOIDC {
		p.RedirectURL = base + "/callback"
	} else {
		p.EntityID, p.ACSURL = base+"/metadata", base+"/acs"
	}
}

var ssoIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// check validates a provider as Settings saves it and fills in defaults. It
// doesn't reach the provider; build does.
func (p *ssoProvider) check() error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return errors.New("give each provider a name, shown on the sign-in button")
	}
	if !ssoIDPattern.MatchString(p.ID) {
		return fmt.Errorf("%s: the id is 1 to 32 lowercase letters, digits and dashes", p.Name)
	}
	switch p.Kind {
	case ssoOIDC:
		p.Issuer = strings.TrimRight(strings.TrimSpace(p.Issuer), "/")
		if err := checkProviderURL(p.Issuer); err != nil {
			return fmt.Errorf("%s: the issuer %w", p.Name, err)
		}
		p.ClientID = strings.TrimSpace(p.ClientID)
		if p.ClientID == "" {
			return fmt.Errorf("%s: set the client ID from the provider's app registration", p.Name)
		}
		if len(p.Scopes) == 0 {
			p.Scopes = []string{"openid", "email", "profile"}
		}
		if !slices.Contains(p.Scopes, "openid") {
			p.Scopes = append([]string{"openid"}, p.Scopes...)
		}
		p.MetadataURL, p.MetadataXML, p.EmailAttr, p.NameAttr = "", "", "", ""
	case ssoSAML:
		p.MetadataURL = strings.TrimSpace(p.MetadataURL)
		p.MetadataXML = strings.TrimSpace(p.MetadataXML)
		if p.MetadataURL == "" && p.MetadataXML == "" {
			return fmt.Errorf("%s: give the identity provider's metadata URL or paste its metadata XML", p.Name)
		}
		if p.MetadataURL != "" {
			if err := checkProviderURL(p.MetadataURL); err != nil {
				return fmt.Errorf("%s: the metadata URL %w", p.Name, err)
			}
			p.MetadataXML = ""
		}
		p.Issuer, p.ClientID, p.Scopes = "", "", nil
	default:
		return fmt.Errorf("%s: the kind is oidc or saml", p.Name)
	}
	p.GroupsClaim = strings.TrimSpace(p.GroupsClaim)
	p.EmailAttr, p.NameAttr = strings.TrimSpace(p.EmailAttr), strings.TrimSpace(p.NameAttr)
	var domains []string
	for _, d := range p.AllowedDomains {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))
		if d != "" && !slices.Contains(domains, d) {
			domains = append(domains, d)
		}
	}
	p.AllowedDomains = domains
	p.RoleMap = strings.TrimSpace(p.RoleMap)
	m, err := newRoleMapping(p.RoleMap, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", p.Name, err)
	}
	if p.DefaultRole != "" && parseRole(p.DefaultRole) == roleNone {
		return fmt.Errorf("%s: the default role is viewer, editor, admin or none", p.Name)
	}
	if len(m.entries) == 0 && p.DefaultRole == "" {
		return fmt.Errorf("%s: map a group to a role or set a default role; otherwise nobody can sign in", p.Name)
	}
	if len(m.entries) > 0 && p.GroupsClaim == "" && !onlyAny(m) {
		return fmt.Errorf("%s: set the groups claim the role mapping reads", p.Name)
	}
	return nil
}

func onlyAny(m *roleMapping) bool {
	for _, e := range m.entries {
		if e.Provider != "*" {
			return false
		}
	}
	return true
}

// checkProviderURL accepts https, and http only on this machine, for trying
// a provider out locally.
func checkProviderURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("must be a full URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return errors.New("must start with https://")
}

// ssoSeal encrypts v with the session key, bound to purpose.
func (s *Server) ssoSeal(purpose string, plain []byte) string {
	aead := *s.auth.aead.Load()
	nonce := make([]byte, aead.NonceSize())
	_, _ = rand.Read(nonce)
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, []byte(purpose)))
}

func (s *Server) ssoUnseal(purpose, value string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	aead := *s.auth.aead.Load()
	ns := aead.NonceSize()
	if err != nil || len(raw) < ns {
		return nil, false
	}
	plain, err := aead.Open(nil, raw[:ns], raw[ns:], []byte(purpose))
	return plain, err == nil
}

// setFlowCookie keeps what a sign-in started with until the provider sends
// the person back. crossPost is for SAML, which comes back as a cross-site
// POST: only SameSite=None cookies come with it, and those need HTTPS.
func (s *Server) setFlowCookie(w http.ResponseWriter, r *http.Request, name, value string, crossPost bool) {
	c := &http.Cookie{
		Name: name, Value: value, Path: "/api/sso/", MaxAge: int(ssoFlowTTL.Seconds()),
		HttpOnly: true, Secure: s.auth.secure(r), SameSite: http.SameSiteLaxMode,
	}
	if crossPost && c.Secure {
		c.SameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, c)
}

func (s *Server) clearFlowCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/api/sso/", MaxAge: -1, HttpOnly: true, Secure: s.auth.secure(r), SameSite: http.SameSiteLaxMode})
}

// ssoFlowTTL is how long someone has at the provider to sign in.
const ssoFlowTTL = 10 * time.Minute

func ssoFail(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/?sso_error="+url.QueryEscape(sentence(msg)), http.StatusSeeOther)
}
