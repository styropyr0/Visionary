/** Anything with console-style methods (console, pino, winston). */
export interface Logger {
  info(msg: string, ...args: unknown[]): void;
  warn(msg: string, ...args: unknown[]): void;
  error(msg: string, ...args: unknown[]): void;
}

export const consoleLogger: Logger = {
  info: (msg, ...args) => console.info(msg, ...args),
  warn: (msg, ...args) => console.warn(msg, ...args),
  error: (msg, ...args) => console.error(msg, ...args),
};

export function errMessage(e: unknown): string {
  if (!(e instanceof Error)) return String(e);
  // Connection failures (ECONNREFUSED to every address) are AggregateErrors
  // with an empty message.
  const inner = (e as { errors?: unknown[] }).errors?.[0];
  return e.message || (inner instanceof Error ? inner.message : "") || (e as { code?: string }).code || e.name;
}
