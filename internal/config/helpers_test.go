package config

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/mcp"
)

// baseYAML is a valid config without routes, for tests that write their own.
const baseYAML = `database: {urlSecret: {name: sidekick-db, key: url}}
model: {connectors: {claude: {provider: anthropic, model: claude-opus-5-5}}, roles: {investigate: claude}}
`

// fakeMaxOutputTokens is the output limit of every fake model.
const fakeMaxOutputTokens = 64000

// errFakeBuild is returned by fake plugins asked to fail when built.
var errFakeBuild = errors.New("dial failed")

// fakeModelConfig is the config of the fake connectors that declare their
// capabilities, like a connector with a table of known models.
type fakeModelConfig struct {
	model.Settings `yaml:",inline"`

	APIKeySecret *registry.SecretRef `yaml:"apiKeySecret"`
	BaseURL      string              `yaml:"baseURL"`
	Region       string              `yaml:"region"`
	API          string              `yaml:"api"`
	Caps         *fakeCaps           `yaml:"capabilities"`
}

type fakeCaps struct {
	ToolCalling   bool `yaml:"toolCalling"`
	ToolChoice    bool `yaml:"toolChoice"`
	ContextTokens int  `yaml:"contextTokens"`
}

// Capabilities implements model.CapabilityDeclarer: a declared block wins,
// otherwise the model can do everything.
func (c *fakeModelConfig) Capabilities() model.Capabilities {
	caps := model.Capabilities{
		ToolCalling:     true,
		ToolChoice:      true,
		ThinkingModes:   []model.ThinkingMode{model.ThinkingAdaptive},
		EffortLevels:    []model.Effort{model.EffortLow, model.EffortMedium, model.EffortHigh, model.EffortXHigh, model.EffortMax},
		MaxOutputTokens: fakeMaxOutputTokens,
	}
	if c.Caps != nil {
		caps.ToolCalling = c.Caps.ToolCalling
		caps.ToolChoice = c.Caps.ToolChoice
		caps.ContextTokens = c.Caps.ContextTokens
	}

	return caps
}

// fakeLazyModelConfig is the config of a connector that learns its
// capabilities only when built.
type fakeLazyModelConfig struct {
	model.Settings `yaml:",inline"`

	ToolCalling bool `yaml:"toolCalling"`
	FailBuild   bool `yaml:"failBuild"`
}

type fakeConnector struct {
	caps model.Capabilities
}

func (f fakeConnector) Capabilities() model.Capabilities { return f.caps }

func (fakeConnector) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("fake connector")
}

type fakeKubernetesConfig struct {
	Namespaces []string `yaml:"namespaces"`
}

type fakeSource struct{}

func (fakeSource) Tools(context.Context) ([]source.Tool, error) { return nil, nil }

func (fakeSource) Call(context.Context, source.Call) (source.Result, error) {
	return source.Result{}, nil
}

func (fakeSource) Close() error { return nil }

type fakeAlertmanagerConfig struct{}

type fakeGenericConfig struct {
	Fields map[string]string `yaml:"fields"`
}

type fakeAdapter struct{}

func (fakeAdapter) Parse(context.Context, intake.Request) ([]event.Event, error) { return nil, nil }

type fakeSlackConfig struct {
	Channel string `yaml:"channel"`
}

func (c *fakeSlackConfig) Validate() error {
	if c.Channel == "" {
		return errors.New("channel: required")
	}

	return nil
}

type fakeWebhookOutputConfig struct {
	URL        string             `yaml:"url"`
	HMACSecret registry.SecretRef `yaml:"hmacSecret"`
}

type fakeSink struct{}

func (fakeSink) Accepts(output.Phase) bool { return true }

func (fakeSink) Publish(context.Context, output.Update) (output.Ref, error) {
	return output.Ref{}, nil
}

// testRegistries returns registries of their own with fake plugin types, so
// tests never touch the global registries. The mcp type checks its section
// with the real mcp.Config but builds a fake source.
func testRegistries() Registries {
	regs := Registries{
		Models:  registry.New[model.Connector]("model"),
		Sources: registry.New[source.Source]("source"),
		Intakes: registry.New[intake.Adapter]("intake"),
		Outputs: registry.New[output.Sink]("output"),
	}

	declared := registry.Typed(func(_ context.Context, _ string, cfg fakeModelConfig, _ registry.Deps) (model.Connector, error) {
		return fakeConnector{caps: cfg.Capabilities()}, nil
	})
	for _, provider := range []string{"anthropic", "bedrock", "openai"} {
		regs.Models.Register(provider, declared)
	}

	regs.Models.Register("lazy", registry.Typed(func(_ context.Context, _ string, cfg fakeLazyModelConfig, _ registry.Deps) (model.Connector, error) {
		if cfg.FailBuild {
			return nil, errFakeBuild
		}

		return fakeConnector{caps: model.Capabilities{ToolCalling: cfg.ToolCalling}}, nil
	}))

	regs.Sources.Register(mcp.Type, registry.Typed(func(context.Context, string, mcp.Config, registry.Deps) (source.Source, error) {
		return fakeSource{}, nil
	}))
	regs.Sources.Register("kubernetes", registry.Typed(func(context.Context, string, fakeKubernetesConfig, registry.Deps) (source.Source, error) {
		return fakeSource{}, nil
	}))
	regs.Intakes.Register("alertmanager", registry.Typed(func(context.Context, string, fakeAlertmanagerConfig, registry.Deps) (intake.Adapter, error) {
		return fakeAdapter{}, nil
	}))
	regs.Intakes.Register("generic", registry.Typed(func(context.Context, string, fakeGenericConfig, registry.Deps) (intake.Adapter, error) {
		return fakeAdapter{}, nil
	}))
	regs.Outputs.Register("slack", registry.Typed(func(context.Context, string, fakeSlackConfig, registry.Deps) (output.Sink, error) {
		return fakeSink{}, nil
	}))
	regs.Outputs.Register("webhook", registry.Typed(func(context.Context, string, fakeWebhookOutputConfig, registry.Deps) (output.Sink, error) {
		return fakeSink{}, nil
	}))

	return regs
}

// testOptions returns load options with the test registries.
func testOptions() LoadOptions {
	return LoadOptions{Registries: testRegistries()}
}

// readFixture returns the content of a file under fixture/.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := fs.ReadFile(os.DirFS("fixture"), name)
	require.NoError(t, err)

	return data
}

// minimalYAML is the smallest valid config, for tests to add keys to.
func minimalYAML(t *testing.T) string {
	t.Helper()

	return string(readFixture(t, "valid/minimal.yaml"))
}

// writeSecrets creates a secrets directory with one file per reference,
// each holding a fake value, and returns its path.
func writeSecrets(t *testing.T, refs ...registry.SecretRef) string {
	t.Helper()

	dir := t.TempDir()
	for _, ref := range refs {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ref.Name), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ref.Name, ref.Key), []byte("fake-value\n"), 0o600))
	}

	return dir
}
