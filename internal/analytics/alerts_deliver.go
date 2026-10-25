package analytics

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// Notifications go through an outbox, alert_deliveries: one row per channel
// and message, written before the send. A failed send is retried with
// backoff and then marked failed. A row still pending after a restart (the
// process died mid-send) is picked up by the retry loop once its lease runs
// out, so a crash costs a late notification rather than a lost one. Activity
// shows every row, so a failed email next to a delivered Slack message is
// visible.

const (
	deliveryLease    = 2 * time.Minute
	deliveryTick     = 15 * time.Second
	deliveryBatch    = 20
	maxDeliveryError = 500
)

// deliveryBackoff is the wait before each retry; its length is the number of
// retries after the first attempt.
var deliveryBackoff = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

type deliveryView struct {
	ID          int64  `json:"id"`
	EventID     int64  `json:"event_id"`
	ChannelID   int64  `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	ChannelKind string `json:"channel_kind"`
	Event       string `json:"event"`  // firing, resolved, reminder, escalation, ack
	Status      string `json:"status"` // pending, sent, failed
	Attempts    int    `json:"attempts"`
	Error       string `json:"error,omitempty"`
	CreatedAt   string `json:"created_at"`
	SentAt      string `json:"sent_at,omitempty"`
	NextAt      string `json:"next_at,omitempty"`
}

// outMessage is what a delivery row keeps to send again.
type outMessage struct {
	Notice   notice  `json:"notice"`
	EventIDs []int64 `json:"event_ids"`
}

type queued struct {
	id int64
	ch alertChannel
}

// enqueue writes a pending row per channel. eventIDs are the alert_events
// the message is about; the first one is shown with the delivery.
func (s *Server) enqueue(ctx context.Context, ruleID int64, eventIDs []int64, chanIDs []int64, n notice) ([]queued, error) {
	if len(chanIDs) == 0 {
		return nil, nil
	}
	chans, err := s.listChannels(ctx)
	if err != nil {
		return nil, err
	}
	want := map[int64]bool{}
	for _, id := range chanIDs {
		want[id] = true
	}
	payload, _ := json.Marshal(outMessage{Notice: n, EventIDs: eventIDs})
	now := time.Now().UTC()
	var first int64
	if len(eventIDs) > 0 {
		first = eventIDs[0]
	}
	var out []queued
	for _, c := range chans {
		if !want[c.ID] {
			continue
		}
		id, err := s.db.InsertID(ctx, `INSERT INTO alert_deliveries (created_at, event_id, rule_id, channel_id, channel_name, channel_kind, event, status, attempts, error, next_at, sent_at, payload)
VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', 0, '', ?, '', ?)`,
			now.Format(tsLayout), first, ruleID, c.ID, c.Name, c.Kind, n.Event, now.Add(deliveryLease).Format(tsLayout), string(payload))
		if err != nil {
			return out, err
		}
		out = append(out, queued{id: id, ch: c})
	}
	return out, nil
}

// sendQueued sends n to each queued channel and records each outcome. It
// reports whether any channel accepted it.
func (s *Server) sendQueued(ctx context.Context, q []queued, n notice, eventIDs []int64) bool {
	ok := false
	// The message may have grown since it was queued (the likely cause is
	// written in between); retries send what was finally sent.
	payload, _ := json.Marshal(outMessage{Notice: n, EventIDs: eventIDs})
	for _, d := range q {
		if err := s.db.Exec(ctx, `UPDATE alert_deliveries SET payload = ? WHERE id = ?`, string(payload), d.id); err != nil {
			s.logger.Warn("alerts: update delivery", "err", err)
		}
		if s.attempt(ctx, d.id, d.ch, n, 0) {
			ok = true
		}
	}
	if ok {
		if err := s.markNotified(ctx, eventIDs); err != nil {
			s.logger.Warn("alerts: mark notified", "err", err)
		}
	}
	return ok
}

// deliver queues and sends in one step.
func (s *Server) deliver(ctx context.Context, ruleID int64, eventIDs []int64, chanIDs []int64, n notice) bool {
	q, err := s.enqueue(ctx, ruleID, eventIDs, chanIDs, n)
	if err != nil {
		s.logger.Warn("alerts: queue notification", "err", err)
	}
	return s.sendQueued(ctx, q, n, eventIDs)
}

