package topaz

import (
	"context"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"

	"github.com/aserto-dev/aserto-grpc/grpcclient"
	"github.com/aserto-dev/aserto-grpc/grpcutil/metrics"
	pol "github.com/aserto-dev/go-grpc/aserto/authorizer/policy/v1"
	"github.com/aserto-dev/topaz/pkg/app/impl"
	"github.com/aserto-dev/topaz/pkg/app/server"
	"github.com/aserto-dev/topaz/pkg/cc/config"
	"github.com/aserto-dev/topaz/resolvers"

	authz2 "github.com/aserto-dev/go-authorizer/aserto/authorizer/v2"
	dir "github.com/aserto-dev/go-grpc/aserto/authorizer/directory/v1"
	info "github.com/aserto-dev/go-grpc/aserto/common/info/v1"
)

// GRPCServerRegistrations is where we register implementations with the GRPC server
func GRPCServerRegistrations(
	ctx context.Context,
	logger *zerolog.Logger,
	cfg *config.Config,
	dop grpcclient.DialOptionsProvider,
	runtimeResolver resolvers.RuntimeResolver,

	implAuthorizerServer *impl.AuthorizerServer,
	implDirectoryServer *impl.DirectoryServer,
	implPolicyServer *impl.PolicyServer,
	implInfo *impl.InfoServer,
) (server.GRPCRegistrations, error) {
	return func(srv *grpc.Server) {
		server.CoreServiceRegistrations(implAuthorizerServer, implDirectoryServer)(srv)
		info.RegisterInfoServer(srv, implInfo)
		pol.RegisterPolicyServer(srv, implPolicyServer)
		metrics.RegisterPrometheusIfEnabled(&cfg.API.Metrics, srv)
	}, nil
}

// GatewayServerRegistrations is where we register implementations with the Gateway server
func GatewayServerRegistrations() server.HandlerRegistrations {
	return func(ctx context.Context, mux *runtime.ServeMux, grpcEndpoint string, opts []grpc.DialOption) error {
		err := authz2.RegisterAuthorizerHandlerFromEndpoint(ctx, mux, grpcEndpoint, opts)
		if err != nil {
			return errors.Wrap(err, "failed to register authorizer v2 handler with gateway")
		}
		err = dir.RegisterDirectoryHandlerFromEndpoint(ctx, mux, grpcEndpoint, opts)
		if err != nil {
			return errors.Wrap(err, "failed to register directory handler with the gateway")
		}
		err = pol.RegisterPolicyHandlerFromEndpoint(ctx, mux, grpcEndpoint, opts)
		if err != nil {
			return errors.Wrap(err, "failed to register policy handler with the gateway")
		}
		err = info.RegisterInfoHandlerFromEndpoint(ctx, mux, grpcEndpoint, opts)
		if err != nil {
			return errors.Wrap(err, "failed to register info handler with the gateway")
		}
		return nil
	}
}
