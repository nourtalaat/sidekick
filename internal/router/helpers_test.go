package router

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	_ "github.com/nourtalaat/sidekick/internal/intake/alertmanager" // Adapter "alertmanager".
	"github.com/nourtalaat/sidekick/internal/model"
	_ "github.com/nourtalaat/sidekick/internal/output/slack" // Output type "slack".
	"github.com/nourtalaat/sidekick/internal/registry"
)

// fakeModelConfig is the config of the fake model provider.
type fakeModelConfig struct {
	model.Settings `yaml:",inline"`
}

// fakeConnector is a model connector that is never called.
type fakeConnector struct{}

func (fakeConnector) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalling: true}
}

func (fakeConnector) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}

// loadConfig loads fixture/sidekick.yaml and builds its plugins, and
// returns the config with the built instances.
func loadConfig(t *testing.T) (*config.Config, *config.Instances) {
	t.Helper()

	regs := config.Registries{Models: fakeModels()}

	cfg, err := config.Load(t.Context(), "fixture/sidekick.yaml", config.LoadOptions{Registries: regs})
	require.NoError(t, err)

	in, err := config.Build(t.Context(), cfg, regs, registry.Deps{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, in.Close()) })

	return cfg, in
}

// fakeModels is a model registry with a provider named fake.
func fakeModels() *registry.Registry[model.Connector] {
	models := registry.New[model.Connector]("model")
	models.Register("fake", registry.Typed(func(context.Context, string, fakeModelConfig, registry.Deps) (model.Connector, error) {
		return fakeConnector{}, nil
	}))

	return models
}
