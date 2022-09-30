package testing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aserto-dev/runtime"
	"github.com/aserto-dev/testutil"
	"github.com/aserto-dev/topaz/pkg/app"
	"github.com/aserto-dev/topaz/pkg/app/instance"
	"github.com/aserto-dev/topaz/pkg/app/topaz"
	"github.com/aserto-dev/topaz/pkg/cc/config"
)

const (
	ten = 10
)

// EngineHarness wraps an Aserto Runtime Engine so we can set it up easily
// and monitor its logs
type EngineHarness struct {
	Engine      *app.Authorizer
	LogDebugger *testutil.LogDebugger

	cleanup func()
	t       *testing.T
}

// Cleanup releases all resources the harness uses and
// shuts down servers and runtimes
func (h *EngineHarness) Cleanup() {
	assert := require.New(h.t)
	// Cleanup the app
	h.cleanup()

	assert.Eventually(func() bool {
		return !PortOpen("127.0.0.1:8484")
	}, ten*time.Second, ten*time.Millisecond)
	assert.Eventually(func() bool {
		return !PortOpen("127.0.0.1:8383")
	}, ten*time.Second, ten*time.Millisecond)
	assert.Eventually(func() bool {
		return !PortOpen("127.0.0.1:8282")
	}, ten*time.Second, ten*time.Millisecond)
}

func (h *EngineHarness) Runtime() *runtime.Runtime {
	result, err := h.Engine.RuntimeResolver.RuntimeFromContext(h.Engine.Context, h.Engine.Configuration.OPA.InstanceID, "", "")
	require.NoError(h.t, err)
	return result
}

func (h *EngineHarness) ContextWithTenant() context.Context {
	return context.WithValue(context.Background(), instance.InstanceIDHeader, h.Engine.Configuration.OPA.InstanceID)
}

// SetupOffline sets up an engine that uses a runtime that loads offline bundles,
// from the assets directory
func SetupOffline(t *testing.T, configOverrides func(*config.Config)) *EngineHarness {
	return setup(t, configOverrides, false)
}

// SetupOnline sets up an engine that uses a runtime that loads online bundles,
// from the online aserto registry service
func SetupOnline(t *testing.T, configOverrides func(*config.Config)) *EngineHarness {
	return setup(t, configOverrides, true)
}

func setup(t *testing.T, configOverrides func(*config.Config), online bool) *EngineHarness {
	assert := require.New(t)

	var err error
	h := &EngineHarness{
		t:           t,
		LogDebugger: testutil.NewLogDebugger(t, "onebox"),
	}

	configFile := AssetDefaultConfigLocal()
	if online {
		configFile = AssetDefaultConfigOnline()
	}
	h.Engine, h.cleanup, err = topaz.BuildTestApp(
		h.LogDebugger,
		h.LogDebugger,
		configFile,
		configOverrides,
	)
	assert.NoError(err)

	err = h.Engine.Start()
	assert.NoError(err)

	if online {
		for i := 0; i < 2; i++ {
			assert.Eventually(func() bool {
				return h.LogDebugger.Contains("Bundle loaded and activated successfully")
			}, ten*time.Second, ten*time.Millisecond)
		}
	}

	assert.Eventually(func() bool {
		return PortOpen("127.0.0.1:8383")
	}, ten*time.Second, ten*time.Millisecond)

	return h
}
