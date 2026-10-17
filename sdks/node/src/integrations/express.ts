import type { IncomingMessage, ServerResponse } from "node:http";
import type { Agent } from "../agent.js";
import { observeRequest, trackedState } from "../capture.js";
import { currentRequest, runInRequest } from "../context.js";
import { resolveAgent } from "../global.js";

/** The parts of Express's request the SDK reads. */
interface ExpressRequest extends IncomingMessage {
  originalUrl?: string;
  baseUrl?: string;
  route?: { path?: unknown };
  user?: unknown;
}

type Next = (err?: unknown) => void;

/** `req.baseUrl + req.route.path`, e.g. `/api/users/:id`. */
export function expressRoute(req: ExpressRequest): string {
  const path = req.route?.path;
  if (path === undefined || path === null) return "";
  return (req.baseUrl ?? "") + (Array.isArray(path) ? path.join(",") : String(path));
}

/**
 * Express middleware (also NestJS on the Express adapter). Mount it first,
 * so it sees every request and the final status:
 *
 *     app.use(visionary.express());
 *     // routes
 *     app.use(visionary.expressErrorHandler()); // after the routes
 */
export function express(agent?: Agent) {
  const a = resolveAgent(agent);
  return function visionaryMiddleware(req: IncomingMessage, res: ServerResponse, next: Next): void {
    const r = req as ExpressRequest;
    const state = observeRequest(a, r, res, () => expressRoute(r), r.originalUrl ?? r.url, () => r.user);
    if (state) runInRequest(state, () => next());
    else next();
  };
}

/**
 * Express error middleware that records the error (errors with a status
 * below 500 are skipped) and passes it on unchanged, so Express (or your own
 * error handler) answers exactly as before. Add it after the routes.
 */
export function expressErrorHandler(agent?: Agent) {
  const a = resolveAgent(agent);
  return function visionaryErrorHandler(err: unknown, req: IncomingMessage, _res: ServerResponse, next: Next): void {
    a.recordUnhandled(err, trackedState(req) ?? currentRequest());
    next(err);
  };
}
