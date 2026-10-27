package analytics

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Traces become requests (api_logs), calls (spans) and exceptions (errors).
// A request is a SERVER span, or a local root span carrying an HTTP method;
// every other span belongs to its nearest request ancestor. Spans whose
// ancestor has not arrived yet wait in memory for up to otlpHold.

type otlpSpan struct {
	traceID, spanID, parentID string
	sp                        *tracepb.Span
	attrs                     otlpAttrs
	res                       *otlpResource
	received                  time.Time
}

type otlpPendingTrace struct {
	spans map[string]*otlpSpan
	first time.Time
}

// otlpReqInfo is a request as its spans and errors see it.
type otlpReqInfo struct {
	requestID                   string
	startNs                     uint64
	method, route, path, userID string
	res                         *otlpResource
	keepSpans                   bool
	stored                      int
}

type otlpRecent struct {
	req     *otlpReqInfo
	expires time.Time
}

func (o *otlpState) ingestTraces(ctx context.Context, rss []*tracepb.ResourceSpans) (int64, error) {
	now := time.Now()
	var spans []*otlpSpan
	var rejected int64
	for _, rs := range rss {
		res := otlpResourceOf(rs.GetResource().GetAttributes())
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				tid, sid := otlpHexID(sp.TraceId), otlpHexID(sp.SpanId)
				if tid == "" || sid == "" || sp.StartTimeUnixNano == 0 || sp.EndTimeUnixNano < sp.StartTimeUnixNano {
					rejected++
					continue
				}
				spans = append(spans, &otlpSpan{
					traceID: tid, spanID: sid, parentID: otlpHexID(sp.ParentSpanId),
					sp: sp, attrs: otlpAttrMap(sp.Attributes), res: res, received: now,
				})
			}
		}
	}
	if len(spans) == 0 {
		return rejected, nil
	}

	o.mu.Lock()
	rows := &otlpRows{}
	groups := map[string]map[string]*otlpSpan{}
	for _, sp := range spans {
		g := groups[sp.traceID]
		if g == nil {
			g = map[string]*otlpSpan{}
			groups[sp.traceID] = g
		}
		g[sp.spanID] = sp
		o.noteRelease(rows, sp.res, sp.sp.StartTimeUnixNano)
	}
	var taken []*otlpSpan
	for tid, g := range groups {
		if p := o.pending[tid]; p != nil {
			for sid, sp := range p.spans {
				if _, dup := g[sid]; !dup {
					g[sid] = sp
					taken = append(taken, sp)
				}
			}
			o.nPending -= len(p.spans)
			delete(o.pending, tid)
		}
		o.processTrace(g, now, rows)
	}
	o.pruneRecent(now)
	o.mu.Unlock()

	if err := o.store(ctx, rows); err != nil {
		// The exporter retries its own spans; put back the ones that were
		// waiting so they are not lost.
		o.mu.Lock()
		for _, sp := range taken {
			o.hold(sp)
		}
		o.mu.Unlock()
		return 0, err
	}
	return rejected, nil
}

// expireTraces resolves traces that waited otlpHold for a missing parent:
// their spans are stored as background work.
func (o *otlpState) expireTraces(now time.Time) *otlpRows {
	rows := &otlpRows{}
	o.mu.Lock()
	defer o.mu.Unlock()
	for tid, p := range o.pending {
		if now.Sub(p.first) < otlpHold {
			continue
		}
		delete(o.pending, tid)
		o.nPending -= len(p.spans)
		o.processTrace(p.spans, now, rows)
	}
	o.expireLogs(rows, now)
	o.pruneRecent(now)
	return rows
}

// hold keeps a span until its request span arrives. The caller holds o.mu.
func (o *otlpState) hold(sp *otlpSpan) bool {
	if o.nPending >= otlpMaxPending {
		return false
	}
	p := o.pending[sp.traceID]
	if p == nil {
		p = &otlpPendingTrace{spans: map[string]*otlpSpan{}, first: sp.received}
		o.pending[sp.traceID] = p
	}
	if _, dup := p.spans[sp.spanID]; !dup {
		p.spans[sp.spanID] = sp
		o.nPending++
	}
	if sp.received.Before(p.first) {
		p.first = sp.received
	}
	return true
}

