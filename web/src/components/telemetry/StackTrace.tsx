import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { CopyButton } from "../Modal";
import "./telemetry.css";

export interface Frame {
  kind: "frame";
  fn: string;
  file: string;
  line: string;
  code?: string;
  app: boolean;
}

type Item = Frame | { kind: "text"; text: string };

// Frames from the standard library, installed packages, the framework and the
// Visionary SDK itself: everything that is not the application's own code.
const LIBRARY_FILE =
  /(site-packages|dist-packages|node_modules|\/go\/pkg\/mod\/|\/usr\/local\/go\/src\/|\/usr\/lib\/go|\/usr\/lib\/python|\/lib\/python\d|\/gems\/|\/rubygems\/|\/\.rbenv\/|<frozen |^node:|\binternal\/(modules|process|timers)\b|\/sdks\/go\/|\/visionary\/)/;
const LIBRARY_FN =
  /^(java\.|javax\.|jdk\.|sun\.|com\.sun\.|kotlin\.|kotlinx\.|scala\.|org\.springframework\.|org\.apache\.|org\.hibernate\.|io\.netty\.|reactor\.|com\.zaxxer\.|com\.fasterxml\.|io\.micrometer\.|System\.|Microsoft\.|github\.com\/styropyr0\/visionary)/;

