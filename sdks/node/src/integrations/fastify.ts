import type { IncomingMessage, ServerResponse } from "node:http";
import type { Agent } from "../agent.js";
import { observeRequest, trackedState } from "../capture.js";
import { runInRequest } from "../context.js";
import { resolveAgent } from "../global.js";

/** The parts of Fastify's request and reply the SDK reads. */
interface FastifyRequest {
  raw: IncomingMessage;
  url: string;
  routeOptions?: { url?: string };
  routerPath?: string;
  user?: unknown;
}
interface FastifyReply {
  raw: ServerResponse;
}
type Done = (err?: Error) => void;
interface FastifyInstance {
  addHook(name: string, fn: (...args: any[]) => void): unknown;
}

/** `routeOptions.url` (Fastify 4.10+), else `routerPath`, e.g. `/users/:id`. */
export function fastifyRoute(request: FastifyRequest): string {
  return request.routeOptions?.url ?? request.routerPath ?? "";
}

/**
 * A Fastify plugin (also NestJS on the Fastify adapter). It applies to the
 * whole app, like fastify-plugin:
 *
 *     await app.register(visionary.fastify());
 *
 * Errors that reach Fastify's error handling (onError) are recorded; Fastify
 * still answers them as before.
 */
export function fastify(agent?: Agent) {
  const plugin = function visionary(instance: FastifyInstance, _opts: unknown, done: Done): void {
    const a = resolveAgent(agent);
    instance.addHook("onRequest", (request: FastifyRequest, reply: FastifyReply, next: Done) => {
      const state = observeRequest(
        a,
        request.raw,
        reply.raw,
        () => fastifyRoute(request),
        request.raw.url,
        () => request.user,
      );
      if (state) runInRequest(state, () => next());
      else next();
    });
    // Body parsing can resume in another context; re-enter before the handler.
    instance.addHook("preHandler", (request: FastifyRequest, _reply: FastifyReply, next: Done) => {
      const state = trackedState(request.raw);
      if (state) runInRequest(state, () => next());
      else next();
    });
    instance.addHook("onError", (request: FastifyRequest, _reply: FastifyReply, error: unknown, next: Done) => {
      a.recordUnhandled(error, trackedState(request.raw));
      next();
    });
    done();
  };
  // Don't encapsulate: the hooks must apply to every route (fastify-plugin's markers).
  Object.assign(plugin, {
    [Symbol.for("skip-override")]: true,
    [Symbol.for("fastify.display-name")]: "visionary",
  });
  return plugin;
}
