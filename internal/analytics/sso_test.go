package analytics

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/crewjam/saml"
	"github.com/go-jose/go-jose/v4"
)

const testPublicURL = "http://visionary.test"

// newSSOServer is a server on a fresh SQLite file with sign-in not set up
// yet, so the first calls act as the local admin.
func newSSOServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t, "sso.db", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	t.Cleanup(func() { _ = s.db.Close() })
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	s.loadSessionSecret(ctx)
	if err := s.refreshLive(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

// call runs one request through the full handler.
func call(t *testing.T, h http.Handler, method, target string, body any, cookies []*http.Cookie, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, testPublicURL+target, rd)
	req.RemoteAddr = "192.0.2.10:5000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func cookieNamed(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name && c.MaxAge >= 0 && c.Value != "" {
			return c
		}
	}
	return nil
}

// ---------- A fake OpenID provider ----------

type fakeOIDC struct {
	srv  *httptest.Server
	key  *rsa.PrivateKey
	kid  string
	mu   sync.Mutex
	auth map[string]fakeGrant
	// Claims go into the next ID token; signWith and nonce, when set, make
	// it wrong in that way.
	claims   map[string]any
	signWith *rsa.PrivateKey
	nonce    string
}

type fakeGrant struct{ nonce, challenge string }

