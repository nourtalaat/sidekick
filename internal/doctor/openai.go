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

// Ollama's own API calls, next to its OpenAI-compatible API.
const (
	ollamaVersionPath = "/api/version"
	ollamaRunningPath = "/api/ps"
)

// ollamaLatestTag ends the name Ollama gives a model ID without a tag.
const ollamaLatestTag = ":latest"

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

// ollamaRunning is Ollama's answer to GET /api/ps: the models it has
// loaded.
type ollamaRunning struct {
	Models []ollamaLoaded `json:"models"`
}

// ollamaLoaded is one loaded model, with the context it runs with. Ollama
// versions that do not report the context leave ContextLength at zero.
type ollamaLoaded struct {
	Name          string `json:"name"`
	Model         string `json:"model"`
	ContextLength int    `json:"context_length"`
}

// checkLocalModels checks each OpenAI-compatible connector that runs a
// local model preset: it warns when the connector has no server set, and
// asks the server whether it is Ollama, so the fix can name the line to
// add. On Ollama, set or detected, it checks that the model runs with at
// least the preset's minimum context.
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

		server, set := cfg.ResolvedServer()
		onOllama := set && server == openai.ServerOllama

		if !set {
			var r Result

			r, onOllama = d.checkServer(ctx, name, cfg.BaseURL, server)
			results = append(results, r)
		}

		if onOllama {
			results = append(results, d.checkOllamaContext(ctx, name, cfg))
		}
	}

	return results
}

// checkServer warns that the connector called name has no server set. It
// calls Ollama's version API at baseURL: when Ollama answers, the fix is
// the server line for Ollama, and isOllama is true. inferred is the server
// Sidekick assumes.
func (d *doctor) checkServer(ctx context.Context, name, baseURL string, inferred openai.Server) (r Result, isOllama bool) {
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
		}, true
	}

	assumed := "no server set, so Sidekick assumes a server that ignores a forced tool choice"
	if inferred == openai.ServerOllama {
		assumed = "no server set, so Sidekick assumes Ollama from the model tag or the port"
	}

	return Result{
		Check: check, Status: StatusWarn,
		Message: fmt.Sprintf("%s; the server at baseURL did not answer as Ollama: %v", assumed, err),
		Fix:     fmt.Sprintf("add the kind of server at baseURL to model.connectors.%s: `server: vllm`, `server: llamacpp` or `server: ollama`", name),
	}, false
}

// checkOllamaContext asks Ollama which context it runs the connector's
// model with, and warns when that is below the preset's minimum or cannot
// be told. Unless OLLAMA_CONTEXT_LENGTH is set, Ollama picks the context
// from the video memory, which is too small below 48 GiB.
func (d *doctor) checkOllamaContext(ctx context.Context, name string, cfg *openai.Config) Result {
	check := "context " + name
	need := cfg.MinContextTokens()
	tag := ollamaModelName(cfg.Model)
	setFix := fmt.Sprintf("set OLLAMA_CONTEXT_LENGTH=%d or more in Ollama's environment and restart Ollama", need)
	confirmFix := fmt.Sprintf("if OLLAMA_CONTEXT_LENGTH is not set to %d or more in Ollama's environment, set it and restart Ollama; then run sidekick doctor again while `ollama ps` lists %s", need, tag)
	unset := "unless OLLAMA_CONTEXT_LENGTH is set, Ollama picks the context from the video memory, which is too small below 48 GiB"

	var running ollamaRunning
	if err := d.getOllama(ctx, cfg.BaseURL, ollamaRunningPath, &running); err != nil {
		return Result{
			Check: check, Status: StatusWarn,
			Message: fmt.Sprintf("cannot ask Ollama which context it runs %s with: %v", tag, err),
			Fix:     confirmFix,
		}
	}

	i := slices.IndexFunc(running.Models, func(m ollamaLoaded) bool { return m.Name == tag || m.Model == tag })
	if i < 0 {
		return Result{
			Check: check, Status: StatusWarn,
			Message: fmt.Sprintf("Ollama has not loaded %s, so its context cannot be checked; %s", tag, unset),
			Fix:     confirmFix,
		}
	}

	got := running.Models[i].ContextLength

	switch {
	case got == 0:
		return Result{
			Check: check, Status: StatusWarn,
			Message: fmt.Sprintf("this Ollama version does not report the context it runs %s with; %s", tag, unset),
			Fix:     confirmFix,
		}
	case got < need:
		return Result{
			Check: check, Status: StatusWarn,
			Message: fmt.Sprintf("Ollama runs %s with a context of %d tokens, below the %d an investigation needs, so long prompts are cut", tag, got, need),
			Fix:     setFix,
		}
	default:
		return Result{
			Check: check, Status: StatusOK,
			Message: fmt.Sprintf("Ollama runs %s with a context of %d tokens, at least the %d an investigation needs", tag, got, need),
		}
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

// ollamaModelName returns the name Ollama lists a model ID under: the ID,
// with Ollama's latest tag when it has none.
func ollamaModelName(id string) string {
	if strings.Contains(id, ":") {
		return id
	}

	return id + ollamaLatestTag
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
