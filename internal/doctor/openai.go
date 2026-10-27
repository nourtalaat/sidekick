package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/model/openai"
)

// ollamaVersionPath is Ollama's own version call, next to its
// OpenAI-compatible API.
const ollamaVersionPath = "/api/version"

// openAICompatPath ends the base URL of an OpenAI-compatible server. Ollama's
// own API sits next to it.
const openAICompatPath = "/v1"

// maxOllamaBody bounds each answer doctor reads from Ollama.
const maxOllamaBody = 1 << 20

// errOllamaBodyTooLarge is returned for an answer above maxOllamaBody.
var errOllamaBodyTooLarge = errors.New("the answer is larger than 1 MiB")

// ollamaVersion is Ollama's answer to GET /api/version.
type ollamaVersion struct {
	Version string `json:"version"`
}

// checkLocalModels checks each OpenAI-compatible connector that runs a
// local model preset: it warns when the connector has no server set, and
// asks the server whether it is Ollama, so the fix can name the line to
// add.
func (d *doctor) checkLocalModels(ctx context.Context) []Result {
	var results []Result

	for _, name := range slices.Sorted(maps.Keys(d.cfg.Model.Connectors)) {
		c := d.cfg.Model.Connectors[name]
		if c.Type != openai.Provider {
			continue
		}

		decoded, err := d.regs.Models.Check(ctx, c.Type, name, c.Section)
		if err != nil {
			continue
		}

		cfg, ok := openAIPreset(decoded)
		if !ok {
			continue
		}

		if server, set := cfg.ResolvedServer(); !set {
			results = append(results, d.checkServer(ctx, name, cfg.BaseURL, server))
		}
	}

	return results
}

// checkServer warns that the connector called name has no server set. It
// calls Ollama's version API at baseURL: when Ollama answers, the fix is
// the server line for Ollama. inferred is the server Sidekick assumes.
func (d *doctor) checkServer(ctx context.Context, name, baseURL string, inferred openai.Server) Result {
	check := "server " + name

	var v ollamaVersion

	err := d.getOllama(ctx, baseURL, ollamaVersionPath, &v)
	if err == nil && v.Version == "" {
		err = fmt.Errorf("%s answered without a version", ollamaVersionPath)
	}

	if err == nil {
		return Result{
			Check: check, Status: StatusWarn,
			Message: fmt.Sprintf("no server set; the server at baseURL is Ollama %s", v.Version),
			Fix:     fmt.Sprintf("add `server: ollama` to model.connectors.%s", name),
		}
	}

	assumed := "no server set, so Sidekick assumes a server that ignores a forced tool choice"
	if inferred == openai.ServerOllama {
		assumed = "no server set, so Sidekick assumes Ollama from the model tag or the port"
	}

	return Result{
		Check: check, Status: StatusWarn,
		Message: fmt.Sprintf("%s; the server at baseURL did not answer as Ollama: %v", assumed, err),
		Fix:     fmt.Sprintf("add the kind of server at baseURL to model.connectors.%s: `server: vllm`, `server: llamacpp` or `server: ollama`", name),
	}
}

// getOllama calls GET on path of Ollama's own API next to the
// OpenAI-compatible baseURL, and decodes the JSON answer into out. It sends
// no credentials: Ollama's API needs none.
func (d *doctor) getOllama(ctx context.Context, baseURL, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	target, err := ollamaURL(baseURL, path)
	if err != nil {
		return fmt.Errorf("address of %s: %w", path, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("make the request for %s: %w", path, err)
	}

	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("call %s: %w", path, err)
	}

	defer func() {
		// The answer is read in full or not needed; a close error changes nothing.
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", path, resp.Status)
	}

	body := httpclient.LimitBody(resp.Body, maxOllamaBody, errOllamaBodyTooLarge)
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return fmt.Errorf("read the answer of %s: %w", path, err)
	}

	return nil
}

// openAIPreset returns the decoded config of an OpenAI-compatible connector
// that runs a local model preset, and false for any other connector.
func openAIPreset(decoded any) (*openai.Config, bool) {
	cfg, ok := decoded.(*openai.Config)
	if !ok || cfg.MinContextTokens() == 0 {
		return nil, false
	}

	return cfg, true
}

// ollamaURL returns the address of path on Ollama's own API: baseURL
// without its /v1 ending, user info, query or fragment, followed by path.
func ollamaURL(baseURL, path string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse baseURL: %w", err)
	}

	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.RawPath = ""
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), openAICompatPath)

	return u.JoinPath(path).String(), nil
}
