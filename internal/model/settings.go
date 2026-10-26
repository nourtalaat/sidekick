package model

import (
	"errors"
	"fmt"
	"time"
)

// Settings are the shared settings every connector instance accepts (MC-5).
// Each connector embeds Settings in its own config struct next to its
// provider-specific fields.
type Settings struct {
	Model       string        `yaml:"model"`
	MaxTokens   int           `yaml:"maxTokens"`
	Temperature *float64      `yaml:"temperature"`
	Timeout     time.Duration `yaml:"timeout"`
	Retries     int           `yaml:"retries"`
	Thinking    Thinking      `yaml:"thinking"`
	Effort      Effort        `yaml:"effort"`
}

// Validate checks the settings against what the configured model supports.
// Errors name the field so config validation can point at it (CFG-5).
func (s Settings) Validate(caps Capabilities) error {
	var errs []error
	if s.Model == "" {
		errs = append(errs, errors.New("model: required"))
	}

	if s.MaxTokens < 0 || (caps.MaxOutputTokens > 0 && s.MaxTokens > caps.MaxOutputTokens) {
		errs = append(errs, fmt.Errorf("maxTokens: %d is outside 0..%d", s.MaxTokens, caps.MaxOutputTokens))
	}

	if !caps.SupportsThinking(s.Thinking.Mode) {
		errs = append(errs, fmt.Errorf("thinking.mode: %q is not supported by %s", s.Thinking.Mode, s.Model))
	}

	if s.Thinking.Mode != ThinkingEnabled && s.Thinking.BudgetTokens != 0 {
		errs = append(errs, errors.New("thinking.budgetTokens: only valid with mode enabled"))
	}

	if !caps.SupportsEffort(s.Effort) {
		errs = append(errs, fmt.Errorf("effort: %q is not supported by %s", s.Effort, s.Model))
	}

	if s.Retries < 0 {
		errs = append(errs, errors.New("retries: must not be negative"))
	}

	return errors.Join(errs...)
}
