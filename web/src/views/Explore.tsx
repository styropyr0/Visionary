import { useEffect, useMemo, useRef, useState } from "react";
import {
  Activity,
  AlertTriangle,
  ArrowUp,
  BookmarkPlus,
  Check,
  ChevronRight,
  Cpu,
  Database,
  Loader2,
  MessageSquarePlus,
  Search,
  Sparkles,
  Square,
  Trash2,
  TrendingUp,
  X,
} from "lucide-react";
import { api, ApiError, type Meta, type Result, type Viz } from "../lib/api";
import { formatCount } from "../lib/format";
import { load, useApp } from "../lib/state";
import { postStream } from "../lib/stream";
import { displayZone, formatRelative, rangeLabel } from "../lib/time";
import { DataTable } from "../components/DataTable";
import { Modal } from "../components/Modal";
import { Panel } from "../components/Panel";
import { RichText } from "../components/RichText";
import { useToast } from "../components/Toast";
import { RefreshControl, TimeRangePicker, TopBar } from "../components/Toolbar";
import { VizView, useChartTokens, vizNote } from "../components/VizView";
import { useSession } from "../lib/team";

interface Step {
  id: number;
  purpose: string;
  sql: string;
  status: "running" | "done" | "error";
  error?: string;
  ms?: number;
  columns?: string[];
  rows?: unknown[][];
  row_count?: number;
  rows_read?: number;
}

interface AnalystPanel {
  title: string;
  description: string;
  query: string;
  viz: Viz;
  result: Result;
}

interface Turn {
  id: string;
  question: string;
  status: "running" | "done" | "error" | "stopped";
  steps: Step[];
  answer?: string;
  followups?: string[];
  panels: AnalystPanel[];
  error?: string;
  model?: string;
  range: string;
  askedAt: string;
}

interface Thread {
  id: string;
  title: string;
  updatedAt: string;
  turns: Turn[];
}

const THREADS_KEY = "vis.threads";
const MAX_THREADS = 15;

const SUGGESTIONS: { icon: typeof Activity; group: string; text: string }[] = [
  { icon: Activity, group: "Incidents", text: "When did latency spike in this range, and what caused it?" },
  { icon: AlertTriangle, group: "Incidents", text: "What is behind the server errors, and are they still happening?" },
  { icon: TrendingUp, group: "Traffic", text: "Which routes grew the most compared with the previous period?" },
  { icon: Search, group: "Traffic", text: "Who are the heaviest users, and what do they call most?" },
  { icon: Cpu, group: "System", text: "Was the server ever CPU or memory bound? What was running then?" },
  { icon: Database, group: "System", text: "Does the database pool run out of connections, and when?" },
];

function newId() {
  return Math.random().toString(36).slice(2, 10) + Date.now().toString(36);
}

// Threads are a per-viewer convenience: kept in this browser only, dropped
// oldest-first when storage is full.
function persist(threads: Thread[]) {
  let list = threads.slice(0, MAX_THREADS);
  while (list.length) {
    try {
      localStorage.setItem(THREADS_KEY, JSON.stringify(list));
      return;
    } catch {
      list = list.slice(0, -1);
    }
  }
}

function readHashQuestion(): string {
  const q = window.location.hash.split("?")[1];
  return q ? (new URLSearchParams(q).get("q") ?? "") : "";
}

