package analytics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// TCP, DNS and SSL certificate checks. Each connects through the same
// address guard as HTTP checks; a DNS check's own server is guarded too.

// TCPConfig is the settings of a TCP check: the port must accept a
// connection, and when Expect is set the first bytes it sends (after Send,
// if any) must contain it.
type TCPConfig struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Send   string `json:"send,omitempty"`
	Expect string `json:"expect,omitempty"`
}

// DNSConfig is the settings of a DNS check. Match is "" (any answer will
// do), contains (every value is among the answers) or equals (the answers
// are exactly the values). Server is empty for the system resolver.
type DNSConfig struct {
	Name       string   `json:"name"`
	RecordType string   `json:"record_type"`
	Server     string   `json:"server,omitempty"`
	Match      string   `json:"match,omitempty"`
	Values     []string `json:"values,omitempty"`
}

// SSLConfig is the settings of a certificate check. Below WarnDays left the
// check is degraded, below FailDays (or expired) it fails.
type SSLConfig struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	ServerName     string `json:"server_name,omitempty"`
	WarnDays       int    `json:"warn_days"`
	FailDays       int    `json:"fail_days"`
	MinTLS         string `json:"min_tls,omitempty"`
	IgnoreHostname bool   `json:"ignore_hostname,omitempty"`
	IgnoreChain    bool   `json:"ignore_chain,omitempty"`
}

var (
	dnsTypes    = []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS"}
	tlsVersions = map[string]uint16{"1.0": tls.VersionTLS10, "1.1": tls.VersionTLS11, "1.2": tls.VersionTLS12, "1.3": tls.VersionTLS13}
)

const (
	tcpMaxBanner = 4096
	dnsMaxValues = 20
)

func (t *TCPConfig) normalize() error {
	h, err := normalizeHost(t.Host)
	if err != nil {
		return err
	}
	t.Host = h
	if t.Port < 1 || t.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if len(t.Send) > 1024 || len(t.Expect) > 512 {
		return errors.New("send is limited to 1024 characters and expect to 512")
	}
	return nil
}

func (d *DNSConfig) normalize() error {
	d.Name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d.Name)), ".")
	if d.Name == "" || len(d.Name) > 253 || !hostNameRe.MatchString(d.Name) {
		return errors.New("give a valid name to resolve, such as example.com")
	}
	d.RecordType = strings.ToUpper(strings.TrimSpace(d.RecordType))
	if d.RecordType == "" {
		d.RecordType = "A"
	}
	if !slices.Contains(dnsTypes, d.RecordType) {
		return errors.New("record type must be A, AAAA, CNAME, MX, TXT or NS")
	}
	d.Server = strings.TrimSpace(d.Server)
	if d.Server != "" {
		host, port := d.Server, "53"
		if h, p, err := net.SplitHostPort(d.Server); err == nil {
			host, port = h, p
		}
		h, err := normalizeHost(host)
		if err != nil {
			return fmt.Errorf("DNS server: %w", err)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return errors.New("DNS server port must be between 1 and 65535")
		}
		d.Server = net.JoinHostPort(h, port)
	}
	if d.Match != "" && d.Match != "contains" && d.Match != "equals" {
		return errors.New("match must be contains or equals")
	}
	vals := d.Values[:0:0]
	for _, v := range d.Values {
		if v = strings.TrimSpace(v); v != "" {
			if len(v) > 500 {
				return errors.New("an expected value is too long")
			}
			vals = append(vals, v)
		}
	}
	if len(vals) > dnsMaxValues {
		return fmt.Errorf("at most %d expected values", dnsMaxValues)
	}
	d.Values = vals
	if d.Match != "" && len(vals) == 0 {
		return errors.New("give the values the answers must " + map[string]string{"contains": "contain", "equals": "equal"}[d.Match])
	}
	if d.Match == "" {
		d.Values = nil
	}
	return nil
}

