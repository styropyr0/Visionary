package visionary

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// profileMaxFolded caps the folded text so a row stays small. The heaviest
// stacks are kept whole up to profileKeepWhole; the rest are rolled up into
// their callers (see foldLines).
const (
	profileMaxFolded = 90_000
	profileKeepWhole = 72_000
)

// A pprof profile, only the parts folding needs. See
// github.com/google/pprof/proto/profile.proto.
type ppSample struct {
	locs  []uint64 // leaf first
	count int64    // value[0]: samples
}

type ppLine struct {
	fn   uint64
	line int64
}

type ppFunc struct {
	name, file int64 // string table indexes
}

type ppProfile struct {
	samples []ppSample
	locs    map[uint64][]ppLine // a location's lines, innermost (inlined) first
	funcs   map[uint64]ppFunc
	strs    []string
}

var errProto = errors.New("visionary: malformed pprof profile")

// pbFields walks one protobuf message, calling f with each field's number,
// wire type, and its value (varint and fixed types) or bytes (wire type 2).
func pbFields(b []byte, f func(num int, wt int, v uint64, data []byte) error) error {
	for len(b) > 0 {
		key, n := uvarint(b)
		if n <= 0 {
			return errProto
		}
		b = b[n:]
		num, wt := int(key>>3), int(key&7)
		var v uint64
		var data []byte
		switch wt {
		case 0:
			v, n = uvarint(b)
			if n <= 0 {
				return errProto
			}
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return errProto
			}
			for i := 7; i >= 0; i-- {
				v = v<<8 | uint64(b[i])
			}
			b = b[8:]
		case 2:
			l, n := uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return errProto
			}
			data = b[n : n+int(l)]
			b = b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return errProto
			}
			v = uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24
			b = b[4:]
		default:
			return errProto
		}
		if err := f(num, wt, v, data); err != nil {
			return err
		}
	}
	return nil
}

func uvarint(b []byte) (uint64, int) {
	var x uint64
	for i := 0; i < len(b) && i < 10; i++ {
		x |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return x, i + 1
		}
	}
	return 0, 0
}

// pbRepeated appends a repeated integer field, packed (wire type 2) or not.
func pbRepeated(dst []uint64, wt int, v uint64, data []byte) ([]uint64, error) {
	if wt == 0 {
		return append(dst, v), nil
	}
	if wt != 2 {
		return dst, errProto
	}
	for len(data) > 0 {
		x, n := uvarint(data)
		if n <= 0 {
			return dst, errProto
		}
		dst = append(dst, x)
		data = data[n:]
	}
	return dst, nil
}

