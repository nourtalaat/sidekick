package bench

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrInvalidSettings is wrapped by every error about bench.yaml, so the
// command can exit with its bad input code.
var ErrInvalidSettings = errors.New("invalid benchmark settings")

// Settings are the settings of bench.yaml.
type Settings struct {
	// Weights weigh the parts of the overall score. Nil means
	// DefaultWeights.
	Weights *Weights `yaml:"weights"`
	// Thresholds are the scores a run must reach.
	Thresholds Thresholds `yaml:"thresholds"`
}

// LoadSettings reads and checks the bench.yaml at path. An empty path
// gives the defaults. Problems give one error that wraps
// ErrInvalidSettings and lists each one as "<file>: <field>: <problem>".
func LoadSettings(path string) (Settings, error) {
	if path == "" {
		return Settings{}, nil
	}

	raw, err := os.ReadFile(path) //nolint:gosec // The user chooses the settings file.
	if err != nil {
		return Settings{}, fmt.Errorf("%w: %w", ErrInvalidSettings, err)
	}

	var s Settings

	problems := decodeStrict(raw, &s)
	if len(problems) == 0 {
		problems = s.validate()
	}

	if len(problems) > 0 {
		return Settings{}, fmt.Errorf("%w:\n%s: %s", ErrInvalidSettings, path, strings.Join(problems, "\n"+path+": "))
	}

	return s, nil
}

// ScoreWeights returns the weights of the overall score.
func (s Settings) ScoreWeights() Weights {
	if s.Weights == nil {
		return DefaultWeights
	}

	return *s.Weights
}

// validate returns the problems of the settings as "<field>: <problem>".
func (s Settings) validate() []string {
	var problems []string

	if w := s.Weights; w != nil {
		for _, part := range []struct {
			key    string
			weight float64
		}{
			{"verdict", w.Verdict},
			{"rootCause", w.RootCause},
			{"evidence", w.Evidence},
		} {
			if part.weight < 0 {
				problems = append(problems, fmt.Sprintf("weights.%s: must not be negative", part.key))
			}
		}

		if w.Verdict+w.RootCause+w.Evidence <= 0 {
			problems = append(problems, "weights: at least one weight must be more than 0")
		}
	}

	return append(problems, s.Thresholds.validate()...)
}