func otlpIsRequest(sp *otlpSpan) bool {
	switch sp.sp.Kind {
	case tracepb.Span_SPAN_KIND_SERVER:
		return true
	case tracepb.Span_SPAN_KIND_INTERNAL, tracepb.Span_SPAN_KIND_UNSPECIFIED:
		return sp.parentID == "" && sp.attrs.str("http.request.method", "http.method") != ""
	}
	return false
}

// processTrace turns one trace's spans into rows. Spans whose request
// ancestor is still missing go back to pending until otlpHold has passed.
// The caller holds o.mu.
func (o *otlpState) processTrace(group map[string]*otlpSpan, now time.Time, rows *otlpRows) {
	spansOn, threshold := o.spansOn(), o.thresholdMs()
	reqs := map[string]*otlpReqInfo{}
	for sid, sp := range group {
		if otlpIsRequest(sp) {
			reqs[sid] = otlpNewReq(sp)
		}
	}
	// owner finds the request a span belongs to; ok is false while an
	// ancestor is missing.
	owner := func(sp *otlpSpan) (*otlpReqInfo, bool) {
		cur := sp
		for range 128 {
			pid := cur.parentID
			if pid == "" {
				return nil, true
			}
			if r := reqs[pid]; r != nil {
				return r, true
			}
			if p := group[pid]; p != nil {
				cur = p
				continue
			}
			if rc := o.recent[pid]; rc != nil && now.Before(rc.expires) {
				return rc.req, true
			}
			return nil, false
		}
		return nil, true
	}

	children := map[*otlpReqInfo][]*otlpSpan{}
	var orphans []*otlpSpan
	for sid, sp := range group {
		if reqs[sid] != nil {
			continue
		}
		r, ok := owner(sp)
		switch {
		case !ok && now.Sub(sp.received) < otlpHold && o.hold(sp):
		case r == nil:
			orphans = append(orphans, sp)
		default:
			children[r] = append(children[r], sp)
		}
	}

	expires := now.Add(otlpRecentTTL)
	for sid, r := range reqs {
		sp := group[sid]
		kids := children[r]
		var dbCount, httpCount int64
		var dbMs, httpMs float64
		for _, c := range kids {
			kind, _, _, _, _ := otlpClassify(c)
			d := otlpDurationMs(c.sp)
			switch kind {
			case "db":
				dbCount++
				dbMs += d
			case "http":
				httpCount++
				httpMs += d
			}
		}
		status := otlpRequestStatus(sp)
		r.keepSpans = spansOn
		if !spansOn {
			rows.add(otlpRequestsTable, otlpRequestRow(sp, r, status, nil, nil, nil, nil))
		} else {
			rows.add(otlpRequestsTable, otlpRequestRow(sp, r, status, dbCount, otlpRound(dbMs), httpCount, otlpRound(httpMs)))
		}
		o.addErrors(rows, sp, r)
		o.remember(rows, sid, &otlpRecent{r, expires})
		delete(children, r)
		o.addChildren(rows, r, kids, expires)
	}
	// Late children of requests written by an earlier export.
	for r, kids := range children {
		o.addChildren(rows, r, kids, expires)
	}
	for _, sp := range orphans {
		if spansOn && otlpDurationMs(sp.sp) >= threshold {
			rows.add(otlpSpansTable, otlpSpanRow(sp, nil))
		}
		o.addErrors(rows, sp, nil)
	}
}

// addChildren stores a request's spans in start order, within the cap, and
// their exceptions. The caller holds o.mu.
func (o *otlpState) addChildren(rows *otlpRows, r *otlpReqInfo, kids []*otlpSpan, expires time.Time) {
	sort.Slice(kids, func(i, j int) bool { return kids[i].sp.StartTimeUnixNano < kids[j].sp.StartTimeUnixNano })
	spanMax := o.spanMax()
	for _, c := range kids {
		if r.keepSpans && r.stored < spanMax {
			rows.add(otlpSpansTable, otlpSpanRow(c, r))
			r.stored++
		}
		o.addErrors(rows, c, r)
		o.remember(rows, c.spanID, &otlpRecent{r, expires})
	}
}

