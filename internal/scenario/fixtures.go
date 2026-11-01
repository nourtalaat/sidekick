package scenario

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/mock"
)

// The statuses of a recorded call, as steps.status holds them.
const (
	statusOK          = "ok"
	statusNoData      = "no_data"
	statusUnavailable = "unavailable"
)

// sidekickNoDataText is what Sidekick tells the model when a tool found no
// data and the source gave no text of its own.
const sidekickNoDataText = "The tool ran and found no data."

// snapshotDir is where the tool lists of sources are kept in a repository.
const snapshotDir = "bench/tools/"

// knownKinds are the evidence kinds a fixture may use.
var knownKinds = []source.EvidenceKind{
	source.EvidenceLogQuery, source.EvidenceMetric, source.EvidenceTrace, source.EvidenceCommit,
	source.EvidenceDeploy, source.EvidenceTicket, source.EvidenceResource, source.EvidenceIncident,
	source.EvidenceMessage, source.EvidenceDashboard, source.EvidenceOther,
}

// Provenance says which step a response of a fixture came from. It holds
// names and numbers only.
type Provenance struct {
	Source   string `json:"source"`
	Fixture  int    `json:"fixture"`
	Response int    `json:"response"`
	Seq      int32  `json:"seq"`
}

// Note is something the exporter skipped, merged or could not decide, in
// words and without any value from the investigation.
type Note struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// The kinds of note.
const (
	NoteSkippedCalls = "skipped_calls"
	NoteMergedCalls  = "merged_calls"
	NoteDroppedArgs  = "dropped_args"
	NoteUnavailable  = "unavailable_source"
	NoteNoCause      = "no_cause"
	NoteNoRubric     = "no_rubric"
)

// sourceFixtures is what the calls to one source become.
type sourceFixtures struct {
	name        string
	unavailable bool
	fixtures    []*fixtureBuild
}

// fixtureBuild is a fixture and the steps of its responses.
type fixtureBuild struct {
	tool      string
	match     matchers
	responses []mock.Response
	seqs      []int32
}

// fixturesOf turns the recorded calls into mock sources, in source name
// order, with the notes and the provenance. Calls with the same tool and the
// same loosened match share one fixture, whose responses are given in call
// order; fixtures with more matchers come first, because the first fixture
// that matches wins.
func fixturesOf(calls []investigator.RecordedCall) (map[string]mock.Config, []string, []Provenance, []Note, error) {
	bySource := map[string]*sourceFixtures{}
	skipped := map[string]int{}
	merged := map[string]int{}
	dropped := map[string][]string{}

	for _, call := range calls {
		src := bySource[call.Source]
		if src == nil {
			src = &sourceFixtures{name: call.Source}
			bySource[call.Source] = src
		}

		switch call.Status {
		case statusOK, statusNoData:
		case statusUnavailable:
			src.unavailable = true

			continue
		default:
			skipped[call.Status]++

			continue
		}

		m, err := loosen(call.Input)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("call %s: %w", call.CallID, err)
		}

		label := call.Source + " " + call.Tool
		dropped[label] = mergeNames(dropped[label], m.dropped)

		fb := src.find(call.Tool, m)
		if fb == nil {
			fb = &fixtureBuild{tool: call.Tool, match: m}
			src.fixtures = append(src.fixtures, fb)
		} else {
			merged[label]++
		}

		fb.responses = append(fb.responses, responseOf(call))
		fb.seqs = append(fb.seqs, call.Seq)
	}

	return assemble(bySource, skipped, merged, dropped)
}

// find returns the fixture for the tool and the matchers, or nil.
func (s *sourceFixtures) find(tool string, m matchers) *fixtureBuild {
	for _, fb := range s.fixtures {
		if fb.tool == tool && fb.match.signature() == m.signature() {
			return fb
		}
	}

	return nil
}

