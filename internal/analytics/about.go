package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Version and Commit are set at build time:
//
//	go build -ldflags "-X github.com/styropyr0/visionary/internal/analytics.Version=v0.3.2 \
//	  -X github.com/styropyr0/visionary/internal/analytics.Commit=e20dc71..."
//
// A build without them falls back to the commit Railway or Go recorded.
var (
	Version = ""
	Commit  = ""
)

// upstreamRepo is where Visionary is developed; the About panel links to it
// and checks it for newer releases.
const upstreamRepo = "styropyr0/Visionary"

// buildInfo is the running version and commit, from the build flags, then the
// platform's environment, then Go's own record of the checkout.
func buildInfo() (version, commit string) {
	version, commit = Version, Commit
	if commit == "" {
		for _, k := range []string{"VISIONARY_COMMIT", "RAILWAY_GIT_COMMIT_SHA", "SOURCE_VERSION", "GIT_COMMIT"} {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				commit = v
				break
			}
		}
	}
	if commit == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" {
					commit = s.Value
				}
			}
		}
	}
	if version == "" {
		version = strings.TrimSpace(os.Getenv("VISIONARY_VERSION"))
	}
	return version, commit
}

type upstreamRelease struct {
	Tag  string `json:"tag"`
	Date string `json:"date,omitempty"`
	URL  string `json:"url"`
	sha  string
}

// releaseCheck caches the upstream release list: GitHub allows 60 anonymous
// requests an hour per IP, and the list changes a few times a month. It
// refreshes in the background, so a slow GitHub never holds up a page.
type releaseCheck struct {
	mu         sync.Mutex
	at         time.Time
	releases   []upstreamRelease
	err        error
	refreshing bool
	logger     *slog.Logger
}

var upstream releaseCheck

// get returns the last result at once, starting a refresh when it is stale.
// checked is false until the first refresh has finished.
func (c *releaseCheck) get() (releases []upstreamRelease, err error, checked bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := time.Hour
	if c.err != nil {
		ttl = 10 * time.Minute
	}
	if (c.at.IsZero() || time.Since(c.at) >= ttl) && !c.refreshing {
		c.refreshing = true
		go c.refresh()
	}
	return c.releases, c.err, !c.at.IsZero()
}

func (c *releaseCheck) refresh() {
	releases, err := fetchUpstreamReleases(context.Background())
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil && c.logger != nil {
		c.logger.Warn("release check", "err", err)
	}
	// A failed refresh keeps the last good list.
	if err == nil || c.releases == nil {
		c.releases, c.err = releases, err
	}
	c.at = time.Now()
	c.refreshing = false
}

// githubGet fetches one GitHub API path, retrying once on a fresh connection
// when the first attempt stalls or fails.
func githubGet(ctx context.Context, path string, v any) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if err = githubGetOnce(ctx, path, v); err == nil {
			return nil
		}
		http.DefaultClient.CloseIdleConnections()
	}
	return err
}

func githubGetOnce(ctx context.Context, path string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "visionary")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("github: %s", res.Status)
	}
	return json.NewDecoder(res.Body).Decode(v)
}

// fetchUpstreamReleases lists the newest release tags with their dates. Tags
// carry no date of their own, so the newest few are dated by their commits.
func fetchUpstreamReleases(ctx context.Context) ([]upstreamRelease, error) {
	var tags []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := githubGet(ctx, "/repos/"+upstreamRepo+"/tags?per_page=50", &tags); err != nil {
		return nil, err
	}
	sort.SliceStable(tags, func(i, j int) bool { return semverLess(tags[j].Name, tags[i].Name) })
	out := []upstreamRelease{}
	for _, t := range tags {
		if _, ok := parseSemver(t.Name); !ok {
			continue
		}
		out = append(out, upstreamRelease{Tag: t.Name, URL: "https://github.com/" + upstreamRepo + "/tree/" + t.Name, sha: t.Commit.SHA})
		if len(out) == 8 {
			break
		}
	}
	// Date the newest five in parallel; a release without a date still shows.
	var wg sync.WaitGroup
	for i := range out[:min(5, len(out))] {
		wg.Add(1)
		go func(rel *upstreamRelease) {
			defer wg.Done()
			var c struct {
				Commit struct {
					Committer struct {
						Date string `json:"date"`
					} `json:"committer"`
				} `json:"commit"`
			}
			if githubGet(ctx, "/repos/"+upstreamRepo+"/commits/"+rel.sha, &c) == nil {
				rel.Date = c.Commit.Committer.Date
			}
		}(&out[i])
	}
	wg.Wait()
	return out, nil
}

func parseSemver(tag string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func semverLess(a, b string) bool {
	va, oka := parseSemver(a)
	vb, okb := parseSemver(b)
	if !oka || !okb {
		return oka == okb && a < b || !oka && okb
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] < vb[i]
		}
	}
	return false
}

// handleProject serves the About panel: what is running, what is newest
// upstream, and the latest deployments of the services reporting in.
func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	version, commit := buildInfo()
	out := map[string]any{
		"version": version,
		"commit":  commit,
		"repo":    "https://github.com/" + upstreamRepo,
	}

	if s.confBool("update_check") {
		releases, err, checked := upstream.get()
		switch {
		case !checked:
			out["releases_pending"] = true
		case releases == nil && err != nil:
			out["releases_error"] = "Could not reach GitHub to check for releases."
		default:
			out["releases"] = releases
			// A build from source knows only its commit; name it after the tag on it.
			if version == "" && commit != "" {
				for _, rel := range releases {
					if rel.sha == commit {
						version = rel.Tag
						out["version"] = version
						break
					}
				}
			}
			if len(releases) > 0 {
				latest := releases[0].Tag
				out["latest"] = latest
				if _, ok := parseSemver(version); ok && semverLess(version, latest) {
					out["update_available"] = true
				}
			}
		}
	}

	deployments := []release{}
	if all, err := s.allReleases(r.Context()); err == nil {
		for i := len(all) - 1; i >= 0 && len(deployments) < 8; i-- {
			deployments = append(deployments, all[i])
		}
	}
	out["deployments"] = deployments
	writeJSON(w, http.StatusOK, out)
}

// releasesStart checks GitHub for releases once at startup, so the About panel
// usually has them before anyone opens it. Settings → Server can turn the
// check off.
func (s *Server) releasesStart() {
	if !s.confBool("update_check") {
		return
	}
	upstream.mu.Lock()
	upstream.logger = s.logger
	upstream.mu.Unlock()
	upstream.get()
}