export function Explore({ meta }: { meta: Meta | undefined }) {
  const { resolved, range } = useApp();
  const [threads, setThreads] = useState<Thread[]>(() => load<Thread[]>(THREADS_KEY, []));
  const [activeId, setActiveId] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const abortRef = useRef<AbortController | null>(null);
  const endRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const running = threads.some((t) => t.turns.some((x) => x.status === "running"));
  const active = threads.find((t) => t.id === activeId) ?? null;
  const askEnabled = meta?.ask_enabled ?? true;
  const appName = meta?.app_name && meta.app_name !== "the application" ? meta.app_name : "your API";

  const update = (threadId: string, turnId: string, fn: (t: Turn) => Turn) => {
    setThreads((all) => {
      const next = all.map((th) =>
        th.id !== threadId
          ? th
          : { ...th, updatedAt: new Date().toISOString(), turns: th.turns.map((t) => (t.id === turnId ? fn(t) : t)) },
      );
      return next;
    });
  };

  useEffect(() => {
    if (!running) persist(threads);
  }, [threads, running]);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [active?.turns.length, active?.turns[active.turns.length - 1]?.steps.length, active?.turns[active.turns.length - 1]?.answer]);

  const ask = async (text: string) => {
    const question = text.trim();
    if (!question || running || !askEnabled) return;
    setDraft("");

    let threadId = activeId;
    const turn: Turn = {
      id: newId(),
      question,
      status: "running",
      steps: [],
      panels: [],
      range: rangeLabel(range),
      askedAt: new Date().toISOString(),
    };
    const history = (active?.turns ?? [])
      .filter((t) => t.answer)
      .map((t) => ({ question: t.question, answer: t.answer!, panels: t.panels.map((p) => p.title) }));

    if (!threadId) {
      threadId = newId();
      const th: Thread = { id: threadId, title: question, updatedAt: turn.askedAt, turns: [turn] };
      setThreads((all) => [th, ...all]);
      setActiveId(threadId);
    } else {
      const id = threadId;
      setThreads((all) => {
        const th = all.find((t) => t.id === id)!;
        return [{ ...th, updatedAt: turn.askedAt, turns: [...th.turns, turn] }, ...all.filter((t) => t.id !== id)];
      });
    }

    const ctrl = new AbortController();
    abortRef.current = ctrl;
    const tid = threadId;
    try {
      await postStream(
        "/api/analyst",
        {
          question,
          from: resolved.from.toISOString(),
          to: resolved.to.toISOString(),
          time_zone: displayZone(),
          history,
        },
        ({ event, data }) => {
          const d = data as Record<string, unknown>;
          update(tid, turn.id, (t) => {
            switch (event) {
              case "step":
                return { ...t, steps: [...t.steps, { ...(d as unknown as Step), status: "running" }] };
              case "step_done":
                return {
                  ...t,
                  steps: t.steps.map((s) => (s.id === d.id ? { ...s, ...(d as Partial<Step>), status: d.error ? "error" : "done" } : s)),
                };
              case "answer":
                return { ...t, answer: String(d.text ?? ""), followups: (d.followups as string[]) ?? [] };
              case "panel":
                return { ...t, panels: [...t.panels, d as unknown as AnalystPanel] };
              case "error":
                return { ...t, status: "error", error: String(d.message ?? "Something went wrong.") };
              case "done":
                return { ...t, status: t.status === "running" ? "done" : t.status, model: String(d.model ?? "") };
              default:
                return t;
            }
          });
        },
        ctrl.signal,
      );
    } catch (e) {
      const stopped = ctrl.signal.aborted;
      update(tid, turn.id, (t) => ({
        ...t,
        status: stopped ? "stopped" : "error",
        error: stopped ? undefined : e instanceof ApiError || e instanceof Error ? e.message : String(e),
      }));
    } finally {
      update(tid, turn.id, (t) => (t.status === "running" ? { ...t, status: "done" } : t));
      abortRef.current = null;
      window.setTimeout(() => inputRef.current?.focus(), 0);
    }
  };

  // A question handed over from another view, e.g. "Investigate" on a spike.
  useEffect(() => {
    const q = readHashQuestion();
    if (q) {
      window.history.replaceState(null, "", "#/explore");
      setActiveId(null);
      ask(q);
    }
  }, []);

  const removeThread = (id: string) => {
    setThreads((all) => all.filter((t) => t.id !== id));
    if (activeId === id) setActiveId(null);
  };

  return (
    <>
      <TopBar
        title="Explore"
        actions={
          <>
            <TimeRangePicker />
            <RefreshControl />
          </>
        }
      />
      <div className="analyst">
        <aside className="analyst-rail">
          <button type="button" className="btn rail-new" onClick={() => setActiveId(null)} disabled={running}>
            <MessageSquarePlus size={15} />
            New analysis
          </button>
          {threads.length > 0 && <div className="nav-label">Recent</div>}
          <div className="rail-list">
            {threads.map((t) => (
              <div key={t.id} className={`rail-item ${t.id === activeId ? "on" : ""}`}>
                <button type="button" className="rail-open" onClick={() => !running && setActiveId(t.id)} title={t.title}>
                  <span className="rail-title">{t.title}</span>
                  <span className="rail-time">
                    {t.turns.length} {t.turns.length === 1 ? "question" : "questions"} · {formatRelative(t.updatedAt)}
                  </span>
                </button>
                <button type="button" className="icon-btn rail-del" aria-label="Delete analysis" onClick={() => removeThread(t.id)} disabled={running}>
                  <Trash2 size={13} />
                </button>
              </div>
            ))}
          </div>
        </aside>

        <section className="analyst-main">
          <div className="analyst-scroll">
            {!active ? (
              <div className="analyst-empty">
                <div className="empty-icon">
                  <Sparkles size={20} />
                </div>
                <h2>What do you want to know about {appName}?</h2>
                <p>
                  Ask in plain language. Visionary investigates the request log and server metrics with read-only queries, shows
                  you each step, and answers with the evidence.
                </p>
                {!askEnabled && (
                  <div className="notice" style={{ maxWidth: 560 }}>
                    <AlertTriangle size={15} />
                    <span>The analyst needs an OpenAI API key in Settings → Server. Dashboards and saved panels still work.</span>
                  </div>
                )}
                <div className="suggest-grid">
                  {SUGGESTIONS.map(({ icon: Icon, group, text }) => (
                    <button type="button" key={text} className="suggest-card" onClick={() => ask(text)} disabled={!askEnabled || running}>
                      <span className="suggest-group">
                        <Icon size={13} />
                        {group}
                      </span>
                      <span>{text}</span>
                    </button>
                  ))}
                </div>
              </div>
            ) : (
              <div className="conversation">
                {active.turns.map((t, i) => (
                  <TurnView key={t.id} turn={t} last={i === active.turns.length - 1} onFollowup={ask} />
                ))}
                <div ref={endRef} />
              </div>
            )}
          </div>

          <form
            className="composer"
            onSubmit={(e) => {
              e.preventDefault();
              ask(draft);
            }}
          >
            <div className="composer-box">
              <textarea
                ref={inputRef}
                rows={1}
                value={draft}
                maxLength={2000}
                placeholder={active ? "Ask a follow-up…" : `Ask about ${appName}'s traffic, errors or servers…`}
                disabled={!askEnabled}
                onChange={(e) => {
                  setDraft(e.target.value);
                  e.target.style.height = "auto";
                  e.target.style.height = `${Math.min(e.target.scrollHeight, 180)}px`;
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.shiftKey) {
                    e.preventDefault();
                    ask(draft);
                  }
                }}
                aria-label="Question"
              />
              {running ? (
                <button type="button" className="composer-send stop" onClick={() => abortRef.current?.abort()} aria-label="Stop">
                  <Square size={12} fill="currentColor" />
                </button>
              ) : (
                <button type="submit" className="composer-send" disabled={!draft.trim() || !askEnabled} aria-label="Ask">
                  <ArrowUp size={16} />
                </button>
              )}
            </div>
            <div className="composer-hint">
              <span>
                Looks at <strong>{rangeLabel(range).toLowerCase()}</strong> · Enter to ask, Shift+Enter for a new line
              </span>
              {meta?.model && <span>{meta.model}</span>}
            </div>
          </form>
        </section>
      </div>
    </>
  );
}

