package alertmanagersilence

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/httpauth"
)

// DefaultCreatedBy is the name shown in Alertmanager as the creator of a
// silence when the config names none.
const DefaultCreatedBy = "sidekick"

// maxCreatedByBytes is the longest createdBy name.
const maxCreatedByBytes = 100

// Config is the config section of one alertmanager.silence action. It
// takes the credentials, headers and TLS keys of the Prometheus source's
// alertmanagerURL, but they are the action's own: give it a token that may
// only reach the calls this action makes, apart from the source's
// read-only one.
type Config struct {
	httpauth.Config `yaml:",inline"`

	// URL is the root of the Alertmanager API, the part before /api/v2. It
	// may hold a path, such as https://mimir.example.com/alertmanager.
	URL string `yaml:"url"`
	// CreatedBy is the name Alertmanager shows as the creator of the
	// silences. Empty means DefaultCreatedBy.
	CreatedBy string `yaml:"createdBy"`
	// Limits say which alerts the action may silence and for how long.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of an alertmanager.silence action. They are
// checked when the model proposes, again right before the silence is made,
// and, for the matchers' shape, never relaxed.
type Limits struct {
	// Scopes say which alerts may be silenced: a silence must pin every
	// label of at least one scope with an equality matcher on an allowed
	// value. At least one scope is required.
	Scopes []mute.Scope `yaml:"scopes"`
	// MaxDuration is the longest silence. Empty means
	// mute.DefaultMaxDuration; the most is mute.CeilingMaxDuration.
	MaxDuration time.Duration `yaml:"maxDuration"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if c.URL == "" {
		errs = append(errs, errors.New("url: required, for example https://alertmanager.example.com"))
	} else if err := httpauth.CheckAPIURL("url", c.URL, c.SendsCredentials()); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, c.Check(httpauth.URL{Key: "url", Raw: c.URL})...)

	if err := checkCreatedBy(c.CreatedBy); err != nil {
		errs = append(errs, err)
	}

	if err := mute.CheckScopes(c.Limits.Scopes); err != nil {
		errs = append(errs, err)
	}

	if _, err := mute.MaxDuration(c.Limits.MaxDuration); err != nil {
		errs = append(errs, fmt.Errorf("limits.maxDuration: %w", err))
	}

	return errors.Join(errs...)
}

// createdBy returns the creator name, with the default filled in.
func (c *Config) createdBy() string {
	if c.CreatedBy == "" {
		return DefaultCreatedBy
	}

	return c.CreatedBy
}

// checkCreatedBy checks that the name is short and has no control or
// format characters. Empty is fine: it means the default.
func checkCreatedBy(name string) error {
	if len(name) > maxCreatedByBytes {
		return fmt.Errorf("createdBy: longer than %d bytes", maxCreatedByBytes)
	}

	if !utf8.ValidString(name) {
		return errors.New("createdBy: not valid UTF-8 text")
	}

	if strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return errors.New("createdBy: must not hold control characters")
	}

	return nil
}
