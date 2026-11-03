package action

import "fmt"

// Bounds of the revision limit of a rollback action.
const (
	// DefaultMaxRevisionsBack is the limit when maxRevisionsBack is not set.
	DefaultMaxRevisionsBack = 1
	// MaxRevisionsBackCeiling is the highest value maxRevisionsBack may have.
	MaxRevisionsBackCeiling = 10
)

// RevisionLimit is embedded, inline, in the config struct of a rollback
// action, beside ReadWriteGate:
//
//	action.RevisionLimit `yaml:",inline"`
//
// It adds one key, maxRevisionsBack: how many revisions back the model may
// ask to go. Unset means DefaultMaxRevisionsBack. A value written in the
// config must be from 1 to MaxRevisionsBackCeiling, so an operator who writes
// 0 gets an error instead of one revision back.
type RevisionLimit struct {
	MaxRevisionsBack *int `yaml:"maxRevisionsBack"`
}

// Max is the most revisions back a request may ask for.
func (l RevisionLimit) Max() int {
	if l.MaxRevisionsBack == nil {
		return DefaultMaxRevisionsBack
	}

	return *l.MaxRevisionsBack
}

// CheckConfig returns an error that starts with "maxRevisionsBack:" when the
// value is set and is not from 1 to MaxRevisionsBackCeiling. Call it from the
// Validate of the config.
func (l RevisionLimit) CheckConfig() error {
	if l.MaxRevisionsBack == nil {
		return nil
	}

	if v := *l.MaxRevisionsBack; v < 1 || v > MaxRevisionsBackCeiling {
		return fmt.Errorf("maxRevisionsBack: %d must be from 1 to %d", v, MaxRevisionsBackCeiling)
	}

	return nil
}

// CheckRequest checks a request to go back the given number of revisions. It
// wraps ErrLimit when the number is above Max, and ErrParams when it is below
// 1. Call it from Validate, so it runs when the model proposes and again when
// the proposal runs.
func (l RevisionLimit) CheckRequest(back int) error {
	if back < 1 {
		return fmt.Errorf("%w: %d revisions back is not a number of revisions", ErrParams, back)
	}

	if limit := l.Max(); back > limit {
		return fmt.Errorf("%d revisions back is more than maxRevisionsBack allows (%d): %w", back, limit, ErrLimit)
	}

	return nil
}
