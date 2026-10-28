package analytics

import (
	"context"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

// ERROR+ records with exception.* attributes are also stored as handled errors,
// linked to the request whose span was active.

func (o *otlpState) ingestLogs(ctx context.Context, rls []*logspb.ResourceLogs) (int64, error) {
	now := time.Now()
	rows := &otlpRows{}
	var lines []*logLine
	errorsOn := o.errorsOn()
	o.mu.Lock()
	for _, rl := range rls {
		resAttrs := otlpAttrMap(rl.GetResource().GetAttributes())
		res := otlpResourceOf(rl.GetResource().GetAttributes())
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				at := lr.GetTimeUnixNano()
				if at == 0 {
					at = lr.GetObservedTimeUnixNano()
				}
				if at == 0 {
					at = uint64(now.UnixNano())
				}
				lines = append(lines, otlpLogLine(res, resAttrs, sl.GetScope().GetName(), lr, at))
				if !errorsOn || lr.GetSeverityNumber() < logspb.SeverityNumber_SEVERITY_NUMBER_ERROR {
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
	if err := o.store(ctx, rows); err != nil {
		return 0, err
	}
	_, err := o.s.ingestLogLines(ctx, lines)
	return 0, err
}

func otlpLogLine(res *otlpResource, resAttrs otlpAttrs, scope string, lr *logspb.LogRecord, at uint64) *logLine {
	l := &logLine{
		at:       time.Unix(0, int64(at)),
		service:  res.service,
		instance: res.instance,
		version:  res.version,
		source:   "otlp",
		host:     resAttrs.str("host.name", "host.id"),
		ctr:      resAttrs.str("container.name", "k8s.container.name"),
		logger:   scope,
		traceID:  otlpHexID(lr.GetTraceId()),
		spanID:   otlpHexID(lr.GetSpanId()),
		attrs:    map[string]any{},
	}
	if n := int(lr.GetSeverityNumber()); n > 0 {
		l.sev = logrules.Clamp((n-1)/4 + 1)
	} else {
		l.sev = logrules.ParseLevel(lr.GetSeverityText())
	}
	fields := map[string]any{}
	for _, kv := range lr.GetAttributes() {
		flatten(fields, kv.GetKey()+".", otlpValue(kv.GetValue()))
	}
	switch b := otlpValue(lr.GetBody()).(type) {
	case map[string]any:
		flatten(fields, "", b)
	case nil:
	default:
		l.message = fieldString(b)
	}
	for _, k := range []string{"k8s.namespace.name", "k8s.pod.name", "deployment.environment"} {
		if v := resAttrs.str(k); v != "" {
			fields[k] = v
		}
	}
	takeLogFields(l, fields)
	return l
}

func otlpValue(v *commonpb.AnyValue) any {
	switch t := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return t.StringValue
	case *commonpb.AnyValue_IntValue:
		return t.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return t.DoubleValue
	case *commonpb.AnyValue_BoolValue:
		return t.BoolValue
	case *commonpb.AnyValue_BytesValue:
		return otlpHexID(t.BytesValue)
	case *commonpb.AnyValue_KvlistValue:
		m := map[string]any{}
		for _, kv := range t.KvlistValue.GetValues() {
			m[kv.GetKey()] = otlpValue(kv.GetValue())
		}
		return m
	case *commonpb.AnyValue_ArrayValue:
		var out []any
		for _, x := range t.ArrayValue.GetValues() {
			out = append(out, otlpValue(x))
		}
		return out
	}
	return nil
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
