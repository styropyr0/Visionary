import type { Result } from "./api";

// Which language wrote a service's system_metrics rows, and the words its
// runtime charts use. The goroutines and heap_* columns hold each SDK's
// nearest equivalent, so their labels depend on the language.

export type Lang = "go" | "python" | "java" | "node" | "ruby" | "dotnet";

export interface RuntimeWords {
  /** Display name of the language runtime. */
  name: string;
  /** What the goroutines column counts. */
  concurrency: string;
  concurrencyHelp: string;
  /** What heap_inuse_mb measures. */
  heap: string;
  heapHelp: string;
}

const WORDS: Record<Lang, RuntimeWords> = {
  go: {
    name: "Go",
    concurrency: "Goroutines",
    concurrencyHelp: "Goroutines and OS threads. A steady climb in goroutines usually means a leak or stuck work.",
    heap: "Go heap",
    heapHelp: "Resident memory of this process and the Go heap in use.",
  },
  python: {
    name: "Python",
    concurrency: "Python threads",
    concurrencyHelp: "Live Python threads and OS threads. A steady climb usually means threads that never finish.",
    heap: "Traced heap",
    heapHelp: "Resident memory of this process. The Python heap shows only while tracemalloc is tracing.",
  },
  java: {
    name: "Java",
    concurrency: "JVM threads",
    concurrencyHelp: "Live JVM threads and OS threads. A steady climb usually means a leak or stuck work.",
    heap: "JVM heap",
    heapHelp: "Resident memory of this process and the JVM heap committed.",
  },
  node: {
    name: "Node.js",
    concurrency: "Active handles",
    concurrencyHelp: "Active event-loop handles (sockets, timers, requests) and OS threads. A steady climb usually means leaked sockets or timers.",
    heap: "V8 heap",
    heapHelp: "Resident memory of this process and the V8 heap in use.",
  },
  ruby: {
    name: "Ruby",
    concurrency: "Ruby threads",
    concurrencyHelp: "Live Ruby threads and OS threads. A steady climb usually means threads that never finish.",
    heap: "Ruby heap",
    heapHelp: "Resident memory of this process and the Ruby object heap.",
  },
  dotnet: {
    name: ".NET",
    concurrency: "Thread pool threads",
    concurrencyHelp: "Thread pool threads and OS threads. A steady climb usually means blocked work starving the pool.",
    heap: ".NET GC heap",
    heapHelp: "Resident memory of this process and the .NET GC heap committed.",
  },
};

/** Used when the rows don't say, or instances run different languages. */
export const NEUTRAL_WORDS: RuntimeWords = {
  name: "",
  concurrency: "Goroutines or threads",
  concurrencyHelp: "Goroutines (Go), live threads (Python, Ruby, JVM), thread-pool threads (.NET) or event-loop handles (Node), and OS threads.",
  heap: "Runtime heap",
  heapHelp: "Resident memory of this process and its runtime heap in use.",
};

/** The language from the sdk column, or for OTLP rows from the runtime. */
export function langOf(sdk: unknown, runtime: unknown): Lang | null {
  const s = String(sdk ?? "").toLowerCase();
  if (s in WORDS) return s as Lang;
  const r = String(runtime ?? "").toLowerCase();
  if (!r) return null;
  if (/^go(\d|$|\s)/.test(r)) return "go";
  if (r.includes("python")) return "python";
  if (r.includes("node")) return "node";
  if (r.includes("ruby")) return "ruby";
  if (r.includes(".net") || r.includes("dotnet")) return "dotnet";
  if (r.includes("java") || r.includes("jdk") || r.includes("jvm")) return "java";
  return null;
}

export function wordsFor(lang: Lang | null): RuntimeWords {
  return lang ? WORDS[lang] : NEUTRAL_WORDS;
}

/** Renames result columns, so chart series and table headers read as labels. */
export function relabel(result: Result, labels: Record<string, string>): Result {
  return { ...result, columns: result.columns.map((c) => labels[c] ?? c) };
}
