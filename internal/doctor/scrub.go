package doctor

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// minScrubLength is the shortest secret value that is replaced in results.
// Shorter values would replace ordinary words; no real token is that short.
const minScrubLength = 4

// scrubbed replaces a secret value in a result.
const scrubbed = "[secret]"

// recordingSecrets resolves secrets through inner and remembers every value
// it returned, so the results can be cleaned of them. Sources may resolve
// secrets from their own goroutines, so it is safe for concurrent use.
type recordingSecrets struct {
	inner registry.Secrets

	mu     sync.Mutex
	values []string
}

var _ registry.Secrets = (*recordingSecrets)(nil)

// Resolve implements registry.Secrets.
func (s *recordingSecrets) Resolve(ctx context.Context, ref registry.SecretRef) ([]byte, error) {
	value, err := s.inner.Resolve(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("read secret: %w", err)
	}

	s.record(strings.TrimRight(string(value), "\r\n"))

	return value, nil
}

// recordURL also records the password of a URL secret, which can show in an
// error on its own.
func (s *recordingSecrets) recordURL(raw string) {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return
	}

	if password, ok := u.User.Password(); ok {
		s.record(password)
	}
}

// scrubResult replaces every recorded secret value in r.
func (s *recordingSecrets) scrubResult(r Result) Result {
	r.Message = s.scrub(r.Message)
	r.Fix = s.scrub(r.Fix)

	details := make([]string, len(r.Details))
	for i, line := range r.Details {
		details[i] = s.scrub(line)
	}

	if r.Details != nil {
		r.Details = details
	}

	return r
}

func (s *recordingSecrets) record(value string) {
	if len(value) < minScrubLength {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.values = append(s.values, value)
}

func (s *recordingSecrets) scrub(text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, v := range s.values {
		text = strings.ReplaceAll(text, v, scrubbed)
	}

	return text
}
