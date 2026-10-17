package analytics

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// OpenTelemetry ingestion over OTLP/HTTP, mapped onto Visionary's tables
// (docs/otlp.md). The bodies of the three Export services have the same wire
// and JSON form as TracesData, MetricsData and LogsData, so the data packages
// are enough and the gRPC service stubs are not linked in.

const (
	// otlpMaxBody caps a request body, on the wire and decompressed.
	otlpMaxBody = 8 << 20
	// otlpMaxInFlight bounds concurrent exports; more get 429 and retry.
	otlpMaxInFlight = 8
)

func (s *Server) otlpRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/traces", s.otlpHandler("rejectedSpans", func(o *otlpState, req *otlpRequest) (int64, error) {
		var data tracepb.TracesData
		if err := req.decode(&data); err != nil {
			return 0, err
		}
		return o.ingestTraces(req.r.Context(), data.ResourceSpans)
	}))
	mux.HandleFunc("POST /v1/metrics", s.otlpHandler("rejectedDataPoints", func(o *otlpState, req *otlpRequest) (int64, error) {
		var data metricspb.MetricsData
		if err := req.decode(&data); err != nil {
			return 0, err
		}
		return o.ingestMetrics(req.r.Context(), data.ResourceMetrics)
	}))
	mux.HandleFunc("POST /v1/logs", s.otlpHandler("rejectedLogRecords", func(o *otlpState, req *otlpRequest) (int64, error) {
		var data logspb.LogsData
		if err := req.decode(&data); err != nil {
			return 0, err
		}
		return o.ingestLogs(req.r.Context(), data.ResourceLogs)
	}))
}

// otlpRequest is one export: its body and how to answer it.
type otlpRequest struct {
	r    *http.Request
	json bool
	body []byte
}

// errOTLPBadData marks a body that does not decode; the exporter must not
// retry it.
var errOTLPBadData = errors.New("bad data")

func (req *otlpRequest) decode(m proto.Message) error {
	if !req.json {
		if err := proto.Unmarshal(req.body, m); err != nil {
			return errors.Join(errOTLPBadData, err)
		}
		return nil
	}
	body, err := otlpHexIDsToBase64(req.body)
	if err != nil {
		return errors.Join(errOTLPBadData, err)
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, m); err != nil {
		return errors.Join(errOTLPBadData, err)
	}
	return nil
}

func (s *Server) otlpHandler(rejectedField string, ingest func(*otlpState, *otlpRequest) (int64, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		req := &otlpRequest{r: r, json: ct == "application/json"}
		if ct != "application/x-protobuf" && ct != "application/json" {
			req.fail(w, http.StatusUnsupportedMediaType, "Content-Type must be application/x-protobuf or application/json")
			return
		}
		if !s.ingestKeyValid(otlpKey(r), "server") {
			req.fail(w, http.StatusUnauthorized, "missing or invalid ingest key")
			return
		}
		o := s.otlp()
		select {
		case o.inflight <- struct{}{}:
			defer func() { <-o.inflight }()
		default:
			w.Header().Set("Retry-After", "1")
			req.fail(w, http.StatusTooManyRequests, "too many concurrent exports")
			return
		}
		if !o.limiter.Allow() {
			w.Header().Set("Retry-After", "1")
			req.fail(w, http.StatusTooManyRequests, "export rate exceeded")
			return
		}
		body, status, err := otlpReadBody(w, r)
		if err != nil {
			req.fail(w, status, err.Error())
			return
		}
		req.body = body

		rejected, err := ingest(o, req)
		switch {
		case errors.Is(err, errOTLPBadData):
			req.fail(w, http.StatusBadRequest, "the body is not a valid OTLP export")
			return
		case err != nil:
			s.logger.Warn("otlp write", "path", r.URL.Path, "err", err)
			w.Header().Set("Retry-After", "5")
			req.fail(w, http.StatusServiceUnavailable, "the database is unavailable; retry later")
			return
		}
		msg := ""
		if rejected > 0 {
			msg = "items without valid ids or timestamps were rejected"
		}
		req.respond(w, rejectedField, rejected, msg)
	}
}