func (o *otlpState) pruneRecent(now time.Time) {
	if len(o.recent) < otlpMaxRecent/2 {
		return
	}
	for k, rc := range o.recent {
		if now.After(rc.expires) {
			delete(o.recent, k)
		}
	}
	if len(o.recent) >= otlpMaxRecent {
		o.recent = map[string]*otlpRecent{}
	}
}

func otlpDurationMs(sp *tracepb.Span) float64 {
	return otlpRound(float64(sp.EndTimeUnixNano-sp.StartTimeUnixNano) / 1e6)
}

func otlpNewReq(sp *otlpSpan) *otlpReqInfo {
	a := sp.attrs
	r := &otlpReqInfo{
		requestID: sp.traceID + "-" + sp.spanID,
		startNs:   sp.sp.StartTimeUnixNano,
		userID:    otlpClip(a.str("enduser.id", "user.id"), 128),
		res:       sp.res,
	}
	r.method = a.str("http.request.method", "http.method")
	target := a.str("http.target")
	full := a.str("url.full", "http.url")
	r.path = a.str("url.path")
	if r.path == "" && target != "" {
		r.path, _, _ = strings.Cut(target, "?")
	}
	if r.path == "" && full != "" {
		if u, err := url.Parse(full); err == nil {
			r.path = u.Path
		}
	}
	r.route = a.str("http.route")
	if rpc := a.str("rpc.system"); rpc != "" && r.method == "" {
		r.method = "RPC"
		if svc, m := a.str("rpc.service"), a.str("rpc.method"); svc != "" || m != "" {
			r.route = "/" + svc + "/" + m
		}
		if r.path == "" {
			r.path = r.route
		}
	}
	if r.method == "" {
		r.method = "SERVER"
	}
	if r.path == "" {
		r.path = sp.sp.Name
	}
	if r.path == "" {
		r.path = "/"
	}
	r.method = otlpClip(strings.ToUpper(r.method), 16)
	r.route = otlpClip(r.route, 255)
	return r
}

// otlpRequestStatus is the HTTP status, or one derived from the gRPC code or
// the span status.
func otlpRequestStatus(sp *otlpSpan) int64 {
	if v, ok := sp.attrs.num("http.response.status_code", "http.status_code"); ok && v > 0 {
		return int64(v)
	}
	if v, ok := sp.attrs.num("rpc.grpc.status_code"); ok {
		switch int(v) {
		case 0:
			return 200
		case 1:
			return 499
		case 3, 9, 11:
			return 400
		case 4:
			return 504
		case 5:
			return 404
		case 6, 10:
			return 409
		case 7:
			return 403
		case 8:
			return 429
		case 12:
			return 501
		case 14:
			return 503
		case 16:
			return 401
		}
		return 500
	}
	if sp.sp.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR {
		return 500
	}
	return 200
}

// otlpRedactHeaders are dropped from captured header attributes, as the SDKs
// do.
var otlpRedactHeaders = map[string]bool{
	"authorization": true, "proxy-authorization": true, "cookie": true, "set-cookie": true,
	"x-api-key": true, "x-auth-token": true, "x-visionary-key": true,
}

