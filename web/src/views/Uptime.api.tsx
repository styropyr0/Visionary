import { useState } from "react";
import { ArrowDown, ArrowUp, CheckCircle2, CircleDashed, Plus, X, XCircle } from "lucide-react";
import { Drawer } from "../components/Modal";
import { Panel } from "../components/Panel";
import { DataTable, type Column } from "../components/DataTable";
import { Select } from "../components/Select";
import type { Result } from "../lib/api";
import { formatBytes, formatMs, toNumber } from "../lib/format";
import { formatLogTime } from "../lib/time";
import { MASK } from "../lib/uptime";
import type { ApiAssertion, ApiExtract, ApiRequest, AssertOp, AssertSource, DetailRun } from "../lib/uptimeKinds";
import { KeyValueRows } from "./Uptime.kinds";
import { withLocation } from "./Uptime.results";
import { StateBadge } from "./Uptime.parts";

const METHODS = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"];

const SOURCES: { value: AssertSource; label: string; ops: AssertOp[]; path?: string }[] = [
  { value: "status", label: "Status", ops: ["in", "equals", "not_equals", "lt", "gte"] },
  { value: "json", label: "JSON path", ops: ["equals", "not_equals", "contains", "exists", "not_exists", "lt", "lte", "gt", "gte", "matches"], path: "$.data.id" },
  { value: "header", label: "Header", ops: ["equals", "not_equals", "contains", "exists", "not_exists", "matches"], path: "Content-Type" },
  { value: "body", label: "Body", ops: ["contains", "not_contains", "matches"] },
  { value: "time", label: "Response time", ops: ["lte", "lt"] },
];

const OP_LABEL: Record<AssertOp, string> = {
  in: "is in",
  equals: "equals",
  not_equals: "does not equal",
  contains: "contains",
  not_contains: "does not contain",
  exists: "exists",
  not_exists: "does not exist",
  lt: "is less than",
  lte: "is at most",
  gt: "is more than",
  gte: "is at least",
  matches: "matches regex",
};

const EXTRACT_SOURCES = [
  { value: "json", label: "JSON path" },
  { value: "header", label: "Header" },
  { value: "regex", label: "Regex on body" },
  { value: "status", label: "Status" },
];

const valuePlaceholder = (a: ApiAssertion) =>
  a.source === "status" ? (a.op === "in" ? "200-299, 301" : "200") : a.source === "time" ? "ms, e.g. 800" : a.op === "matches" ? "^ok$" : "Expected value";

const rowsOf = (h?: Record<string, string>) => Object.entries(h ?? {}).map(([k, v]) => ({ k, v }));

