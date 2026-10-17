import type { IncomingMessage, ServerResponse } from "node:http";
import type { Agent } from "../agent.js";
import { observeRequest } from "../capture.js";
import { currentRequest, runInRequest } from "../context.js";
import { resolveAgent } from "../global.js";

/**
 * Wraps a plain `node:http` request handler (or any framework that exposes
 * one). There is no router, so the route is whatever the handler passes to
 * `setRoute()`. Exceptions are recorded and rethrown unchanged.
 *
 *     http.createServer(visionary.wrap((req, res) => { ... })).listen(3000);
 */
export function wrap<Req extends IncomingMessage, Res extends ServerResponse>(
  handler: (req: Req, res: Res) => unknown,
  agent?: Agent,
): (req: Req, res: Res) => unknown {
  const a = resolveAgent(agent);
  return function visionaryHandler(req: Req, res: Res): unknown {
    const state = observeRequest(a, req, res, () => "");
    const run = () => {
      let result: unknown;
      try {
        result = handler(req, res);
      } catch (e) {
        a.recordUnhandled(e, state ?? currentRequest());
        throw e;
      }
      if (result && typeof (result as Promise<unknown>).then === "function") {
        return (result as Promise<unknown>).catch((e) => {
          a.recordUnhandled(e, state);
          throw e;
        });
      }
      return result;
    };
    return state ? runInRequest(state, run) : run();
  };
}
