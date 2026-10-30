package analytics

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OpenID Connect: the authorization code flow with PKCE, a state bound to
// the browser by a cookie, and a nonce in the ID token, which is checked
// against the provider's published keys (JWKS).

const oidcCookie = "visionary_oidc"

// ssoHTTP calls identity providers: discovery, keys, tokens, metadata.
var ssoHTTP = &http.Client{Timeout: 15 * time.Second}

type oidcClient struct {
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
}

// oidcFlow is what the cookie carries between the start and the callback.
type oidcFlow struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Test     bool   `json:"t,omitempty"`
	Expires  int64  `json:"e"`
}

func ssoContext(ctx context.Context) context.Context {
	return context.WithValue(oidc.ClientContext(ctx, ssoHTTP), oauth2.HTTPClient, ssoHTTP)
}

// oidcFor reads the provider's discovery document once per setup.
func (s *Server) oidcFor(ctx context.Context, st *ssoSetup, p *ssoProvider) (*oidcClient, error) {
	state := s.sso()
	state.mu.Lock()
	c := state.oidc[p.ID]
	state.mu.Unlock()
	if c != nil {
		return c, nil
	}
	c, err := newOIDCClient(ctx, p, st.secrets[p.ID], st.cfg.PublicURL)
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	if state.setup.Load() == st {
		state.oidc[p.ID] = c
	}
	state.mu.Unlock()
	return c, nil
}

func newOIDCClient(ctx context.Context, p *ssoProvider, secret, publicURL string) (*oidcClient, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	prov, err := oidc.NewProvider(ssoContext(cctx), p.Issuer)
	if err != nil {
		var mismatch *oidc.IssuerMismatchError
		if errors.As(err, &mismatch) {
			return nil, fmt.Errorf("%s: the issuer in its discovery document is %q, not %q; use that as the issuer URL", p.Name, mismatch.Discovered, mismatch.Provided)
		}
		return nil, fmt.Errorf("%s: can't read %s/.well-known/openid-configuration: %v", p.Name, p.Issuer, err)
	}
	q := *p
	q.urls(publicURL)
	return &oidcClient{
		provider: prov,
		// Keys are fetched from the provider's jwks_uri and refreshed when
		// a token is signed with one not seen yet.
		verifier: prov.Verifier(&oidc.Config{ClientID: p.ClientID}),
		oauth: oauth2.Config{
			ClientID: p.ClientID, ClientSecret: secret, Endpoint: prov.Endpoint(),
			RedirectURL: q.RedirectURL, Scopes: p.Scopes,
		},
	}, nil
}

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request, st *ssoSetup, p *ssoProvider, test bool) {
	c, err := s.oidcFor(r.Context(), st, p)
	if err != nil {
		s.logger.Warn("single sign-on start failed", "provider", p.ID, "err", err)
		s.ssoError(w, r, test, err.Error())
		return
	}
	f := oidcFlow{Provider: p.ID, State: randomToken(), Nonce: randomToken(), Verifier: oauth2.GenerateVerifier(), Test: test, Expires: time.Now().Add(ssoFlowTTL).Unix()}
	plain, _ := json.Marshal(f)
	s.setFlowCookie(w, r, oidcCookie, s.ssoSeal(oidcCookie, plain), false)
	http.Redirect(w, r, c.oauth.AuthCodeURL(f.State, oauth2.S256ChallengeOption(f.Verifier), oidc.Nonce(f.Nonce)), http.StatusFound)
}

// handleOIDCCallback is where the provider sends people back with a code.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	var f oidcFlow
	ck, err := r.Cookie(oidcCookie)
	s.clearFlowCookie(w, r, oidcCookie)
	if err == nil {
		if plain, ok := s.ssoUnseal(oidcCookie, ck.Value); ok {
			_ = json.Unmarshal(plain, &f)
		}
	}
	q := r.URL.Query()
	id := r.PathValue("id")
	if f.Provider != id || time.Now().Unix() > f.Expires || q.Get("state") == "" ||
		subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(f.State)) != 1 {
		ssoFail(w, r, "That sign-in link has expired or was started in another browser. Try again.")
		return
	}
	st := s.ssoSetup()
	p := st.provider(id)
	if f.Test {
		p = st.anyProvider(id)
	}
	if p == nil || p.Kind != ssoOIDC {
		s.ssoError(w, r, f.Test, "that single sign-on provider isn't turned on")
		return
	}
	if e := q.Get("error"); e != "" {
		msg := e
		if d := q.Get("error_description"); d != "" {
			msg = d
		}
		s.auditAs(r, nil, "auth.sign_in_failed", "", map[string]any{"mode": ssoOIDC, "provider": p.ID, "error": clip(msg, 200)})
		s.ssoError(w, r, f.Test, p.Name+" didn't sign you in: "+msg)
		return
	}
	if !f.Test && !s.auth.allowLogin(r) {
		ssoFail(w, r, "Too many attempts. Try again in a minute.")
		return
	}
	ident, err := s.oidcIdentity(r.Context(), st, p, &f, q.Get("code"))
	if err != nil {
		s.logger.Warn("single sign-on failed", "provider", p.ID, "err", err)
		if !f.Test {
			s.auditAs(r, nil, "auth.sign_in_failed", "", map[string]any{"mode": ssoOIDC, "provider": p.ID, "error": clip(err.Error(), 200)})
		}
		s.ssoError(w, r, f.Test, err.Error())
		return
	}
	s.ssoComplete(w, r, p, ident, f.Test)
}