// parsePprof decodes a (gzipped) pprof profile.
func parsePprof(raw []byte) (*ppProfile, error) {
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		if raw, err = io.ReadAll(zr); err != nil {
			return nil, err
		}
	}
	p := &ppProfile{locs: map[uint64][]ppLine{}, funcs: map[uint64]ppFunc{}}
	err := pbFields(raw, func(num, wt int, _ uint64, data []byte) error {
		if wt != 2 {
			return nil
		}
		switch num {
		case 2: // sample
			var s ppSample
			var values []uint64
			err := pbFields(data, func(num, wt int, v uint64, data []byte) (err error) {
				switch num {
				case 1:
					s.locs, err = pbRepeated(s.locs, wt, v, data)
				case 2:
					values, err = pbRepeated(values, wt, v, data)
				}
				return err
			})
			if err != nil {
				return err
			}
			if len(values) > 0 {
				s.count = int64(values[0])
			}
			p.samples = append(p.samples, s)
		case 4: // location
			var id uint64
			var lines []ppLine
			err := pbFields(data, func(num, wt int, v uint64, data []byte) error {
				switch {
				case num == 1 && wt == 0:
					id = v
				case num == 4 && wt == 2:
					var l ppLine
					if err := pbFields(data, func(num, wt int, v uint64, _ []byte) error {
						if wt == 0 {
							switch num {
							case 1:
								l.fn = v
							case 2:
								l.line = int64(v)
							}
						}
						return nil
					}); err != nil {
						return err
					}
					lines = append(lines, l)
				}
				return nil
			})
			if err != nil {
				return err
			}
			p.locs[id] = lines
		case 5: // function
			var id uint64
			var f ppFunc
			err := pbFields(data, func(num, wt int, v uint64, _ []byte) error {
				if wt == 0 {
					switch num {
					case 1:
						id = v
					case 2:
						f.name = int64(v)
					case 4:
						f.file = int64(v)
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			p.funcs[id] = f
		case 6: // string_table
			p.strs = append(p.strs, string(data))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (p *ppProfile) str(i int64) string {
	if i < 0 || i >= int64(len(p.strs)) {
		return ""
	}
	return p.strs[i]
}

type ppFrame struct {
	fn, file string
	line     int64
}

// stack is a sample's frames, outermost caller first. A location with
// several lines holds inlined calls, the last line being the caller.
func (p *ppProfile) stack(s ppSample) []ppFrame {
	var out []ppFrame
	for _, id := range s.locs {
		for _, l := range p.locs[id] {
			f := p.funcs[l.fn]
			out = append(out, ppFrame{fn: p.str(f.name), file: p.str(f.file), line: l.line})
		}
	}
	slices.Reverse(out)
	return out
}

// ownGoroutine reports whether a stack (outermost first) belongs to a
// goroutine the SDK or the profiler itself started.
func ownGoroutine(stack []ppFrame) bool {
	for _, f := range stack {
		if f.fn == "runtime.goexit" {
			continue
		}
		return strings.HasPrefix(f.fn, sdkPackage+".") || strings.HasPrefix(f.fn, sdkPackage+"/") ||
			f.fn == "runtime/pprof.profileWriter"
	}
	return false
}

// foldProfile turns a pprof CPU profile into folded stacks ("frame;frame n"
// per line, heaviest first, capped) and the number of samples counted.
func foldProfile(raw []byte, n *frameNamer) (string, int, error) {
	p, err := parsePprof(raw)
	if err != nil {
		return "", 0, err
	}
	lines := map[string]int64{}
	var total int64
	var names []string
	for _, s := range p.samples {
		if s.count <= 0 || len(s.locs) == 0 {
			continue
		}
		stack := p.stack(s)
		if len(stack) == 0 || ownGoroutine(stack) {
			continue
		}
		names = names[:0]
		for i, f := range stack {
			// Only the sampled frame keeps its line: callers are one frame
			// per function, whichever line they called from.
			if i < len(stack)-1 {
				f.line = 0
			}
			names = append(names, n.frame(f))
		}
		lines[strings.Join(names, ";")] += s.count
		total += s.count
	}
	return foldLines(lines), int(total), nil
}

// foldLines writes the heaviest stacks first, within profileMaxFolded bytes
// (sdks/SPEC.md, "CPU profiles"): whole lines up to profileKeepWhole, then
// the rest cut after the deepest frame they share with a kept stack and
// ending in "(other)", then one "(other) n" for whatever is left.
func foldLines(lines map[string]int64) string {
	keys := make([]string, 0, len(lines))
	for k := range lines {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if lines[keys[i]] != lines[keys[j]] {
			return lines[keys[i]] > lines[keys[j]]
		}
		return keys[i] < keys[j]
	})
	var b strings.Builder
	line := func(k string, n int64) string { return k + " " + strconv.FormatInt(n, 10) + "\n" }
	// Whole lines first, leaving room for the rolled-up ones.
	i := 0
	for ; i < len(keys); i++ {
		l := line(keys[i], lines[keys[i]])
		if b.Len()+len(l) > profileKeepWhole {
			break
		}
		b.WriteString(l)
	}
	if i == len(keys) {
		return b.String()
	}
	// The rest are cut after the deepest frame they share with a kept stack
	// and end in "(other)", so their time stays under the right caller.
	prefixes := map[string]bool{}
	for _, k := range keys[:i] {
		for j := 0; j < len(k); j++ {
			if k[j] == ';' {
				prefixes[k[:j]] = true
			}
		}
		prefixes[k] = true
	}
	rolled := map[string]int64{}
	var other int64
	for _, k := range keys[i:] {
		cut := ""
		for j := 0; j <= len(k); j++ {
			if (j == len(k) || k[j] == ';') && prefixes[k[:j]] {
				cut = k[:j]
			}
		}
		switch cut {
		case "":
			other += lines[k]
		case k: // the whole stack leads into a kept one: nothing to cut
			rolled[k] += lines[k]
		default:
			rolled[cut+";(other)"] += lines[k]
		}
	}
	rest := make([]string, 0, len(rolled))
	for k := range rolled {
		rest = append(rest, k)
	}
	sort.Slice(rest, func(i, j int) bool {
		if rolled[rest[i]] != rolled[rest[j]] {
			return rolled[rest[i]] > rolled[rest[j]]
		}
		return rest[i] < rest[j]
	})
	for _, k := range rest {
		l := line(k, rolled[k])
		if b.Len()+len(l) > profileMaxFolded-32 { // room for the last "(other) n"
			other += rolled[k]
			continue
		}
		b.WriteString(l)
	}
	if other > 0 {
		b.WriteString(line("(other)", other))
	}
	return b.String()
}

// frameNamer names frames "pkg.Func file:line", with file saying whose code
// it is: relative for the application, lib: for modules, std: for Go itself.
type frameNamer struct {
	goroot   string   // "<GOROOT>/src/", forward slashes
	roots    []string // project root and working directory, each ending in "/"
	mainPath string   // the main module's path
	fold     bool     // compare paths case-insensitively (Windows)
	// replaced are modules replaced by a local directory (go.mod replace
	// => ../dir): third-party code that lives outside the module cache.
	replaced []dirModule
}

type dirModule struct {
	dir    string // ending in "/"
	module string
}

func newFrameNamer() *frameNamer {
	n := &frameNamer{fold: runtime.GOOS == "windows"}
	//lint:ignore SA1019 the build's GOROOT is what profile paths carry
	if g := runtime.GOROOT(); g != "" {
		n.goroot = strings.TrimSuffix(filepath.ToSlash(g), "/") + "/src/"
	}
	if wd, err := os.Getwd(); err == nil {
		for dir := wd; ; {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				n.roots = append(n.roots, strings.TrimSuffix(filepath.ToSlash(dir), "/")+"/")
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		if r := strings.TrimSuffix(filepath.ToSlash(wd), "/") + "/"; !slices.Contains(n.roots, r) {
			n.roots = append(n.roots, r)
		}
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		n.mainPath = bi.Main.Path
		base := ""
		if len(n.roots) > 0 {
			base = n.roots[0]
		}
		for _, d := range bi.Deps {
			r := d.Replace
			if r == nil || (r.Version != "" && r.Version != "(devel)") || r.Path == "" {
				continue // replaced by another module version, not a directory
			}
			dir := filepath.ToSlash(r.Path)
			if !isAbsSlash(dir) {
				if base == "" {
					continue
				}
				dir = filepath.ToSlash(filepath.Join(base, dir))
			}
			n.replaced = append(n.replaced, dirModule{dir: strings.TrimSuffix(dir, "/") + "/", module: d.Path})
		}
		// Deepest directory first.
		sort.Slice(n.replaced, func(i, j int) bool { return len(n.replaced[i].dir) > len(n.replaced[j].dir) })
	}
	return n
}

func (n *frameNamer) frame(f ppFrame) string {
	name := f.fn
	if name == "" {
		name = "(unknown)"
	}
	if file := n.file(f.file, f.fn); file != "" {
		name += " " + file
		if f.line > 0 {
			name += ":" + strconv.FormatInt(f.line, 10)
		}
	}
	return strings.ReplaceAll(name, ";", ",")
}

func (n *frameNamer) hasPrefix(s, prefix string) bool {
	if n.fold {
		return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
	}
	return strings.HasPrefix(s, prefix)
}

// file labels a source path; "" when there is none.
func (n *frameNamer) file(path, fn string) string {
	if path == "" {
		return ""
	}
	p := strings.ReplaceAll(path, "\\", "/")
	if n.goroot != "" && n.hasPrefix(p, n.goroot) {
		return "std:" + p[len(n.goroot):]
	}
	if i := strings.Index(p, "/pkg/mod/"); i >= 0 {
		rest := p[i+len("/pkg/mod/"):]
		if strings.HasPrefix(rest, "golang.org/toolchain@") {
			if j := strings.Index(rest, "/src/"); j >= 0 {
				return "std:" + rest[j+len("/src/"):]
			}
		}
		return "lib:" + moduleFile(rest)
	}
	if i := strings.LastIndex(p, "/vendor/"); i >= 0 {
		return "lib:" + p[i+len("/vendor/"):]
	}
	if !isAbsSlash(p) {
		return n.trimmed(p)
	}
	for _, m := range n.replaced {
		if n.hasPrefix(p, m.dir) {
			return "lib:" + m.module + "/" + p[len(m.dir):]
		}
	}
	for _, r := range n.roots {
		if n.hasPrefix(p, r) {
			return p[len(r):]
		}
	}
	// Built elsewhere: the function's package says whose code it is.
	pkg := symbolPackage(fn)
	base := p[strings.LastIndex(p, "/")+1:]
	switch {
	case pkg == "" || pkg == "main":
		return base
	case n.mainPath != "" && (pkg == n.mainPath || strings.HasPrefix(pkg, n.mainPath+"/")):
		if rel := strings.TrimPrefix(strings.TrimPrefix(pkg, n.mainPath), "/"); rel != "" {
			return rel + "/" + base
		}
		return base
	case !strings.Contains(firstElem(pkg), "."):
		if strings.HasSuffix(p, "/src/"+pkg+"/"+base) {
			return "std:" + pkg + "/" + base
		}
		return p
	}
	return "lib:" + pkg + "/" + base
}

// trimmed labels a path from a -trimpath build: "module@version/file" for
// dependencies, "mainmodule/file" for the application, "pkg/file" for Go.
func (n *frameNamer) trimmed(p string) string {
	switch {
	case strings.Contains(p, "@"):
		return "lib:" + moduleFile(p)
	case n.mainPath != "" && strings.HasPrefix(p, n.mainPath+"/"):
		return p[len(n.mainPath)+1:]
	case !strings.Contains(firstElem(p), "."):
		return "std:" + p
	}
	return "lib:" + p
}

// moduleFile turns "github.com/!foo/bar@v1.2.3/sub/x.go" (a module cache
// path) into "github.com/Foo/bar/sub/x.go".
func moduleFile(rest string) string {
	at := strings.Index(rest, "@")
	if at < 0 {
		return unescapeModule(rest)
	}
	tail := ""
	if slash := strings.Index(rest[at:], "/"); slash >= 0 {
		tail = rest[at+slash:]
	}
	return unescapeModule(rest[:at]) + tail
}

// unescapeModule undoes the module cache's "!x" for an upper-case X.
func unescapeModule(s string) string {
	if !strings.Contains(s, "!") {
		return s
	}
	var b strings.Builder
	up := false
	for _, r := range s {
		switch {
		case r == '!':
			up = true
		case up:
			b.WriteRune(unicode.ToUpper(r))
			up = false
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// symbolPackage is the import path of a function symbol such as
// "github.com/x/y.(*T).M" or "net/http.HandlerFunc.ServeHTTP".
func symbolPackage(fn string) string {
	if i := strings.Index(fn, "["); i >= 0 {
		fn = fn[:i]
	}
	slash := strings.LastIndex(fn, "/")
	dot := strings.Index(fn[slash+1:], ".")
	if dot < 0 {
		return ""
	}
	return fn[:slash+1+dot]
}

func firstElem(p string) string {
	if i := strings.Index(p, "/"); i >= 0 {
		return p[:i]
	}
	return p
}

func isAbsSlash(p string) bool {
	return strings.HasPrefix(p, "/") || (len(p) >= 3 && p[1] == ':' && p[2] == '/')
}
