package analytics

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewjam/saml"
)

// The request ID is derived from a sealed RelayState, so any server sharing
// the database can check InResponseTo without keeping state

const (
	samlCookie   = "visionary_saml"
	samlRelay    = "visionary/saml-relay"
	samlMetaTTL  = 24 * time.Hour
	samlMaxBytes = 1 << 20
)

type samlClient struct {
	sp      *saml.ServiceProvider
	fetched time.Time
}

func (s *Server) samlFor(ctx context.Context, st *ssoSetup, p *ssoProvider) (*samlClient, error) {
	state := s.sso()
	state.mu.Lock()
	c := state.saml[p.ID]
	state.mu.Unlock()
	if c != nil && (p.MetadataURL == "" || time.Since(c.fetched) < samlMetaTTL) {
		return c, nil
	}
	next, err := newSAMLClient(ctx, p, st.spPEM, st.cfg.PublicURL)
	if err != nil {
		if c != nil {
			s.logger.Warn("reading SAML metadata again failed; keeping the copy from before", "provider", p.ID, "err", err)
			c.fetched = time.Now()
			return c, nil
		}
		return nil, err
	}
	state.mu.Lock()
	if state.setup.Load() == st {
		state.saml[p.ID] = next
	}
	state.mu.Unlock()
	return next, nil
}

func newSAMLClient(ctx context.Context, p *ssoProvider, spPEM, publicURL string) (*samlClient, error) {
	key, cert, err := parseSPKeyPair(spPEM)
	if err != nil {
		return nil, err
	}
	raw := []byte(p.MetadataXML)
	if p.MetadataURL != "" {
		if raw, err = fetchMetadata(ctx, p.MetadataURL); err != nil {
			return nil, fmt.Errorf("%s: can't read the metadata at %s: %v", p.Name, p.MetadataURL, err)
		}
	}
	idp, err := parseIDPMetadata(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: the identity provider's metadata %v", p.Name, err)
	}
	q := *p
	q.urls(publicURL)
	acs, _ := url.Parse(q.ACSURL)
	meta, _ := url.Parse(q.EntityID)
	sp := &saml.ServiceProvider{
		EntityID: q.EntityID, Key: key, Certificate: cert, MetadataURL: *meta, AcsURL: *acs,
		IDPMetadata: idp, AuthnNameIDFormat: saml.EmailAddressNameIDFormat,
	}
	if sp.GetSSOBindingLocation(saml.HTTPRedirectBinding) == "" {
		return nil, fmt.Errorf("%s: the identity provider's metadata has no HTTP-Redirect sign-in endpoint", p.Name)
	}
	signing := false
	for _, d := range idp.IDPSSODescriptors {
		for _, k := range d.KeyDescriptors {
			if k.Use == "" || k.Use == "signing" {
				signing = len(k.KeyInfo.X509Data.X509Certificates) > 0 || signing
			}
		}
	}
	if !signing {
		return nil, fmt.Errorf("%s: the identity provider's metadata has no signing certificate, so its responses can't be checked", p.Name)
	}
	return &samlClient{sp: sp, fetched: time.Now()}, nil
}

func fetchMetadata(ctx context.Context, u string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := ssoHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("it answered %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, samlMaxBytes))
}

func parseIDPMetadata(raw []byte) (*saml.EntityDescriptor, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, errors.New("is empty")
	}
	var ed saml.EntityDescriptor
	if err := xml.Unmarshal(raw, &ed); err == nil && len(ed.IDPSSODescriptors) > 0 {
		return &ed, nil
	}
	var all saml.EntitiesDescriptor
	if err := xml.Unmarshal(raw, &all); err == nil {
		for i := range all.EntityDescriptors {
			if len(all.EntityDescriptors[i].IDPSSODescriptors) > 0 {
				return &all.EntityDescriptors[i], nil
			}
		}
	}
	return nil, errors.New("isn't SAML metadata for an identity provider (no IDPSSODescriptor)")
}

func newSPKeyPair() (string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "Visionary SAML"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	out := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	return string(out), nil
}

