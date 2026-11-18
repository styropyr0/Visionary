import type { IncomingMessage, ServerResponse } from "node:http";
import type { Agent } from "../agent.js";
import { clientIp, trackedState } from "../capture.js";
import { RequestState } from "../context.js";
import type { Denial, LimitRequest, LimitState, Limiter } from "./limiter.js";

const LIMIT = Symbol.for("visionary.ratelimit");

interface Checked {
  limiter: Limiter;
  state: LimitState;
  req: LimitRequest;
  /** The logged request's state, or a private one holding setUser. */
  user: RequestState;
  answered: boolean;
}

/** The parts of the response objects the SDK can answer through. */
interface FastifyReply {
  raw: ServerResponse;
  code(status: number): FastifyReply;
  header(name: string, value: string): FastifyReply;
  send(body: string): unknown;
}
interface KoaContext {
  res: ServerResponse;
  status: number;
  body: unknown;
  set(name: string, value: string): void;
}

function first(v: string | string[] | undefined): string {
  return (Array.isArray(v) ? v[0] : v) ?? "";
}

function limitRequest(req: IncomingMessage, url: string): LimitRequest {
  const q = url.indexOf("?");
  let query: URLSearchParams | undefined;
  return {
    method: req.method ?? "GET",
    path: q >= 0 ? url.slice(0, q) : url,
    remoteIp: clientIp(req),
    header: (name) => first(req.headersDistinct?.[name]) || first(req.headers[name]),
    query: (name) => {
      query ??= new URLSearchParams(q >= 0 ? url.slice(q + 1) : "");
      return query.get(name) ?? "";
    },
  };
}

/** Sends a denial through a Node response, a Fastify reply or a Koa context. */
export function answer(target: unknown, d: Denial): void {
  const t = target as Record<string, unknown>;
  if (typeof t.setHeader === "function" && typeof t.end === "function") {
    const res = target as ServerResponse;
    if (res.headersSent) return;
    res.statusCode = d.status;
    res.setHeader("Content-Type", d.contentType);
    if (d.retryAfter > 0) res.setHeader("Retry-After", String(d.retryAfter));
    res.end(d.body);
  } else if (typeof t.send === "function" && typeof t.code === "function") {
    const reply = (target as FastifyReply).code(d.status).header("Content-Type", d.contentType);
    if (d.retryAfter > 0) reply.header("Retry-After", String(d.retryAfter));
    reply.send(d.body);
  } else if (typeof t.set === "function" && t.res) {
    const ctx = target as KoaContext;
    ctx.status = d.status;
    ctx.set("Content-Type", d.contentType);
    if (d.retryAfter > 0) ctx.set("Retry-After", String(d.retryAfter));
    ctx.body = d.body;
  }
}

/**
 * The request phase, before the app's handler: answers through `reply` and
 * returns true when a rule denies the request. Fails open.
 */
export function checkRequest(agent: Agent, req: IncomingMessage, reply: unknown, url: string, userOf?: () => unknown): boolean {
  const limiter = agent.rateLimits?.limiter;
  const marked = req as unknown as Record<symbol, Checked | undefined>;
  if (!limiter?.active || marked[LIMIT]) return false;
  try {
    const lr = limitRequest(req, url);
    let user = trackedState(req);
    if (!user) {
      user = new RequestState("", 0, 0n, lr.method, lr.path);
      user.userOf = userOf;
    }
    const state = limiter.checkRequest(lr, Date.now() / 1000);
    const checked: Checked = { limiter, state, req: lr, user, answered: false };
    marked[LIMIT] = checked;
    if (!state.denial) return false;
    checked.answered = true;
    answer(reply, state.denial);
    return true;
  } catch {
    return false;
  }
}

/** The private user holder of a rate-limited request that is not logged. */
export function limitUserHolder(req: unknown): RequestState | undefined {
  const c = (req as Record<symbol, Checked | undefined> | undefined)?.[LIMIT];
  return c && !trackedState(req) ? c.user : undefined;
}

function checkedOf(req: unknown): Checked | undefined {
  const r = req as Record<string | symbol, any> | undefined;
  return r?.[LIMIT] ?? r?.raw?.[LIMIT] ?? r?.req?.[LIMIT];
}

/**
 * Checks the rate-limit rules that need the user (by user, role or an
 * attribute such as the plan) once setUser has run, and answers the request
 * when one denies it. Call it after your auth code and stop when it returns
 * true. Only the first call per request counts; outside a request the SDK's
 * middleware handled it returns false.
 *
 *     setUser({ id: user.id, role: user.role });
 *     if (rateLimited(req, res, { plan: user.plan })) return;
 *
 * `req` is the Node/Express request, a Fastify request or a Koa context;
 * `res` the Node/Express response, a Fastify reply or a Koa context.
 */
export function rateLimited(req: object, res: object, attrs?: Record<string, string> | null): boolean {
  try {
    const c = checkedOf(req);
    if (!c) return false;
    if (c.answered) return true;
    c.user.resolveUser();
    const clean: Record<string, string> = {};
    for (const [k, v] of Object.entries(attrs ?? {})) if (v !== undefined && v !== null) clean[k] = String(v);
    const d = c.limiter.checkUser(c.state, c.req, { id: c.user.userId, role: c.user.userRole }, clean, Date.now() / 1000);
    if (!d) return false;
    c.answered = true;
    answer(res, d);
    return true;
  } catch {
    return false;
  }
}
