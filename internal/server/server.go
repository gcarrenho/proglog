package server

import (
	"context"

	api "github.com/gcarrenho/proglog/api/v1"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpc_auth "github.com/grpc-ecosystem/go-grpc-middleware/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"time"

	grpc_zap "github.com/grpc-ecosystem/go-grpc-middleware/logging/zap"
	grpc_ctxtags "github.com/grpc-ecosystem/go-grpc-middleware/tags"
	"go.opencensus.io/plugin/ocgrpc"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Config struct {
	CommitLog  CommitLog
	Authorizer Authorizer
}

const (
	objectWildcard = "*"
	produceAction  = "produce"
	consumeAction  = "consume"
)

var _ api.LogServer = (*grpcServer)(nil)

type grpcServer struct {
	api.UnimplementedLogServer
	*Config
}

// The NewGRPCServer role configures and creates a gRPC server with various interceptor options for logging,
// authentication, and statistics handling. It also registers a custom server that implements LogServer service-specific logic.
// This configuration ensures that the server is well instrumented for monitoring and security,
// and that any errors during the configuration process are handled appropriately.
func newgrpcServer(config *Config) (srv *grpcServer, err error) {
	srv = &grpcServer{
		Config: config,
	}
	return srv, nil
}

func NewGRPCServer(config *Config, grpcOpts ...grpc.ServerOption) (
	*grpc.Server,
	error,
) {
	logger := zap.L().Named("server") // create a log
	zapOpts := []grpc_zap.Option{
		grpc_zap.WithDurationField(
			func(duration time.Duration) zapcore.Field {
				return zap.Int64(
					"grpc.time_ns",
					duration.Nanoseconds(),
				)
			},
		),
	} // Config of options of logger. In thiss case the duration in nanoseconds

	trace.ApplyConfig(trace.Config{DefaultSampler: trace.AlwaysSample()}) // Config of th trace to always show the request
	err := view.Register(ocgrpc.DefaultServerViews...)                    // View register of opencensus
	if err != nil {
		return nil, err
	}

	/* Config of interceptors
	Interceptores de Stream: Se configuran los interceptores de stream para el servidor gRPC. Estos interceptores son funciones que se ejecutan antes y después de que el servidor procese una llamada RPC.
	grpc_ctxtags.StreamServerInterceptor: Interceptor para gestionar etiquetas de contexto.
	grpc_zap.StreamServerInterceptor: Interceptor para el logger zap.
	grpc_auth.StreamServerInterceptor: Interceptor para la autenticación.

	Interceptores Unarios: De manera similar, se configuran los interceptores unarios (para llamadas RPC individuales).
	grpc_ctxtags.UnaryServerInterceptor: Interceptor para gestionar etiquetas de contexto.
	grpc_zap.UnaryServerInterceptor: Interceptor para el logger zap.
	grpc_auth.UnaryServerInterceptor: Interceptor para la autenticación.

	Manejador de Estadísticas: Se añade un manejador de estadísticas (grpc.StatsHandler) de OpenCensus.
	*/
	grpcOpts = append(grpcOpts,
		grpc.StreamInterceptor(
			grpc_middleware.ChainStreamServer(
				grpc_ctxtags.StreamServerInterceptor(),
				grpc_zap.StreamServerInterceptor(logger, zapOpts...),
				grpc_auth.StreamServerInterceptor(authenticate),
			)), grpc.UnaryInterceptor(grpc_middleware.ChainUnaryServer(
			grpc_ctxtags.UnaryServerInterceptor(),
			grpc_zap.UnaryServerInterceptor(logger, zapOpts...),
			grpc_auth.UnaryServerInterceptor(authenticate),
		)),
		grpc.StatsHandler(&ocgrpc.ServerHandler{}),
	)

	gsrv := grpc.NewServer(grpcOpts...) // Create the server gRPC with grpcOpts
	srv, err := newgrpcServer(config)   // Create an instance of gRPC server with config
	if err != nil {
		return nil, err
	}
	api.RegisterLogServer(gsrv, srv) // Registers  the our server(srv) in the gRPC server under th "LogServer" services defined in api.
	return gsrv, nil
}

func (s *grpcServer) Produce(ctx context.Context, req *api.ProduceRequest) (
	*api.ProduceResponse, error) {
	if err := s.Authorizer.Authorize(
		subject(ctx),
		objectWildcard,
		produceAction,
	); err != nil {
		return nil, err
	}
	offset, err := s.CommitLog.Append(req.Record)
	if err != nil {
		return nil, err
	}
	return &api.ProduceResponse{Offset: offset}, nil
}

func (s *grpcServer) Consume(ctx context.Context, req *api.ConsumeRequest) (
	*api.ConsumeResponse, error) {
	if err := s.Authorizer.Authorize(
		subject(ctx),
		objectWildcard,
		consumeAction,
	); err != nil {
		return nil, err
	}
	record, err := s.CommitLog.Read(req.Offset)
	if err != nil {
		return nil, err
	}
	return &api.ConsumeResponse{Record: record}, nil
}

func (s *grpcServer) ProduceStream(
	stream api.Log_ProduceStreamServer,
) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}
		res, err := s.Produce(stream.Context(), req)
		if err != nil {
			return err
		}
		if err = stream.Send(res); err != nil {
			return err
		}
	}
}

func (s *grpcServer) ConsumeStream(
	req *api.ConsumeRequest,
	stream api.Log_ConsumeStreamServer,
) error {
	for {
		select {
		case <-stream.Context().Done():
			return nil
		default:
			res, err := s.Consume(stream.Context(), req)
			switch err.(type) {
			case nil:
			case api.ErrOffsetOutOfRange:
				continue
			default:
				return err
			}
			if err = stream.Send(res); err != nil {
				return err
			}
			req.Offset++
		}
	}
}

type CommitLog interface {
	Append(*api.Record) (uint64, error)
	Read(uint64) (*api.Record, error)
}

type Authorizer interface {
	Authorize(subject, object, action string) error
}

func authenticate(ctx context.Context) (context.Context, error) {
	peer, ok := peer.FromContext(ctx)
	if !ok {
		return ctx, status.New(
			codes.Unknown,
			"couldn't find peer info",
		).Err()
	}

	if peer.AuthInfo == nil {
		return context.WithValue(ctx, subjectContextKey{}, ""), nil
	}

	tlsInfo := peer.AuthInfo.(credentials.TLSInfo)
	subject := tlsInfo.State.VerifiedChains[0][0].Subject.CommonName
	ctx = context.WithValue(ctx, subjectContextKey{}, subject)

	return ctx, nil
}

func subject(ctx context.Context) string {
	return ctx.Value(subjectContextKey{}).(string)
}

type subjectContextKey struct{}