func otlpRequestRow(sp *otlpSpan, r *otlpReqInfo, status int64, dbCount, dbMs, httpCount, httpMs any) []any {
	a := sp.attrs
	query := a.str("url.query")
	if query == "" {
		_, query, _ = strings.Cut(a.str("http.target"), "?")
	}
	headers := map[string]string{}
	for k := range a {
		if name, ok := strings.CutPrefix(k, "http.request.header."); ok {
			name = strings.ToLower(name)
			if otlpRedactHeaders[name] {
				headers[name] = "[REDACTED]"
			} else {
				headers[name] = a.str(k)
			}
		}
	}
	var headersJSON string
	if len(headers) > 0 {
		b, _ := json.Marshal(headers)
		headersJSON = string(b)
	}
	reqSize, _ := a.num("http.request.body.size", "http.request_content_length", "http.request.size")
	respSize, _ := a.num("http.response.body.size", "http.response_content_length", "http.response.size")
	return []any{
		r.requestID,
		otlpTime(sp.sp.StartTimeUnixNano),
		r.method,
		r.path,
		otlpNullStr(r.route),
		otlpNullStr(query),
		status,
		otlpDurationMs(sp.sp),
		otlpNullStr(otlpClip(a.str("client.address", "http.client_ip", "network.peer.address", "net.sock.peer.addr", "net.peer.ip"), 64)),
		otlpNullStr(a.str("user_agent.original", "http.user_agent")),
		otlpNullStr(r.userID),
		otlpNullStr(otlpClip(a.str("user.name", "enduser.name"), 255)),
		otlpNullStr(otlpClip(a.str("enduser.role", "user.roles"), 64)),
		otlpNullStr(headersJSON),
		otlpNullStr(otlpClip(a.str("http.request.header.content-type"), 255)),
		nil,
		int64(0),
		int64(reqSize),
		otlpNullStr(otlpClip(a.str("http.response.header.content-type"), 255)),
		nil,
		int64(0),
		int64(respSize),
		nil,
		nil,
		otlpNullStr(r.res.service),
		otlpNullStr(r.res.version),
		dbCount, dbMs, httpCount, httpMs,
		// The account a sign-in tried; see LoginAttempt in the Go SDK.
		otlpNullStr(otlpClip(a.str("visionary.login_account", "enduser.login"), 255)),
	}
}

// otlpClassify maps a span onto the spans table's kind, name, target,
// detail and row count.
func otlpClassify(sp *otlpSpan) (kind, name, target, detail string, rowCount any) {
	a := sp.attrs
	if system := a.str("db.system.name", "db.system"); system != "" {
		kind = "db"
		if system == "redis" || system == "memcached" {
			kind = "cache"
		}
		stmt := a.str("db.query.text", "db.statement")
		if stmt != "" {
			stmt = otlpSanitizeSQL(stmt)
		}
		op := a.str("db.operation.name", "db.operation")
		table := a.str("db.collection.name", "db.sql.table", "db.mongodb.collection")
		switch {
		case op != "":
			name = strings.TrimSpace(strings.ToUpper(op) + " " + table)
		case kind == "db" && stmt != "":
			name = otlpSQLName(stmt)
		case stmt != "":
			name, _, _ = strings.Cut(stmt, " ")
			name = strings.ToUpper(name)
		}
		target = strings.TrimSuffix(system+"/"+a.str("db.namespace", "db.name"), "/")
		detail = stmt
		if v, ok := a.num("db.response.returned_rows", "db.rows_affected"); ok {
			rowCount = int64(v)
		}
	} else if method := a.str("http.request.method", "http.method"); method != "" && sp.sp.Kind == tracepb.Span_SPAN_KIND_CLIENT {
		kind = "http"
		full := a.str("url.full", "http.url")
		host := a.str("server.address", "net.peer.name", "http.host")
		if u, err := url.Parse(full); err == nil && full != "" {
			if host == "" {
				host = u.Hostname()
			}
			u.RawQuery, u.Fragment, u.User = "", "", nil
			full = u.String()
		}
		method = strings.ToUpper(method)
		name = strings.TrimSpace(method + " " + host)
		target = host
		detail = strings.TrimSpace(method + " " + full)
	} else if system := a.str("messaging.system"); system != "" {
		kind = "queue"
		name = strings.TrimSpace(a.str("messaging.operation.name", "messaging.operation") + " " + a.str("messaging.destination.name", "messaging.destination"))
		target = system
	} else {
		kind = "custom"
	}
	if name == "" {
		name = sp.sp.Name
	}
	if name == "" {
		name = kind
	}
	return kind, otlpClip(name, 255), otlpClip(target, 255), otlpClip(detail, 10000), rowCount
}

