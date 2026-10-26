package config

import (
	"context"
	"errors"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// ErrEmptyHashKey is returned by RedactOptions when the hash key secret is
// an empty file.
var ErrEmptyHashKey = errors.New("the redaction hash key is empty")

// RedactOptions returns the options for redact.New. It reads the hash key
// through s with its bytes unchanged, because the key is binary. The logger
// is left for the caller to set.
func (r Redaction) RedactOptions(ctx context.Context, s registry.Secrets) (redact.Options, error) {
	opts := redact.Options{BuiltIn: r.BuiltIn}

	for _, c := range r.Custom {
		opts.Custom = append(opts.Custom, redact.Rule{Name: c.Name, Pattern: c.Pattern})
	}

	if r.HashKeySecret == nil {
		return opts, nil
	}

	ref := *r.HashKeySecret

	key, err := s.Resolve(ctx, ref)
	if err != nil {
		return redact.Options{}, fmt.Errorf("redaction.hashKeySecret: %w", err)
	}

	if len(key) == 0 {
		return redact.Options{}, fmt.Errorf("redaction.hashKeySecret %s/%s: %w", ref.Name, ref.Key, ErrEmptyHashKey)
	}

	opts.HashKey = key

	return opts, nil
}
