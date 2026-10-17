import type { Agent } from "../agent.js";
import { trackedState } from "../capture.js";
import type { Options } from "../config.js";
import { currentRequest } from "../context.js";
import { init, resolveAgent } from "../global.js";
import { optionalRequire } from "../util.js";

/** The parts of NestJS's ExecutionContext and CallHandler the SDK uses. */
interface ExecutionContext {
  getType?(): string;
  switchToHttp(): { getRequest(): any };
}
interface CallHandler {
  handle(): { pipe(...ops: unknown[]): unknown };
}

/**
 * A NestJS interceptor that records exceptions thrown by controllers
 * (HttpExceptions below 500 are skipped) and rethrows them, so Nest's
 * exception filters answer exactly as before. Needs rxjs, which every Nest
 * app has. VisionaryModule registers it globally.
 */
export class VisionaryInterceptor {
  private readonly rx = optionalRequire("rxjs");

  constructor(private readonly agent?: Agent) {}

  intercept(context: ExecutionContext, next: CallHandler): unknown {
    const rx = this.rx;
    if (!rx?.catchError || (context.getType && context.getType() !== "http")) return next.handle();
    const req = context.switchToHttp().getRequest();
    return next.handle().pipe(
      rx.catchError((err: unknown) => {
        resolveAgent(this.agent).recordUnhandled(err, trackedState(req?.raw ?? req) ?? currentRequest());
        return rx.throwError(() => err);
      }),
    );
  }
}

/**
 * A tiny NestJS module: `VisionaryModule.forRoot()` initialises the SDK and
 * registers VisionaryInterceptor for every controller. Request logging itself
 * comes from the HTTP adapter, in main.ts:
 *
 *     app.use(visionary.express());               // Express adapter
 *     await app.register(visionary.fastify());    // Fastify adapter
 *
 * No decorators are needed: a dynamic module is plain metadata, and
 * "APP_INTERCEPTOR" is @nestjs/core's token for global interceptors.
 */
export class VisionaryModule {
  static forRoot(options?: Options) {
    const agent = init(options);
    return {
      module: VisionaryModule,
      global: true,
      providers: [{ provide: "APP_INTERCEPTOR", useValue: new VisionaryInterceptor(agent) }],
    };
  }
}
