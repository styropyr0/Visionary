package visionary

import (
	"context"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

const (
	packagesDelay = 30 * time.Second
	maxPackages   = 5000
)

var packageColumns = []string{"created_at", "service", "instance", "version", "ecosystem", "name", "pkg_version"}

type modulePackage struct{ name, version string }

// goPackages lists the modules the binary was built with, plus the standard library
func goPackages() []modulePackage {
	var out []modulePackage
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			v := d.Version
			if d.Replace != nil && d.Replace.Version != "" {
				v = d.Replace.Version
			}
			out = append(out, modulePackage{d.Path, v})
		}
	}
	return append(out, modulePackage{"stdlib", goVersion(runtime.Version())})
}

// goVersion is go1.26.2 (or go1.26.2 X:exp) as 1.26.2, empty for devel builds
func goVersion(v string) string {
	if !strings.HasPrefix(v, "go") {
		return ""
	}
	v = strings.TrimPrefix(v, "go")
	if i := strings.IndexAny(v, " +-"); i >= 0 {
		v = v[:i]
	}
	return v
}

func packageRows(pkgs []modulePackage, at string, id identity) [][]any {
	seen := map[modulePackage]bool{}
	var rows [][]any
	for _, p := range pkgs {
		if p.name == "" || p.version == "" || seen[p] {
			continue
		}
		seen[p] = true
		rows = append(rows, []any{at, nullable(id.service), id.instance, nullable(id.version), "Go", p.name, p.version})
		if len(rows) == maxPackages {
			break
		}
	}
	return rows
}

// reportPackages writes app_packages once, a while after start
func (a *Agent) reportPackages(ctx context.Context) {
	if !sleepCtx(ctx, packagesDelay) {
		return
	}
	ctx, cancel := context.WithTimeout(quiet(ctx), time.Minute)
	defer cancel()
	d1 := a.db.stringParams()
	g := rowGroup{
		table:   "app_packages",
		columns: packageColumns,
		insert:  insertSQL("app_packages", packageColumns, d1),
		rows:    packageRows(goPackages(), time.Now().UTC().Format(tsLayout), a.ident),
	}
	stmts := g.statements(nil, d1)
	perCall := len(stmts)
	if d1 {
		perCall = maxStatementsPerCall
	}
	for len(stmts) > 0 {
		n := min(len(stmts), perCall)
		if err := a.db.batch(ctx, stmts[:n]); err != nil {
			a.cfg.Logger.Error("visionary: write packages failed", "err", err)
			return
		}
		stmts = stmts[n:]
	}
}
