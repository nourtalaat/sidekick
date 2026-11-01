package strategy

import (
	"context"
	"errors"
	"maps"
)

// ErrNotFound means no strategy has the name.
var ErrNotFound = errors.New("no strategy has that name")

// Source gives the strategies in effect. A *Set is a Source that never
// changes; the strategystore cache is one that follows the runtime
// strategies.
type Source interface {
	// Current returns the strategies in effect now. The caller must not
	// change them.
	Current() *Set
	// Version returns the strategy name at the given version, which is how
	// an investigation records the one it started with. exact is false
	// when that version is no longer available, in which case the
	// strategy in effect is returned instead. The error wraps ErrNotFound
	// when no strategy has the name.
	Version(ctx context.Context, name, version string) (st Strategy, exact bool, err error)
}

var _ Source = (*Set)(nil)

// Entry is a strategy in effect and what it takes the place of.
type Entry struct {
	Strategy Strategy
	// Overrides lists the layers below the strategy that have one of the
	// same name, lowest first.
	Overrides []Origin
}

// Current returns s, which never changes.
func (s *Set) Current() *Set {
	return s
}

// Version returns the strategy in effect with the name, and whether its
// version is the one asked for.
func (s *Set) Version(_ context.Context, name, version string) (Strategy, bool, error) {
	st, ok := s.Get(name)
	if !ok {
		return Strategy{}, false, ErrNotFound
	}

	return st, st.Version == version, nil
}

// WithRuntime returns a set with the runtime strategies added. The layers
// are, from the lowest, built-in, runtime and file: a runtime strategy
// replaces the built-in one of its name, and a file strategy replaces both.
// s is not changed.
func (s *Set) WithRuntime(runtime []Strategy) *Set {
	out := &Set{builtIn: s.builtIn, file: s.file, runtime: byName(runtime)}
	out.byName = effective(out.builtIn, out.runtime, out.file)

	return out
}

// Entries returns every strategy in effect, in name order, with the layers
// each one takes the place of.
func (s *Set) Entries() []Entry {
	if s == nil {
		return nil
	}

	names := s.Names()
	out := make([]Entry, len(names))

	for i, name := range names {
		st := s.byName[name]
		out[i] = Entry{Strategy: st, Overrides: s.overridden(name, st.Origin)}
	}

	return out
}

// overridden lists the layers below winner that have a strategy named name.
func (s *Set) overridden(name string, winner Origin) []Origin {
	var out []Origin

	for _, o := range []Origin{BuiltIn, Runtime, File} {
		if o == winner {
			break
		}

		if _, ok := s.Layer(o, name); ok {
			out = append(out, o)
		}
	}

	return out
}

// Layer returns the strategy of the given layer with the name, even when a
// higher layer replaces it.
func (s *Set) Layer(origin Origin, name string) (Strategy, bool) {
	if s == nil {
		return Strategy{}, false
	}

	var layer map[string]Strategy

	switch origin {
	case BuiltIn:
		layer = s.builtIn
	case Runtime:
		layer = s.runtime
	case File:
		layer = s.file
	}

	st, ok := layer[name]

	return st, ok
}

// effective returns the strategy in effect for each name.
func effective(builtIn, runtime, file map[string]Strategy) map[string]Strategy {
	out := maps.Clone(builtIn)
	if out == nil {
		out = map[string]Strategy{}
	}

	maps.Copy(out, runtime)
	maps.Copy(out, file)

	return out
}

// byName indexes strategies by name.
func byName(list []Strategy) map[string]Strategy {
	out := make(map[string]Strategy, len(list))
	for _, st := range list {
		out[st.Name] = st
	}

	return out
}
