package visionary

import (
	"context"
	"regexp"
	"strings"
)

var (
	gqlStrings = regexp.MustCompile(`"""[\s\S]*?"""|"(?:[^"\\]|\\.)*"`)
	gqlNumbers = regexp.MustCompile(`([:\[,(=]\s*)-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?`)
	gqlSpace   = regexp.MustCompile(`\s+`)
)

// GraphQLOperation records one GraphQL operation as a span, named after its
// type and name ("query GetUser"). The document is kept with its literal
// values removed, so the dashboard can measure its depth and field count and
// link it to the queries its resolvers ran. Call the returned function when
// the operation is done:
//
//	end := visionary.GraphQLOperation(ctx, "query", "GetUser", document)
//	result := execute(ctx, document, variables)
//	end(firstError(result))
//
// With gqlgen, call it from an AroundOperations handler extension, using
// graphql.GetOperationContext(ctx) for the operation and its raw query.
func GraphQLOperation(ctx context.Context, opType, name, document string) (end func(err error)) {
	if opType == "" {
		opType = "query"
	}
	if name == "" {
		name = "anonymous"
	}
	markKind(KindGraphQL)
	s := beginSpan(ctx, KindGraphQL)
	if s == nil {
		return func(error) {}
	}
	s.rec.name = truncateRunes(opType+" "+name, 200)
	s.rec.detail = truncateRunes(GraphQLDocument(document), maxDetail)
	return s.end
}

// GraphQLDocument strips a document's literal values (strings, numbers) and
// collapses whitespace. Variables, field names and arguments stay.
func GraphQLDocument(doc string) string {
	doc = gqlStrings.ReplaceAllString(doc, `"?"`)
	doc = gqlNumbers.ReplaceAllString(doc, "${1}?")
	return strings.TrimSpace(gqlSpace.ReplaceAllString(doc, " "))
}