func otlpSpanRow(sp *otlpSpan, r *otlpReqInfo) []any {
	kind, name, target, detail, rowCount := otlpClassify(sp)
	status, errMsg := "ok", ""
	if st := sp.sp.GetStatus(); st.GetCode() == tracepb.Status_STATUS_CODE_ERROR {
		status, errMsg = "error", st.GetMessage()
	}
	if kind == "http" {
		if code, ok := sp.attrs.num("http.response.status_code", "http.status_code"); ok && code >= 500 {
			status = "error"
			if errMsg == "" {
				errMsg = "HTTP " + otlpParam(int64(code))
			}
		}
	}
	if status == "error" && errMsg == "" {
		for _, ev := range sp.sp.Events {
			if ev.Name == "exception" {
				errMsg = otlpAttrMap(ev.Attributes).str("exception.message", "exception.type")
				break
			}
		}
	}
	var requestID, offset any
	if r != nil {
		requestID = r.requestID
		offset = otlpRound((float64(sp.sp.StartTimeUnixNano) - float64(r.startNs)) / 1e6)
	}
	return []any{
		otlpTime(sp.sp.StartTimeUnixNano),
		requestID,
		otlpNullStr(sp.res.service),
		sp.res.instance,
		kind,
		name,
		otlpNullStr(target),
		otlpNullStr(detail),
		offset,
		otlpDurationMs(sp.sp),
		status,
		otlpNullStr(otlpClip(errMsg, 500)),
		rowCount,
	}
}

// addErrors stores the span's exception events. The caller holds o.mu.
func (o *otlpState) addErrors(rows *otlpRows, sp *otlpSpan, r *otlpReqInfo) {
	if !o.errorsOn() {
		return
	}
	for _, ev := range sp.sp.Events {
		if ev.Name != "exception" {
			continue
		}
		a := otlpAttrMap(ev.Attributes)
		// An exception that escaped the span, or ended a failed request,
		// was not handled by the application.
		unhandled := a.boolean("exception.escaped") ||
			(otlpIsRequest(sp) && sp.sp.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR)
		at := ev.TimeUnixNano
		if at == 0 {
			at = sp.sp.EndTimeUnixNano
		}
		o.addError(rows, sp.res, r, sp.spanID, a, at, !unhandled)
	}
}

// addError adds one errors row from exception.* attributes, at most
// otlpErrorsPerMin per fingerprint per minute. The caller holds o.mu.
func (o *otlpState) addError(rows *otlpRows, res *otlpResource, r *otlpReqInfo, spanID string, a otlpAttrs, ns uint64, handled bool) {
	typ := otlpClip(a.str("exception.type"), 255)
	if typ == "" {
		typ = "Exception"
	}
	if spanID != "" {
		now := time.Now()
		key := spanID + "\n" + typ
		if at, ok := o.excSeen[key]; ok && now.Sub(at) < otlpRecentTTL {
			return
		}
		if len(o.excSeen) >= otlpMaxPending {
			o.excSeen = map[string]time.Time{}
		}
		o.excSeen[key] = now
	}
	msg := otlpClip(a.str("exception.message"), 1000)
	stack := otlpClip(a.str("exception.stacktrace"), 16<<10)
	fp := otlpFingerprint(typ, msg, stack)

	minute := time.Now().Unix() / 60
	if minute != o.fpMinute {
		o.fpMinute, o.fpCount = minute, map[string]int{}
	}
	if o.fpCount[fp] >= otlpErrorsPerMin {
		return
	}
	o.fpCount[fp]++

	var requestID, method, route, path, userID any
	if r != nil {
		requestID, method, route, path, userID = r.requestID, r.method, otlpNullStr(r.route), r.path, otlpNullStr(r.userID)
	}
	h := int64(0)
	if handled {
		h = 1
	}
	rows.add(otlpErrorsTable, []any{
		otlpTime(ns), otlpNullStr(res.service), res.instance, otlpNullStr(res.version),
		requestID, method, route, path, userID,
		typ, otlpNullStr(msg), otlpNullStr(stack), fp, h, otlpNullStr(res.runtime),
	})
}