export function ApiRequestsEditor({ requests, onChange }: { requests: ApiRequest[]; onChange: (r: ApiRequest[]) => void }) {
  const set = (i: number, patch: Partial<ApiRequest>) => onChange(requests.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const move = (i: number, d: number) => {
    const next = [...requests];
    [next[i], next[i + d]] = [next[i + d], next[i]];
    onChange(next);
  };
  const vars = (upTo: number) => requests.slice(0, upTo).flatMap((r) => (r.extract ?? []).map((e) => e.var).filter(Boolean));
  return (
    <div className="field">
      <span>Requests</span>
      <ol className="up-steps">
        {requests.map((r, i) => (
          <li key={i} className="up-step">
            <div className="up-step-head">
              <span className="up-step-n" aria-hidden>
                {i + 1}
              </span>
              <input className="input" style={{ flex: 1 }} value={r.name ?? ""} placeholder={`Request ${i + 1} name (optional)`} aria-label="Request name" maxLength={120} onChange={(e) => set(i, { name: e.target.value })} />
              <button type="button" className="icon-btn" aria-label="Move up" title="Move up" disabled={i === 0} onClick={() => move(i, -1)}>
                <ArrowUp size={14} />
              </button>
              <button type="button" className="icon-btn" aria-label="Move down" title="Move down" disabled={i === requests.length - 1} onClick={() => move(i, 1)}>
                <ArrowDown size={14} />
              </button>
              <button type="button" className="icon-btn" aria-label="Remove request" title="Remove request" disabled={requests.length === 1} onClick={() => onChange(requests.filter((_, j) => j !== i))}>
                <X size={14} />
              </button>
            </div>
            <div className="up-step-fields">
              <RequestFields request={r} onChange={(p) => set(i, p)} vars={vars(i)} />
            </div>
          </li>
        ))}
      </ol>
      <div>
        <button type="button" className="btn ghost sm" disabled={requests.length >= 10} onClick={() => onChange([...requests, { method: "GET", url: requests[requests.length - 1]?.url.replace(/^(https?:\/\/[^/]+).*$/, "$1/") ?? "https://", assert: [{ source: "status", op: "in", value: "200-299" }] }])}>
          <Plus size={14} />
          Add request
        </button>
      </div>
      <small>
        Requests run in order; the first that fails stops the run. Use a saved value as <code>{"{{var.NAME}}"}</code> and a secret as <code>{"{{secret.NAME}}"}</code> in the URL, headers and body. Without a status assertion, a status of 400 or more fails.
      </small>
    </div>
  );
}

function RequestFields({ request: r, onChange, vars }: { request: ApiRequest; onChange: (p: Partial<ApiRequest>) => void; vars: string[] }) {
  const [headers, setHeaders] = useState(() => rowsOf(r.headers));
  const writeHeaders = (rows: { k: string; v: string }[]) => {
    setHeaders(rows);
    const h: Record<string, string> = {};
    for (const { k, v } of rows) if (k.trim()) h[k.trim()] = v;
    onChange({ headers: h });
  };
  const hasBody = !["GET", "HEAD"].includes(r.method) || !!r.body;
  const extract = r.extract ?? [];
  const asserts = r.assert ?? [];
  const setExtract = (i: number, p: Partial<ApiExtract>) => onChange({ extract: extract.map((e, j) => (j === i ? { ...e, ...p } : e)) });
  const setAssert = (i: number, p: Partial<ApiAssertion>) => onChange({ assert: asserts.map((a, j) => (j === i ? { ...a, ...p } : a)) });
  return (
    <>
      <div className="up-row">
        <div style={{ width: 110 }}>
          <Select label="Method" value={r.method} align="left" onChange={(v) => onChange({ method: v })} options={METHODS.map((m) => ({ value: m, label: m }))} />
        </div>
        <input className="input mono" style={{ flex: 1 }} value={r.url} aria-label="URL" placeholder="https://api.example.com/v1/login" onChange={(e) => onChange({ url: e.target.value })} required />
      </div>
      {vars.length > 0 && (
        <small>
          Saved so far: {vars.map((v, i) => (
            <span key={v}>
              {i > 0 && ", "}
              <code>{`{{var.${v}}}`}</code>
            </span>
          ))}
        </small>
      )}
      <div className="up-sub-h">Headers</div>
      <KeyValueRows rows={headers} onChange={writeHeaders} keyLabel="Header" valueLabel="Value or Bearer {{secret.TOKEN}}" max={20} mask={(_, v) => v === MASK} />
      {hasBody && (
        <>
          <div className="up-row" style={{ alignItems: "center" }}>
            <div className="up-sub-h" style={{ flex: 1 }}>
              Body
            </div>
            <div className="segmented" role="radiogroup" aria-label="Body type">
              {(["json", "text", "form"] as const).map((t) => (
                <button key={t} type="button" role="radio" aria-checked={(r.body_type || "json") === t} className={(r.body_type || "json") === t ? "on" : ""} onClick={() => onChange({ body_type: t })}>
                  {t === "json" ? "JSON" : t === "text" ? "Text" : "Form"}
                </button>
              ))}
            </div>
          </div>
          <textarea
            className="textarea code"
            rows={3}
            value={r.body ?? ""}
            aria-label="Body"
            placeholder={(r.body_type || "json") === "form" ? "user=probe\npassword={{secret.PASSWORD}}" : '{"user":"probe","password":"{{secret.PASSWORD}}"}'}
            onChange={(e) => onChange({ body: e.target.value, body_type: r.body_type || "json" })}
          />
        </>
      )}
      <div className="up-sub-h">Assertions</div>
      {asserts.map((a, i) => {
        const src = SOURCES.find((s) => s.value === a.source) ?? SOURCES[0];
        const noValue = a.op === "exists" || a.op === "not_exists";
        return (
          <div className="up-row" key={i}>
            <div style={{ width: 150 }}>
              <Select label="Check" value={a.source} align="left" options={SOURCES.map((s) => ({ value: s.value, label: s.label }))} onChange={(v) => {
                const next = SOURCES.find((s) => s.value === v)!;
                setAssert(i, { source: next.value, op: next.ops[0], path: next.path ? "" : undefined });
              }} />
            </div>
            {src.path !== undefined && <input className="input mono" style={{ flex: 1 }} value={a.path ?? ""} placeholder={src.path} aria-label={src.label} onChange={(e) => setAssert(i, { path: e.target.value })} />}
            <div style={{ width: 150 }}>
              <Select label="Comparison" value={a.op} align="left" options={src.ops.map((o) => ({ value: o, label: OP_LABEL[o] }))} onChange={(v) => setAssert(i, { op: v as AssertOp })} />
            </div>
            {!noValue && <input className="input mono" style={{ flex: 1 }} value={a.value ?? ""} placeholder={valuePlaceholder(a)} aria-label="Value" onChange={(e) => setAssert(i, { value: e.target.value })} />}
            <button type="button" className="icon-btn" aria-label="Remove assertion" title="Remove" onClick={() => onChange({ assert: asserts.filter((_, j) => j !== i) })}>
              <X size={15} />
            </button>
          </div>
        );
      })}
      <div>
        <button type="button" className="btn ghost sm" disabled={asserts.length >= 20} onClick={() => onChange({ assert: [...asserts, { source: "json", path: "", op: "exists" }] })}>
          <Plus size={14} />
          Add assertion
        </button>
      </div>
      <div className="up-sub-h">Save values for later requests</div>
      {extract.map((e, i) => (
        <div className="up-row" key={i}>
          <input className="input mono" style={{ width: 130 }} value={e.var} placeholder="token" aria-label="Variable name" onChange={(ev) => setExtract(i, { var: ev.target.value })} />
          <div style={{ width: 150 }}>
            <Select label="From" value={e.source} align="left" options={EXTRACT_SOURCES} onChange={(v) => setExtract(i, { source: v as ApiExtract["source"], path: v === "status" ? undefined : "" })} />
          </div>
          {e.source !== "status" && (
            <input className="input mono" style={{ flex: 1 }} value={e.path ?? ""} placeholder={e.source === "json" ? "$.data.token" : e.source === "header" ? "Location" : '"id":\\s*(\\d+)'} aria-label="Path" onChange={(ev) => setExtract(i, { path: ev.target.value })} />
          )}
          <button type="button" className="icon-btn" aria-label="Remove value" title="Remove" onClick={() => onChange({ extract: extract.filter((_, j) => j !== i) })}>
            <X size={15} />
          </button>
        </div>
      ))}
      <div>
        <button type="button" className="btn ghost sm" disabled={extract.length >= 10} onClick={() => onChange({ extract: [...extract, { var: "", source: "json", path: "" }] })}>
          <Plus size={14} />
          Save a value
        </button>
      </div>
    </>
  );
}

const asResult = (columns: string[], rows: unknown[][]): Result => ({ columns, rows, truncated: false, rows_read: rows.length, duration_ms: 0, sql: "", bucket: 0, from: "", to: "" });

const ms = (v: unknown) => (toNumber(v) === null ? <span className="dim">—</span> : formatMs(toNumber(v)!));

export function ApiRunsPanel({ runs, loading, error }: { runs?: DetailRun[]; loading: boolean; error?: Error | null }) {
  const [open, setOpen] = useState<DetailRun | null>(null);
  const keys = ["created_at", "ok", "duration_ms", "requests", "failed", "error", "i"];
  const result = runs && asResult(keys, runs.map((r, i) => {
    const reqs = r.detail.requests ?? [];
    const failed = reqs.find((q) => q.state === "failed");
    return [r.created_at, r.ok, r.duration_ms, `${reqs.filter((q) => q.state === "passed").length} / ${reqs.length}`, failed?.name ?? "", r.error, i];
  }));
  const cols: Column[] = [
    { key: "created_at", label: "Time", width: 190, render: (v) => <span className="mono">{formatLogTime(String(v))}</span> },
    { key: "ok", label: "Result", width: 110, render: (v) => <StateBadge state={v ? "up" : "down"} /> },
    { key: "duration_ms", label: "Total", align: "right", render: ms },
    { key: "requests", label: "Passed", align: "right" },
    { key: "failed", label: "Failed request", render: (v) => (v ? <span className="up-err">{String(v)}</span> : <span className="dim">—</span>) },
    { key: "error", label: "Error", render: (v) => (v ? <span className="up-err">{String(v)}</span> : <span className="dim">—</span>) },
  ];
  return (
    <>
      <Panel
        className="span-12"
        title="Runs"
        description="The latest 500 runs in this range, newest first. Click one for each request's status, time, size and the assertion that failed."
        result={result}
        loading={loading}
        error={error ?? undefined}
        bodyHeight={120}
        table={result && (runs!.length === 0 ? <div className="panel-state">No runs in this range.</div> : <DataTable {...withLocation(result, cols, runs)} maxHeight={440} sortable={false} onRowClick={(row) => setOpen(runs![Number(row[6])])} />)}
      />
      {open && (
        <Drawer title={`Run at ${formatLogTime(open.created_at)}`} onClose={() => setOpen(null)}>
          <ApiRequestTable run={open} />
        </Drawer>
      )}
    </>
  );
}

export function ApiRequestTable({ run }: { run: DetailRun }) {
  const reqs = run.detail.requests ?? [];
  return (
    <div className="up-run">
      <StateBadge state={run.ok ? "up" : "down"} large />
      {run.error && <div className="up-err">{run.error}</div>}
      <ol className="up-run-steps">
        {reqs.map((q, i) => {
          const Icon = q.state === "passed" ? CheckCircle2 : q.state === "failed" ? XCircle : CircleDashed;
          return (
            <li key={i} className={`up-run-step ${q.state}`}>
              <Icon size={14} aria-hidden />
              <div className="up-run-step-text">
                <strong>
                  {i + 1}. {q.name}
                </strong>
                <span className="mono dim">
                  {q.method} {q.url}
                </span>
                {q.state !== "skipped" && (
                  <span className="dim">
                    {q.status ? `HTTP ${q.status}` : "No response"}
                    {q.duration_ms !== undefined && ` · ${formatMs(q.duration_ms)}`}
                    {q.size !== undefined && ` · ${formatBytes(q.size)}`}
                  </span>
                )}
                {q.state === "skipped" && <span>Not run: an earlier request failed.</span>}
                {q.assertion && (
                  <span className="up-err">
                    Expected {q.assertion}
                    {q.got && ` (got ${q.got})`}
                  </span>
                )}
                {q.error && <span className="up-err">{q.error}</span>}
                {q.excerpt && <pre className="up-run-list mono">{q.excerpt}</pre>}
              </div>
            </li>
          );
        })}
      </ol>
    </div>
  );
}
