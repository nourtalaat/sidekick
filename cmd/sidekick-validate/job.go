package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/validate"
)

// defaultTerminationLog is where Kubernetes reads a container's
// termination message from.
const defaultTerminationLog = "/dev/termination-log"

// jobError is what job mode prints when the validation could not run.
type jobError struct {
	Error string `json:"error"`
}

// newJobCmd builds `sidekick-validate job`, the Kubernetes Job mode.
func newJobCmd() *cobra.Command {
	var (
		tools          toolFlags
		requestPath    string
		terminationLog string
	)

	cmd := &cobra.Command{
		Use:   "job",
		Short: "Run one validation from a request file and print the result",
		Long: `Run one validation. The request is read from --request, in the same
JSON form POST /validate takes. The result is printed on standard output
and written to the termination log, cut down to the 4096 bytes Kubernetes
keeps there, with "truncated": true when anything was left out.

The exit status is 0 when the validation ran, whether the files passed or
not, and 1 when it could not run; then {"error": "..."} is printed and
written instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runJob(cmd.Context(), &tools, requestPath, cmd.OutOrStdout(), terminationLog)
		},
	}

	tools.add(cmd)
	cmd.Flags().StringVar(&requestPath, "request", "", "the request file (required)")
	cmd.Flags().StringVar(&terminationLog, "termination-log", defaultTerminationLog,
		"where to write the result for Kubernetes; empty for nowhere")

	if err := cmd.MarkFlagRequired("request"); err != nil {
		panic(fmt.Sprintf("mark --request required: %v", err))
	}

	return cmd
}

// runJob validates the request in requestPath and reports the result, or
// the reason it could not run, on out and in the termination log.
func runJob(ctx context.Context, tools *toolFlags, requestPath string, out io.Writer, terminationLog string) error {
	log := newLogger()

	res, runErr := validateFile(ctx, tools, log, requestPath)
	if runErr != nil {
		writeJobOutput(log, out, terminationLog, jobError{Error: runErr.Error()}, nil)

		return fmt.Errorf("job: %w", runErr)
	}

	message, err := validate.TerminationMessage(res)
	if err != nil {
		log.Warn("could not fit the result in the termination message", "error", err)
	}

	writeJobOutput(log, out, terminationLog, res, message)

	return nil
}

// validateFile reads and runs one request.
func validateFile(ctx context.Context, tools *toolFlags, log *slog.Logger, requestPath string) (validate.Result, error) {
	s, err := tools.service(log)
	if err != nil {
		return validate.Result{}, fmt.Errorf("start: %w", err)
	}

	f, err := os.Open(requestPath) //nolint:gosec // The Job names the request file it mounted.
	if err != nil {
		return validate.Result{}, fmt.Errorf("open the request: %w", err)
	}
	defer func() {
		// The file is only read; a close error changes nothing.
		_ = f.Close()
	}()

	req, err := validate.DecodeRequest(io.LimitReader(f, validate.MaxRequestBytes))
	if err != nil {
		return validate.Result{}, fmt.Errorf("read %s: %w", requestPath, err)
	}

	res, err := s.Validate(ctx, req)
	if err != nil {
		return validate.Result{}, fmt.Errorf("validate: %w", err)
	}

	return res, nil
}

// writeJobOutput prints v as JSON on out and writes it, or message when it
// is set, to the termination log. Failures are logged: the exit status
// already tells whether the validation ran.
func writeJobOutput(log *slog.Logger, out io.Writer, terminationLog string, v any, message []byte) {
	raw, err := json.Marshal(v)
	if err != nil {
		log.Error("could not encode the output", "error", err)

		return
	}

	if _, err := fmt.Fprintf(out, "%s\n", raw); err != nil {
		log.Error("could not print the output", "error", err)
	}

	if terminationLog == "" {
		return
	}

	if message == nil {
		message = raw
	}

	if err := os.WriteFile(terminationLog, message, 0o600); err != nil {
		log.Warn("could not write the termination log", "path", terminationLog, "error", err)
	}
}
