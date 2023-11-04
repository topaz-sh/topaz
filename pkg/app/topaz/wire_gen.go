// Code generated by Wire. DO NOT EDIT.

//go:generate go run github.com/google/wire/cmd/wire
//go:build !wireinject
// +build !wireinject

package topaz

import (
	"github.com/aserto-dev/logger"
	"github.com/aserto-dev/service-host"
	"github.com/aserto-dev/topaz/pkg/app"
	"github.com/aserto-dev/topaz/pkg/app/impl"
	"github.com/aserto-dev/topaz/pkg/cc"
	"github.com/aserto-dev/topaz/pkg/cc/config"
	"github.com/aserto-dev/topaz/resolvers"
	"github.com/google/wire"
	"google.golang.org/grpc"
)

// Injectors from wire.go:

func BuildApp(logOutput logger.Writer, errOutput logger.ErrWriter, configPath config.Path, overrides config.Overrider) (*app.Topaz, func(), error) {
	ccCC, cleanup, err := cc.NewCC(logOutput, errOutput, configPath, overrides)
	if err != nil {
		return nil, nil, err
	}
	context := ccCC.Context
	zerologLogger := ccCC.Log
	v := DefaultGRPCOptions()
	configConfig := ccCC.Config
	serviceFactory := builder.NewServiceFactory()
	serviceManager := builder.NewServiceManager(zerologLogger)
	v2 := DefaultServices()
	topaz := &app.Topaz{
		Context:        context,
		Logger:         zerologLogger,
		ServerOptions:  v,
		Configuration:  configConfig,
		ServiceBuilder: serviceFactory,
		Manager:        serviceManager,
		Services:       v2,
	}
	return topaz, func() {
		cleanup()
	}, nil
}

func BuildTestApp(logOutput logger.Writer, errOutput logger.ErrWriter, configPath config.Path, overrides config.Overrider) (*app.Topaz, func(), error) {
	ccCC, cleanup, err := cc.NewTestCC(logOutput, errOutput, configPath, overrides)
	if err != nil {
		return nil, nil, err
	}
	context := ccCC.Context
	zerologLogger := ccCC.Log
	v := DefaultGRPCOptions()
	configConfig := ccCC.Config
	serviceFactory := builder.NewServiceFactory()
	serviceManager := builder.NewServiceManager(zerologLogger)
	v2 := DefaultServices()
	topaz := &app.Topaz{
		Context:        context,
		Logger:         zerologLogger,
		ServerOptions:  v,
		Configuration:  configConfig,
		ServiceBuilder: serviceFactory,
		Manager:        serviceManager,
		Services:       v2,
	}
	return topaz, func() {
		cleanup()
	}, nil
}

// wire.go:

var (
	commonSet = wire.NewSet(resolvers.New, impl.NewAuthorizerServer, builder.NewServiceFactory, builder.NewServiceManager, DefaultGRPCOptions,
		DefaultServices, wire.FieldsOf(new(*cc.CC), "Config", "Log", "Context", "ErrGroup"), wire.FieldsOf(new(*config.Config), "Common", "DecisionLogger"), wire.Struct(new(app.Topaz), "*"),
	)

	appTestSet = wire.NewSet(
		commonSet, cc.NewTestCC,
	)

	appSet = wire.NewSet(
		commonSet, cc.NewCC,
	)
)

func DefaultGRPCOptions() []grpc.ServerOption {
	return nil
}

func DefaultServices() map[string]app.ServiceTypes {
	return make(map[string]app.ServiceTypes)
}
