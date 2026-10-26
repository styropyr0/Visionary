package analytics

import (
	"bufio"
	"context"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

// Threat intelligence: where an address is and who runs it (optional MaxMind
// or DB-IP .mmdb files named in Settings → Server → Threat intelligence), and
// whether a blocklist the operator configured names it. Nothing is fetched
// or looked up unless those settings are filled in.
//
// prepareFindings runs on every finding before it is written: suppression
// rules drop it, the address gets its place and network, and a listed address
// raises the severity one level.

// geoInfo is what the databases know about one address.
type geoInfo struct {
	Country     string  `json:"country,omitempty"` // ISO code
	CountryName string  `json:"country_name,omitempty"`
	City        string  `json:"city,omitempty"`
	Lat         float64 `json:"lat,omitempty"`
	Lon         float64 `json:"lon,omitempty"`
	HasLoc      bool    `json:"-"`
	ASN         uint    `json:"asn,omitempty"`
	Org         string  `json:"org,omitempty"`
}

// place is "City, Country" or what of it is known.
func (g geoInfo) place() string {
	switch {
	case g.City != "" && g.CountryName != "":
		return g.City + ", " + g.CountryName
	case g.CountryName != "":
		return g.CountryName
	}
	return g.Country
}

type mmdb struct {
	path    string
	modTime time.Time
	r       *maxminddb.Reader
	err     string
}

// open (re)opens the file when the setting or the file changed.
func (m *mmdb) open(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		if m.r != nil {
			_ = m.r.Close()
		}
		*m = mmdb{}
		return
	}
	st, err := os.Stat(path)
	if err == nil && path == m.path && st.ModTime().Equal(m.modTime) && m.r != nil {
		return
	}
	if m.r != nil {
		_ = m.r.Close()
	}
	*m = mmdb{path: path}
	if err != nil {
		m.err = err.Error()
		return
	}
	r, err := maxminddb.Open(path)
	if err != nil {
		m.err = err.Error()
		return
	}
	m.r, m.modTime = r, st.ModTime()
}

type intelState struct {
	mu        sync.Mutex
	city, asn mmdb
	checked   time.Time // when the database paths were last compared

	lists     []blocklist
	listsConf string
	fetchedAt time.Time
}

type blocklist struct {
	URL      string `json:"url"`
	Name     string `json:"name"`
	Entries  int    `json:"entries"`
	Error    string `json:"error,omitempty"`
	Fetched  string `json:"fetched,omitempty"`
	prefixes []netip.Prefix
}

var intelStates sync.Map // *Server -> *intelState

func (s *Server) intel() *intelState {
	v, _ := intelStates.LoadOrStore(s, &intelState{})
	return v.(*intelState)
}

// lookupIP returns what the configured databases know about ip; zero for
// private addresses or when none is configured.
func (s *Server) lookupIP(ip string) geoInfo {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil || !publicAddr(a) {
		return geoInfo{}
	}
	in := s.intel()
	in.mu.Lock()
	defer in.mu.Unlock()
	if time.Since(in.checked) > 30*time.Second {
		in.city.open(s.conf("geoip_city_db"))
		in.asn.open(s.conf("geoip_asn_db"))
		in.checked = time.Now()
	}
	var g geoInfo
	addr := net.IP(a.Unmap().AsSlice())
	if in.city.r != nil {
		var rec struct {
			Country struct {
				ISO   string            `maxminddb:"iso_code"`
				Names map[string]string `maxminddb:"names"`
			} `maxminddb:"country"`
			City struct {
				Names map[string]string `maxminddb:"names"`
			} `maxminddb:"city"`
			Location struct {
				Lat *float64 `maxminddb:"latitude"`
				Lon *float64 `maxminddb:"longitude"`
			} `maxminddb:"location"`
		}
		if in.city.r.Lookup(addr, &rec) == nil {
			g.Country, g.CountryName, g.City = rec.Country.ISO, rec.Country.Names["en"], rec.City.Names["en"]
			if rec.Location.Lat != nil && rec.Location.Lon != nil {
				g.Lat, g.Lon, g.HasLoc = *rec.Location.Lat, *rec.Location.Lon, true
			}
		}
	}
	if in.asn.r != nil {
		var rec struct {
			ASN uint   `maxminddb:"autonomous_system_number"`
			Org string `maxminddb:"autonomous_system_organization"`
		}
		if in.asn.r.Lookup(addr, &rec) == nil {
			g.ASN, g.Org = rec.ASN, rec.Org
		}
	}
	return g
}

// geoEnabled reports whether a location database is open.
func (s *Server) geoEnabled() bool {
	s.lookupIP("8.8.8.8") // opens the files if the setting changed
	in := s.intel()
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.city.r != nil
}

