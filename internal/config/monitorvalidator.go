package config

import (
	"slices"

	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// Defaults of the validator section.
const (
	// DefaultValidatorURL is where the chart's validator sidecar listens.
	DefaultValidatorURL = validateclient.DefaultURL
	// ValidatorModeSidecar is the default mode: a validator in each
	// Sidekick pod that Sidekick calls over HTTP.
	ValidatorModeSidecar ValidatorMode = "sidecar"
	// ValidatorModeJob is the mode that runs each validation as a
	// Kubernetes Job. Sidekick cannot use it yet.
	ValidatorModeJob ValidatorMode = "job"
)

// ValidatorMode says where the monitor validator runs.
type ValidatorMode string

// knownValidatorModes are the values of validator.mode.
var knownValidatorModes = []ValidatorMode{ValidatorModeSidecar, ValidatorModeJob}

// MonitorValidator tells Sidekick how to reach the monitor validator, which
// checks the files of a pull request action before the pull request opens
// (docs/validator.md). The chart's validator values set up the validator
// itself; this section sets how Sidekick calls it.
type MonitorValidator struct {
	// Mode says where validations run. Only sidecar works for now.
	Mode ValidatorMode `yaml:"mode"`
	// URL is the validator's address in sidecar mode.
	URL string `yaml:"url"`
	// AllowDownloads mirrors the chart value of the same name, so one block
	// can serve both. Sidekick does not read it.
	AllowDownloads bool `yaml:"allowDownloads"`
}

func (m *MonitorValidator) setDefaults() {
	m.Mode = ValidatorModeSidecar
	m.URL = DefaultValidatorURL
}

func (v *validator) checkMonitorValidator() {
	m := v.cfg.Validator

	if !slices.Contains(knownValidatorModes, m.Mode) {
		v.errs.add("validator.mode", "expected sidecar or job, got %q%s", m.Mode, didYouMean(string(m.Mode), asStrings(knownValidatorModes)))
	}

	// TODO: [SK-99] Run validations as Kubernetes Jobs in job mode.
	if m.Mode == ValidatorModeJob {
		v.errs.add("validator.mode", "job mode is not available yet; use sidecar")
	}

	if !isHTTPURL(m.URL) {
		v.errs.add("validator.url", "expected an absolute http or https URL such as %s, got %q", DefaultValidatorURL, m.URL)
	}
}
