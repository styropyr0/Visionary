package analytics

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func otlpGRPCClient(t *testing.T, s *Server) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := s.otlpGRPCServer()
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodecV2(otlpRawCodec{}), grpc.MaxCallSendMsgSize(64<<20)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func otlpGRPCExportCall(conn *grpc.ClientConn, service, key string, m proto.Message, opts ...grpc.CallOption) ([]byte, error) {
	body, err := proto.Marshal(m)
	if err != nil {
		return nil, err
	}
	return otlpGRPCRaw(conn, service, key, body, opts...)
}

func otlpGRPCRaw(conn *grpc.ClientConn, service, key string, body []byte, opts ...grpc.CallOption) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if key != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+key)
	}
	var out []byte
	err := conn.Invoke(ctx, "/"+service+"/Export", body, &out, opts...)
	return out, err
}

func otlpStr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func TestOTLPGRPCExport(t *testing.T) {
	s := logsTestServer(t, nil)
	conn := otlpGRPCClient(t, s)
	ctx := context.Background()
	res := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{otlpStr("service.name", "grpc-shop")}}
	now := time.Now().Add(-time.Second)

	traceID := []byte{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36}
	traces := &tracepb.TracesData{ResourceSpans: []*tracepb.ResourceSpans{{Resource: res, ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
		{TraceId: traceID, SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8}, Name: "GET /items", Kind: tracepb.Span_SPAN_KIND_SERVER,
			StartTimeUnixNano: uint64(now.UnixNano()), EndTimeUnixNano: uint64(now.Add(20 * time.Millisecond).UnixNano()),
			Attributes: []*commonpb.KeyValue{otlpStr("http.request.method", "GET"), otlpStr("http.route", "/items")}},
		{Name: "broken", StartTimeUnixNano: 1, EndTimeUnixNano: 2},
	}}}}}}
	out, err := otlpGRPCExportCall(conn, otlpSignals[0].service, "k1", traces)
	if err != nil {
		t.Fatal(err)
	}
	var resp []byte
	if num, _, n := protowire.ConsumeTag(out); n > 0 && num == 1 {
		resp, _ = protowire.ConsumeBytes(out[n:])
	}
	if len(resp) == 0 || resp[1] != 1 || !strings.Contains(string(resp), otlpRejectedMsg) {
		t.Fatalf("partial_success: %x", out)
	}
	q, err := s.db.Query(ctx, `SELECT method, route, service FROM api_logs`)
	if err != nil || len(q.Rows) != 1 || q.Rows[0][0] != "GET" || q.Rows[0][1] != "/items" || q.Rows[0][2] != "grpc-shop" {
		t.Fatalf("api_logs %+v %v", q, err)
	}

	metrics := &metricspb.MetricsData{ResourceMetrics: []*metricspb.ResourceMetrics{{Resource: res, ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
		{Name: "queue.depth", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
			DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: uint64(now.UnixNano()), Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 7}}}}}},
	}}}}}}
	if out, err := otlpGRPCExportCall(conn, otlpSignals[1].service, "k1", metrics, grpc.UseCompressor(gzip.Name)); err != nil || len(out) != 0 {
		t.Fatalf("metrics: %x %v", out, err)
	}
	q, err = s.db.Query(ctx, `SELECT value FROM metrics WHERE name = 'queue.depth'`)
	if err != nil || len(q.Rows) != 1 || toFloat(q.Rows[0][0]) != 7 {
		t.Fatalf("metrics %+v %v", q, err)
	}

	logs := &logspb.LogsData{ResourceLogs: []*logspb.ResourceLogs{{Resource: res, ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{
		{TimeUnixNano: uint64(now.UnixNano()), SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_WARN, Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "over grpc"}}},
	}}}}}}
	if _, err := otlpGRPCExportCall(conn, otlpSignals[2].service, "k1", logs); err != nil {
		t.Fatal(err)
	}
	if rows := logRows(t, s); len(rows) != 1 || rows[0]["message"] != "over grpc" || rows[0]["service"] != "grpc-shop" {
		t.Fatalf("logs %+v", rows)
	}
}

func TestOTLPGRPCErrors(t *testing.T) {
	s := logsTestServer(t, nil)
	conn := otlpGRPCClient(t, s)
	traces := otlpSignals[0].service

	if _, err := otlpGRPCRaw(conn, traces, "", nil); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no key: %v", err)
	}
	if _, err := otlpGRPCRaw(conn, traces, "wrong", nil); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong key: %v", err)
	}
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-visionary-key", "k1")
	var out []byte
	if err := conn.Invoke(ctx, "/"+traces+"/Export", []byte{}, &out); err != nil {
		t.Fatalf("x-visionary-key: %v", err)
	}
	if _, err := otlpGRPCRaw(conn, traces, "k1", []byte{0xff, 0xff}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad body: %v", err)
	}

	big := make([]byte, otlpMaxBody+1)
	if _, err := otlpGRPCRaw(conn, traces, "k1", big); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversize: %v", err)
	}
	if _, err := otlpGRPCRaw(conn, traces, "k1", big, grpc.UseCompressor(gzip.Name)); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversize gzip: %v", err)
	}

	o := s.otlp()
	for range otlpMaxInFlight {
		o.inflight <- struct{}{}
	}
	_, err := otlpGRPCRaw(conn, traces, "k1", nil)
	for range otlpMaxInFlight {
		<-o.inflight
	}
	st := status.Convert(err)
	if st.Code() != codes.ResourceExhausted || len(st.Details()) != 1 {
		t.Fatalf("busy: %v", err)
	}
	if ri, ok := st.Details()[0].(*errdetails.RetryInfo); !ok || ri.GetRetryDelay().AsDuration() != time.Second {
		t.Fatalf("retry info: %+v", st.Details())
	}

	_ = s.db.Close()
	if v, ok := otlpStates.Load(s); ok && v.(*otlpState).sql != nil {
		_ = v.(*otlpState).sql.Close()
	}
	span := &tracepb.TracesData{ResourceSpans: []*tracepb.ResourceSpans{{ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
		{TraceId: make16(), SpanId: []byte{1, 1, 1, 1, 1, 1, 1, 1}, Kind: tracepb.Span_SPAN_KIND_SERVER, Name: "x",
			StartTimeUnixNano: uint64(time.Now().UnixNano()), EndTimeUnixNano: uint64(time.Now().UnixNano())}}}}}}}
	if _, err := otlpGRPCExportCall(conn, traces, "k1", span); status.Code(err) != codes.Unavailable {
		t.Fatalf("db down: %v", err)
	}
}

func make16() []byte {
	b := make([]byte, 16)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return b
}
