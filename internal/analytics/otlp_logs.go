package analytics

import (
	"context"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// Logs: only records at ERROR or above that carry exception.* attributes are
// kept, as errors rows (a logged exception, so handled = 1), linked to the
// request whose span was active. Everything else is accepted and dropped;
// Visionary is not a log store.

func (o *otlpState) ingestLogs(ctx context.Context, rls []*logspb.ResourceLogs) (int64, error) {
	if !o.errorsOn {
		return 0, nil
	}
	now := time.Now()
	rows := &otlpRows{}
	o.mu.Lock()
	for _, rl := range rls {
		res := otlpResourceOf(rl.GetResource().GetAttributes())
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				if lr.GetSeverityNumber() < logspb.SeverityNumber_SEVERITY_NUMBER_ERROR {
					continue
				}
				a := otlpAttrMap(lr.GetAttributes())
				if a.str("exception.type", "exception.message", "exception.stacktrace") == "" {
					continue
				}
				if a.str("exception.message") == "" {
					if body := lr.GetBody().GetStringValue(); body != "" {
						a["exception.message"] = lr.GetBody()
					}
				}
				sid := otlpHexID(lr.GetSpanId())
				at := lr.GetTimeUnixNano()
				if at == 0 {
					at = lr.GetObservedTimeUnixNano()
				}
				if at == 0 {
					at = uint64(now.UnixNano())
				}
				o.noteRelease(rows, res, at)
				rc := o.recent[sid]
				switch {
				case rc != nil && now.Before(rc.expires):
					o.addError(rows, res, rc.req, sid, a, at, true)
				case sid != "" && o.nPendingLogs < otlpMaxPendingLogs:
					// The span has not arrived yet; it usually comes in the
					// next trace export.
					o.pendingLogs[sid] = append(o.pendingLogs[sid], &otlpPendingLog{res, a, at, now})
					o.nPendingLogs++
				default:
					o.addError(rows, res, nil, sid, a, at, true)
				}
			}
		}
	}
	o.mu.Unlock()
	return 0, o.store(ctx, rows)
}

const otlpMaxPendingLogs = 5000

type otlpPendingLog struct {
	res      *otlpResource
	attrs    otlpAttrs
	at       uint64
	received time.Time
}

// remember records a span of a written request and stores the exception
// logs that were waiting for it. The span's own exception events were added
// first, so a log of the same exception is dropped as a duplicate. The
// caller holds o.mu.
func (o *otlpState) remember(rows *otlpRows, spanID string, rc *otlpRecent) {
	o.recent[spanID] = rc
	for _, l := range o.pendingLogs[spanID] {
		o.addError(rows, l.res, rc.req, spanID, l.attrs, l.at, true)
		o.nPendingLogs--
	}
	delete(o.pendingLogs, spanID)
}

// expireLogs stores, unlinked, the exception logs whose span never came.
// The caller holds o.mu.
func (o *otlpState) expireLogs(rows *otlpRows, now time.Time) {
	for sid, logs := range o.pendingLogs {
		if now.Sub(logs[0].received) < otlpHold {
			continue
		}
		for _, l := range logs {
			o.addError(rows, l.res, nil, sid, l.attrs, l.at, true)
		}
		o.nPendingLogs -= len(logs)
		delete(o.pendingLogs, sid)
	}
}