// publicAddr is an address worth looking up: not loopback, private or
// link-local.
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && !a.IsLoopback() && !a.IsPrivate() && !a.IsLinkLocalUnicast() && !a.IsUnspecified() && !a.IsMulticast()
}

// listedIn names the blocklists that contain ip.
func (s *Server) listedIn(ip string) []string {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return nil
	}
	a = a.Unmap()
	in := s.intel()
	in.mu.Lock()
	defer in.mu.Unlock()
	var out []string
	for _, l := range in.lists {
		for _, p := range l.prefixes {
			if p.Contains(a) {
				out = append(out, l.Name)
				break
			}
		}
	}
	return out
}

// Blocklists are fetched at start, every blocklistEvery, and soon after the
// setting changes.
const (
	blocklistEvery    = 6 * time.Hour
	blocklistMaxBytes = 32 << 20
)

func (s *Server) intelLoop(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		s.refreshBlocklists(ctx, false)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// refreshBlocklists fetches every configured feed when the list changed or
// the last fetch is old (or force).
func (s *Server) refreshBlocklists(ctx context.Context, force bool) {
	conf := s.conf("security_blocklists")
	in := s.intel()
	in.mu.Lock()
	stale := force || conf != in.listsConf || time.Since(in.fetchedAt) > blocklistEvery
	in.mu.Unlock()
	if !stale {
		return
	}
	var lists []blocklist
	for _, line := range strings.Split(conf, "\n") {
		u := strings.TrimSpace(line)
		if u == "" || strings.HasPrefix(u, "#") {
			continue
		}
		lists = append(lists, s.fetchBlocklist(ctx, u))
		if len(lists) >= 20 {
			break
		}
	}
	in.mu.Lock()
	in.lists, in.listsConf, in.fetchedAt = lists, conf, time.Now()
	in.mu.Unlock()
	for _, l := range lists {
		if l.Error != "" {
			s.logger.Warn("security: blocklist fetch failed", "url", l.URL, "err", l.Error)
		}
	}
}

func (s *Server) fetchBlocklist(ctx context.Context, raw string) blocklist {
	l := blocklist{URL: raw, Name: blocklistName(raw)}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		l.Error = "not an http(s) URL"
		return l
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, raw, nil)
	if err != nil {
		l.Error = err.Error()
		return l
	}
	req.Header.Set("User-Agent", "Visionary blocklist fetcher")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		l.Error = err.Error()
		return l
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		l.Error = "HTTP " + resp.Status
		return l
	}
	l.prefixes = parseBlocklist(io.LimitReader(resp.Body, blocklistMaxBytes))
	l.Entries, l.Fetched = len(l.prefixes), time.Now().UTC().Format(tsLayout)
	return l
}

// parseBlocklist reads one address or CIDR range per line; text after #, ;
// or // and anything after the first field is ignored.
func parseBlocklist(r io.Reader) []netip.Prefix {
	var out []netip.Prefix
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		line := sc.Text()
		for _, c := range []string{"#", ";", "//"} {
			if i := strings.Index(line, c); i >= 0 {
				line = line[:i]
			}
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if p, err := netip.ParsePrefix(f[0]); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(f[0]); err == nil {
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		}
	}
	return out
}

// blocklistName is a short label for a feed: its host and file name.
func blocklistName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	name := u.Host
	if i := strings.LastIndex(u.Path, "/"); i >= 0 && i < len(u.Path)-1 {
		name += "/" + u.Path[i+1:]
	}
	return name
}

// raiseSeverity is one level up, at most critical.
func raiseSeverity(sev string) string {
	switch sev {
	case "info":
		return "warning"
	default:
		return "critical"
	}
}

