package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/validate"
)

// Where the validator image puts its tools.
const (
	defaultPromtool = "/usr/local/bin/promtool"
	defaultTofu     = "/usr/local/bin/tofu"
	defaultMirror   = "/usr/local/share/sidekick-validate/providers"
	defaultWorkDir  = "/tmp"
)

// toolFlags are the flags serve and job share.
type toolFlags struct {
	promtool       string
	tofu           string
	mirror         string
	workDir        string
	allowDownloads bool
}

// add registers the shared flags on cmd.
func (f *toolFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.promtool, "promtool", defaultPromtool, "path of promtool")
	cmd.Flags().StringVar(&f.tofu, "tofu", defaultTofu, "path of tofu")
	cmd.Flags().StringVar(&f.mirror, "mirror", defaultMirror, "directory of the OpenTofu provider mirror")
	cmd.Flags().StringVar(&f.workDir, "work-dir", defaultWorkDir, "directory for the files of running validations")
	cmd.Flags().BoolVar(&f.allowDownloads, "allow-downloads", false,
		"let OpenTofu download providers that are not in the mirror, and remote modules")
}

// service builds the validation service from the flags.
func (f *toolFlags) service(log *slog.Logger) (*validate.Service, error) {
	s, err := validate.New(validate.Options{
		Promtool:       f.promtool,
		Tofu:           f.tofu,
		Mirror:         f.mirror,
		WorkDir:        f.workDir,
		AllowDownloads: f.allowDownloads,
		Logger:         log,
	})
	if err != nil {
		return nil, fmt.Errorf("start the validator: %w", err)
	}

	return s, nil
}

// newLogger logs JSON to standard error, which leaves standard output to
// the result in job mode.
func newLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, nil))
}
