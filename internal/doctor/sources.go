package doctor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/source"
)

// sourcePollInterval is how often a source that is still connecting is
// asked for its tools again.
const sourcePollInterval = 200 * time.Millisecond

// checkSources builds each source, waits for it to connect and list its
// tools, and reports which tools the model would see.
func (d *doctor) checkSources(ctx context.Context) []Result {
	if len(d.cfg.Sources) == 0 {
		return []Result{{Check: "sources", Status: StatusSkip, Message: "no sources"}}
	}

	results := make([]Result, 0, len(d.cfg.Sources))

	for _, name := range slices.Sorted(maps.Keys(d.cfg.Sources)) {
		results = append(results, d.checkSource(ctx, name))
	}

	return results
}

// checkSource checks one source.
func (d *doctor) checkSource(ctx context.Context, name string) Result {
	check := "source " + name
	section := d.cfg.Sources[name]

	var settings source.Settings

	if decoded, err := d.regs.Sources.Check(ctx, section.Type, name, section); err == nil {
		if configured, ok := decoded.(source.Configured); ok {
			settings = configured.SourceSettings()
		}
	}

	logs := &lastError{}
	deps := d.deps()
	deps.Logger = slog.New(logs)

	src, err := d.regs.Sources.Build(ctx, section.Type, name, section, deps)
	if err != nil {
		return Result{
			Check: check, Status: StatusFail,
			Message: "cannot set up the source: " + err.Error(),
			Fix:     "mount every secret the source names; `sidekick config validate --check-secrets -f <file>` points at the field",
		}
	}

	defer func() {
		// The source was only built to check it; a close error changes no result.
		_ = src.Close()
	}()

	list, err := d.waitForTools(ctx, src)
	if err != nil {
		message := "cannot list its tools: " + err.Error()
		if last := logs.get(); last != "" {
			message += " (last error: " + last + ")"
		}

		return Result{
			Check: check, Status: StatusFail, Message: message,
			Fix: "check the source's url, that the server is running and that Sidekick's pod can reach it; for an MCP sidecar, check its logs with `kubectl logs <pod> -c <sidecar>`",
		}
	}

	exposed, dropped := tools.Expose(name, settings, list)

	details := make([]string, 0, len(dropped))
	for _, t := range dropped {
		details = append(details, fmt.Sprintf("%s is not exposed: %s", t.Tool, dropReason(t)))
	}

	r := Result{Check: check, Status: StatusOK, Message: fmt.Sprintf("connected; %d of %d tools exposed", len(exposed), len(list)), Details: details}
	if len(exposed) == 0 {
		r.Status = StatusWarn
		r.Fix = "the model can call none of its tools; check tools.allow and tools.deny, or set tools.trustAnnotations: false if the server marks read-only tools as writing"
	}

	return r
}

// waitForTools asks src for its tools until it answers with something
// other than source.ErrUnavailable, or the wait ends.
func (d *doctor) waitForTools(ctx context.Context, src source.Source) ([]source.Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, d.sourceWait)
	defer cancel()

	ticker := time.NewTicker(sourcePollInterval)
	defer ticker.Stop()

	for {
		list, err := src.Tools(ctx)
		if !errors.Is(err, source.ErrUnavailable) {
			if err != nil {
				return nil, fmt.Errorf("list tools: %w", err)
			}

			return list, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("not connected after %s: %w", d.sourceWait, err)
		case <-ticker.C:
		}
	}
}

// lastError is a log handler that keeps the "error" attribute of the last
// warning or error a source logged, such as why it cannot connect.
type lastError struct {
	mu   sync.Mutex
	last string
}

var _ slog.Handler = (*lastError)(nil)

// Enabled implements slog.Handler: only warnings and errors are kept.
func (h *lastError) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

// Handle implements slog.Handler.
func (h *lastError) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key != "error" {
			return true
		}

		h.mu.Lock()
		h.last = a.Value.String()
		h.mu.Unlock()

		return false
	})

	return nil
}

// WithAttrs implements slog.Handler. Attributes given here never hold the
// error, so they are dropped.
func (h *lastError) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

// WithGroup implements slog.Handler.
func (h *lastError) WithGroup(string) slog.Handler {
	return h
}

func (h *lastError) get() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.last
}

// dropReason says why a tool is not exposed.
func dropReason(t tools.Dropped) string {
	if t.Detail != "" {
		return string(t.Reason) + ", " + t.Detail
	}

	return string(t.Reason)
}
