package config

import (
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/nourtalaat/sidekick/internal/validateclient"
	"github.com/nourtalaat/sidekick/internal/validateclient/validatejob"
)

// Defaults of the validator section.
const (
	// DefaultValidatorURL is where the chart's validator sidecar listens.
	DefaultValidatorURL = validateclient.DefaultURL
	// ValidatorModeSidecar is the default mode: a validator in each
	// Sidekick pod that Sidekick calls over HTTP.
	ValidatorModeSidecar ValidatorMode = "sidecar"
	// ValidatorModeJob is the mode that runs each validation as a
	// Kubernetes Job.
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
	// Mode says where validations run.
	Mode ValidatorMode `yaml:"mode"`
	// URL is the validator's address in sidecar mode.
	URL string `yaml:"url"`
	// AllowDownloads mirrors the chart value of the same name, so one block
	// can serve both. In job mode it lets the Jobs download providers and
	// modules; in sidecar mode Sidekick does not read it.
	AllowDownloads bool `yaml:"allowDownloads"`
	// Job describes the Jobs of job mode. The chart fills it in.
	Job ValidatorJob `yaml:"job"`
}

// ValidatorJob describes the Job that runs one validation in job mode. The
// chart sets these from its validator values, so they stay in one place.
type ValidatorJob struct {
	// Image is the validator image. It is required in job mode.
	Image string `yaml:"image"`
	// ImagePullPolicy is Always, IfNotPresent or Never; empty means the
	// cluster's default.
	ImagePullPolicy string `yaml:"imagePullPolicy"`
	// ImagePullSecrets are the names of Secrets that give access to the
	// image.
	ImagePullSecrets []string `yaml:"imagePullSecrets"`
	// ServiceAccountName is the service account of the Job pods, which
	// should not be Sidekick's own.
	ServiceAccountName string `yaml:"serviceAccountName"`
	// Resources are the requests and limits of the validator container,
	// as Kubernetes quantities such as 100m or 1Gi.
	Resources ValidatorResources `yaml:"resources"`
	// WorkDirSizeLimit is the size limit of the directory for the files of
	// a validation, such as 2Gi. Empty means no limit.
	WorkDirSizeLimit string `yaml:"workDirSizeLimit"`
	// PodLabels are put on the Jobs, their pods and ConfigMaps. The chart's
	// NetworkPolicy for the validator selects the pods by them.
	PodLabels map[string]string `yaml:"podLabels"`
}

// ValidatorResources are resource requests and limits by resource name.
type ValidatorResources struct {
	Requests map[string]string `yaml:"requests"`
	Limits   map[string]string `yaml:"limits"`
}

// knownPullPolicies are the values of validator.job.imagePullPolicy.
var knownPullPolicies = []string{string(corev1.PullAlways), string(corev1.PullIfNotPresent), string(corev1.PullNever)}

// Validator builds the client for the validator the section describes, as
// a sidecar client or a job-mode client. The job-mode client makes no call
// to the cluster until the first validation. hc is the HTTP client of the
// sidecar client; nil means the default.
func (m *MonitorValidator) Validator(hc *http.Client, log *slog.Logger) (validateclient.Validator, error) {
	if m.Mode != ValidatorModeJob {
		return validateclient.New(m.URL, hc), nil
	}

	opts, err := m.jobOptions(log)
	if err != nil {
		return nil, fmt.Errorf("validator.job: %w", err)
	}

	return validatejob.New(opts), nil
}

// jobOptions converts the job section to the options of the job-mode client.
func (m *MonitorValidator) jobOptions(log *slog.Logger) (validatejob.Options, error) {
	j := m.Job

	requests, err := quantities(j.Resources.Requests)
	if err != nil {
		return validatejob.Options{}, fmt.Errorf("resources.requests: %w", err)
	}

	limits, err := quantities(j.Resources.Limits)
	if err != nil {
		return validatejob.Options{}, fmt.Errorf("resources.limits: %w", err)
	}

	opts := validatejob.Options{
		Image:              j.Image,
		ImagePullPolicy:    corev1.PullPolicy(j.ImagePullPolicy),
		ImagePullSecrets:   j.ImagePullSecrets,
		ServiceAccountName: j.ServiceAccountName,
		Resources:          corev1.ResourceRequirements{Requests: requests, Limits: limits},
		PodLabels:          maps.Clone(j.PodLabels),
		AllowDownloads:     m.AllowDownloads,
		Logger:             log,
	}

	if j.WorkDirSizeLimit != "" {
		size, err := resource.ParseQuantity(j.WorkDirSizeLimit)
		if err != nil {
			return validatejob.Options{}, fmt.Errorf("workDirSizeLimit: %w", err)
		}

		opts.WorkDirSize = size
	}

	return opts, nil
}

// quantities parses resource quantities by resource name.
func quantities(in map[string]string) (corev1.ResourceList, error) {
	if len(in) == 0 {
		return nil, nil
	}

	out := make(corev1.ResourceList, len(in))

	for name, value := range in {
		q, err := resource.ParseQuantity(value)
		if err != nil {
			return nil, fmt.Errorf("%s %q: %w", name, value, err)
		}

		out[corev1.ResourceName(name)] = q
	}

	return out, nil
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

	if !isHTTPURL(m.URL) {
		v.errs.add("validator.url", "expected an absolute http or https URL such as %s, got %q", DefaultValidatorURL, m.URL)
	}

	if m.Mode == ValidatorModeJob {
		v.checkValidatorJob(m.Job)
	}
}

// checkValidatorJob checks the job section, which job mode needs.
func (v *validator) checkValidatorJob(j ValidatorJob) {
	if j.Image == "" {
		v.errs.add("validator.job.image", "required in job mode; the chart sets it when validator.mode is job")
	}

	if j.ImagePullPolicy != "" && !slices.Contains(knownPullPolicies, j.ImagePullPolicy) {
		v.errs.add("validator.job.imagePullPolicy", "expected Always, IfNotPresent or Never, got %q%s", j.ImagePullPolicy, didYouMean(j.ImagePullPolicy, knownPullPolicies))
	}

	for _, kind := range []struct {
		path   string
		values map[string]string
	}{{"requests", j.Resources.Requests}, {"limits", j.Resources.Limits}} {
		if _, err := quantities(kind.values); err != nil {
			v.errs.add("validator.job.resources."+kind.path, "%v", err)
		}
	}

	if j.WorkDirSizeLimit != "" {
		if _, err := resource.ParseQuantity(j.WorkDirSizeLimit); err != nil {
			v.errs.add("validator.job.workDirSizeLimit", "expected a quantity such as 2Gi, got %q", j.WorkDirSizeLimit)
		}
	}

	for _, key := range slices.Sorted(maps.Keys(j.PodLabels)) {
		for _, msg := range validation.IsQualifiedName(key) {
			v.errs.add("validator.job.podLabels", "label %q: %s", key, msg)
		}

		for _, msg := range validation.IsValidLabelValue(j.PodLabels[key]) {
			v.errs.add("validator.job.podLabels", "label %q: value %q: %s", key, j.PodLabels[key], msg)
		}
	}
}
