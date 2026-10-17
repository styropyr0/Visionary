import type { IncomingMessage, ServerResponse } from "node:http";
import type { Agent } from "../agent.js";
import { observeRequest } from "../capture.js";
import { currentRequest, runInRequest } from "../context.js";
import { resolveAgent } from "../global.js";

/** The parts of Koa's context the SDK reads. */
interface KoaContext {
  req: IncomingMessage;
  res: ServerResponse;
  originalUrl?: string;
  _matchedRoute?: unknown;
  state?: { user?: unknown };
}

/**
 * Koa middleware. Use it first. The route is koa-router's / @koa/router's
 * `ctx._matchedRoute`, e.g. `/users/:id`. Errors that propagate through it
 * are recorded and rethrown, so Koa answers them as before.
 *
 *     app.use(visionary.koa());
 */
export function koa(agent?: Agent) {
  const a = resolveAgent(agent);
  return function visionaryMiddleware(ctx: KoaContext, next: () => Promise<unknown>): Promise<unknown> {
    const route = () => (ctx._matchedRoute === undefined ? "" : String(ctx._matchedRoute));
    const state = observeRequest(a, ctx.req, ctx.res, route, ctx.originalUrl ?? ctx.req.url, () => ctx.state?.user);
    const run = async () => {
      try {
        return await next();
      } catch (e) {
        a.recordUnhandled(e, state ?? currentRequest());
        throw e;
      }
    };
    return state ? runInRequest(state, run) : run();
  };
}