func parseSPKeyPair(bundle string) (crypto.Signer, *x509.Certificate, error) {
	if bundle == "" {
		return nil, nil, errors.New("the SAML key isn't set up yet; save the provider again")
	}
	var key *rsa.PrivateKey
	var cert *x509.Certificate
	rest := []byte(bundle)
	for {
		var b *pem.Block
		if b, rest = pem.Decode(rest); b == nil {
			break
		}
		switch b.Type {
		case "RSA PRIVATE KEY":
			key, _ = x509.ParsePKCS1PrivateKey(b.Bytes)
		case "PRIVATE KEY":
			if k, err := x509.ParsePKCS8PrivateKey(b.Bytes); err == nil {
				key, _ = k.(*rsa.PrivateKey)
			}
		case "CERTIFICATE":
			if cert == nil {
				cert, _ = x509.ParseCertificate(b.Bytes)
			}
		}
	}
	switch {
	case key == nil:
		return nil, nil, errors.New("the SAML private key must be an RSA key in PEM")
	case cert == nil:
		return nil, nil, errors.New("the SAML certificate must be in PEM")
	}
	if pub, ok := cert.PublicKey.(*rsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		return nil, nil, errors.New("the SAML certificate isn't for that private key")
	}
	return key, cert, nil
}

func spCertPEM(bundle string) string {
	_, cert, err := parseSPKeyPair(bundle)
	if err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
}

func (s *Server) ensureSPKeyPair(ctx context.Context) error {
	m, err := s.readSettings(ctx)
	if err != nil {
		return err
	}
	if m[settingSAMLSP][0] != "" {
		return nil
	}
	bundle, err := newSPKeyPair()
	if err != nil {
		return err
	}
	return dbErr(s.db.Exec(ctx, s.q.insertSettingIfAbsent(), settingSAMLSP, bundle, nowTS()))
}

func (s *Server) samlRelayState(test bool) string {
	plain := make([]byte, 17)
	binary.BigEndian.PutUint64(plain, uint64(time.Now().Add(ssoFlowTTL).Unix()))
	if test {
		plain[8] = 1
	}
	_, _ = rand.Read(plain[9:])
	return s.ssoSeal(samlRelay, plain)
}

func samlRequestID(relay string) string {
	sum := sha256.Sum256([]byte("request:" + relay))
	return "id-" + hex.EncodeToString(sum[:20])
}

func samlBinding(relay string) string {
	sum := sha256.Sum256([]byte("browser:" + relay))
	return hex.EncodeToString(sum[:16])
}

func (s *Server) samlStart(w http.ResponseWriter, r *http.Request, st *ssoSetup, p *ssoProvider, test bool) {
	c, err := s.samlFor(r.Context(), st, p)
	if err != nil {
		s.logger.Warn("single sign-on start failed", "provider", p.ID, "err", err)
		s.ssoError(w, r, test, err.Error())
		return
	}
	relay := s.samlRelayState(test)
	req, err := c.sp.MakeAuthenticationRequest(c.sp.GetSSOBindingLocation(saml.HTTPRedirectBinding), saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		s.ssoError(w, r, test, err.Error())
		return
	}
	req.ID = samlRequestID(relay)
	u, err := req.Redirect(relay, c.sp)
	if err != nil {
		s.ssoError(w, r, test, err.Error())
		return
	}
	s.setFlowCookie(w, r, samlCookie, samlBinding(relay), true)
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) handleSAMLMetadata(w http.ResponseWriter, r *http.Request) {
	st := s.ssoSetup()
	p := st.anyProvider(r.PathValue("id"))
	if p == nil || p.Kind != ssoSAML {
		writeError(w, http.StatusNotFound, "no such SAML provider")
		return
	}
	key, cert, err := parseSPKeyPair(st.spPEM)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	q := *p
	q.urls(st.cfg.PublicURL)
	acs, _ := url.Parse(q.ACSURL)
	meta, _ := url.Parse(q.EntityID)
	sp := &saml.ServiceProvider{EntityID: q.EntityID, Key: key, Certificate: cert, MetadataURL: *meta, AcsURL: *acs, AuthnNameIDFormat: saml.EmailAddressNameIDFormat}
	out, err := xml.MarshalIndent(sp.Metadata(), "", "  ")
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	w.Header().Set("Content-Disposition", `inline; filename="visionary-`+p.ID+`.xml"`)
	_, _ = w.Write(out)
}