// prepareFindings drops suppressed findings and adds what is known about
// each one's address.
func (s *Server) prepareFindings(ctx context.Context, found []finding) ([]finding, error) {
	if len(found) == 0 {
		return found, nil
	}
	rules, err := s.activeSuppressions(ctx)
	if err != nil {
		return nil, err
	}
	out := found[:0]
	for _, f := range found {
		if suppressed(rules, f) {
			continue
		}
		if f.ip != "" {
			if g := s.lookupIP(f.ip); g.Country != "" || g.Org != "" {
				if f.detail == nil {
					f.detail = map[string]any{}
				}
				if p := g.place(); p != "" {
					f.detail["place"] = p
				}
				if g.Country != "" {
					f.detail["country"] = g.Country
				}
				if g.Org != "" {
					f.detail["network"] = g.Org
				}
			}
			if lists := s.listedIn(f.ip); len(lists) > 0 {
				if f.detail == nil {
					f.detail = map[string]any{}
				}
				f.detail["listed_in"] = lists
				f.severity = raiseSeverity(f.severity)
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// handleIPInfo is everything Visionary knows about one address: where it is,
// who runs it, which blocklists name it, its findings and its recent traffic.
func (s *Server) handleIPInfo(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.PathValue("ip"))
	a, err := netip.ParseAddr(ip)
	if err != nil {
		writeError(w, http.StatusBadRequest, "not an IP address")
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	g := s.lookupIP(ip)
	out := map[string]any{
		"ip": ip, "public": publicAddr(a), "geo": g, "place": g.place(), "listed_in": s.listedIn(ip),
		"ignored": s.security().st.ignored(ip),
	}

	findings, err := s.db.Query(ctx, `SELECT kind, COUNT(*) AS findings, SUM(count) AS occurrences, MAX(severity) AS severity, MAX(created_at) AS last
FROM security_events WHERE ip = ? AND created_at >= ? GROUP BY kind ORDER BY last DESC`, ip, now.AddDate(0, 0, -90).Format(tsLayout))
	if err != nil {
		s.fail(w, err)
		return
	}
	var kinds []map[string]any
	for _, row := range findings.Rows {
		if len(row) == 5 {
			kinds = append(kinds, map[string]any{"kind": toString(row[0]), "findings": toInt(row[1]), "occurrences": toInt(row[2]),
				"severity": toString(row[3]), "last": toString(row[4])})
		}
	}
	out["findings"] = kinds

	st := &s.security().st
	login, fail := st.loginCond(s.q), authFailCond(s.q)
	week := now.AddDate(0, 0, -7).Format(tsLayout)
	traffic, err := s.db.Query(ctx, `SELECT COUNT(*) AS requests, MIN(created_at) AS first, MAX(created_at) AS last,
  `+countIf(login+" AND "+fail)+` AS failed_logins, `+countIf(login+" AND status < 400")+` AS logins,
  `+countIf("status >= 400 AND status < 500")+` AS client_errors, `+countIf("status >= 500")+` AS server_errors,
  COUNT(DISTINCT `+loginAccountExpr(s.q)+`) AS accounts, COUNT(DISTINCT COALESCE(user_agent, '')) AS agents
FROM api_logs WHERE created_at >= ? AND ip = ?`, week, ip)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(traffic.Rows) == 1 && len(traffic.Rows[0]) == 9 {
		row := traffic.Rows[0]
		out["traffic"] = map[string]any{"days": 7, "requests": toInt(row[0]), "first": toString(row[1]), "last": toString(row[2]),
			"failed_logins": toInt(row[3]), "logins": toInt(row[4]), "client_errors": toInt(row[5]), "server_errors": toInt(row[6]),
			"accounts": toInt(row[7]), "user_agents": toInt(row[8])}
	}
	acct := loginAccountExpr(s.q)
	accounts, err := s.db.Query(ctx, `SELECT `+acct+` AS account, COUNT(*) AS n, `+countIf(login+" AND "+fail)+` AS failed
FROM api_logs WHERE created_at >= ? AND ip = ? AND `+hasAccount+`
GROUP BY `+acct+` ORDER BY n DESC LIMIT 10`, week, ip)
	if err != nil {
		s.fail(w, err)
		return
	}
	var accs []map[string]any
	for _, row := range accounts.Rows {
		if len(row) == 3 {
			accs = append(accs, map[string]any{"account": toString(row[0]), "requests": toInt(row[1]), "failed_logins": toInt(row[2])})
		}
	}
	out["accounts"] = accs
	portal, err := s.db.Query(ctx, `SELECT `+countIf("action = 'auth.sign_in_failed'")+`, `+countIf("action = 'auth.sign_in'")+`
FROM audit_log WHERE created_at >= ? AND ip = ?`, week, ip)
	if err == nil && len(portal.Rows) == 1 && len(portal.Rows[0]) == 2 {
		out["portal"] = map[string]any{"failed_sign_ins": toInt(portal.Rows[0][0]), "sign_ins": toInt(portal.Rows[0][1])}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleIntelStatus shows whether the databases opened and what each
// blocklist holds, for Settings and the Security page.
func (s *Server) handleIntelStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") == "1" && s.can(r, "admin") {
		s.refreshBlocklists(r.Context(), true)
	}
	s.lookupIP("8.8.8.8")
	in := s.intel()
	in.mu.Lock()
	db := func(m mmdb) map[string]any {
		return map[string]any{"path": m.path, "open": m.r != nil, "error": m.err}
	}
	out := map[string]any{"city": db(in.city), "asn": db(in.asn), "blocklists": append([]blocklist(nil), in.lists...)}
	if !in.fetchedAt.IsZero() {
		out["fetched"] = in.fetchedAt.UTC().Format(tsLayout)
	}
	in.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// distanceKm is the great-circle distance between two points.
func distanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat, dLon := rad(lat2-lat1), rad(lon2-lon1)
	h := math.Pow(math.Sin(dLat/2), 2) + math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Pow(math.Sin(dLon/2), 2)
	return 2 * 6371 * math.Asin(math.Sqrt(min(h, 1)))
}
