package analytics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// OTLP/gRPC on its own port (Settings → Server → Ingest), for
// OTEL_EXPORTER_OTLP_PROTOCOL=grpc. A raw codec hands request bytes to the same
// ingest as OTLP/HTTP, so no generated types are needed.

// otlpRawCodec passes messages through as bytes: requests are decoded by the
// signal, responses are already encoded.
type otlpRawCodec struct{}

func (otlpRawCodec) Name() string { return "proto" }

func (otlpRawCodec) Marshal(v any) (mem.BufferSlice, error) {
	b, ok := v.([]byte)
	if !ok {
		return nil, fmt.Errorf("otlp grpc: cannot marshal %T", v)
	}
	return mem.BufferSlice{mem.SliceBuffer(b)}, nil
}

func (otlpRawCodec) Unmarshal(data mem.BufferSlice, v any) error {
	p, ok := v.(*[]byte)
	if !ok {
		return fmt.Errorf("otlp grpc: cannot unmarshal into %T", v)
	}
	*p = data.Materialize()
	return nil
}

func (s *Server) otlpGRPCServer() *grpc.Server {
	srv := grpc.NewServer(
		grpc.ForceServerCodecV2(otlpRawCodec{}),
		grpc.MaxRecvMsgSize(otlpMaxBody),
	)
	for _, sig := range otlpSignals {
		srv.RegisterService(&grpc.ServiceDesc{
			ServiceName: sig.service,
			HandlerType: (*any)(nil),
			Methods:     []grpc.MethodDesc{{MethodName: "Export", Handler: s.otlpGRPCExport(sig)}},
		}, nil)
	}
	return srv
}

func (s *Server) otlpGRPCExport(sig otlpSignal) grpc.MethodHandler {
	return func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (_ any, err error) {
		defer func() {
			if p := recover(); p != nil {
				s.logger.Error("otlp grpc panicked", "signal", sig.name, "panic", p)
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		md, _ := metadata.FromIncomingContext(ctx)
		key := otlpKeyFrom(func(k string) string {
			if v := md.Get(k); len(v) > 0 {
				return v[0]
			}
			return ""
		})
		o, release, err := s.otlpAdmit(key)
		switch {
		case errors.Is(err, errOTLPKey):
			return nil, status.Error(codes.Unauthenticated, err.Error())
		case err != nil:
			return nil, otlpRetryStatus(codes.ResourceExhausted, err.Error(), time.Second)
		}
		defer release()
		var body []byte
		if err := dec(&body); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}

		rejected, err := s.otlpIngest(ctx, o, sig, func(m proto.Message) error { return otlpUnmarshal(body, m) })
		switch {
		case errors.Is(err, errOTLPBadData):
			return nil, status.Error(codes.InvalidArgument, "the body is not a valid OTLP export")
		case err != nil:
			return nil, otlpRetryStatus(codes.Unavailable, "the database is unavailable; retry later", 5*time.Second)
		}
		msg := ""
		if rejected > 0 {
			msg = otlpRejectedMsg
		}
		return otlpExportResponse(rejected, msg), nil
	}
}

// otlpRetryStatus carries RetryInfo: OTLP exporters retry RESOURCE_EXHAUSTED
// only when it is there.
func otlpRetryStatus(code codes.Code, msg string, after time.Duration) error {
	st := status.New(code, msg)
	if d, err := st.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(after)}); err == nil {
		st = d
	}
	return st.Err()
}

// otlpGRPCAddr reads the listen address; a bare port listens on every interface.
func (s *Server) otlpGRPCAddr() string {
	addr := strings.TrimSpace(s.conf("otlp_grpc_addr"))
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	return addr
}

func (s *Server) otlpGRPCStart(ctx context.Context) {
	if !s.confBool("otlp_grpc") {
		return
	}
	addr := s.otlpGRPCAddr()
	go func() {
		lis, err := otlpListen(ctx, addr)
		if err != nil {
			s.logger.Error("otlp grpc listen", "addr", addr, "err", err)
			return
		}
		srv := s.otlpGRPCServer()
		go func() {
			<-ctx.Done()
			done := make(chan struct{})
			go func() { srv.GracefulStop(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				srv.Stop()
			}
		}()
		s.logger.Info("otlp grpc listening", "addr", addr)
		if err := srv.Serve(lis); err != nil {
			s.logger.Error("otlp grpc server", "err", err)
		}
	}()
}

// otlpListen retries for a few seconds: after a restart on Windows the old
// process may still hold the port.
func otlpListen(ctx context.Context, addr string) (net.Listener, error) {
	var lc net.ListenConfig
	deadline := time.Now().Add(10 * time.Second)
	for {
		lis, err := lc.Listen(ctx, "tcp", addr)
		if err == nil || time.Now().After(deadline) || ctx.Err() != nil {
			return lis, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