func (s *Server) handleSAMLACS(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, samlMaxBytes)
	if err := r.ParseForm(); err != nil {
		ssoFail(w, r, "The identity provider's response couldn't be read.")
		return
	}
	relay := r.PostForm.Get("RelayState")
	plain, ok := s.ssoUnseal(samlRelay, relay)
	if !ok || len(plain) != 17 || time.Now().Unix() > int64(binary.BigEndian.Uint64(plain)) {
		// Sign-in started at the identity provider has no RelayState of ours
		ssoFail(w, r, "That sign-in has expired or didn't start here. Start it from Visionary's sign-in page.")
		return
	}
	test := plain[8] == 1
	ck, err := r.Cookie(samlCookie)
	s.clearFlowCookie(w, r, samlCookie)
	if err == nil {
		if !constantTimeEqual(ck.Value, samlBinding(relay)) {
			ssoFail(w, r, "That sign-in was started in another browser. Try again.")
			return
		}
	} else if s.auth.secure(r) {
		ssoFail(w, r, "Your browser didn't keep the sign-in cookie. Allow cookies for this site and try again.")
		return
	}
	st := s.ssoSetup()
	id := r.PathValue("id")
	p := st.provider(id)
	if test {
		p = st.anyProvider(id)
	}
	if p == nil || p.Kind != ssoSAML {
		s.ssoError(w, r, test, "that single sign-on provider isn't turned on")
		return
	}
	if !test && !s.auth.allowLogin(r) {
		ssoFail(w, r, "Too many attempts. Try again in a minute.")
		return
	}
	c, err := s.samlFor(r.Context(), st, p)
	if err != nil {
		s.ssoError(w, r, test, err.Error())
		return
	}
	assertion, err := c.sp.ParseResponse(r, []string{samlRequestID(relay)})
	if err != nil {
		var ie *saml.InvalidResponseError
		if errors.As(err, &ie) && ie.PrivateErr != nil {
			err = ie.PrivateErr
		}
		s.logger.Warn("SAML response refused", "provider", p.ID, "err", err)
		if !test {
			s.auditAs(r, nil, "auth.sign_in_failed", "", map[string]any{"mode": ssoSAML, "provider": p.ID, "error": clip(err.Error(), 200)})
		}
		s.ssoError(w, r, test, "the response from "+p.Name+" isn't valid: "+err.Error())
		return
	}
	if id := samlRequestID(relay); !s.sso().firstUse(id) || !s.firstUse(r.Context(), "saml:"+id, ssoFlowTTL) {
		ssoFail(w, r, "That sign-in was already used. Start again from the sign-in page.")
		return
	}
	s.ssoComplete(w, r, p, samlIdentity(p, assertion), test)
}

// s.firstUse asks the other replicas. Any response is refused 90 seconds after it was issued
func (st *ssoState) firstUse(requestID string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	for k, at := range st.used {
		if now.Sub(at) > ssoFlowTTL {
			delete(st.used, k)
		}
	}
	if _, ok := st.used[requestID]; ok {
		return false
	}
	st.used[requestID] = now
	return true
}

// Where providers usually put the email, name and groups when no attribute
// is named in Settings
var (
	samlEmailNames = []string{"email", "mail", "emailaddress", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress", "urn:oid:0.9.2342.19200300.100.1.3"}
	samlNameNames  = []string{"name", "displayname", "http://schemas.microsoft.com/identity/claims/displayname", "urn:oid:2.16.840.1.113730.3.1.241"}
)

func samlIdentity(p *ssoProvider, a *saml.Assertion) *ssoIdentity {
	attrs := map[string][]string{}
	for _, st := range a.AttributeStatements {
		for _, at := range st.Attributes {
			var vals []string
			for _, v := range at.Values {
				if v := strings.TrimSpace(v.Value); v != "" {
					vals = append(vals, v)
				}
			}
			for _, n := range []string{at.Name, at.FriendlyName} {
				if n != "" {
					attrs[strings.ToLower(n)] = append(attrs[strings.ToLower(n)], vals...)
				}
			}
		}
	}
	first := func(names ...string) string {
		for _, n := range names {
			if n == "" {
				continue
			}
			if v := attrs[strings.ToLower(n)]; len(v) > 0 {
				return v[0]
			}
		}
		return ""
	}
	id := &ssoIdentity{Claims: map[string]any{}}
	if a.Subject != nil && a.Subject.NameID != nil {
		id.Subject = a.Subject.NameID.Value
		id.Claims["NameID"] = id.Subject
	}
	for k, v := range attrs {
		id.Claims[k] = v
	}
	if p.EmailAttr != "" {
		id.Email = first(p.EmailAttr)
	} else if id.Email = first(samlEmailNames...); id.Email == "" && strings.Contains(id.Subject, "@") {
		id.Email = id.Subject
	}
	if p.NameAttr != "" {
		id.Name = first(p.NameAttr)
	} else if id.Name = first(samlNameNames...); id.Name == "" {
		id.Name = strings.TrimSpace(first("givenname", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/givenname") + " " +
			first("surname", "sn", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/surname"))
	}
	if p.GroupsClaim != "" {
		if v, ok := attrs[strings.ToLower(p.GroupsClaim)]; ok {
			id.Groups = append([]string{}, v...)
		}
	}
	return id
}
