import { useEffect, useMemo, useRef, useState } from "react";
import { Pause, Play, ShieldCheck } from "lucide-react";
import { api } from "../lib/api";
import { useQuery } from "../lib/useQuery";
import { ReplayEngine, idleGaps, type ReplayEvent } from "../lib/replay";
import { ErrorNotice } from "../components/ErrorNotice";
import { Select } from "../components/Select";

interface ReplayMeta {
  session: Record<string, unknown>;
  chunks: { seq: number; started: string; ended: string; events: number; bytes: number }[];
}

/** A timeline event shown on the scrubber. */
export interface ReplayMarker {
  at: string;
  kind: string;
  label: string;
  bad: boolean;
}

const SPEEDS = ["1", "2", "4", "8"].map((v) => ({ value: v, label: `${v}×` }));
const STAGE_H = 460;

function clockTime(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

export function ReplayPlayer({ id, markers }: { id: string; markers: ReplayMarker[] }) {
  const meta = useQuery(`rum-replay:${id}`, (signal) => api<ReplayMeta>(`/api/rum/replay/${encodeURIComponent(id)}`, { signal }));
  const [events, setEvents] = useState<ReplayEvent[] | null>(null);
  const [loaded, setLoaded] = useState(0);
  const [loadError, setLoadError] = useState<Error | null>(null);

  useEffect(() => {
    if (!meta.data) return;
    const ctrl = new AbortController();
    (async () => {
      const all: ReplayEvent[] = [];
      for (const c of meta.data!.chunks) {
        const part = await api<ReplayEvent[]>(`/api/rum/replay/${encodeURIComponent(id)}/chunk/${c.seq}`, { signal: ctrl.signal });
        all.push(...part);
        setLoaded((n) => n + 1);
      }
      all.sort((a, b) => a[0] - b[0]);
      setEvents(all);
    })().catch((e) => {
      if (!ctrl.signal.aborted) setLoadError(e instanceof Error ? e : new Error(String(e)));
    });
    return () => ctrl.abort();
  }, [meta.data, id]);

  if (meta.error) return <ErrorNotice title="Could not load the replay" error={meta.error} />;
  if (loadError) return <ErrorNotice title="Could not load the replay" error={loadError} />;
  if (!events) {
    const total = meta.data?.chunks.length ?? 0;
    return <div className="panel-state">Loading the recording{total ? ` (${loaded} of ${total} parts)` : ""}…</div>;
  }
  if (!events.length) return <div className="panel-state">This recording is empty.</div>;
  return <Player events={events} markers={markers} />;
}

function Player({ events, markers }: { events: ReplayEvent[]; markers: ReplayMarker[] }) {
  const frameRef = useRef<HTMLIFrameElement>(null);
  const stageRef = useRef<HTMLDivElement>(null);
  const engine = useRef<ReplayEngine | null>(null);
  const clock = useRef(events[0][0]);
  const [ready, setReady] = useState(false);
  const [now, setNow] = useState(events[0][0]);
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState("1");
  const [skip, setSkip] = useState(true);
  const [view, setView] = useState({ w: 1280, h: 800 });
  const [pointer, setPointer] = useState<{ x: number; y: number; clicks: number } | null>(null);
  const [stageW, setStageW] = useState(800);
  const start = events[0][0];
  const end = events[events.length - 1][0];
  const gaps = useMemo(() => idleGaps(events), [events]);

  useEffect(() => {
    const el = stageRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setStageW(el.clientWidth));
    ro.observe(el);
    setStageW(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  useEffect(() => {
    if (!ready || !frameRef.current) return;
    const e = new ReplayEngine(frameRef.current, events);
    e.onViewport = (w, h) => setView({ w, h });
    e.onPointer = (x, y, click) => setPointer((p) => ({ x, y, clicks: (p?.clicks ?? 0) + (click ? 1 : 0) }));
    engine.current = e;
    e.seek(start);
    clock.current = start;
    setNow(start);
  }, [ready, events, start]);

  useEffect(() => {
    if (!playing) return;
    let raf = 0;
    let last = performance.now();
    const step = (ts: number) => {
      const e = engine.current;
      if (!e) return;
      let t = clock.current + (ts - last) * Number(speed);
      last = ts;
      if (skip) {
        const gap = gaps.find(([a, b]) => t >= a && t < b);
        if (gap) t = gap[1];
      }
      const done = t >= end;
      if (done) t = end;
      e.advance(t);
      clock.current = t;
      setNow(t);
      if (done) setPlaying(false);
      else raf = requestAnimationFrame(step);
    };
    raf = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf);
  }, [playing, speed, skip, gaps, end]);

  const seek = (t: number) => {
    t = Math.min(end, Math.max(start, t));
    engine.current?.seek(t);
    clock.current = t;
    setNow(t);
  };

  const span = Math.max(1, end - start);
  const scale = Math.min(1, stageW / view.w, STAGE_H / view.h);
  const pct = (at: number) => `${(100 * (at - start)) / span}%`;
  const shown = markers
    .map((m) => ({ ...m, t: new Date(m.at).getTime() }))
    .filter((m) => m.t >= start - 1000 && m.t <= end + 1000)
    .map((m) => ({ ...m, t: Math.min(end, Math.max(start, m.t)) }));

  return (
    <div className="rp">
      <div className="rp-stage" ref={stageRef} style={{ height: Math.round(view.h * scale) }}>
        <div className="rp-screen" style={{ width: view.w, height: view.h, left: Math.max(0, (stageW - view.w * scale) / 2), transform: `scale(${scale})` }}>
          <iframe ref={frameRef} src="/replay-frame.html" sandbox="allow-same-origin" title="Session replay" onLoad={() => setReady(true)} />
          {pointer && (
            <span className="rp-pointer" style={{ left: pointer.x, top: pointer.y }}>
              <span key={pointer.clicks} className={pointer.clicks ? "rp-click" : ""} />
            </span>
          )}
        </div>
      </div>
      <div className="rp-track" onPointerDown={(e) => {
        const r = e.currentTarget.getBoundingClientRect();
        seek(start + ((e.clientX - r.left) / r.width) * span);
      }}>
        {gaps.map(([a, b], i) => (
          <span key={i} className="rp-idle" style={{ left: pct(a), width: `${(100 * (b - a)) / span}%` }} />
        ))}
        <span className="rp-played" style={{ width: pct(now) }} />
        {shown.map((m, i) => (
          <button
            type="button"
            key={i}
            className={`rp-mark kind-${m.kind} ${m.bad ? "bad" : ""}`}
            style={{ left: pct(m.t) }}
            title={`${clockTime(m.t - start)} ${m.label}`}
            aria-label={`Go to ${m.label}`}
            onPointerDown={(e) => e.stopPropagation()}
            onClick={() => seek(m.t - 1000)}
          />
        ))}
      </div>
      <div className="rp-controls">
        <button type="button" className="icon-btn" onClick={() => (now >= end ? (seek(start), setPlaying(true)) : setPlaying(!playing))} aria-label={playing ? "Pause" : "Play"}>
          {playing ? <Pause size={16} /> : <Play size={16} />}
        </button>
        <span className="mono dim">
          {clockTime(now - start)} / {clockTime(span)}
        </span>
        <Select label="Speed" value={speed} options={SPEEDS} onChange={setSpeed} align="left" />
        <label className="rp-skip">
          <input type="checkbox" checked={skip} onChange={(e) => setSkip(e.target.checked)} />
          Skip inactivity
        </label>
        <span className="rp-legend dim">
          <span className="rp-key kind-page_load" /> Page <span className="rp-key bad" /> Error or failed call <span className="rp-key kind-api" /> API call
          <span className="rp-key kind-server_log" /> Server
        </span>
      </div>
      <p className="rp-note dim">
        <ShieldCheck size={14} aria-hidden />
        <span>
          Privacy masked in the browser before upload: input values show as asterisks unless the app turned that off, passwords, emails, phone numbers and card
          fields are never recorded, and blocked elements show as striped boxes. Styles and images load from the recorded site, so a page that changed since may look
          different.
        </span>
      </p>
    </div>
  );
}
