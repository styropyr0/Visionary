package visionary

import (
	"context"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// sdkModule is the module this package ships in, for the releases row.
const sdkModule = "github.com/styropyr0/visionary"

// identity labels every row: which service, which running copy, which release.
type identity struct {
	service, instance, version string
}

// releaseVars are the platform variables that carry the deployed commit, in
// the order the contract checks them after VISIONARY_RELEASE.
var releaseVars = []string{
	"VISIONARY_RELEASE",
	"RAILWAY_GIT_COMMIT_SHA", "RENDER_GIT_COMMIT", "VERCEL_GIT_COMMIT_SHA", "HEROKU_SLUG_COMMIT",
	"SOURCE_VERSION", "GIT_COMMIT", "GITHUB_SHA", "K_REVISION",
}

// releaseFromEnv is the release this process runs, or "" when unknown.
func releaseFromEnv() string {
	for _, k := range releaseVars {
		if v := env(k, ""); v != "" {
			return shortRelease(v)
		}
	}
	return ""
}

// shortRelease shortens commit hashes to 12 characters; other version strings
// (v1.4.2, a Cloud Run revision) are kept as they are.
func shortRelease(v string) string {
	v = strings.TrimSpace(v)
	if len(v) <= 12 {
		return v
	}
	for _, c := range v {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return v
		}
	}
	return strings.ToLower(v[:12])
}

var releaseColumns = []string{"created_at", "service", "instance", "version", "sdk", "sdk_version", "runtime"}

// writeRelease records the deploy marker for this start.
func (a *Agent) writeRelease(ctx context.Context) {
	if a.ident.version == "" {
		return
	}
	st := stmt{
		sql: insertSQL("releases", releaseColumns, a.db.stringParams()),
		args: []any{
			time.Now().UTC().Format(tsLayout), nullable(a.ident.service), a.ident.instance, a.ident.version,
			"go", nullable(sdkVersion()), runtime.Version(),
		},
	}
	if err := a.db.batch(ctx, []stmt{st}); err != nil {
		a.cfg.Logger.Error("visionary: write release failed", "err", err)
	}
}

// sdkVersion is the version of this module the application was built with.
func sdkVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	if bi.Main.Path == sdkModule {
		return cleanVersion(bi.Main.Version)
	}
	for _, d := range bi.Deps {
		if d.Path == sdkModule {
			if d.Replace != nil && d.Replace.Version != "" {
				return cleanVersion(d.Replace.Version)
			}
			return cleanVersion(d.Version)
		}
	}
	return ""
}

func cleanVersion(v string) string {
	if v == "(devel)" {
		return ""
	}
	return v
}

// active is the most recently started Agent. Tracers are installed on pools
// and clients before the Agent exists, so spans outside a request find it
// here; inside a request they use the request's Agent.
var active atomic.Pointer[Agent]

// quietKey marks the SDK's own writes, so a wrapped pool or transport the SDK
// shares with the application does not trace Visionary itself.
type quietKey struct{}

func quiet(ctx context.Context) context.Context { return context.WithValue(ctx, quietKey{}, true) }

func isQuiet(ctx context.Context) bool { return ctx.Value(quietKey{}) != nil }

// millis is d in milliseconds with 3 decimals.
func millis(d time.Duration) float64 {
	return math.Round(float64(d.Microseconds())) / 1000
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// truncateRunes cuts s to at most n characters.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// hostname is the default instance label.
func hostname() string {
	h, _ := os.Hostname()
	return h
}
