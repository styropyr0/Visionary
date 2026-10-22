import { MAX_DETAIL, span, startSpan } from "./spans.js";

/** Strips a GraphQL document's literal values (strings, numbers) and
 * collapses whitespace. Variables, fields and arguments stay. */
export function sanitizeGraphQL(doc: string): string {
  return doc
    .replace(/"""[\s\S]*?"""|"(?:[^"\\]|\\.)*"/g, '"?"')
    .replace(/([:[,(=]\s*)-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g, "$1?")
    .replace(/\s+/g, " ")
    .trim();
}

function opName(type: string | undefined, name: string | undefined | null): string {
  return `${type || "query"} ${name || "anonymous"}`.slice(0, 200);
}

/**
 * Records one GraphQL operation around `fn` as a span named "query GetUser",
 * with its document (literals removed) so the dashboard can measure its depth
 * and field count and link it to the queries its resolvers ran.
 *
 *     const result = await visionary.graphqlOperation("query", "GetUser", source, () => execute(args));
 */
export function graphqlOperation<T>(type: string, name: string | undefined, document: string, fn: () => T): T {
  return span("graphql", opName(type, name), fn, { detail: sanitizeGraphQL(document).slice(0, MAX_DETAIL) });
}

/**
 * An Apollo Server (v4+) plugin recording each operation as a span:
 *
 *     new ApolloServer({ typeDefs, resolvers, plugins: [visionary.apolloPlugin()] });
 *
 * The span starts when execution starts, so parsing and validation are left
 * out, and fails when the response carries errors.
 */
export function apolloPlugin() {
  return {
    async requestDidStart() {
      let s: ReturnType<typeof startSpan>;
      return {
        async executionDidStart(ctx: any) {
          s = startSpan({
            kind: "graphql",
            name: opName(ctx?.operation?.operation, ctx?.operationName ?? ctx?.operation?.name?.value),
            detail: sanitizeGraphQL(String(ctx?.source ?? ctx?.request?.query ?? "")).slice(0, MAX_DETAIL),
          });
        },
        async willSendResponse(ctx: any) {
          const errors = ctx?.errors ?? ctx?.response?.body?.singleResult?.errors;
          s?.end(errors?.length ? (errors[0] ?? new Error("GraphQL error")) : undefined);
        },
      };
    },
  };
}