// Go's standard library lives under GOROOT/src wherever Go is installed; when
// the path does not tell, its well-known top-level packages do.
function isGoStdlib(fn: string, file: string): boolean {
  if (/\/(go|goroot)[^/]*\/src\//i.test(file) || /\/libexec\/src\//.test(file)) return true;
  const path = fn.split("(")[0];
  if (path.includes("/")) return /^(archive|bufio|bytes|context|crypto|database|encoding|errors|fmt|hash|internal|io|log|math|mime|net|os|path|reflect|regexp|runtime|sort|strconv|strings|sync|syscall|text|time|unicode)\//.test(path);
  return /^(runtime|sync|reflect|syscall|testing|fmt|errors|strings|strconv|bufio|bytes|context|io|os|sort|time)\./.test(path);
}

function isLibrary(fn: string, file: string): boolean {
  return LIBRARY_FILE.test(file) || LIBRARY_FN.test(fn);
}

/**
 * Splits a stack trace in any of the SDK languages' usual formats into frames
 * and text lines (headers, messages, "Caused by"). Lines it does not
 * recognize stay as text, so nothing is lost.
 */
export function parseStack(stack: string): Item[] {
  const lines = stack.replace(/\r/g, "").split("\n");
  const out: Item[] = [];
  const frame = (fn: string, file: string, line: string, code?: string): Frame => ({
    kind: "frame",
    fn: fn.trim(),
    file: file.trim(),
    line,
    code,
    app: !isLibrary(fn, file),
  });
  for (let i = 0; i < lines.length; i++) {
    const l = lines[i];
    if (l.trim() === "") continue;
    let m: RegExpMatchArray | null;
    // Python: File "/app/x.py", line 12, in handler  (+ the source line)
    if ((m = l.match(/^\s*File "([^"]+)", line (\d+), in (.+)$/))) {
      const next = lines[i + 1];
      const code = next !== undefined && /^\s{4,}\S/.test(next) && !/^\s*File "/.test(next) ? next.trim() : undefined;
      if (code !== undefined) i++;
      out.push(frame(m[3], m[1], m[2], code));
      continue;
    }
    // Java / Kotlin: at com.acme.Foo.bar(Foo.java:12)
    if ((m = l.match(/^\s*at ([\w$.<>/]+)\((.*?)(?::(\d+))?\)\s*$/)) && !l.includes(" in ")) {
      out.push(frame(m[1].replace(/^[\w.]+\//, ""), m[2], m[3] ?? ""));
      continue;
    }
    // .NET: at Acme.Foo.Bar() in /src/Foo.cs:line 12
    if ((m = l.match(/^\s*at (.+?\)) in (.+):line (\d+)\s*$/))) {
      out.push(frame(m[1], m[2], m[3]));
      continue;
    }
    // Node: at fn (/app/x.js:1:2)  or  at /app/x.js:1:2
    if ((m = l.match(/^\s*at (?:(.+?) \()?((?:\/|[A-Za-z]:\\|node:|file:)[^()]*?):(\d+):\d+\)?\s*$/))) {
      out.push(frame(m[1] ?? "<anonymous>", m[2], m[3]));
      continue;
    }
    // Ruby: /app/x.rb:12:in 'Foo#bar'
    if ((m = l.match(/^\s*(\/[^:]+\.rb):(\d+):in [`'](.+)'\s*$/))) {
      out.push(frame(m[3], m[1], m[2]));
      continue;
    }
    // Go: a function line followed by "\t/path/file.go:12 +0x1d".
    const next = lines[i + 1];
    if (next !== undefined && (m = next.match(/^\t(\S+\.go):(\d+)(?: \+0x[0-9a-f]+)?$/)) && !l.startsWith("\t")) {
      // Drop the argument list only: receivers like (*Handler) stay in the name.
      const fn = l.replace(/^created by /, "").replace(/ in goroutine \d+$/, "").replace(/\([^()]*\)$/, "");
      const f = frame(fn, m[1], m[2]);
      if (isGoStdlib(fn, f.file)) f.app = false;
      out.push(f);
      i++;
      continue;
    }
    out.push({ kind: "text", text: l });
  }
  return out;
}

type Group = { kind: "text"; text: string } | { kind: "app"; frame: Frame; first: boolean } | { kind: "lib"; frames: Frame[] };

function group(items: Item[]): Group[] {
  const out: Group[] = [];
  let firstApp = true;
  for (const it of items) {
    if (it.kind === "text") {
      out.push(it);
    } else if (it.app) {
      out.push({ kind: "app", frame: it, first: firstApp });
      firstApp = false;
    } else {
      const last = out[out.length - 1];
      if (last?.kind === "lib") last.frames.push(it);
      else out.push({ kind: "lib", frames: [it] });
    }
  }
  return out;
}

function FrameRow({ f, top }: { f: Frame; top?: boolean }) {
  return (
    <div className={`st-frame ${f.app ? "app" : "lib"} ${top ? "top" : ""}`}>
      <div className="st-fn">{f.fn}</div>
      <div className="st-loc">
        {f.file}
        {f.line && <span className="st-line">:{f.line}</span>}
      </div>
      {f.code && <pre className="st-code">{f.code}</pre>}
    </div>
  );
}

function LibraryFrames({ frames }: { frames: Frame[] }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="st-lib">
      <button type="button" className="st-toggle" onClick={() => setOpen(!open)} aria-expanded={open}>
        {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
        {frames.length} library {frames.length === 1 ? "frame" : "frames"}
        {!open && <span className="st-lib-names">{frames.slice(0, 2).map((f) => shortFn(f.fn)).join(", ")}{frames.length > 2 ? ", …" : ""}</span>}
      </button>
      {open && frames.map((f, i) => <FrameRow key={i} f={f} />)}
    </div>
  );
}

function shortFn(fn: string): string {
  const last = fn.split("/").pop() ?? fn;
  return last.length > 36 ? last.split(".").slice(-2).join(".") : last;
}

/**
 * A stack trace with the application's own frames emphasized and runs of
 * library frames collapsed. The first application frame is where it failed.
 */
export function StackTrace({ stack }: { stack: string }) {
  const [raw, setRaw] = useState(false);
  const groups = useMemo(() => group(parseStack(stack)), [stack]);
  const frames = groups.some((g) => g.kind !== "text");
  return (
    <div className="stacktrace">
      <div className="st-bar">
        {frames && (
          <div className="btn-group">
            <button type="button" className={`btn sm ${raw ? "" : "on"}`} aria-pressed={!raw} onClick={() => setRaw(false)}>
              Frames
            </button>
            <button type="button" className={`btn sm ${raw ? "on" : ""}`} aria-pressed={raw} onClick={() => setRaw(true)}>
              Raw
            </button>
          </div>
        )}
        <span style={{ marginLeft: "auto" }}>
          <CopyButton text={stack} />
        </span>
      </div>
      {raw || !frames ? (
        <pre className="code mono st-raw">{stack}</pre>
      ) : (
        <div className="st-frames mono">
          {groups.map((g, i) =>
            g.kind === "text" ? (
              <div key={i} className="st-text">
                {g.text}
              </div>
            ) : g.kind === "app" ? (
              <FrameRow key={i} f={g.frame} top={g.first} />
            ) : (
              <LibraryFrames key={i} frames={g.frames} />
            ),
          )}
        </div>
      )}
    </div>
  );
}
