package grafanasilence

import (
	"errors"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/alertmanagersilence"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/httpauth"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Config is the config section of one grafana.silence action.
type Config struct {
	// URL is the root of Grafana, such as https://acme.grafana.net, with the
	// sub-path if Grafana is served from one. It must be https, or http on a
	// loopback host.
	URL string `yaml:"url"`
	// TokenSecret holds a Grafana service account token. Give the action a
	// token of its own, apart from the token of any Grafana source and of
	// the grafana.alert_rule action. The token fixes the organization the
	// action works in.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// TLS is a CA to trust, or a client certificate for mutual TLS.
	TLS *httpauth.TLS `yaml:"tls"`
	// CreatedBy is the name Grafana shows as the creator of the silences.
	// Empty means alertmanagersilence.DefaultCreatedBy.
	CreatedBy string `yaml:"createdBy"`
	// Limits say which alerts the action may silence and for how long. They
	// are the limits of alertmanager.silence.
	Limits alertmanagersilence.Limits `yaml:"limits"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if c.URL == "" {
		errs = append(errs, errors.New("url: required, for example https://acme.grafana.net"))
	} else if err := httpauth.CheckAPIURL("url", c.URL, true); err != nil {
		errs = append(errs, err)
	}

	if c.TokenSecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("tokenSecret: required"))
	}

	errs = append(errs, httpauth.Config{TLS: c.TLS}.Check(httpauth.URL{Key: "url", Raw: c.URL})...)

	if err := alertmanagersilence.CheckCreatedBy(c.CreatedBy); err != nil {
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

// silencerConfig turns the config into the one the shared silencer reads:
// the token becomes the bearer token.
func (c *Config) silencerConfig() alertmanagersilence.Config {
	token := c.TokenSecret

	return alertmanagersilence.Config{
		Config:    httpauth.Config{BearerTokenSecret: &token, TLS: c.TLS},
		URL:       c.URL,
		CreatedBy: c.CreatedBy,
		Limits:    c.Limits,
	}
}
