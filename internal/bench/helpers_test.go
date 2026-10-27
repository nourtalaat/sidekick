package bench

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	_ "github.com/nourtalaat/sidekick/internal/intake/alertmanager" // Intake adapter "alertmanager", which the scenarios use.
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// testConfig is the config the tests load.
const testConfig = "fixture/sidekick.yaml"

// scriptedConfig is the config of the scripted model provider.
type scriptedConfig struct {
	model.Settings `yaml:",inline"`
}

// loadTestConfig loads the test config with conn as its scripted
// connector, and returns it with the registries it was loaded with.
func loadTestConfig(t *testing.T, conn model.Connector) (*config.Config, config.Registries) {
	t.Helper()

	models := registry.New[model.Connector]("model")
	models.Register("scripted", registry.Typed(func(context.Context, string, scriptedConfig, registry.Deps) (model.Connector, error) {
		return conn, nil
	}))

	regs := config.Registries{Models: models}

	cfg, err := config.Load(t.Context(), testConfig, config.LoadOptions{Registries: regs})
	require.NoError(t, err)

	return cfg, regs
}
