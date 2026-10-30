package analytics

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Session replay: the browser SDK records the page (a DOM snapshot, then its
// mutations, mouse, scroll and inputs, masked on the device) and posts it in
// gzipped chunks to POST /api/ingest/replay. replay_chunks keeps each chunk
// as base64 text, which every backend stores the same way; replay_sessions
// indexes them by session. Off until Settings → Server → Ingest turns it on.

type replayState struct {
	// bytes is the stored total, counted by the sweep and added to on ingest.
	bytes atomic.Int64
}

var replayStates sync.Map // *Server -> *replayState

func (s *Server) replay() *replayState {
	v, _ := replayStates.LoadOrStore(s, &replayState{})
	return v.(*replayState)
}

func (s *Server) replaySchema() []string {
	switch s.q.d {
	case sdk.Postgres:
		return []string{`CREATE TABLE IF NOT EXISTS replay_sessions (
    id          BIGSERIAL PRIMARY KEY,
    session_id  TEXT NOT NULL UNIQUE,
    started_at  TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    app         TEXT,
    user_id     TEXT,
    url         TEXT,
    reason      TEXT,
    chunks      INTEGER NOT NULL DEFAULT 0,
    bytes       BIGINT NOT NULL DEFAULT 0,
    archived    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_replay_sessions_created ON replay_sessions (created_at);
CREATE TABLE IF NOT EXISTS replay_chunks (
    id          BIGSERIAL PRIMARY KEY,
    session_id  TEXT NOT NULL,
    seq         BIGINT NOT NULL,
    created_at  TEXT NOT NULL,
    ended_at    TEXT NOT NULL,
    events      INTEGER NOT NULL,
    bytes       INTEGER NOT NULL,
    payload     TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_replay_chunks_seq ON replay_chunks (session_id, seq);
CREATE INDEX IF NOT EXISTS idx_replay_chunks_created ON replay_chunks (created_at)`}
	case sdk.MySQL:
		return []string{`CREATE TABLE IF NOT EXISTS replay_sessions (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    session_id  VARCHAR(128) NOT NULL UNIQUE,
    started_at  VARCHAR(32) NOT NULL,
    created_at  VARCHAR(32) NOT NULL,
    app         VARCHAR(32),
    user_id     VARCHAR(128),
    url         VARCHAR(2048),
    reason      VARCHAR(16),
    chunks      INT NOT NULL DEFAULT 0,
    bytes       BIGINT NOT NULL DEFAULT 0,
    archived    INT NOT NULL DEFAULT 0
) CHARACTER SET utf8mb4;
CREATE INDEX idx_replay_sessions_created ON replay_sessions (created_at);
CREATE TABLE IF NOT EXISTS replay_chunks (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    session_id  VARCHAR(128) NOT NULL,
    seq         BIGINT NOT NULL,
    created_at  VARCHAR(32) NOT NULL,
    ended_at    VARCHAR(32) NOT NULL,
    events      INT NOT NULL,
    bytes       INT NOT NULL,
    payload     MEDIUMTEXT NOT NULL
) CHARACTER SET utf8mb4;
CREATE UNIQUE INDEX idx_replay_chunks_seq ON replay_chunks (session_id, seq);
CREATE INDEX idx_replay_chunks_created ON replay_chunks (created_at)`}
	}
	return []string{`CREATE TABLE IF NOT EXISTS replay_sessions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL UNIQUE,
    started_at  TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    app         TEXT,
    user_id     TEXT,
    url         TEXT,
    reason      TEXT,
    chunks      INTEGER NOT NULL DEFAULT 0,
    bytes       INTEGER NOT NULL DEFAULT 0,
    archived    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_replay_sessions_created ON replay_sessions (created_at);
CREATE TABLE IF NOT EXISTS replay_chunks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    created_at  TEXT NOT NULL,
    ended_at    TEXT NOT NULL,
    events      INTEGER NOT NULL,
    bytes       INTEGER NOT NULL,
    payload     TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_replay_chunks_seq ON replay_chunks (session_id, seq);
CREATE INDEX IF NOT EXISTS idx_replay_chunks_created ON replay_chunks (created_at)`}
}

func (s *Server) replayRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/ingest/replay", s.handleReplayIngest)
	mux.HandleFunc("OPTIONS /api/ingest/replay", s.handleClientPreflight)
	mux.Handle("GET /api/rum/replay/{id}", s.authed(s.handleReplay))
	mux.Handle("GET /api/rum/replay/{id}/chunk/{seq}", s.authed(s.handleReplayChunk))
	mux.HandleFunc("GET /replay-frame.html", s.handleReplayFrame)
}

func (s *Server) replayStart(ctx context.Context) {
	go s.replayLoop(ctx)
}