func newFakeOIDC(t *testing.T) *fakeOIDC {
	f := &fakeOIDC{key: mustRSA(t), kid: "k1", auth: map[string]fakeGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{
			"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize", "token_endpoint": f.srv.URL + "/token",
			"jwks_uri": f.srv.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: f.kid, Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		g, ok := f.auth[r.PostForm.Get("code")]
		delete(f.auth, r.PostForm.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		id, secret, _ := r.BasicAuth()
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge || id != "visionary" || secret != "s3cret" {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "bad code, verifier or client"})
			return
		}
		writeJSON(w, 200, map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 300, "id_token": f.idToken(t, g.nonce)})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// authorize stands in for the person signing in at the provider: it reads
// the redirect Visionary sent and returns the callback URL with a code.
func (f *fakeOIDC) authorize(t *testing.T, location string) (string, url.Values) {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil || !strings.HasPrefix(location, f.srv.URL+"/authorize") {
		t.Fatalf("not sent to the provider: %q", location)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("missing PKCE, nonce or state: %v", q)
	}
	code := randomToken()
	f.mu.Lock()
	f.auth[code] = fakeGrant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
	f.mu.Unlock()
	cb, _ := url.Parse(q.Get("redirect_uri"))
	return cb.Path + "?code=" + code + "&state=" + url.QueryEscape(q.Get("state")), q
}

func (f *fakeOIDC) idToken(t *testing.T, nonce string) string {
	key := f.key
	if f.signWith != nil {
		key = f.signWith
	}
	if f.nonce != "" {
		nonce = f.nonce
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", f.kid))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claims := map[string]any{"iss": f.srv.URL, "aud": "visionary", "sub": "user-1", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "nonce": nonce}
	for k, v := range f.claims {
		claims[k] = v
	}
	raw, _ := json.Marshal(claims)
	jws, err := signer.Sign(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := jws.CompactSerialize()
	return out
}

func mustRSA(t *testing.T) *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// setupOIDC saves the provider and makes single sign-on the only way in.
func setupOIDC(t *testing.T, s *Server, f *fakeOIDC) http.Handler {
	t.Helper()
	h := s.Handler()
	w := call(t, h, "PUT", "/api/settings/sso", map[string]any{
		"public_url": testPublicURL,
		"providers": []map[string]any{{
			"id": "acme", "kind": "oidc", "name": "Acme SSO", "enabled": true, "issuer": f.srv.URL, "client_id": "visionary",
			"groups_claim": "groups", "role_map": "eng-admins=admin,eng=editor", "default_role": "viewer",
			"allowed_domains": []string{"acme.com"}, "create_users": true,
		}},
		"secrets": map[string]string{"acme": "s3cret"},
	}, nil)
	if w.Code != 200 {
		t.Fatalf("save sso: %d %s", w.Code, w.Body)
	}
	w = call(t, h, "PUT", "/api/settings/signin", map[string]any{"config": map[string]string{"type": "sso"}}, nil)
	if w.Code != 200 {
		t.Fatalf("save sign-in: %d %s", w.Code, w.Body)
	}
	return h
}

// oidcSignIn runs the whole flow and returns the final response.
func oidcSignIn(t *testing.T, h http.Handler, f *fakeOIDC) *httptest.ResponseRecorder {
	t.Helper()
	w := call(t, h, "GET", "/api/sso/acme/start", nil, nil)
	if w.Code != http.StatusFound {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	flow := cookieNamed(w, oidcCookie)
	if flow == nil || flow.SameSite != http.SameSiteLaxMode || !flow.HttpOnly {
		t.Fatalf("flow cookie: %+v", flow)
	}
	cb, _ := f.authorize(t, w.Header().Get("Location"))
	return call(t, h, "GET", cb, nil, []*http.Cookie{flow})
}

func ssoErrorOf(w *httptest.ResponseRecorder) string {
	u, _ := url.Parse(w.Header().Get("Location"))
	return u.Query().Get("sso_error")
}

func TestOIDCSignIn(t *testing.T) {
	s := newSSOServer(t)
	f := newFakeOIDC(t)
	h := setupOIDC(t, s, f)

	if w := call(t, h, "GET", "/api/session", nil, nil); w.Code != 401 {
		t.Fatalf("sign-in should be required now: %d", w.Code)
	}
	var cfg map[string]any
	_ = json.Unmarshal(call(t, h, "GET", "/api/auth/config", nil, nil).Body.Bytes(), &cfg)
	if cfg["type"] != "sso" || len(cfg["sso"].([]any)) != 1 {
		t.Fatalf("auth config: %v", cfg)
	}

	f.claims = map[string]any{"email": "Alice@acme.com", "email_verified": true, "name": "Alice", "groups": []string{"eng", "eng-admins"}}
	w := oidcSignIn(t, h, f)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("callback: %d %s %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	sess := cookieNamed(w, sessionCookie)
	if sess == nil || sess.SameSite != http.SameSiteStrictMode {
		t.Fatalf("no strict session cookie: %+v", sess)
	}
	w = call(t, h, "GET", "/api/session", nil, []*http.Cookie{sess})
	var view struct {
		Role string `json:"role"`
		User struct {
			ID, Handle string
		} `json:"user"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &view)
	if w.Code != 200 || view.Role != "admin" || view.User.Handle != "alice@acme.com" {
		t.Fatalf("session: %d %s", w.Code, w.Body)
	}
	a, _ := s.findAccount(context.Background(), "username", "alice@acme.com")
	if a == nil || a.Source != ssoOIDC || a.Role != "admin" || a.hash != "" {
		t.Fatalf("account: %+v", a)
	}

	// Disabling the account in Settings signs the person out at once.
	if w := call(t, h, "PUT", "/api/team/accounts/"+a.ID, map[string]any{"disabled": true}, []*http.Cookie{sess}); w.Code != 200 {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "GET", "/api/session", nil, []*http.Cookie{sess}); w.Code != 401 {
		t.Fatalf("disabled account kept its session: %d", w.Code)
	}
}

func TestOIDCRefusals(t *testing.T) {
	s := newSSOServer(t)
	f := newFakeOIDC(t)
	h := setupOIDC(t, s, f)
	good := map[string]any{"email": "bob@acme.com", "email_verified": true, "groups": []string{"eng"}}

	cases := []struct {
		name  string
		setup func()
		want  string
	}{
		{"other domain", func() { f.claims = map[string]any{"email": "eve@evil.com", "email_verified": true} }, "can't sign in here"},
		{"unverified email", func() { f.claims = map[string]any{"email": "bob@acme.com", "email_verified": false} }, "isn't verified"},
		{"no email", func() { f.claims = map[string]any{} }, "didn't send an email"},
		{"wrong key", func() { f.claims, f.signWith = good, mustRSA(t) }, "isn't valid"},
		{"wrong nonce", func() { f.claims, f.nonce = good, "replayed" }, "nonce"},
	}
	for _, c := range cases {
		f.claims, f.signWith, f.nonce = nil, nil, ""
		c.setup()
		w := oidcSignIn(t, h, f)
		if msg := ssoErrorOf(w); !strings.Contains(msg, c.want) || cookieNamed(w, sessionCookie) != nil {
			t.Errorf("%s: got %d %q", c.name, w.Code, msg)
		}
	}

	// A callback without the browser's flow cookie, or with another state.
	f.claims, f.signWith, f.nonce = good, nil, ""
	w := call(t, h, "GET", "/api/sso/acme/start", nil, nil)
	cb, _ := f.authorize(t, w.Header().Get("Location"))
	if msg := ssoErrorOf(call(t, h, "GET", cb, nil, nil)); !strings.Contains(msg, "expired") {
		t.Errorf("no cookie: %q", msg)
	}
	w = call(t, h, "GET", "/api/sso/acme/start", nil, nil)
	cb, _ = f.authorize(t, w.Header().Get("Location"))
	forged := strings.Replace(cb, "state=", "state=x", 1)
	if msg := ssoErrorOf(call(t, h, "GET", forged, nil, []*http.Cookie{cookieNamed(w, oidcCookie)})); !strings.Contains(msg, "expired") {
		t.Errorf("forged state: %q", msg)
	}
}

// A test sign-in shows what would happen and signs nobody in.
func TestOIDCTestSignIn(t *testing.T) {
	s := newSSOServer(t)
	f := newFakeOIDC(t)
	h := s.Handler()
	w := call(t, h, "PUT", "/api/settings/sso", map[string]any{
		"public_url": testPublicURL,
		"providers": []map[string]any{{"id": "acme", "kind": "oidc", "name": "Acme", "enabled": false, "issuer": f.srv.URL,
			"client_id": "visionary", "default_role": "viewer", "create_users": true}},
		"secrets": map[string]string{"acme": "s3cret"},
	}, nil)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	f.claims = map[string]any{"email": "carol@acme.com"}
	w = call(t, h, "GET", "/api/sso/acme/test", nil, nil)
	cb, _ := f.authorize(t, w.Header().Get("Location"))
	w = call(t, h, "GET", cb, nil, []*http.Cookie{cookieNamed(w, oidcCookie)})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "would be signed in as viewer") || cookieNamed(w, sessionCookie) != nil {
		t.Fatalf("test page: %d %s", w.Code, w.Body)
	}
	if a, _ := s.findAccount(context.Background(), "username", "carol@acme.com"); a != nil {
		t.Fatal("a test sign-in created an account")
	}
}

// ---------- SAML ----------

type fakeIdP struct {
	idp  *saml.IdentityProvider
	meta string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	key := mustRSA(t)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "idp"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	meta, _ := url.Parse("https://idp.test/metadata")
	sso, _ := url.Parse("https://idp.test/sso")
	idp := &saml.IdentityProvider{Key: key, Signer: key, Certificate: cert, MetadataURL: *meta, SSOURL: *sso}
	raw, err := xml.Marshal(idp.Metadata())
	if err != nil {
		t.Fatal(err)
	}
	return &fakeIdP{idp: idp, meta: string(raw)}
}

// respond signs a response to the AuthnRequest Visionary sent, as the IdP
// would after the person signs in, and returns the form it would post.
func (f *fakeIdP) respond(t *testing.T, h http.Handler, location string, who *saml.Session) url.Values {
	t.Helper()
	u, _ := url.Parse(location)
	relay := u.Query().Get("RelayState")
	if !strings.HasPrefix(location, "https://idp.test/sso?") || relay == "" || len(relay) > 80 {
		t.Fatalf("redirect: %q", location)
	}
	w := call(t, h, "GET", "/api/sso/okta/metadata", nil, nil)
	var sp saml.EntityDescriptor
	if err := xml.Unmarshal(w.Body.Bytes(), &sp); err != nil {
		t.Fatalf("metadata: %v %s", err, w.Body)
	}
	req := &saml.IdpAuthnRequest{
		IDP: f.idp, RelayState: relay, Now: time.Now(), HTTPRequest: httptest.NewRequest("GET", location, nil),
		Request:                 saml.AuthnRequest{ID: samlRequestID(relay)},
		ServiceProviderMetadata: &sp, SPSSODescriptor: &sp.SPSSODescriptors[0],
		ACSEndpoint: &sp.SPSSODescriptors[0].AssertionConsumerServices[0],
	}
	if err := (saml.DefaultAssertionMaker{}).MakeAssertion(req, who); err != nil {
		t.Fatal(err)
	}
	form, err := req.PostBinding()
	if err != nil {
		t.Fatal(err)
	}
	return url.Values{"SAMLResponse": {form.SAMLResponse}, "RelayState": {form.RelayState}}
}

func postACS(t *testing.T, h http.Handler, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	return call(t, h, "POST", "/api/sso/okta/acs", form.Encode(), cookies, "Content-Type", "application/x-www-form-urlencoded")
}

func TestSAMLSignIn(t *testing.T) {
	s := newSSOServer(t)
	idp := newFakeIdP(t)
	h := s.Handler()
	w := call(t, h, "PUT", "/api/settings/sso", map[string]any{
		"public_url": testPublicURL,
		"providers": []map[string]any{{"id": "okta", "kind": "saml", "name": "Okta", "enabled": true, "metadata_xml": idp.meta,
			"groups_claim": "eduPersonAffiliation", "role_map": "ops=editor", "create_users": true}},
	}, nil)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	var view ssoView
	_ = json.Unmarshal(w.Body.Bytes(), &view)
	if !strings.Contains(view.SPCert, "BEGIN CERTIFICATE") || view.Config.Providers[0].ACSURL != testPublicURL+"/api/sso/okta/acs" {
		t.Fatalf("view: %+v", view)
	}
	if w := call(t, h, "PUT", "/api/settings/signin", map[string]any{"config": map[string]string{"type": "sso"}}, nil); w.Code != 200 {
		t.Fatalf("sign-in: %d %s", w.Code, w.Body)
	}
	bob := &saml.Session{ID: "s1", NameID: "bob@acme.com", UserEmail: "bob@acme.com", UserCommonName: "Bob", Groups: []string{"ops"}}

	start := call(t, h, "GET", "/api/sso/okta/start", nil, nil)
	bind := cookieNamed(start, samlCookie)
	form := idp.respond(t, h, start.Header().Get("Location"), bob)
	w = postACS(t, h, form, []*http.Cookie{bind})
	sess := cookieNamed(w, sessionCookie)
	if w.Code != http.StatusSeeOther || sess == nil {
		t.Fatalf("acs: %d %s %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	if w := call(t, h, "GET", "/api/session", nil, []*http.Cookie{sess}); !strings.Contains(w.Body.String(), `"role":"editor"`) {
		t.Fatalf("session: %s", w.Body)
	}

	// The same response again, a tampered one, and one for another request.
	if msg := ssoErrorOf(postACS(t, h, form, []*http.Cookie{bind})); !strings.Contains(msg, "already") {
		t.Errorf("replayed: %q", msg)
	}
	start = call(t, h, "GET", "/api/sso/okta/start", nil, nil)
	form = idp.respond(t, h, start.Header().Get("Location"), bob)
	raw, _ := base64.StdEncoding.DecodeString(form.Get("SAMLResponse"))
	tampered := url.Values{"SAMLResponse": {base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(raw), "https://idp.test/metadata", "https://evil.test/metadata", 1)))}, "RelayState": {form.Get("RelayState")}}
	if msg := ssoErrorOf(postACS(t, h, tampered, []*http.Cookie{cookieNamed(start, samlCookie)})); !strings.Contains(msg, "isn't valid") {
		t.Errorf("tampered: %q", msg)
	}
	other := call(t, h, "GET", "/api/sso/okta/start", nil, nil)
	form.Set("RelayState", mustQuery(other.Header().Get("Location"), "RelayState"))
	if msg := ssoErrorOf(postACS(t, h, form, nil)); !strings.Contains(msg, "isn't valid") {
		t.Errorf("response to another request: %q", msg)
	}
}

func mustQuery(location, key string) string {
	u, _ := url.Parse(location)
	return u.Query().Get(key)
}
