/**
 * Visionary collector for Node.js. It writes one row per HTTP request
 * (api_logs), periodic host/process/runtime samples (system_metrics), and the
 * spans, errors and releases of the service, in batches off the request path.
 *
 *     import visionary from "visionary-sdk";
 *     visionary.init();
 *     app.use(visionary.express());
 */
import { setRoute, setUser } from "./context.js";
import { captureException, close, getAgent, init } from "./global.js";
import { express, expressErrorHandler } from "./integrations/express.js";
import { fastify } from "./integrations/fastify.js";
import { koa } from "./integrations/koa.js";
import { VisionaryInterceptor, VisionaryModule } from "./integrations/nest.js";
import { wrap } from "./integrations/node-http.js";
import { mysqlPoolStats, pgPoolStats } from "./system/pool.js";
import { instrumentMysql2, instrumentPg, instrumentSqlite, prismaExtension } from "./telemetry/db.js";
import { explainWith } from "./telemetry/explain.js";
import { apolloPlugin, graphqlOperation } from "./telemetry/graphql.js";
import { background, span } from "./telemetry/spans.js";

export { Agent, SDK_VERSION } from "./agent.js";
export type { Dialect, LookupResult, Options, User } from "./config.js";
export type { Logger } from "./log.js";
export type { DbStats } from "./system/recorder.js";
export type { SpanKind } from "./telemetry/spans.js";
export type { Explainer } from "./telemetry/explain.js";
export { expressRoute } from "./integrations/express.js";
export { fastifyRoute } from "./integrations/fastify.js";
export {
  VisionaryInterceptor,
  VisionaryModule,
  apolloPlugin,
  background,
  captureException,
  close,
  express,
  expressErrorHandler,
  explainWith,
  fastify,
  getAgent,
  graphqlOperation,
  init,
  instrumentMysql2,
  instrumentPg,
  instrumentSqlite,
  koa,
  mysqlPoolStats,
  pgPoolStats,
  prismaExtension,
  setRoute,
  setUser,
  span,
  wrap,
};

const visionary = {
  init,
  close,
  getAgent,
  setUser,
  setRoute,
  captureException,
  span,
  background,
  graphqlOperation,
  apolloPlugin,
  explainWith,
  express,
  expressErrorHandler,
  fastify,
  koa,
  wrap,
  VisionaryModule,
  VisionaryInterceptor,
  instrumentPg,
  instrumentMysql2,
  instrumentSqlite,
  prismaExtension,
  pgPoolStats,
  mysqlPoolStats,
};

export default visionary;
