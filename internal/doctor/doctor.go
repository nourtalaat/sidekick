// Package doctor checks that Sidekick can reach everything its config
// names: the database and its schema, the Slack bot token, each model
// connector's API key and each source, and warns about models without a
// price, about OpenAI-compatible investigate connectors that cannot be
// forced to call a tool, about local model presets without a server set,
// and about presets that Ollama runs with too small a context. Each check
// gives a Result with a fix for any problem. The checks make only calls
// that change nothing and cost nothing, and no secret value ever appears
// in a Result.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Status is the outcome of one check.
type Status string

// Statuses, from good to bad.
const (
	// StatusOK means the check passed.
	StatusOK Status = "ok"
	// StatusSkip means there was nothing to check.
	StatusSkip Status = "skip"
	// StatusWarn means Sidekick works, but something should be fixed.
	StatusWarn Status = "warn"
	// StatusFail means Sidekick cannot do part of its work.
	StatusFail Status = "fail"
)

// DefaultSourceWait is how long a source has to connect and list its
// tools. It is longer than the MCP source's own connect timeout, so its
// first attempt can finish.
const DefaultSourceWait = 15 * time.Second

// checkTimeout bounds each check that makes one call.
const checkTimeout = 20 * time.Second

// Result is the outcome of one check.
type Result struct {
	// Check names what was checked, such as "database" or "source signoz".
	Check  string
	Status Status
	// Message says what the check found.
	Message string
	// Details are extra lines, such as the tools a source does not expose.
	Details []string
	// Fix says how to solve the problem, for warn and fail.
	Fix string
}

// Options set up Run. Only Config is required.
type Options struct {
	// Config is the loaded config. It does not need its secrets checked:
	// each check reads the secrets it needs and reports those it cannot.
	Config *config.Config
	// Registries are where plugin types are looked up. The zero value
	// means the global registries.
	Registries config.Registries
	// Secrets resolves secret references. Nil means the files under the
	// config's secrets.dir.
	Secrets registry.Secrets
	// HTTP is the client for Slack, model providers and HTTP sources. Nil
	// means the Sidekick default client.
	HTTP *http.Client
	// OpenDatabase connects to the database. Nil means Postgres through
	// the store package.
	OpenDatabase OpenDatabase
	// SourceWait is how long each source has to connect. Zero means
	// DefaultSourceWait.
	SourceWait time.Duration
}

// Run runs every check, in order: database, Slack, model keys, prices,
// tool choice, local model servers and their context, sources. It never
// stops early, so one run shows every problem. Secret values that any check
// read are replaced in the results.
func Run(ctx context.Context, opts Options) []Result {
	d := newDoctor(opts)

	var results []Result

	results = append(results, d.checkDatabase(ctx))
	results = append(results, d.checkSlack(ctx)...)
	results = append(results, d.checkModels(ctx)...)
	results = append(results, d.checkPrices(ctx)...)
	results = append(results, d.checkToolChoice(ctx)...)
	results = append(results, d.checkLocalModels(ctx)...)
	results = append(results, d.checkSources(ctx)...)

	for i := range results {
		results[i] = d.secrets.scrubResult(results[i])
	}

	return results
}

// Failed reports whether any result failed.
func Failed(results []Result) bool {
	for _, r := range results {
		if r.Status == StatusFail {
			return true
		}
	}

	return false
}

// Write prints one line per result, its status first, then its details
// and fix indented below it.
func Write(w io.Writer, results []Result) error {
	for _, r := range results {
		if _, err := fmt.Fprintf(w, "%-4s  %s: %s\n", label(r.Status), r.Check, r.Message); err != nil {
			return fmt.Errorf("write result: %w", err)
		}

		for _, line := range r.Details {
			if _, err := fmt.Fprintf(w, "      - %s\n", line); err != nil {
				return fmt.Errorf("write result: %w", err)
			}
		}

		if r.Fix != "" {
			if _, err := fmt.Fprintf(w, "      fix: %s\n", r.Fix); err != nil {
				return fmt.Errorf("write result: %w", err)
			}
		}
	}

	return nil
}

// doctor holds what every check shares.
type doctor struct {
	cfg        *config.Config
	regs       config.Registries
	secrets    *recordingSecrets
	http       *http.Client
	open       OpenDatabase
	sourceWait time.Duration
}

func newDoctor(opts Options) *doctor {
	s := opts.Secrets
	if s == nil {
		s = secrets.NewDir(opts.Config.Secrets.Dir)
	}

	hc := opts.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	open := opts.OpenDatabase
	if open == nil {
		open = openPostgres
	}

	wait := opts.SourceWait
	if wait <= 0 {
		wait = DefaultSourceWait
	}

	return &doctor{
		cfg:        opts.Config,
		regs:       opts.Registries.WithDefaults(),
		secrets:    &recordingSecrets{inner: s},
		http:       hc,
		open:       open,
		sourceWait: wait,
	}
}

// deps are the plugin dependencies for building one instance to check it.
func (d *doctor) deps() registry.Deps {
	deps := registry.Deps{Secrets: d.secrets, HTTP: d.http, Logger: slog.New(slog.DiscardHandler)}
	if d.cfg.Slack != nil {
		deps.SlackBotToken = d.cfg.Slack.BotTokenSecret
	}

	return deps
}

// label is how a status is printed. Failures are in capitals, so they
// stand out.
func label(s Status) string {
	if s == StatusFail {
		return "FAIL"
	}

	return string(s)
}

// secretProblem says why the secret at field cannot be read, in the words
// config validation uses.
func secretProblem(field string, ref registry.SecretRef, err error) string {
	reason := err.Error()

	switch {
	case errors.Is(err, fs.ErrNotExist):
		reason = "the file does not exist"
	case errors.Is(err, fs.ErrPermission):
		reason = "permission denied"
	}

	return fmt.Sprintf("%s: cannot read secret %s/%s: %s", field, ref.Name, ref.Key, reason)
}

// secretFix is the fix for a secret that cannot be read.
func secretFix(ref registry.SecretRef) string {
	return fmt.Sprintf("create the Kubernetes Secret %s with the key %s, and list %s in the chart's secrets value so it is mounted", ref.Name, ref.Key, ref.Name)
}
