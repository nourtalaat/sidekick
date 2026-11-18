package bench

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// actionChoiceTag marks the public scenarios where the model must pick one
// of several actions, or none.
const actionChoiceTag = "action-choice"

// minDistractors is how many actions the route of the no-proposal scenario
// lists, so that proposing nothing is a choice among several.
const minDistractors = 3

// silenceScenarioID is the scenario whose silence the duration and matcher
// checks are tested against.
const silenceScenarioID = "silence-batch-job-maintenance-window"

// actionTypes are the types of the real actions that the action-choice
// scenarios use, and the one parameter value that names a change.
var actionTypes = []string{
	"kubernetes.rollback", "helm.rollback", "argocd.rollback", "aws.ecs_rollback", "launchdarkly.flag", "github.revert", "gitlab.revert", "alertmanager.silence", "turn_off",
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

func TestActionChoiceScenariosCoverTheActions(t *testing.T) {
	t.Parallel()

	expecting := map[string]int{}

	var noProposal []Scenario

	for _, sc := range loadActionChoice(t) {
		if sc.Expected.NoProposal {
			noProposal = append(noProposal, sc)
		}

		for _, p := range sc.Expected.Proposals {
			expecting[filepath.Base(sc.Actions[p.Action].Spec.Snapshot)]++
		}
	}

	for _, snapshot := range []string{
		"kubernetes.rollback.json", "helm.rollback.json", "argocd.rollback.json", "aws.ecs_rollback.json", "launchdarkly.flag.json",
		"github.revert.json", "gitlab.revert.json", "alertmanager.silence.json",
	} {
		require.Equal(t, 1, expecting[snapshot], "one scenario expects a proposal of %s", snapshot)
	}

	require.Len(t, noProposal, 2, "two scenarios expect no proposal")

	for _, sc := range noProposal {
		require.GreaterOrEqual(t, len(sc.Route.Actions), minDistractors, "the route of %s lists several actions that are wrong", sc.ID)
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

func TestSilenceScenarioChecksTheSilence(t *testing.T) {
	t.Parallel()

	sc := loadActionChoiceByID(t, silenceScenarioID)
	require.Len(t, sc.Expected.Proposals, 1)

	equal := func(name, value string) map[string]any {
		return map[string]any{"name": name, "value": value, "isRegex": false, "isEqual": true}
	}

	rightMatchers := []any{equal("alertname", "BatchJobFailed"), equal("job", "invoice-export"), equal("env", "prod")}

	tests := []struct {
		name     string
		matchers []any
		duration string
		want     bool
	}{
		{name: "covers the window", matchers: rightMatchers, duration: "3h", want: true},
		{name: "exactly the rest of the window", matchers: rightMatchers, duration: "2h25m", want: true},
		{name: "hours with a fraction", matchers: rightMatchers, duration: "2.5h", want: true},
		{name: "in minutes", matchers: rightMatchers, duration: "180m", want: true},
		{name: "the longest the action allows", matchers: rightMatchers, duration: "6h", want: true},
		{name: "ends before the window does", matchers: rightMatchers, duration: "2h24m", want: false},
		{name: "much too short", matchers: rightMatchers, duration: "30m", want: false},
		{name: "above what the action allows", matchers: rightMatchers, duration: "6h1m", want: false},
		{name: "far above what the action allows", matchers: rightMatchers, duration: "12h", want: false},
		{name: "without the job", matchers: []any{equal("alertname", "BatchJobFailed"), equal("env", "prod")}, duration: "3h", want: false},
		{name: "without the alert name", matchers: []any{equal("job", "invoice-export"), equal("env", "prod")}, duration: "3h", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			params := map[string]any{"matchers": tt.matchers, "duration": tt.duration, "comment": "Planned maintenance"}

			require.Equal(t, tt.want, sc.Expected.Proposals[0].fits("alertmanager-silence", params))
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

// loadActionChoiceByID returns the action choice scenario with the ID.
func loadActionChoiceByID(t *testing.T, id string) Scenario {
	t.Helper()

	for _, sc := range loadActionChoice(t) {
		if sc.ID == id {
			return sc
		}
	}

	require.FailNow(t, "no action choice scenario", "ID %q", id)

	return Scenario{}
}

// loadActionChoice loads the public scenarios tagged actionChoiceTag.
func loadActionChoice(t *testing.T) []Scenario {
	t.Helper()

	return slices.DeleteFunc(loadPublic(t), func(sc Scenario) bool {
		return !slices.Contains(sc.Tags, actionChoiceTag)
	})
}
