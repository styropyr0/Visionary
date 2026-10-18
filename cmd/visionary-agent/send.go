package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// sender posts reports in order. While the server is unreachable it keeps up
// to an hour of them in memory and retries with backoff; a report the server
// rejects (4xx other than 429) is dropped, since sending it again would fail
// the same way.
type sender struct {
	url    string
	key    string
	client *http.Client
	log    *slog.Logger
	max    int

	mu      sync.Mutex
	queue   [][]byte // gzipped JSON, oldest first
	dropped int
	wake    chan struct{}
}

const (
	backoffMin = 5 * time.Second
	backoffMax = 5 * time.Minute
)

func newSender(cfg config, logger *slog.Logger) *sender {
	return &sender{
		url:    cfg.url + agentapi.Path,
		key:    cfg.key,
		client: &http.Client{Timeout: 30 * time.Second},
		log:    logger,
		max:    max(1, int(time.Hour/cfg.report)),
		wake:   make(chan struct{}, 1),
	}
}

func (s *sender) enqueue(rep *agentapi.Report) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(rep); err != nil {
		s.log.Error("encode report", "err", err)
		return
	}
	_ = zw.Close()
	s.mu.Lock()
	s.queue = append(s.queue, buf.Bytes())
	if over := len(s.queue) - s.max; over > 0 {
		s.queue = s.queue[over:]
		s.dropped += over
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *sender) run(ctx context.Context) {
	var backoff time.Duration
	retry := time.NewTimer(time.Hour)
	retry.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			if backoff > 0 {
				continue // the retry timer will send it
			}
		case <-retry.C:
		}
		wait := s.flush(ctx)
		switch {
		case wait == 0:
			backoff = 0
		case wait > 0:
			// The server said when to come back.
			backoff = wait
			retry.Reset(wait)
		default:
			backoff = min(max(backoffMin, backoff*2), backoffMax)
			retry.Reset(backoff)
		}
	}
}

// drain sends what is queued until ctx ends, at shutdown.
func (s *sender) drain(ctx context.Context) {
	for ctx.Err() == nil {
		if s.flush(ctx) != 0 {
			return
		}
		s.mu.Lock()
		empty := len(s.queue) == 0
		s.mu.Unlock()
		if empty {
			return
		}
	}
}

// flush sends queued reports oldest first. It returns 0 when the queue is
// empty, a positive Retry-After, or -1 to back off.
func (s *sender) flush(ctx context.Context) time.Duration {
	for {
		s.mu.Lock()
		if s.dropped > 0 {
			s.log.Warn("server unreachable for over an hour; dropped the oldest reports", "dropped", s.dropped)
			s.dropped = 0
		}
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return 0
		}
		body := s.queue[0]
		pending := len(s.queue)
		s.mu.Unlock()

		status, retryAfter, msg, err := s.post(ctx, body)
		switch {
		case err == nil && status < 300:
			s.pop(body)
			continue
		case err == nil && status >= 400 && status < 500 && status != http.StatusTooManyRequests:
			s.log.Error("server rejected the report; dropping it", "status", status, "error", msg)
			s.pop(body)
			continue
		case err != nil:
			s.log.Warn("report not delivered; will retry", "err", err, "buffered", pending)
		default:
			s.log.Warn("report not delivered; will retry", "status", status, "error", msg, "buffered", pending)
		}
		if retryAfter > 0 {
			return retryAfter
		}
		return -1
	}
}

func (s *sender) pop(body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) > 0 && &s.queue[0][0] == &body[0] {
		s.queue = s.queue[1:]
	}
}

// post sends one report and returns the status, the Retry-After wait and
// the server's error message.
func (s *sender) post(ctx context.Context, body []byte) (int, time.Duration, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("User-Agent", "visionary-agent/"+version)
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, 0, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var wait time.Duration
	if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && n > 0 {
		wait = min(time.Duration(n)*time.Second, backoffMax)
	}
	var e struct {
		Error string `json:"error"`
	}
	if resp.StatusCode >= 400 && json.Unmarshal(raw, &e) != nil {
		e.Error = http.StatusText(resp.StatusCode)
	}
	return resp.StatusCode, wait, e.Error, nil
}