function TurnView({ turn, last, onFollowup }: { turn: Turn; last: boolean; onFollowup: (q: string) => void }) {
  const working = turn.status === "running";
  const [stepsOpen, setStepsOpen] = useState(false);
  const thinking = working && !turn.answer && turn.steps.every((s) => s.status !== "running");

  return (
    <article className="turn">
      <div className="turn-q">
        <span className="turn-q-text">{turn.question}</span>
        <span className="turn-q-meta">{turn.range}</span>
      </div>

      {(turn.steps.length > 0 || working) && (
        <div className={`investigation ${working ? "live" : ""}`}>
          <button type="button" className="investigation-head" onClick={() => setStepsOpen(!stepsOpen)} disabled={working}>
            {working ? <Loader2 size={14} className="spin" /> : <Check size={14} />}
            <span>
              {working
                ? turn.steps.length === 0
                  ? "Planning the investigation…"
                  : `Investigating · ${turn.steps.length} ${turn.steps.length === 1 ? "query" : "queries"}`
                : `Investigated with ${turn.steps.length} ${turn.steps.length === 1 ? "query" : "queries"}`}
            </span>
            {!working && <ChevronRight size={14} className={`chev ${stepsOpen ? "open" : ""}`} />}
          </button>
          {(working || stepsOpen) && turn.steps.length > 0 && (
            <ol className="steps">
              {turn.steps.map((s) => (
                <StepView key={s.id} step={s} expandable={!working} />
              ))}
              {thinking && (
                <li className="step thinking">
                  <span className="step-dot">
                    <Loader2 size={12} className="spin" />
                  </span>
                  <span className="step-purpose">Reading the results…</span>
                </li>
              )}
            </ol>
          )}
        </div>
      )}

      {turn.answer && (
        <div className="answer fade-in">
          <RichText text={turn.answer} />
        </div>
      )}

      {turn.panels.length > 0 && (
        <div className={`answer-panels n${Math.min(turn.panels.length, 2)}`}>
          {turn.panels.map((p, i) => (
            <AnswerPanel key={i} panel={p} question={turn.question} />
          ))}
        </div>
      )}

      {turn.status === "error" && (
        <div className="notice turn-error">
          <AlertTriangle size={15} />
          <span>{turn.error}</span>
        </div>
      )}
      {turn.status === "stopped" && <div className="turn-stopped">Stopped.</div>}

      {last && !working && turn.followups && turn.followups.length > 0 && (
        <div className="followups">
          {turn.followups.map((f) => (
            <button type="button" key={f} className="followup" onClick={() => onFollowup(f)}>
              <Sparkles size={12} />
              {f}
            </button>
          ))}
        </div>
      )}
    </article>
  );
}

