package watch

import (
	"fmt"
	"time"

	"github.com/nourtalaat/sidekick/internal/strategy"
)

// Global defaults and bounds of watcher settings, used when watch sets
// none.
const (
	DefaultInterval        = 30 * time.Second
	DefaultTimeout         = 60 * time.Minute
	DefaultPassesToRecover = 2
	DefaultMinInterval     = 10 * time.Second
	DefaultMaxInterval     = 15 * time.Minute
	DefaultMaxTimeout      = 4 * time.Hour
)

// Settings are the timing settings of a watcher: the interval between
// checks, how long the watcher may run, the passing checks in a row that
// count as recovered, and the time limit of one check. At one level of
// defaults, zero means the next level decides.
type Settings = strategy.WatcherSettings

// Level is one level of settings, with the config path it is set at, such
// as routes[0].watcherDefaults. Problems name the path of the level that
// set the value. An empty path names the fields alone, as a start_watcher
// input does.
type Level struct {
	Path     string
	Settings Settings
}

// Bounds limit the interval and timeout a watcher may resolve to. Zero
// means no bound. Path is where the bounds are set, such as watch or
// routes[0].watcherLimits.
type Bounds struct {
	Path        string
	MinInterval time.Duration
	MaxInterval time.Duration
	MaxTimeout  time.Duration
}

// Problem is a setting outside its bounds, as a config error: the path of
// the field and a message that names the bound.
type Problem struct {
	Path    string
	Message string
}

// String returns the problem as "path: message".
func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}

	return p.Path + ": " + p.Message
}

// Resolve picks each setting from the first level that sets it, from the
// most specific (a watcher's own values) to the least (the global
// defaults), and checks the result against every bounds. A setting that
// no level sets stays zero and is not checked.
func Resolve(levels []Level, bounds []Bounds) (Settings, []Problem) {
	var (
		out      Settings
		problems []Problem
	)

	interval, intervalAt := pick(levels, func(s Settings) time.Duration { return s.Interval })
	timeout, timeoutAt := pick(levels, func(s Settings) time.Duration { return s.Timeout })
	passes, passesAt := pick(levels, func(s Settings) int { return s.PassesToRecover })
	checkTimeout, checkTimeoutAt := pick(levels, func(s Settings) time.Duration { return s.CheckTimeout })

	out = Settings{Interval: interval, Timeout: timeout, PassesToRecover: passes, CheckTimeout: checkTimeout}

	if interval != 0 {
		problems = append(problems, intervalProblems(field(intervalAt, "interval"), interval, bounds)...)
	}

	if timeout != 0 {
		problems = append(problems, timeoutProblems(field(timeoutAt, "timeout"), timeout, bounds)...)
	}

	if passes < 0 {
		problems = append(problems, Problem{Path: field(passesAt, "passesToRecover"), Message: "must be at least 1"})
	}

	if checkTimeout < 0 {
		problems = append(problems, Problem{Path: field(checkTimeoutAt, "checkTimeout"), Message: "must not be negative"})
	}

	return out, problems
}

// Within checks that inner bounds sit inside outer ones, and that the
// minimum interval is not above the maximum.
func Within(inner, outer Bounds) []Problem {
	var problems []Problem

	if inner.MinInterval != 0 {
		problems = append(problems, intervalProblems(field(inner.Path, "minInterval"), inner.MinInterval, []Bounds{outer})...)
	}

	if inner.MaxInterval != 0 {
		problems = append(problems, intervalProblems(field(inner.Path, "maxInterval"), inner.MaxInterval, []Bounds{outer})...)
	}

	if inner.MaxTimeout != 0 {
		problems = append(problems, timeoutProblems(field(inner.Path, "maxTimeout"), inner.MaxTimeout, []Bounds{outer})...)
	}

	maximum := inner.MaxInterval
	if maximum == 0 {
		maximum = outer.MaxInterval
	}

	if inner.MinInterval != 0 && maximum != 0 && inner.MinInterval > maximum {
		problems = append(problems, Problem{
			Path:    field(inner.Path, "minInterval"),
			Message: fmt.Sprintf("%s is above the maximum interval %s", inner.MinInterval, maximum),
		})
	}

	return problems
}

// Complete fills settings no level set with the built-in defaults, so a
// watcher always has every value.
func Complete(s Settings) Settings {
	if s.Interval == 0 {
		s.Interval = DefaultInterval
	}

	if s.Timeout == 0 {
		s.Timeout = DefaultTimeout
	}

	if s.PassesToRecover == 0 {
		s.PassesToRecover = DefaultPassesToRecover
	}

	if s.CheckTimeout == 0 {
		s.CheckTimeout = DefaultCheckTimeout
	}

	return s
}

// intervalProblems checks an interval against the bounds.
func intervalProblems(path string, d time.Duration, bounds []Bounds) []Problem {
	var problems []Problem

	if d < 0 {
		return []Problem{{Path: path, Message: "must not be negative"}}
	}

	for _, b := range bounds {
		if b.MinInterval != 0 && d < b.MinInterval {
			problems = append(problems, Problem{Path: path, Message: fmt.Sprintf("%s is below %s %s", d, field(b.Path, "minInterval"), b.MinInterval)})
		}

		if b.MaxInterval != 0 && d > b.MaxInterval {
			problems = append(problems, Problem{Path: path, Message: fmt.Sprintf("%s is above %s %s", d, field(b.Path, "maxInterval"), b.MaxInterval)})
		}
	}

	return problems
}

// timeoutProblems checks a timeout against the bounds.
func timeoutProblems(path string, d time.Duration, bounds []Bounds) []Problem {
	var problems []Problem

	if d < 0 {
		return []Problem{{Path: path, Message: "must not be negative"}}
	}

	for _, b := range bounds {
		if b.MaxTimeout != 0 && d > b.MaxTimeout {
			problems = append(problems, Problem{Path: path, Message: fmt.Sprintf("%s is above %s %s", d, field(b.Path, "maxTimeout"), b.MaxTimeout)})
		}
	}

	return problems
}

// pick returns the value of the first level that sets one, and that
// level's path.
func pick[T comparable](levels []Level, get func(Settings) T) (T, string) {
	var zero T

	for _, l := range levels {
		if v := get(l.Settings); v != zero {
			return v, l.Path
		}
	}

	return zero, ""
}

// field joins a path and a field name.
func field(path, name string) string {
	if path == "" {
		return name
	}

	return path + "." + name
}