// otlpKey reads the ingest key from x-visionary-key or Authorization: Bearer,
// so OTEL_EXPORTER_OTLP_HEADERS can carry either.
func otlpKey(r *http.Request) string {
	if k := strings.TrimSpace(r.Header.Get("x-visionary-key")); k != "" {
		return k
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

// otlpReadBody reads the body, gunzipping it, and enforces otlpMaxBody on
// both the wire and the decompressed size.
func otlpReadBody(w http.ResponseWriter, r *http.Request) ([]byte, int, error) {
	var src io.Reader = http.MaxBytesReader(w, r.Body, otlpMaxBody)
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(src)
		if err != nil {
			return nil, otlpReadStatus(err), errors.New("the gzip body is invalid")
		}
		defer zr.Close()
		src = zr
	default:
		return nil, http.StatusUnsupportedMediaType, errors.New("Content-Encoding must be gzip or identity")
	}
	body, err := io.ReadAll(io.LimitReader(src, otlpMaxBody+1))
	if err != nil {
		return nil, otlpReadStatus(err), errors.New("the body could not be read")
	}
	if len(body) > otlpMaxBody {
		return nil, http.StatusRequestEntityTooLarge, errors.New("the body is larger than 8 MB")
	}
	return body, 0, nil
}

func otlpReadStatus(err error) int {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// respond writes Export<Signal>ServiceResponse, with partial_success when
// something was rejected.
func (req *otlpRequest) respond(w http.ResponseWriter, rejectedField string, rejected int64, msg string) {
	if req.json {
		body := map[string]any{}
		if rejected > 0 || msg != "" {
			body["partialSuccess"] = map[string]any{rejectedField: rejected, "errorMessage": msg}
		}
		writeJSON(w, http.StatusOK, body)
		return
	}
	var out []byte
	if rejected > 0 || msg != "" {
		var ps []byte
		ps = protowire.AppendTag(ps, 1, protowire.VarintType)
		ps = protowire.AppendVarint(ps, uint64(rejected))
		ps = protowire.AppendTag(ps, 2, protowire.BytesType)
		ps = protowire.AppendString(ps, msg)
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, ps)
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// fail answers with a google.rpc.Status, as OTLP/HTTP asks for errors.
func (req *otlpRequest) fail(w http.ResponseWriter, status int, msg string) {
	code := otlpRPCCode(status)
	if req.json {
		writeJSON(w, status, map[string]any{"code": code, "message": msg})
		return
	}
	var out []byte
	out = protowire.AppendTag(out, 1, protowire.VarintType)
	out = protowire.AppendVarint(out, uint64(code))
	out = protowire.AppendTag(out, 2, protowire.BytesType)
	out = protowire.AppendString(out, msg)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(status)
	_, _ = w.Write(out)
}

// otlpRPCCode maps an HTTP status to the google.rpc.Code of the error body.
func otlpRPCCode(status int) int {
	switch status {
	case http.StatusBadRequest, http.StatusUnsupportedMediaType:
		return 3 // INVALID_ARGUMENT
	case http.StatusUnauthorized:
		return 16 // UNAUTHENTICATED
	case http.StatusRequestEntityTooLarge, http.StatusTooManyRequests:
		return 8 // RESOURCE_EXHAUSTED
	case http.StatusServiceUnavailable:
		return 14 // UNAVAILABLE
	}
	return 13 // INTERNAL
}

// otlpIDKeys are the fields OTLP/JSON writes as hex where protobuf JSON
// expects base64.
var otlpIDKeys = map[string]bool{
	"traceId": true, "spanId": true, "parentSpanId": true,
	"trace_id": true, "span_id": true, "parent_span_id": true,
}

// otlpHexIDsToBase64 rewrites the hex trace and span ids of an OTLP/JSON body
// into the base64 protojson decodes bytes from.
func otlpHexIDsToBase64(body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				if s, ok := x.(string); ok && otlpIDKeys[k] {
					if b, err := hex.DecodeString(s); err == nil {
						t[k] = base64.StdEncoding.EncodeToString(b)
					}
					continue
				}
				walk(x)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(v)
	return json.Marshal(v)
}
