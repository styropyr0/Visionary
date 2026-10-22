package analytics

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// CPU profiles: the SDKs sample the process now and then while requests
// are being served (when profiling is on in Settings), and store what they saw as folded stacks along with the
// milliseconds each route's requests took meanwhile. The flame graph merges every profile in
// the range, or only those taken while a chosen route was busy.

const (
	maxFlameProfiles = 60
	// Frames under this share of all samples are folded into their parent,
	// which keeps the tree small enough to draw.
	flameMinShare = 0.0005
)

// handleProfiles lists the profiles in the range and the routes that were
// busy while they were taken, by the time their requests took.
func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	where, args := profileService(r)
	res, err := s.telemetryQuery(r.Context(), `
SELECT id, created_at, service, instance, version, sdk, duration_ms, samples, routes
FROM profiles WHERE created_at >= {{from}} AND created_at < {{to}}`+where+`
ORDER BY created_at DESC LIMIT 200`, tr, args...)
	if err != nil {
		if missingTable(err, "profiles") {
			writeJSON(w, http.StatusOK, map[string]any{"profiles": &Result{Columns: []string{}}, "routes": []any{}})
			return
		}
		s.fail(w, err)
		return
	}
	busy := map[string]int64{}
	ri := indexOf(res.Columns, "routes")
	for _, row := range res.Rows {
		for route, n := range profileRoutes(row[ri]) {
			busy[route] += n
		}
	}
	type hot struct {
		Route  string `json:"route"`
		BusyMs int64  `json:"busy_ms"`
	}
	var routes []hot
	for k, v := range busy {
		routes = append(routes, hot{k, v})
	}
	sort.Slice(routes, func(a, b int) bool { return routes[a].BusyMs > routes[b].BusyMs })
	writeJSON(w, http.StatusOK, map[string]any{"profiles": res, "routes": head(routes, 30)})
}

// profileService narrows profiles to one service (?service=, "" for those
// without a name); every service when the parameter is absent.
func profileService(r *http.Request) (string, []any) {
	if !r.URL.Query().Has("service") {
		return "", nil
	}
	return ` AND COALESCE(service, '') = ?`, []any{r.URL.Query().Get("service")}
}

// profileRoutes reads a profile's routes column: {"GET /api/x": busy ms}.
func profileRoutes(v any) map[string]int64 {
	out := map[string]int64{}
	if s := toString(v); s != "" {
		_ = json.Unmarshal([]byte(s), &out)
	}
	return out
}

type flameNode struct {
	Name     string       `json:"name"`
	Value    int64        `json:"value"`
	Self     int64        `json:"self"`
	Children []*flameNode `json:"children,omitempty"`
	index    map[string]*flameNode
}

func (n *flameNode) child(name string) *flameNode {
	if n.index == nil {
		n.index = map[string]*flameNode{}
	}
	c := n.index[name]
	if c == nil {
		c = &flameNode{Name: name}
		n.index[name] = c
		n.Children = append(n.Children, c)
	}
	return c
}

// prune drops branches under min samples into their parent's own time and
// orders children by weight.
func (n *flameNode) prune(min int64) {
	kept := n.Children[:0]
	for _, c := range n.Children {
		if c.Value < min {
			n.Self += c.Value
			continue
		}
		c.prune(min)
		kept = append(kept, c)
	}
	n.Children = kept
	sort.Slice(n.Children, func(a, b int) bool { return n.Children[a].Value > n.Children[b].Value })
	n.index = nil
}

// handleFlame merges profiles into one tree: a single profile (id), the ones
// taken while a route was busy (route), or every profile in the range.
func (s *Server) handleFlame(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	where, args := profileService(r)
	sql := `SELECT id, routes, folded FROM profiles WHERE created_at >= {{from}} AND created_at < {{to}}` + where
	if id, err := strconv.ParseInt(q.Get("id"), 10, 64); err == nil {
		sql += ` AND id = ?`
		args = append(args, id)
	}
	sql += ` ORDER BY created_at DESC LIMIT ` + strconv.Itoa(maxFlameProfiles*4)
	rows, err := s.db.Query(r.Context(), tr.render(sql, s.q), args...)
	if err != nil {
		if missingTable(err, "profiles") {
			writeJSON(w, http.StatusOK, map[string]any{"root": &flameNode{Name: "all"}, "profiles": 0})
			return
		}
		s.fail(w, dbErr(err))
		return
	}
	route := q.Get("route")
	root := &flameNode{Name: "all"}
	used := 0
	for _, row := range rows.Rows {
		if used >= maxFlameProfiles {
			break
		}
		if route != "" && profileRoutes(row[1])[route] == 0 {
			continue
		}
		used++
		for _, line := range strings.Split(toString(row[2]), "\n") {
			sp := strings.LastIndexByte(line, ' ')
			if sp <= 0 {
				continue
			}
			n, err := strconv.ParseInt(line[sp+1:], 10, 64)
			if err != nil || n <= 0 {
				continue
			}
			node := root
			root.Value += n
			frames := strings.Split(line[:sp], ";")
			for _, f := range frames {
				node = node.child(f)
				node.Value += n
			}
			node.Self += n
		}
	}
	root.prune(max(int64(float64(root.Value)*flameMinShare), 1))
	writeJSON(w, http.StatusOK, map[string]any{"root": root, "profiles": used})
}
