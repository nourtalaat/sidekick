package strategystore

import (
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/strategy"
)

// Skipped is a runtime strategy that is stored but not in effect, and why.
type Skipped struct {
	Name    string
	Version int
	Reason  string
}

// Reasons for skipping a runtime strategy.
const reasonShadowed = "a strategy file has the same name, and is used instead"

// Effective is the strategies in effect: the base set with the runtime
// strategies that pass the checks added to it.
type Effective struct {
	Set *strategy.Set
	// Shadowed are runtime strategies with the name of a file strategy,
	// which is used instead.
	Shadowed []Skipped
	// Invalid are stored versions that no longer pass the strategy checks,
	// for example because the checks became stricter. The strategy of a
	// lower layer is used instead, if there is one.
	Invalid []Skipped
}

// Merge returns the strategies in effect from base, the built-in and file
// strategies, and current, the current version of every runtime strategy.
// Runtime strategies are checked again; those that fail are left out and
// listed in Invalid.
func Merge(base *strategy.Set, current []Version) Effective {
	var (
		e     Effective
		valid []strategy.Strategy
	)

	for _, v := range current {
		st, problems := strategy.ParseRuntime([]byte(v.Body), v.Number)
		if len(problems) == 0 && st.Name != v.Name {
			problems = []strategy.FieldProblem{{Path: "name", Message: fmt.Sprintf("is %q but the strategy is stored as %q", st.Name, v.Name)}}
		}

		if len(problems) > 0 {
			e.Invalid = append(e.Invalid, Skipped{Name: v.Name, Version: v.Number, Reason: joinProblems(problems)})

			continue
		}

		valid = append(valid, st)
	}

	e.Set = base.WithRuntime(valid)

	for _, st := range valid {
		if won, _ := e.Set.Get(st.Name); won.Origin == strategy.File {
			e.Shadowed = append(e.Shadowed, Skipped{Name: st.Name, Version: versionNumber(st), Reason: reasonShadowed})
		}
	}

	return e
}

// versionNumber returns the number in the version of a runtime strategy.
func versionNumber(st strategy.Strategy) int {
	n, _ := strategy.RuntimeVersionNumber(st.Version)

	return n
}

// joinProblems lists problems on one line, separated by semicolons.
func joinProblems(problems []strategy.FieldProblem) string {
	parts := make([]string, len(problems))
	for i, p := range problems {
		parts[i] = p.String()
	}

	return strings.Join(parts, "; ")
}