// oidcIdentity exchanges the code and verifies the ID token: signature,
// issuer, audience, expiry and nonce.
func (s *Server) oidcIdentity(ctx context.Context, st *ssoSetup, p *ssoProvider, f *oidcFlow, code string) (*ssoIdentity, error) {
	if code == "" {
		return nil, errors.New("the provider sent no authorization code")
	}
	c, err := s.oidcFor(ctx, st, p)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ssoContext(ctx), 20*time.Second)
	defer cancel()
	tok, err := c.oauth.Exchange(cctx, code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode != "" {
			return nil, fmt.Errorf("%s refused the code: %s %s (check the client ID, the client secret and the redirect URL)", p.Name, re.ErrorCode, re.ErrorDescription)
		}
		return nil, fmt.Errorf("exchanging the code with %s failed: %v", p.Name, err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, fmt.Errorf("%s sent no ID token; is the openid scope allowed for this app?", p.Name)
	}
	idt, err := c.verifier.Verify(cctx, raw)
	if err != nil {
		return nil, fmt.Errorf("the ID token from %s isn't valid: %v", p.Name, err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(f.Nonce)) != 1 {
		return nil, errors.New("the ID token's nonce doesn't match this sign-in")
	}
	claims := map[string]any{}
	if err := idt.Claims(&claims); err != nil {
		return nil, fmt.Errorf("the ID token's claims can't be read: %v", err)
	}
	if _, ok := claims["email"]; !ok && c.provider.UserInfoEndpoint() != "" {
		// Some providers put the email only in userinfo.
		if ui, err := c.provider.UserInfo(cctx, oauth2.StaticTokenSource(tok)); err == nil && ui.Subject == idt.Subject {
			extra := map[string]any{}
			if ui.Claims(&extra) == nil {
				for k, v := range extra {
					if _, ok := claims[k]; !ok {
						claims[k] = v
					}
				}
			}
		}
	}
	id := &ssoIdentity{Subject: idt.Subject, Claims: claims}
	id.Email = claimString(claims, "email")
	if v, ok := claims["email_verified"]; ok {
		b := v == true || strings.EqualFold(fmt.Sprint(v), "true")
		id.EmailVerified = &b
	}
	id.Name = claimString(claims, "name")
	if id.Name == "" {
		id.Name = strings.TrimSpace(claimString(claims, "given_name") + " " + claimString(claims, "family_name"))
	}
	if p.GroupsClaim != "" {
		id.Groups = claimList(claims, p.GroupsClaim)
	}
	return id, nil
}

// claimValue finds a claim by its exact name first (namespaced claims such as
// https://example.com/roles contain dots), then as a dotted path.
func claimValue(claims map[string]any, name string) (any, bool) {
	if v, ok := claims[name]; ok {
		return v, true
	}
	var cur any = claims
	for _, part := range strings.Split(name, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func claimString(claims map[string]any, name string) string {
	v, _ := claimValue(claims, name)
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// claimList reads a list claim. It is nil when the claim is missing, so a
// provider that leaves it out doesn't look like someone in no group.
func claimList(claims map[string]any, name string) []string {
	v, ok := claimValue(claims, name)
	if !ok {
		return nil
	}
	out := []string{}
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			switch e := e.(type) {
			case string:
				out = append(out, e)
			case float64:
				out = append(out, strconv.FormatFloat(e, 'f', -1, 64))
			}
		}
	case string:
		for _, e := range strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == ' ' }) {
			out = append(out, e)
		}
	}
	return out
}
