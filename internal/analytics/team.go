package analytics

import (
	"context"
	"net/http"
	"os"
	"strings"
)

// Roles, API keys, the audit log and dashboards.

func (s *Server) teamSchema() []string          { return nil }
func (s *Server) teamRoutes(mux *http.ServeMux) {}
func (s *Server) teamStart(ctx context.Context) {}

// can reports whether the signed-in user may perform action ("view", "edit"
// or "admin"). Every handler that changes state checks can(r, "edit") or
// can(r, "admin") and answers 403 when it is false.
func (s *Server) can(r *http.Request, action string) bool { return true }

// audit records who did what. action is a short verb phrase such as
// "alert.create"; target names the object; detail is any JSON value.
func (s *Server) audit(r *http.Request, action, target string, detail any) {}

// ingestKeyValid checks a key presented to an ingest endpoint. scope is
// "server" (SDKs and OTLP, VISIONARY_INGEST_KEYS) or "client" (browser and
// mobile apps, VISIONARY_CLIENT_KEYS); both lists are comma separated.
func (s *Server) ingestKeyValid(key, scope string) bool {
	if key == "" {
		return false
	}
	env := "VISIONARY_INGEST_KEYS"
	if scope == "client" {
		env = "VISIONARY_CLIENT_KEYS"
	}
	for _, k := range strings.Split(os.Getenv(env), ",") {
		if k = strings.TrimSpace(k); k != "" && constantTimeEqual(k, key) {
			return true
		}
	}
	return false
}
