//go:build integration

package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// startTimeout bounds how long a test waits for the app to start serving.
const startTimeout = 30 * time.Second

var (
	testBuild       = buildinfo.Info{Version: "v0.0.0-test", Commit: "abc123"}
	errFakeGenerate = errors.New("the fake connector does not generate")
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// running is an App that a test started with Run.
type running struct {
	app *App
	// metrics and main are the base URLs of the two listeners.
	metrics string
	main    string
	logs    *syncBuffer
	// stop ends Run and returns its result.
	stop func() error
}

// newApp writes a config from fixture/sidekick.yaml for the database at
// dbURL with the migration mode, and returns the App New makes from it.
func newApp(t *testing.T, dbURL string, mode config.MigrationMode, logs io.Writer) (*App, error) {
	t.Helper()

	return New(t.Context(), Options{
		ConfigPath: writeConfig(t, dbURL, mode),
		Registries: fakeRegistries(),
		Build:      testBuild,
		LogOutput:  logs,
	})
}

// startApp makes an App with newApp, lets register add to it, runs it and
// waits until it serves. The App stops when the test ends, if the test did
// not stop it.
func startApp(t *testing.T, dbURL string, mode config.MigrationMode, register func(a *App)) *running {
	t.Helper()

	logs := &syncBuffer{}

	a, err := newApp(t, dbURL, mode, logs)
	require.NoError(t, err)

	if register != nil {
		register(a)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- a.Run(ctx) }()

	select {
	case <-a.Started():
	case err := <-done:
		cancel()
		t.Fatalf("Run returned before serving: %v", err)
	case <-time.After(startTimeout):
		cancel()
		t.Fatal("the app did not start serving")
	}

	var once sync.Once

	var runErr error

	stop := func() error {
		once.Do(func() {
			cancel()

			runErr = <-done
		})

		return runErr
	}

	t.Cleanup(func() { require.NoError(t, stop()) })

	listen, metrics := a.Addrs()

	return &running{app: a, main: "http://" + listen.String(), metrics: "http://" + metrics.String(), logs: logs, stop: stop}
}

// get sends a GET request to url and returns the status and the body.
func get(t *testing.T, url string) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(body)
}

// writeConfig writes fixture/sidekick.yaml with its placeholders filled in
// and a secrets directory that holds dbURL and a fake API key, and returns
// the config's path.
func writeConfig(t *testing.T, dbURL string, mode config.MigrationMode) string {
	t.Helper()

	secretsDir := t.TempDir()
	for path, value := range map[string]string{"sidekick-db/url": dbURL, "anthropic/apiKey": "fake-key"} {
		full := filepath.Join(secretsDir, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
		require.NoError(t, os.WriteFile(full, []byte(value+"\n"), 0o600))
	}

	content := readFixture(t, "sidekick.yaml")
	content = strings.NewReplacer("SECRETS_DIR", secretsDir, "MIGRATIONS", string(mode)).Replace(content)

	path := filepath.Join(t.TempDir(), "sidekick.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

func readFixture(t *testing.T, name string) string {
	t.Helper()

	content, err := fs.ReadFile(os.DirFS("fixture"), name)
	require.NoError(t, err)

	return string(content)
}

// fakeRegistries returns registries whose only plugin is a fake anthropic
// model provider.
func fakeRegistries() config.Registries {
	models := registry.New[model.Connector]("model")
	models.Register("anthropic", registry.Typed(func(context.Context, string, fakeModelConfig, registry.Deps) (model.Connector, error) {
		return fakeConnector{}, nil
	}))

	return config.Registries{Models: models}
}

type fakeModelConfig struct {
	model.Settings `yaml:",inline"`

	APIKeySecret *registry.SecretRef `yaml:"apiKeySecret"`
}

func (*fakeModelConfig) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalling: true}
}

type fakeConnector struct{}

func (fakeConnector) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalling: true}
}

func (fakeConnector) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errFakeGenerate
}

// syncBuffer is a bytes.Buffer that the app's goroutines can log to while
// a test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n, err := b.buf.Write(p)
	if err != nil {
		return n, err //nolint:wrapcheck // A Writer passes its error on as it is.
	}

	return n, nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}
