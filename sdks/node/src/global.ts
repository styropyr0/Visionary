import { Agent } from "./agent.js";
import type { Options } from "./config.js";

let current: Agent | undefined;

/**
 * Builds the process-wide agent (from the environment, with `options` taking
 * precedence), starts it and flushes it when the event loop empties. Returns
 * the existing agent when called again. Never throws: with no database
 * configured, or on any problem, it returns an agent that does nothing.
 */
export function init(options: Options = {}): Agent {
  if (current) return current;
  let agent: Agent;
  try {
    agent = new Agent(options).start();
  } catch (e) {
    console.error("visionary: init failed; collection is off", e);
    agent = new Agent({ ...options, db: undefined, databaseUrl: "", d1: {}, logger: silent });
  }
  current = agent;
  process.once("beforeExit", () => void agent.close());
  return agent;
}

const silent = { info() {}, warn() {}, error() {} };

/** The agent set up by init(), if any. */
export function getAgent(): Agent | undefined {
  return current;
}

/** The given agent, else the one from init() (initialising it from the
 * environment if nobody has). */
export function resolveAgent(agent?: Agent): Agent {
  return agent ?? current ?? init();
}

/** Flushes and stops the agent from init(). Call it on shutdown (SIGTERM),
 * after the server stops accepting requests. */
export async function close(timeoutMs?: number): Promise<void> {
  await current?.close(timeoutMs);
}

/** Records a handled error (handled = 1); inside a request it links to it. */
export function captureException(err: unknown): void {
  current?.recordError(err, true);
}
