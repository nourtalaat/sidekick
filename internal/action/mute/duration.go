package mute

import (
	"fmt"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
)

// The limits on how long a mute lasts.
const (
	// MinDuration is the shortest mute.
	MinDuration = 5 * time.Minute
	// DefaultMaxDuration is the longest mute when the config sets no limit.
	DefaultMaxDuration = 2 * time.Hour
	// CeilingMaxDuration is the longest value a config may set as its
	// limit.
	CeilingMaxDuration = 24 * time.Hour
)

// MaxDuration returns the longest mute an action allows: configured, or
// DefaultMaxDuration when it is zero. It fails for a value below
// MinDuration or above CeilingMaxDuration.
func MaxDuration(configured time.Duration) (time.Duration, error) {
	switch {
	case configured == 0:
		return DefaultMaxDuration, nil
	case configured < MinDuration:
		return 0, fmt.Errorf("%s is below %s, the shortest mute", configured, FormatDuration(MinDuration))
	case configured > CeilingMaxDuration:
		return 0, fmt.Errorf("%s is above %s, the longest limit a mute may have", configured, FormatDuration(CeilingMaxDuration))
	default:
		return configured, nil
	}
}

// ParseDuration reads raw as a Go duration, such as 30m or 2h, and checks
// that it is between MinDuration and limit. Text that is not a duration
// wraps action.ErrParams; a duration out of range wraps action.ErrLimit.
func ParseDuration(raw string, limit time.Duration) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("duration %q is not a Go duration such as 30m or 2h: %w: %w", raw, action.ErrParams, err)
	}

	switch {
	case d < MinDuration:
		return 0, fmt.Errorf("duration %s is below %s, the shortest mute: %w", FormatDuration(d), FormatDuration(MinDuration), action.ErrLimit)
	case d > limit:
		return 0, fmt.Errorf("duration %s is above %s, the longest this action allows: %w", FormatDuration(d), FormatDuration(limit), action.ErrLimit)
	default:
		return d, nil
	}
}

// FormatDuration writes d the short way: 2h, 1h30m, 5m.
func FormatDuration(d time.Duration) string {
	s := d.String()

	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}

	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}

	return s
}