// assemble builds the mock configs, the names of the unavailable sources,
// the provenance and the notes.
func assemble(bySource map[string]*sourceFixtures, skipped, merged map[string]int, dropped map[string][]string) (map[string]mock.Config, []string, []Provenance, []Note, error) {
	configs := map[string]mock.Config{}

	var (
		unavailable []string
		provenance  []Provenance
	)

	for _, name := range slices.Sorted(maps.Keys(bySource)) {
		src := bySource[name]

		if len(src.fixtures) == 0 {
			configs[name] = mock.Config{Unavailable: src.unavailable}

			if src.unavailable {
				unavailable = append(unavailable, name)
			}

			continue
		}

		sort.SliceStable(src.fixtures, func(i, j int) bool { return len(src.fixtures[i].match.args) > len(src.fixtures[j].match.args) })

		cfg := mock.Config{}

		for i, fb := range src.fixtures {
			cfg.Fixtures = append(cfg.Fixtures, fb.fixture())

			for j, seq := range fb.seqs {
				provenance = append(provenance, Provenance{Source: name, Fixture: i, Response: j, Seq: seq})
			}
		}

		configs[name] = cfg
	}

	if len(configs) == 0 {
		return nil, nil, nil, nil, ErrNoSourceCalls
	}

	return configs, unavailable, provenance, notesOf(unavailable, skipped, merged, dropped), nil
}

// fixture returns the mock fixture.
func (fb *fixtureBuild) fixture() mock.Fixture {
	f := mock.Fixture{Tool: fb.tool}

	if len(fb.match.args) > 0 {
		f.Match = mock.Match{Args: fb.match.args}
	}

	if len(fb.responses) == 1 {
		f.Respond = &fb.responses[0]
	} else {
		f.Responses = fb.responses
	}

	return f
}

// responseOf returns the answer a call got.
func responseOf(call investigator.RecordedCall) mock.Response {
	r := mock.Response{Text: call.Text}

	if call.Status == statusNoData {
		r.NoData = true

		if call.Text == sidekickNoDataText {
			r.Text = ""
		}
	}

	for _, e := range call.Evidence {
		kind := e.Kind
		if !slices.Contains(knownKinds, kind) {
			kind = source.EvidenceOther
		}

		title := e.Title
		if title == "" {
			title = e.URL
		}

		if title == "" {
			continue
		}

		r.Evidence = append(r.Evidence, mock.Evidence{Kind: kind, Title: title, URL: e.URL})
	}

	return r
}

// notesOf describes what the exporter skipped, merged and dropped.
func notesOf(unavailable []string, skipped, merged map[string]int, dropped map[string][]string) []Note {
	var notes []Note

	if len(skipped) > 0 {
		parts := make([]string, 0, len(skipped))
		for _, status := range slices.Sorted(maps.Keys(skipped)) {
			parts = append(parts, fmt.Sprintf("%d with status %s", skipped[status], status))
		}

		notes = append(notes, Note{Kind: NoteSkippedCalls, Text: "Calls left out because they have no answer to replay: " + strings.Join(parts, ", ") + "."})
	}

	for _, label := range slices.Sorted(maps.Keys(merged)) {
		notes = append(notes, Note{Kind: NoteMergedCalls, Text: fmt.Sprintf("%s: %d more call(s) had the same loosened match, so one fixture answers them in call order.", label, merged[label])})
	}

	for _, label := range slices.Sorted(maps.Keys(dropped)) {
		if len(dropped[label]) > 0 {
			notes = append(notes, Note{Kind: NoteDroppedArgs, Text: fmt.Sprintf("%s: the fixture does not match on %s.", label, strings.Join(dropped[label], ", "))})
		}
	}

	for _, name := range unavailable {
		notes = append(notes, Note{Kind: NoteUnavailable, Text: fmt.Sprintf("%s was unavailable in the investigation, so the scenario marks it unavailable and expects the report to say so.", name)})
	}

	return notes
}

// mergeNames adds the names in more to names, without repeats, sorted.
func mergeNames(names, more []string) []string {
	for _, n := range more {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}

	sort.Strings(names)

	return names
}
