package analytics

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"
)

// clientFingerprint groups client errors by the rule in sdks/SPEC.md
// ("Errors"): the first 16 hex characters of SHA-1 over type + "\n" + the top
// five in-app frames, innermost first, as function@file (no line numbers).
// Browser (V8, SpiderMonkey, JavaScriptCore), JVM (Android) and Apple
// crash-report stacks all list the innermost frame first, so frames are taken
// in the order they appear. When a stack has frames but none are in-app, the
// top five frames of any kind are used; only a stack without frames falls
// back to the message with digit runs as 0.
func clientFingerprint(typ, message, stack string) string {
	var app, all []string
	for _, line := range strings.Split(stack, "\n") {
		f, ok := parseFrame(strings.TrimSpace(line))
		if !ok {
			continue
		}
		all = append(all, f.fn+"@"+f.file)
		if !f.library {
			app = append(app, f.fn+"@"+f.file)
		}
	}
	frames := app
	if len(frames) == 0 {
		frames = all
	}
	if len(frames) > 5 {
		frames = frames[:5]
	}
	tail := strings.Join(frames, "\n")
	if tail == "" {
		tail = digitRun.ReplaceAllString(message, "0")
	}
	sum := sha1.Sum([]byte(typ + "\n" + tail))
	return hex.EncodeToString(sum[:])[:16]
}

type stackFrame struct {
	fn, file string
	library  bool // browser internals, extensions, the platform, the SDK
}

var (
	digitRun = regexp.MustCompile(`\d+`)
	// V8: "at fn (file:1:2)", "at file:1:2", "at async fn (file:1:2)".
	v8Frame = regexp.MustCompile(`^at (?:async )?(?:(.+?) \((.+?)\)|(.+))$`)
	// SpiderMonkey and JavaScriptCore: "fn@file:1:2", "@file:1:2".
	geckoFrame = regexp.MustCompile(`^([^@\s]*)@(.+)$`)
	// JVM: "at com.example.Foo.bar(Foo.java:42)".
	jvmFrame = regexp.MustCompile(`^at ([\w$.<>/]+)\.([\w$<>-]+)\(([^)]*)\)$`)
	// Apple: "3   MyApp   0x0000000102a4c function + 120", optionally
	// followed by " (File.swift:42)" when symbolicated.
	appleFrame = regexp.MustCompile(`^\d+\s+(\S+)\s+0x[0-9a-fA-F]+\s+(.+?)(?:\s+\+\s+\d+)?(?:\s+\(([^)]*)\))?$`)
	lineCol    = regexp.MustCompile(`(:\d+)+$`)
	// Bundlers put a content hash in chunk names (index-B7x9Qa2c.js,
	// main.3f2a9c1d.js); it changes every build, so it is dropped.
	buildHash = regexp.MustCompile(`[.-][A-Za-z0-9_]{6,}(\.(?:m|c)?[jt]sx?)$`)
)

var jvmLibraries = []string{"java.", "javax.", "jdk.", "sun.", "com.sun.", "android.", "androidx.", "com.android.", "dalvik.", "kotlin.", "kotlinx.", "com.google.android.", "com.google.common.", "okhttp3.", "okio.", "retrofit2.", "io.reactivex.", "com.squareup."}

func parseFrame(line string) (stackFrame, bool) {
	if line == "" {
		return stackFrame{}, false
	}
	if m := jvmFrame.FindStringSubmatch(line); m != nil {
		class, file := m[1], m[3]
		if i := strings.IndexByte(file, ':'); i >= 0 {
			file = file[:i]
		}
		lib := strings.Contains(strings.ToLower(class), "visionary")
		for _, p := range jvmLibraries {
			lib = lib || strings.HasPrefix(class, p)
		}
		return stackFrame{fn: class + "." + m[2], file: file, library: lib}, true
	}
	if m := v8Frame.FindStringSubmatch(line); m != nil {
		fn, loc := m[1], m[2]
		if loc == "" {
			fn, loc = "", m[3]
		}
		return jsFrame(fn, loc), true
	}
	if m := appleFrame.FindStringSubmatch(line); m != nil {
		module, fn, file := m[1], m[2], m[3]
		if i := strings.IndexByte(file, ':'); i >= 0 {
			file = file[:i]
		}
		if file == "" {
			file = module
		}
		return stackFrame{fn: fn, file: file, library: appleLibrary(module)}, true
	}
	if m := geckoFrame.FindStringSubmatch(line); m != nil && strings.Contains(m[2], ":") {
		return jsFrame(m[1], m[2]), true
	}
	return stackFrame{}, false
}

func jsFrame(fn, loc string) stackFrame {
	if fn == "" {
		fn = "<anonymous>"
	}
	file := lineCol.ReplaceAllString(stripQuery(loc), "")
	lower := strings.ToLower(file)
	lib := strings.Contains(lower, "-extension://") || strings.HasPrefix(lower, "webkit-masked-url") ||
		strings.Contains(lower, "[native code]") || lower == "native" || lower == "<anonymous>" ||
		strings.Contains(lower, "/node_modules/") || strings.Contains(lower, "visionary")
	// Keep only the path: the same bundle served from another host or CDN
	// is the same code.
	if i := strings.Index(file, "://"); i >= 0 {
		rest := file[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			file = rest[j:]
		} else {
			file = "/"
		}
	}
	file = buildHash.ReplaceAllStringFunc(file, func(m string) string {
		sub := buildHash.FindStringSubmatch(m)
		if !strings.ContainsAny(m[1:len(m)-len(sub[1])], "0123456789") {
			return m // a word such as ".min" or "-vendor", not a hash
		}
		return sub[1]
	})
	return stackFrame{fn: fn, file: file, library: lib}
}

func appleLibrary(module string) bool {
	switch module {
	case "UIKitCore", "UIKit", "Foundation", "CoreFoundation", "GraphicsServices", "SwiftUI", "SwiftUICore", "QuartzCore",
		"CFNetwork", "dyld", "CoreData", "Combine", "AttributeGraph", "FrontBoardServices", "UIKitServices":
		return true
	}
	return strings.HasPrefix(module, "libsystem_") || strings.HasSuffix(module, ".dylib") || strings.Contains(strings.ToLower(module), "visionary")
}
