package analytics

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// Every 10 minutes: count replay storage, delete the oldest sessions over the
// cap, drop orphaned chunks, and copy sessions idle for an hour to the log
// archive's bucket when one is set.

const (
	replayEvery       = 10 * time.Minute
	replayArchiveIdle = time.Hour
	replayArchiveRun  = 20
)

func (s *Server) replayLoop(ctx context.Context) {
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.replayPass(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil && !missingTable(err, "replay_sessions") {
			s.logger.Warn("replay sweep", "err", err)
		}
		t.Reset(replayEvery)
	}
}

func (s *Server) replayPass(ctx context.Context, now time.Time) error {
	total, err := s.replayBytes(ctx)
	if err != nil {
		return err
	}
	if capMB := s.confInt("replay_max_mb"); capMB > 0 && total > int64(capMB)<<20 {
		if total, err = s.trimReplays(ctx, total-int64(capMB)<<20); err != nil {
			return err
		}
	}
	s.replay().bytes.Store(total)
	// Chunks are written just before their session row, so leave new ones be.
	if err := s.db.Exec(ctx, `DELETE FROM replay_chunks WHERE created_at < ? AND session_id NOT IN (SELECT session_id FROM replay_sessions)`,
		now.Add(-time.Hour).Format(tsLayout)); err != nil {
		return dbErr(err)
	}
	if c, ok := s.archiveConf(); ok {
		if _, err := s.archiveReplays(ctx, c, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) replayBytes(ctx context.Context) (int64, error) {
	res, err := s.db.Query(ctx, `SELECT COALESCE(SUM(bytes), 0) FROM replay_sessions`)
	if err != nil {
		return 0, dbErr(err)
	}
	if len(res.Rows) == 0 {
		return 0, nil
	}
	return toInt(res.Rows[0][0]), nil
}

func (s *Server) trimReplays(ctx context.Context, over int64) (int64, error) {
	res, err := s.db.Query(ctx, `SELECT session_id, bytes FROM replay_sessions ORDER BY created_at LIMIT 1000`)
	if err != nil {
		return 0, dbErr(err)
	}
	var ids []any
	for _, row := range res.Rows {
		if over <= 0 {
			break
		}
		ids = append(ids, row[0])
		over -= toInt(row[1])
	}
	if err := s.deleteReplays(ctx, ids); err != nil {
		return 0, err
	}
	return s.replayBytes(ctx)
}

func (s *Server) deleteReplays(ctx context.Context, ids []any) error {
	for len(ids) > 0 {
		n := min(len(ids), 100)
		marks := strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
		for _, t := range []string{"replay_chunks", "replay_sessions"} {
			if err := s.db.Exec(ctx, `DELETE FROM `+t+` WHERE session_id IN (`+marks+`)`, ids[:n]...); err != nil {
				return dbErr(err)
			}
		}
		ids = ids[n:]
	}
	return nil
}

const replayArchivePage = 8

// archiveReplays writes each idle session as one gzipped NDJSON object,
// <prefix>/replays/2026/10/30/<session>.ndjson.gz, a line per chunk with its
// payload as stored. A session that records again is written again.
func (s *Server) archiveReplays(ctx context.Context, c archiveConf, now time.Time) (int, error) {
	res, err := s.db.Query(ctx, `SELECT session_id, started_at FROM replay_sessions WHERE archived = 0 AND created_at < ? ORDER BY created_at LIMIT `+strconv.Itoa(replayArchiveRun),
		now.Add(-replayArchiveIdle).Format(tsLayout))
	if err != nil {
		return 0, dbErr(err)
	}
	done := 0
	for _, row := range res.Rows {
		id := toString(row[0])
		started, _ := time.Parse(tsLayout, toString(row[1]))
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		enc := json.NewEncoder(zw)
		// Chunks are up to half a megabyte and D1 caps a response, so read a
		// page at a time.
		for after := int64(-1); ; {
			chunks, err := s.db.Query(ctx, `SELECT seq, created_at, ended_at, events, payload FROM replay_chunks WHERE session_id = ? AND seq > ? ORDER BY seq LIMIT `+strconv.Itoa(replayArchivePage), id, after)
			if err != nil {
				return done, dbErr(err)
			}
			for _, ch := range chunks.Rows {
				after = toInt(ch[0])
				if err := enc.Encode(map[string]any{"session_id": id, "seq": after, "started": ch[1], "ended": ch[2], "events": toInt(ch[3]), "payload_gzip_base64": ch[4]}); err != nil {
					return done, err
				}
			}
			if len(chunks.Rows) < replayArchivePage {
				break
			}
		}
		if err := zw.Close(); err != nil {
			return done, err
		}
		key := "replays/" + started.UTC().Format("2006/01/02") + "/" + id + archiveObjectExt
		if c.prefix != "" {
			key = c.prefix + "/" + key
		}
		if err := c.put(ctx, key, bytes.NewReader(buf.Bytes()), int64(buf.Len())); err != nil {
			return done, err
		}
		if err := s.db.Exec(ctx, `UPDATE replay_sessions SET archived = 1 WHERE session_id = ?`, id); err != nil {
			return done, dbErr(err)
		}
		done++
	}
	return done, nil
}
