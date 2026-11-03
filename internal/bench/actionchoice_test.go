package bench

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// actionChoiceTag marks the public scenarios where the model must pick one
// of several actions, or none.
const actionChoiceTag = "action-choice"

// actionTypes are the types of the real actions that the action-choice
// scenarios use, and the one parameter value that names a change.
var actionTypes = []string{
	"kubernetes.rollback", "helm.rollback", "argocd.rollback", "aws.ecs_rollback", "launchdarkly.flag", "github.revert", "turn_off",
}

func TestActionChoiceScenariosOfferAChoice(t *testing.T) {
	t.Parallel()

	for _, sc := range loadActionChoice(t) {
		t.Run(sc.ID, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, action.Approved, sc.Route.Mode)
			require.Greater(t, len(sc.Route.Actions), 1, "the route lists more than the right action")
			require.ElementsMatch(t, sc.Route.Actions, slices.Collect(maps.Keys(sc.Actions)))
			require.True(t, sc.Expected.NoProposal || len(sc.Expected.Proposals) > 0, "the scenario states its expected proposals or noProposal")
		})
	}
}

func TestActionChoiceProposalsFitTheirActions(t *testing.T) {
	t.Parallel()

	for _, sc := range loadActionChoice(t) {
		t.Run(sc.ID, func(t *testing.T) {
			t.Parallel()

			expected := slices.Concat(sc.Expected.Proposals, sc.Expected.AcceptableProposals)

			for _, p := range expected {
				properties := schemaProperties(t, sc.Actions[p.Action].Spec.Snapshot)

				for path := range p.Params {
					steps, err := parsePath(path)
					require.NoError(t, err)
					require.Contains(t, properties, steps[0].key, "action %s: the check %q names a parameter that its schema does not have", p.Action, path)
				}
			}
		})
	}
}

func TestActionChoiceScenariosDoNotNameTheAnswer(t *testing.T) {
	t.Parallel()

	for _, sc := range loadActionChoice(t) {
		t.Run(sc.ID, func(t *testing.T) {
			t.Parallel()

			forbidden := slices.Concat(slices.Collect(maps.Keys(sc.Actions)), actionTypes)

			for place, text := range visibleText(t, sc) {
				for _, word := range forbidden {
					require.NotContains(t, strings.ToLower(text), word, "%s names %q, which no real tool would show", place, word)
				}
			}
		})
	}
}

// visibleText returns the text of a scenario that the model can read, by
// where it is: the alert, the route details and every answer of a fixture.
func visibleText(t *testing.T, sc Scenario) map[string]string {
	t.Helper()

	body, err := sc.Body()
	require.NoError(t, err)

	out := map[string]string{"the alert payload": string(body)}

	for name, detail := range sc.Route.Details {
		out["route detail "+name] = detail
	}

	for name, src := range sc.Sources {
		for i, f := range src.Fixtures {
			answers := f.Responses
			if f.Respond != nil {
				answers = append(answers, *f.Respond)
			}

			for j, a := range answers {
				out[fmt.Sprintf("source %s, fixture %d (%s), answer %d", name, i, f.Tool, j)] = a.Text
			}
		}
	}

	return out
}

// schemaProperties returns the names of the parameters in the paramsSchema
// of a spec snapshot file.
func schemaProperties(t *testing.T, snapshot string) []string {
	t.Helper()

	raw, err := os.ReadFile(snapshot) //nolint:gosec // A snapshot path from a public scenario.
	require.NoError(t, err)

	var file struct {
		ParamsSchema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"paramsSchema"`
	}

	require.NoError(t, json.Unmarshal(raw, &file))

	return slices.Collect(maps.Keys(file.ParamsSchema.Properties))
}

// loadActionChoice loads the public scenarios tagged actionChoiceTag.
func loadActionChoice(t *testing.T) []Scenario {
	t.Helper()

	return slices.DeleteFunc(loadPublic(t), func(sc Scenario) bool {
		return !slices.Contains(sc.Tags, actionChoiceTag)
	})
}
