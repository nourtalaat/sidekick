package source

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"time"
)

// Defaults for the shared source settings.
const (
	// DefaultTimeout bounds one tool call when a source sets no timeout.
	DefaultTimeout = 30 * time.Second
	// DefaultConcurrency is how many calls to one source may run at once
	// on one replica when the source sets no limit.
	DefaultConcurrency = 4
)

// ErrUnavailable is returned, wrapped, by a source that cannot take calls
// right now, for example an MCP server that is not connected. The guard
// reports such calls as unavailable rather than failed.
var ErrUnavailable = errors.New("source unavailable")

// ToolError is returned by Call when the tool ran but reported an error of
// its own, such as a query the server could not parse. The source itself is
// working, so the guard does not count it against the source's health, and
// it passes Message to the model as an untrusted tool error.
type ToolError struct {
	// Message is the tool's own error text. It comes from the source and is
	// untrusted.
	Message string
}

// Error returns the tool's message.
func (e *ToolError) Error() string {
	return "tool reported an error: " + e.Message
}

// Settings are the keys every source config shares. Each source embeds it
// inline in its own config struct, so tools, timeout and concurrency sit
// next to the source's own keys:
//
//	type Config struct {
//		source.Settings `yaml:",inline"`
//		URL string `yaml:"url"`
//	}
//
// The embedded struct gives the config the SourceSettings method, which
// config validation and the guard read it through.
type Settings struct {
	// Tools says which of the source's tools the model may see, and under
	// which names.
	Tools ToolSettings `yaml:"tools"`
	// Timeout bounds one tool call. Zero means DefaultTimeout.
	Timeout time.Duration `yaml:"timeout"`
	// Concurrency is how many calls may run at once on one replica. Zero
	// means DefaultConcurrency.
	Concurrency int `yaml:"concurrency"`
}

// ToolSettings filter and rename a source's tools (SRC-2).
type ToolSettings struct {
	// Filter holds the allow and deny globs, matched against the tool names
	// the source uses. Deny wins.
	Filter `yaml:",inline"`
	// Rename maps a tool name the source uses to the name the model sees
	// after the source prefix, for example to fit the 64 character limit.
	Rename map[string]string `yaml:"rename"`
	// TrustAnnotations says whether the source's own read-only hints decide
	// which tools are writing tools, as MCP tool annotations do. Nil means
	// true. When false, only the allow and deny globs decide. Sources
	// without such hints ignore it.
	TrustAnnotations *bool `yaml:"trustAnnotations"`
}

// Configured is implemented by every config struct that embeds Settings.
type Configured interface {
	SourceSettings() Settings
}

// SourceSettings returns the shared settings. Configs that embed Settings
// get it as a promoted method.
func (s Settings) SourceSettings() Settings {
	return s
}

// CallTimeout returns the timeout of one call, with the default applied.
func (s Settings) CallTimeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}

	return DefaultTimeout
}

// MaxConcurrency returns the per-replica call limit, with the default
// applied.
func (s Settings) MaxConcurrency() int {
	if s.Concurrency > 0 {
		return s.Concurrency
	}

	return DefaultConcurrency
}

// Check checks the settings of the source called sourceName without any
// network call: the globs parse, the limits are not negative, and every
// renamed tool gets a qualified name providers accept. Each error starts
// with the key path of its field, so config validation can place it.
func (s Settings) Check(sourceName string) error {
	var errs []error

	if s.Timeout < 0 {
		errs = append(errs, errors.New("timeout: must not be negative"))
	}

	if s.Concurrency < 0 {
		errs = append(errs, errors.New("concurrency: must not be negative"))
	}

	errs = append(errs, checkGlobs("tools.allow", s.Tools.Allow)...)
	errs = append(errs, checkGlobs("tools.deny", s.Tools.Deny)...)
	errs = append(errs, s.Tools.checkRename(sourceName)...)

	return errors.Join(errs...)
}

// TrustsAnnotations reports whether the source's read-only hints decide,
// which is the default.
func (t ToolSettings) TrustsAnnotations() bool {
	return t.TrustAnnotations == nil || *t.TrustAnnotations
}

// checkRename checks that every new name is a valid tool name that fits
// MaxToolNameLen with the source prefix, and that no two tools get the same
// name. It checks nothing when the source name itself is invalid, which
// config validation reports on its own.
func (t ToolSettings) checkRename(sourceName string) []error {
	if !sourceNamePattern.MatchString(sourceName) {
		return nil
	}

	var errs []error

	from := make([]string, 0, len(t.Rename))
	for k := range t.Rename {
		from = append(from, k)
	}

	slices.Sort(from)

	seen := map[string]string{}

	for _, old := range from {
		name := t.Rename[old]
		key := fmt.Sprintf("tools.rename.%s", old)

		if _, err := QualifiedName(sourceName, name); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))

			continue
		}

		if prev, ok := seen[name]; ok {
			errs = append(errs, fmt.Errorf("%s: %q is also the new name of %q", key, name, prev))

			continue
		}

		seen[name] = old
	}

	return errs
}

// checkGlobs checks that every pattern is valid path.Match syntax.
func checkGlobs(key string, patterns []string) []error {
	var errs []error

	for i, p := range patterns {
		if _, err := path.Match(p, ""); err != nil {
			errs = append(errs, fmt.Errorf("%s[%d]: invalid glob %q: %w", key, i, p, err))
		}
	}

	return errs
}
