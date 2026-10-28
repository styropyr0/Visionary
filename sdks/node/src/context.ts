import { AsyncLocalStorage } from "node:async_hooks";
import type { User } from "./config.js";
import type { HeldLogs } from "./logs/pipeline.js";
import type { SpanRow } from "./telemetry/spans.js";

/** For the CPU profiler: requests started so far, and while a profile runs,
 * the milliseconds each route's finished requests took ("GET /users/:id"). */
export const requestTally: { started: number; busy: Record<string, number> | null } = { started: 0, busy: null };

/**
 * What the SDK knows about the request being handled. Integrations create one
 * per logged request and run the rest of the request inside it, so app code
 * (auth middleware, handlers, database calls) can describe it.
 */
export class RequestState {
  userId = "";
  userHandle = "";
  userRole = "";
  /** The account a sign-in on this request tried (loginAttempt). */
  loginAccount = "";
  route = "";
  /** Span slots are taken in start order; only the first spanMax keep rows. */
  nextSlot = 0;
  spans: SpanRow[] = [];
  dbCount = 0;
  dbMs = 0;
  httpCount = 0;
  httpMs = 0;
  done = false;
  /** Spans of background work stored after the request was logged. */
  late = 0;
  traceId = "";
  /** An error was captured, so the request counts as failed. */
  failed = false;
  heldLogs?: HeldLogs;
  /** The integration's route lookup (Express req.route, Fastify routeOptions). */
  routeOf?: () => string | undefined;
  /** The framework's signed-in user, used when setUser was not called. */
  userOf?: () => unknown;

  constructor(
    readonly requestId: string,
    readonly startedAt: number,
    readonly startHr: bigint,
    readonly method: string,
    readonly path: string,
  ) {}

  /** setRoute() wins over what the framework reports. */
  resolveRoute(): string {
    if (this.route) return this.route;
    try {
      return this.routeOf?.() ?? "";
    } catch {
      return "";
    }
  }

  /** Falls back to the framework user (Passport/JWT req.user, Koa ctx.state.user). */
  resolveUser(): void {
    if (this.userId || !this.userOf) return;
    try {
      const u = this.userOf() as Record<string, unknown> | undefined;
      if (!u || typeof u !== "object") return;
      const id = u.id ?? u._id ?? u.sub ?? u.userId;
      if (id === undefined || id === null || typeof id === "object") return;
      const text = (v: unknown) => (typeof v === "string" ? v : undefined);
      this.setUser(id, text(u.username) ?? text(u.email) ?? text(u.handle) ?? text(u.name), text(u.role));
    } catch {
      // a getter that throws is not worth failing the log for
    }
  }

  setUser(id: unknown, handle?: string, role?: string): void {
    this.userId = id === null || id === undefined ? "" : String(id);
    this.userHandle = handle ?? "";
    this.userRole = role ?? "";
  }
}

const requests = new AsyncLocalStorage<RequestState>();
const quiet = new AsyncLocalStorage<boolean>();

export function currentRequest(): RequestState | undefined {
  return requests.getStore();
}

export function runInRequest<T>(state: RequestState, fn: () => T): T {
  return requests.run(state, fn);
}

/** Runs the SDK's own I/O so instrumentation doesn't record it as spans. */
export function quietly<T>(fn: () => T): T {
  return quiet.run(true, fn);
}

export function isQuiet(): boolean {
  return quiet.getStore() === true;
}

/**
 * Attaches the signed-in user to the request being logged. Call it from auth
 * middleware once the user is known. Only the id is required. Outside a
 * request handled by a Visionary integration it does nothing.
 *
 *     setUser({ id: user.id, handle: user.email, role: user.role })
 *     setUser(user.id)
 */
export function setUser(user: User | string | number | null | undefined, handle?: string, role?: string): void {
  const state = requests.getStore();
  if (!state) return;
  if (user !== null && typeof user === "object") state.setUser(user.id, user.handle, user.role);
  else state.setUser(user, handle, role);
}

/**
 * Records the account a sign-in request tried: the username or email typed,
 * whether or not it exists or the password was right. Call it in the login
 * handler; the security detectors use it to tell one IP trying many accounts,
 * many IPs trying one account, and a signed-in user trying someone else's.
 * No-op outside a request.
 *
 *     loginAttempt(req.body.email)
 */
export function loginAttempt(account: string): void {
  const state = requests.getStore();
  if (state) state.loginAccount = account ?? "";
}

/** Records the route pattern (e.g. `/users/:id`) for routers the integration
 * can't see. No-op outside a request. */
export function setRoute(pattern: string): void {
  const state = requests.getStore();
  if (state) state.route = pattern ?? "";
}