// attempt sends one delivery and writes the result: sent, a retry time, or
// failed once the retries are used up. prior is the attempts made before.
func (s *Server) attempt(ctx context.Context, id int64, c alertChannel, n notice, prior int) bool {
	err := s.sendNotice(ctx, c, n)
	now := time.Now().UTC()
	if err == nil {
		if e := s.db.Exec(ctx, `UPDATE alert_deliveries SET status = 'sent', attempts = ?, error = '', sent_at = ?, next_at = '' WHERE id = ?`,
			prior+1, now.Format(tsLayout), id); e != nil {
			s.logger.Warn("alerts: record delivery", "err", e)
		}
		return true
	}
	s.logger.Warn("alerts: notification failed", "channel_id", c.ID, "channel", c.Name, "kind", c.Kind, "attempt", prior+1, "err", err)
	status, next := "failed", ""
	if prior < len(deliveryBackoff) {
		status, next = "pending", now.Add(deliveryBackoff[prior]).Format(tsLayout)
	}
	if e := s.db.Exec(ctx, `UPDATE alert_deliveries SET status = ?, attempts = ?, error = ?, next_at = ? WHERE id = ?`,
		status, prior+1, truncate(err.Error(), maxDeliveryError), next, id); e != nil {
		s.logger.Warn("alerts: record delivery", "err", e)
	}
	return false
}

// deliveryLoop retries pending deliveries that are due.
func (s *Server) deliveryLoop(ctx context.Context) {
	tick := time.NewTicker(deliveryTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			s.retryDue(ctx, now.UTC())
		}
	}
}

func (s *Server) retryDue(ctx context.Context, now time.Time) {
	defer s.alertEngine().recoverLog("delivery retry")
	res, err := s.db.Query(ctx, `SELECT id, channel_id, attempts, payload FROM alert_deliveries
WHERE status = 'pending' AND next_at <> '' AND next_at <= ? ORDER BY next_at LIMIT `+strconv.Itoa(deliveryBatch), now.Format(tsLayout))
	if err != nil {
		if !missingTable(err, "alert_deliveries") {
			s.logger.Warn("alerts: load deliveries", "err", err)
		}
		return
	}
	for _, row := range res.Rows {
		if len(row) != 4 {
			continue
		}
		id, chanID, prior := toInt(row[0]), toInt(row[1]), int(toInt(row[2]))
		// The lease keeps the next tick from sending it again meanwhile.
		if err := s.db.Exec(ctx, `UPDATE alert_deliveries SET next_at = ? WHERE id = ?`, now.Add(deliveryLease).Format(tsLayout), id); err != nil {
			continue
		}
		var m outMessage
		if err := json.Unmarshal([]byte(toString(row[3])), &m); err != nil {
			_ = s.db.Exec(ctx, `UPDATE alert_deliveries SET status = 'failed', error = 'unreadable message', next_at = '' WHERE id = ?`, id)
			continue
		}
		c, err := s.getChannel(ctx, chanID)
		if err != nil {
			continue
		}
		if c == nil {
			_ = s.db.Exec(ctx, `UPDATE alert_deliveries SET status = 'failed', error = 'channel deleted', next_at = '' WHERE id = ?`, id)
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if s.attempt(sctx, id, *c, m.Notice, prior) {
			if err := s.markNotified(sctx, m.EventIDs); err != nil {
				s.logger.Warn("alerts: mark notified", "err", err)
			}
		}
		cancel()
	}
}

// eventDeliveries loads the deliveries of the given events, by event id.
func (s *Server) eventDeliveries(ctx context.Context, ids []int64) (map[int64][]deliveryView, error) {
	out := map[int64][]deliveryView{}
	for len(ids) > 0 {
		chunk := head(ids, 200)
		ids = ids[len(chunk):]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		res, err := s.db.Query(ctx, `SELECT id, event_id, channel_id, channel_name, channel_kind, event, status, attempts, error, created_at, sent_at, next_at
FROM alert_deliveries WHERE event_id IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(chunk)), ", ")+`) ORDER BY id`, args...)
		if err != nil {
			if missingTable(err, "alert_deliveries") {
				return out, nil
			}
			return nil, err
		}
		for _, row := range res.Rows {
			if len(row) != 12 {
				continue
			}
			d := deliveryView{
				ID: toInt(row[0]), EventID: toInt(row[1]), ChannelID: toInt(row[2]), ChannelName: toString(row[3]),
				ChannelKind: toString(row[4]), Event: toString(row[5]), Status: toString(row[6]), Attempts: int(toInt(row[7])),
				Error: toString(row[8]), CreatedAt: toString(row[9]), SentAt: toString(row[10]), NextAt: toString(row[11]),
			}
			out[d.EventID] = append(out[d.EventID], d)
		}
	}
	return out, nil
}
