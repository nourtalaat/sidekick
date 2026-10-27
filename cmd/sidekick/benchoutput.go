package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/buildinfo"
)

const (
	// configHashLength is how many hex digits of the config file's SHA-256
	// a report keeps.
	configHashLength = 12
	// ciEnv is set to true by CI systems such as GitHub Actions.
	ciEnv = "CI"
	// Who started a run, as report.json and bench_runs record it.
	triggeredByCLI = "cli"
	triggeredByCI  = "ci"
	// outputFileMode is the mode of the files bench run writes.
	outputFileMode = 0o644
)

// benchSettings loads bench.yaml at path, and the baseline report it
// names, if any.
func benchSettings(path string) (bench.Settings, *bench.JSONReport, error) {
	settings, err := bench.LoadSettings(path)
	if err != nil {
		return bench.Settings{}, nil, fmt.Errorf("load --bench-config: %w", err)
	}

	if settings.Thresholds.Baseline == "" {
		return settings, nil, nil
	}

	baseline, err := bench.ReadReport(settings.Thresholds.Baseline)
	if err != nil {
		return bench.Settings{}, nil, fmt.Errorf("read the baseline of %s: %w", path, err)
	}

	return settings, baseline, nil
}

// printBenchResults prints the spread of each scenario, the run totals and
// the thresholds.
func printBenchResults(cmd *cobra.Command, results *bench.Results, thresholds []bench.ThresholdResult) error {
	if err := results.WriteSummary(cmd.OutOrStdout()); err != nil {
		return fmt.Errorf("print the summary: %w", err)
	}

	if err := bench.WriteThresholds(cmd.OutOrStdout(), thresholds); err != nil {
		return fmt.Errorf("print the thresholds: %w", err)
	}

	return nil
}

// writeBenchOutputs writes the files of --out, --junit and --markdown that
// are set.
func writeBenchOutputs(results *bench.Results, info bench.RunInfo, thresholds []bench.ThresholdResult, flags benchFlags) error {
	if flags.out != "" {
		rep, err := results.Report(info, thresholds)
		if err != nil {
			return fmt.Errorf("build report.json: %w", err)
		}

		if err := writeFile(flags.out, rep.WriteJSON); err != nil {
			return fmt.Errorf("write --out: %w", err)
		}
	}

	if flags.junit != "" {
		err := writeFile(flags.junit, func(w io.Writer) error { return results.WriteJUnit(w, thresholds) })
		if err != nil {
			return fmt.Errorf("write --junit: %w", err)
		}
	}

	if flags.markdown != "" {
		err := writeFile(flags.markdown, func(w io.Writer) error { return results.WriteMarkdown(w, info, thresholds) })
		if err != nil {
			return fmt.Errorf("write --markdown: %w", err)
		}
	}

	return nil
}

// benchInfo returns what the run records about how it ran.
func benchInfo(runner *bench.Runner, flags benchFlags) bench.RunInfo {
	build := buildinfo.Current()

	info := runner.Info()
	info.Label = flags.label
	info.SidekickVersion = build.Version
	info.GitSHA = build.Commit
	info.ConfigHash = configHash(flags.configPath)
	info.ScenarioSet = flags.scenarios
	info.TriggeredBy = triggeredByCLI

	if os.Getenv(ciEnv) == "true" {
		info.TriggeredBy = triggeredByCI
	}

	return info
}

// configHash returns the start of the SHA-256 of the config file, or an
// empty string when it cannot be read. The file holds secret references,
// never secret values.
func configHash(path string) string {
	raw, err := os.ReadFile(path) //nolint:gosec // The user chooses the config file.
	if err != nil {
		return ""
	}

	sum := sha256.Sum256(raw)

	return hex.EncodeToString(sum[:])[:configHashLength]
}

// thresholdNames lists the names of the thresholds.
func thresholdNames(results []bench.ThresholdResult) string {
	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Name
	}

	return strings.Join(names, ", ")
}

// writeFile creates the file at path and writes it with write.
func writeFile(path string, write func(io.Writer) error) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, outputFileMode) //nolint:gosec // The user chooses the output files.
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}

	defer func() {
		if cerr := f.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close %s: %w", path, cerr))
		}
	}()

	if err := write(f); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}