func (s *SSLConfig) normalize() error {
	h, err := normalizeHost(s.Host)
	if err != nil {
		return err
	}
	s.Host = h
	if s.Port == 0 {
		s.Port = 443
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	s.ServerName = strings.TrimSpace(s.ServerName)
	if s.ServerName != "" {
		if s.ServerName, err = normalizeHost(s.ServerName); err != nil {
			return fmt.Errorf("server name: %w", err)
		}
	}
	if s.WarnDays == 0 {
		s.WarnDays = 14
	}
	if s.FailDays < 0 || s.FailDays > 365 || s.WarnDays < 0 || s.WarnDays > 365 {
		return errors.New("days must be between 0 and 365")
	}
	if s.WarnDays < s.FailDays {
		return errors.New("warn days must be at least the fail days")
	}
	if _, ok := tlsVersions[s.MinTLS]; s.MinTLS != "" && !ok {
		return errors.New("minimum TLS version must be 1.0, 1.1, 1.2 or 1.3")
	}
	return nil
}

// unescape reads \r, \n, \t and \\ in what a TCP check sends or expects.
func unescape(s string) string {
	return strings.NewReplacer(`\r`, "\r", `\n`, "\n", `\t`, "\t", `\\`, `\`).Replace(s)
}

func printable(b []byte) string {
	out := make([]rune, 0, len(b))
	for _, r := range strings.ToValidUTF8(string(b), "") {
		switch {
		case r == '\n' || r == '\t':
			out = append(out, r)
		case r == '\r':
		case r < 0x20 || r == 0x7f:
			out = append(out, '·')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

type tcpDetail struct {
	Address   string  `json:"address,omitempty"`
	ConnectMs float64 `json:"connect_ms,omitempty"`
	Banner    string  `json:"banner,omitempty"`
}

func (u *uptimeRunner) probeTCP(ctx context.Context, c UptimeCheck) (res uptimeResult) {
	start := time.Now()
	timeout := time.Duration(c.TimeoutMs) * time.Millisecond
	detail := tcpDetail{}
	defer func() {
		res.CreatedAt = start.UTC().Format(tsLayout)
		res.DurationMs = msSince(start)
		res.Degraded = res.OK && c.DegradedMs > 0 && res.DurationMs > float64(c.DegradedMs)
		res.Detail = detail
	}()
	fail := func(err error) uptimeResult {
		res.OK, res.Error, res.blocked = false, probeError(err, c.TimeoutMs), errors.Is(err, errBlockedAddress)
		return res
	}
	t := c.TCP
	if err := u.precheckHost(t.Host); err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := u.kindDialer(timeout).DialContext(ctx, "tcp", net.JoinHostPort(t.Host, strconv.Itoa(t.Port)))
	if err != nil {
		return fail(err)
	}
	defer conn.Close()
	detail.ConnectMs = msSince(start)
	detail.Address = conn.RemoteAddr().String()
	res.ConnectMs = msPtr(detail.ConnectMs)
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	if t.Send != "" {
		if _, err := conn.Write([]byte(unescape(t.Send))); err != nil {
			return fail(fmt.Errorf("could not send: %w", err))
		}
	}
	if t.Expect != "" {
		want := unescape(t.Expect)
		buf := make([]byte, 0, 512)
		chunk := make([]byte, 512)
		for !strings.Contains(string(buf), want) && len(buf) < tcpMaxBanner {
			n, err := conn.Read(chunk)
			buf = append(buf, chunk[:n]...)
			if err != nil {
				break
			}
		}
		detail.Banner = clip(printable(buf), 200)
		if !strings.Contains(string(buf), want) {
			if len(buf) == 0 {
				return fail(fmt.Errorf("nothing received; expected %q", t.Expect))
			}
			return fail(fmt.Errorf("expected %q in the reply", t.Expect))
		}
	}
	res.OK = true
	return res
}

type dnsDetail struct {
	Server    string   `json:"server"`
	Type      string   `json:"type"`
	Answers   []string `json:"answers"`
	ResolveMs float64  `json:"resolve_ms,omitempty"`
}

// dnsNorm makes an answer or expected value comparable.
func dnsNorm(recordType, v string) string {
	v = strings.TrimSpace(v)
	if recordType == "TXT" {
		return v
	}
	v = strings.TrimSuffix(strings.ToLower(v), ".")
	if a, err := netip.ParseAddr(v); err == nil {
		return a.String()
	}
	return v
}

// dnsHas is whether an answer is the value; an MX answer ("10 mx.host")
// also matches its host alone.
func dnsHas(recordType string, answers []string, value string) bool {
	want := dnsNorm(recordType, value)
	for _, a := range answers {
		got := dnsNorm(recordType, a)
		if got == want {
			return true
		}
		if recordType == "MX" {
			if _, host, ok := strings.Cut(got, " "); ok && host == want {
				return true
			}
		}
	}
	return false
}

func (u *uptimeRunner) resolver(ctx context.Context, server string, timeout time.Duration) (*net.Resolver, error) {
	if server == "" {
		return net.DefaultResolver, nil
	}
	host, port, _ := net.SplitHostPort(server)
	addr, err := u.resolveGuarded(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("DNS server: %w", err)
	}
	target := net.JoinHostPort(addr.String(), port)
	d := &net.Dialer{Timeout: timeout}
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return d.DialContext(ctx, network, target)
	}}, nil
}

func lookupDNS(ctx context.Context, r *net.Resolver, recordType, name string) ([]string, error) {
	var out []string
	switch recordType {
	case "A", "AAAA":
		network := "ip4"
		if recordType == "AAAA" {
			network = "ip6"
		}
		addrs, err := r.LookupNetIP(ctx, network, name)
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			out = append(out, a.Unmap().String())
		}
	case "CNAME":
		cname, err := r.LookupCNAME(ctx, name)
		if err != nil {
			return nil, err
		}
		out = append(out, strings.TrimSuffix(cname, "."))
	case "MX":
		mx, err := r.LookupMX(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, m := range mx {
			out = append(out, fmt.Sprintf("%d %s", m.Pref, strings.TrimSuffix(m.Host, ".")))
		}
	case "TXT":
		txt, err := r.LookupTXT(ctx, name)
		if err != nil {
			return nil, err
		}
		out = append(out, txt...)
	case "NS":
		ns, err := r.LookupNS(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, n := range ns {
			out = append(out, strings.TrimSuffix(n.Host, "."))
		}
	}
	return out, nil
}

func (u *uptimeRunner) probeDNS(ctx context.Context, c UptimeCheck) (res uptimeResult) {
	start := time.Now()
	timeout := time.Duration(c.TimeoutMs) * time.Millisecond
	d := c.DNS
	detail := dnsDetail{Server: d.Server, Type: d.RecordType, Answers: []string{}}
	if detail.Server == "" {
		detail.Server = "system resolver"
	}
	defer func() {
		res.CreatedAt = start.UTC().Format(tsLayout)
		res.DurationMs = msSince(start)
		res.Degraded = res.OK && c.DegradedMs > 0 && res.DurationMs > float64(c.DegradedMs)
		res.Detail = detail
	}()
	fail := func(err error) uptimeResult {
		res.OK, res.Error, res.blocked = false, probeError(err, c.TimeoutMs), errors.Is(err, errBlockedAddress)
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r, err := u.resolver(ctx, d.Server, timeout)
	if err != nil {
		return fail(err)
	}
	t0 := time.Now()
	answers, err := lookupDNS(ctx, r, d.RecordType, d.Name)
	detail.ResolveMs = msSince(t0)
	res.DNSMs = msPtr(detail.ResolveMs)
	if err != nil {
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			return fail(fmt.Errorf("no %s record for %s", d.RecordType, d.Name))
		}
		if errors.As(err, &de) && de.IsTimeout {
			return fail(fmt.Errorf("timed out after %s", timeout))
		}
		return fail(err)
	}
	slices.Sort(answers)
	detail.Answers = answers
	if len(answers) == 0 {
		return fail(fmt.Errorf("no %s record for %s", d.RecordType, d.Name))
	}
	switch d.Match {
	case "contains":
		for _, v := range d.Values {
			if !dnsHas(d.RecordType, answers, v) {
				return fail(fmt.Errorf("%q is not among the answers", v))
			}
		}
	case "equals":
		for _, v := range d.Values {
			if !dnsHas(d.RecordType, answers, v) {
				return fail(fmt.Errorf("%q is not among the answers", v))
			}
		}
		for _, a := range answers {
			if !slices.ContainsFunc(d.Values, func(v string) bool { return dnsHas(d.RecordType, []string{a}, v) }) {
				return fail(fmt.Errorf("unexpected answer %q", clip(a, 100)))
			}
		}
	}
	res.OK = true
	return res
}

type sslDetail struct {
	Subject       string   `json:"subject,omitempty"`
	Issuer        string   `json:"issuer,omitempty"`
	Names         []string `json:"names,omitempty"`
	NotBefore     string   `json:"not_before,omitempty"`
	NotAfter      string   `json:"not_after,omitempty"`
	DaysLeft      int      `json:"days_left"`
	TLSVersion    string   `json:"tls_version,omitempty"`
	Cipher        string   `json:"cipher,omitempty"`
	Chain         []string `json:"chain,omitempty"`
	ChainValid    bool     `json:"chain_valid"`
	ChainError    string   `json:"chain_error,omitempty"`
	HostnameValid bool     `json:"hostname_valid"`
	Address       string   `json:"address,omitempty"`
}

func tlsVersionName(v uint16) string {
	for k, n := range tlsVersions {
		if n == v {
			return k
		}
	}
	return "unknown"
}

func certName(c *x509.Certificate) string {
	if c.Subject.CommonName != "" {
		return c.Subject.CommonName
	}
	return c.Subject.String()
}

func (u *uptimeRunner) probeSSL(ctx context.Context, c UptimeCheck) (res uptimeResult) {
	start := time.Now()
	timeout := time.Duration(c.TimeoutMs) * time.Millisecond
	cfg := c.SSL
	detail := sslDetail{}
	defer func() {
		res.CreatedAt = start.UTC().Format(tsLayout)
		res.DurationMs = msSince(start)
		res.Detail = detail
	}()
	fail := func(err error) uptimeResult {
		res.OK, res.Error, res.blocked = false, probeError(err, c.TimeoutMs), errors.Is(err, errBlockedAddress)
		return res
	}
	if err := u.precheckHost(cfg.Host); err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := u.kindDialer(timeout).DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return fail(err)
	}
	defer raw.Close()
	res.ConnectMs = msPtr(msSince(start))
	detail.Address = raw.RemoteAddr().String()
	sni := cfg.ServerName
	if sni == "" {
		sni = cfg.Host
	}
	serverName := sni
	if _, err := netip.ParseAddr(sni); err == nil {
		serverName = ""
	}
	// Verification is done below, so each problem can be named and some
	// ignored.
	conn := tls.Client(raw, &tls.Config{ServerName: serverName, InsecureSkipVerify: true, MinVersion: tls.VersionTLS10}) //nolint:gosec
	t0 := time.Now()
	if err := conn.HandshakeContext(ctx); err != nil {
		return fail(fmt.Errorf("TLS handshake failed: %w", err))
	}
	res.TLSMs = msPtr(msSince(t0))
	st := conn.ConnectionState()
	if len(st.PeerCertificates) == 0 {
		return fail(errors.New("the server sent no certificate"))
	}
	leaf := st.PeerCertificates[0]
	now := time.Now()
	detail.Subject, detail.Issuer, detail.Names = certName(leaf), certName(&x509.Certificate{Subject: leaf.Issuer}), leaf.DNSNames
	for _, ip := range leaf.IPAddresses {
		detail.Names = append(detail.Names, ip.String())
	}
	detail.NotBefore, detail.NotAfter = leaf.NotBefore.UTC().Format(tsLayout), leaf.NotAfter.UTC().Format(tsLayout)
	detail.DaysLeft = int(leaf.NotAfter.Sub(now).Hours() / 24)
	if leaf.NotAfter.Before(now) {
		detail.DaysLeft = -1
	}
	detail.TLSVersion, detail.Cipher = tlsVersionName(st.Version), tls.CipherSuiteName(st.CipherSuite)
	for _, cert := range st.PeerCertificates {
		detail.Chain = append(detail.Chain, certName(cert))
	}
	res.CertExpiresAt = leaf.NotAfter.UTC().Format(tsLayout)
	inter := x509.NewCertPool()
	for _, cert := range st.PeerCertificates[1:] {
		inter.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Intermediates: inter, CurrentTime: now}); err != nil {
		detail.ChainError = clip(err.Error(), 200)
	} else {
		detail.ChainValid = true
	}
	detail.HostnameValid = leaf.VerifyHostname(sni) == nil

	switch {
	case now.Before(leaf.NotBefore):
		return fail(fmt.Errorf("the certificate is not valid until %s", detail.NotBefore))
	case leaf.NotAfter.Before(now):
		return fail(fmt.Errorf("the certificate expired on %s", detail.NotAfter[:10]))
	case detail.DaysLeft < cfg.FailDays:
		return fail(fmt.Errorf("the certificate expires in %d days (fails under %d)", detail.DaysLeft, cfg.FailDays))
	case !detail.ChainValid && !cfg.IgnoreChain:
		return fail(fmt.Errorf("the certificate chain is not trusted: %s", detail.ChainError))
	case !detail.HostnameValid && !cfg.IgnoreHostname:
		return fail(fmt.Errorf("the certificate is not valid for %s", sni))
	case cfg.MinTLS != "" && st.Version < tlsVersions[cfg.MinTLS]:
		return fail(fmt.Errorf("the server negotiated TLS %s (minimum %s)", detail.TLSVersion, cfg.MinTLS))
	}
	res.OK = true
	res.Degraded = detail.DaysLeft < cfg.WarnDays || (c.DegradedMs > 0 && msSince(start) > float64(c.DegradedMs))
	return res
}