function StepView({ step, expandable }: { step: Step; expandable: boolean }) {
  const [open, setOpen] = useState(false);
  const preview = useMemo<Result | null>(
    () =>
      step.columns && step.rows
        ? { columns: step.columns, rows: step.rows, truncated: false, rows_read: 0, duration_ms: 0, sql: step.sql, bucket: 0, from: "", to: "" }
        : null,
    [step],
  );
  return (
    <li className={`step ${step.status}`}>
      <span className="step-dot">
        {step.status === "running" ? <Loader2 size={12} className="spin" /> : step.status === "error" ? <X size={12} /> : <Check size={12} />}
      </span>
      <div className="step-body">
        <button type="button" className="step-line" onClick={() => expandable && setOpen(!open)} disabled={!expandable}>
          <span className="step-purpose">{step.purpose}</span>
          <span className="step-meta">
            {step.status === "error"
              ? "Retrying"
              : step.status === "done"
                ? `${formatCount(step.row_count ?? 0, false)} rows · ${step.ms ?? 0} ms`
                : ""}
          </span>
        </button>
        {open && (
          <div className="step-detail">
            <pre className="code mono">{step.sql}</pre>
            {step.error && <div className="form-error">{step.error}</div>}
            {preview && preview.rows.length > 0 && <DataTable result={preview} sortable={false} />}
            {preview && (step.row_count ?? 0) > preview.rows.length && (
              <div className="panel-note" style={{ padding: "6px 0 0" }}>
                First {preview.rows.length} of {formatCount(step.row_count ?? 0, false)} rows.
              </div>
            )}
          </div>
        )}
      </div>
    </li>
  );
}

function AnswerPanel({ panel, question }: { panel: AnalystPanel; question: string }) {
  const tokens = useChartTokens();
  const toast = useToast();
  const [saving, setSaving] = useState(false);
  const canEdit = useSession().can("edit");
  const height = 260;
  const isTable = panel.viz.type === "table";
  return (
    <>
      <Panel
        title={panel.title}
        description={panel.description}
        result={panel.result}
        bodyHeight={height}
        note={isTable ? undefined : vizNote(panel.result, panel.viz, tokens)}
        chart={isTable ? undefined : <VizView result={panel.result} viz={panel.viz} height={height} title={panel.title} />}
        table={<DataTable result={panel.result} maxHeight={height} defaultUnit={panel.viz.unit} />}
        extraActions={
          // Viewers can explore but not save.
          canEdit ? (
            <button type="button" className="icon-btn" title="Save to Saved panels" aria-label="Save panel" onClick={() => setSaving(true)}>
              <BookmarkPlus size={15} />
            </button>
          ) : undefined
        }
      />
      {saving && (
        <SaveDialog
          panel={panel}
          prompt={question}
          onClose={() => setSaving(false)}
          onSaved={() => {
            setSaving(false);
            toast("ok", "Saved to Saved panels");
          }}
        />
      )}
    </>
  );
}

function SaveDialog({
  panel,
  prompt,
  onClose,
  onSaved,
}: {
  panel: AnalystPanel;
  prompt: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [title, setTitle] = useState(panel.title);
  const [description, setDescription] = useState(panel.description);
  const [err, setErr] = useState("");
  const [pending, setPending] = useState(false);

  const submit = async () => {
    setPending(true);
    setErr("");
    try {
      await api("/api/panels", {
        method: "POST",
        json: { title, description, prompt, query: panel.query, viz: panel.viz },
      });
      onSaved();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      setPending(false);
    }
  };

  return (
    <Modal
      title="Save panel"
      narrow
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" onClick={submit} disabled={!title.trim() || pending}>
            {pending ? "Saving…" : "Save"}
          </button>
        </>
      }
    >
      <label className="field">
        <span>Title</span>
        <input className="input" value={title} maxLength={120} onChange={(e) => setTitle(e.target.value)} autoFocus />
      </label>
      <label className="field">
        <span>Description</span>
        <textarea className="textarea" rows={3} value={description} maxLength={500} onChange={(e) => setDescription(e.target.value)} />
      </label>
      <p style={{ margin: 0, color: "var(--text-3)", fontSize: 12 }}>
        Saved panels are shared with everyone who can sign in, and re-run for whatever time range is selected.
      </p>
      {err && (
        <span className="form-error">
          <AlertTriangle size={14} />
          {err}
        </span>
      )}
    </Modal>
  );
}
