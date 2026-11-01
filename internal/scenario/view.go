package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/scrub"
	"github.com/nourtalaat/sidekick/internal/source/mock"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Notes shown next to a pair whose original is not shown.
const (
	noteGone    = "The original is no longer kept."
	noteChanged = "The fixtures were changed in the editor, so the original is not matched to them."
)

// jsonIndent is how the pairs indent JSON.
const jsonIndent = "  "

// Pair is one part of the scenario next to the original it was made from.
type Pair struct {
	// Ref names the part, for example "sources.signoz.fixtures[0].responses[1].text".
	Ref   string
	Label string
	// Original is what the investigation recorded, redacted when it was
	// stored. It is nil when it is not shown; Note says why.
	Original *string
	Cleaned  string
	Note     string
}

// Detail is a scenario and what a reviewer needs to check it.
type Detail struct {
	Scenario db.Scenario
	// Notes are what the exporter skipped, merged or dropped.
	Notes []Note
	// Candidates are places the cleaner kept that look like IDs.
	Candidates []scrub.Candidate
	// Pairs put each part of the scenario next to its original.
	Pairs []Pair
}

// Detail returns a scenario with its notes, the places the cleaner could
// not classify, and the pairs of cleaned part and original. It reads the
// originals from the investigation's event and steps while they are kept.
func (s *Service) Detail(ctx context.Context, id string) (Detail, error) {
	q := db.New(s.pool)

	row, err := q.GetScenario(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ErrNotFound
	}

	if err != nil {
		return Detail{}, fmt.Errorf("read scenario %s: %w", id, err)
	}

	out := Detail{Scenario: row}

	if err := json.Unmarshal(row.Notes, &out.Notes); err != nil {
		return Detail{}, fmt.Errorf("decode the notes of scenario %s: %w", id, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(row.Body), &doc); err != nil {
		return Detail{}, fmt.Errorf("read the file of scenario %s: %w", id, err)
	}

	out.Candidates = slices.DeleteFunc(s.cleaner.Candidates(&doc), func(c scrub.Candidate) bool { return c.Path == "id" })

	sc, err := bench.ParseScenario([]byte(row.Body))
	if err != nil {
		// A reviewer cannot have saved this, but show what there is.
		return out, nil
	}

	var provenance []Provenance
	if err := json.Unmarshal(row.Provenance, &provenance); err != nil {
		return Detail{}, fmt.Errorf("decode the provenance of scenario %s: %w", id, err)
	}

	originals, err := s.originals(ctx, q, row.FromInvestigation)
	if err != nil {
		return Detail{}, fmt.Errorf("read the originals: %w", err)
	}

	out.Pairs = pairsOf(&doc, sc, provenance, originals)

	return out, nil
}

// originals is what the investigation recorded and the scenario was made
// from, when it is still kept.
type originals struct {
	// payload is the redacted original payload of the first alert.
	payload []byte
	cause   string
	// calls are the recorded source calls, by step sequence number.
	calls map[int32]investigator.RecordedCall
}

// originals reads the originals of an investigation. Anything that is gone
// is left empty.
func (s *Service) originals(ctx context.Context, q *db.Queries, id *uuid.UUID) (originals, error) {
	out := originals{calls: map[int32]investigator.RecordedCall{}}
	if id == nil {
		return out, nil
	}

	inv, err := q.GetInvestigation(ctx, *id)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}

	if err != nil {
		return originals{}, fmt.Errorf("read the investigation: %w", err)
	}

	out.cause = confirmedCause(inv)

	ev, err := q.GetFirstAlertEventOfGroup(ctx, inv.GroupID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return originals{}, fmt.Errorf("read the first alert: %w", err)
	}

	out.payload = ev.Raw

	steps, err := q.ListSteps(ctx, inv.ID)
	if err != nil {
		return originals{}, fmt.Errorf("read the steps: %w", err)
	}

	calls, err := investigator.RecordedCalls(steps)
	if err != nil {
		return originals{}, fmt.Errorf("read the source calls: %w", err)
	}

	for _, c := range calls {
		out.calls[c.Seq] = c
	}

	return out, nil
}

// pairsOf lines up each cleaned part of the scenario with its original.
func pairsOf(doc *yaml.Node, sc bench.Scenario, provenance []Provenance, orig originals) []Pair {
	var pairs []Pair

	payload := child(child(doc, "event"), "payload")

	pairs = append(pairs, Pair{
		Ref: "event.payload", Label: "Event payload", Original: prettyJSON(orig.payload), Cleaned: nodeJSON(payload),
		Note: noteIf(len(orig.payload) == 0, noteGone),
	})

	for _, name := range sc.SourceNames() {
		pairs = append(pairs, fixturePairs(doc, name, sc.Sources[name], provenance, orig)...)
	}

	cause := orig.cause

	pairs = append(pairs, Pair{
		Ref: "expected.rootCause.summary", Label: "Expected cause", Original: textIf(cause != "", cause),
		Cleaned: sc.Expected.RootCause.Summary, Note: noteIf(cause == "", noteGone),
	})

	return pairs
}

// fixturePairs lines up the fixtures of one source: the call each one
// matches, and each answer, with the recorded call.
func fixturePairs(doc *yaml.Node, name string, cfg mock.Config, provenance []Provenance, orig originals) []Pair {
	var pairs []Pair

	fixtures := child(child(child(doc, "sources"), name), "fixtures")
	changed := fixtureCount(provenance, name) != len(cfg.Fixtures)

	for i, f := range cfg.Fixtures {
		ref := fmt.Sprintf("sources.%s.fixtures[%d]", name, i)
		label := name + " " + f.Tool

		call, hasCall := recordedAt(provenance, orig, name, i, 0)
		note := noteIf(changed, noteChanged)

		if !changed && !hasCall {
			note = noteGone
		}

		var input *string
		if hasCall && !changed {
			input = prettyJSON(call.Input)
		}

		var match *yaml.Node
		if fixtures != nil && i < len(fixtures.Content) {
			match = child(fixtures.Content[i], "match")
		}

		pairs = append(pairs, Pair{Ref: ref + ".match", Label: label + ": call arguments and what the fixture matches on", Original: input, Cleaned: nodeJSON(match), Note: note})

		answers := f.Responses
		if f.Respond != nil {
			answers = []mock.Response{*f.Respond}
		}

		for j, a := range answers {
			answerRef := ref + ".respond.text"
			if f.Respond == nil {
				answerRef = fmt.Sprintf("%s.responses[%d].text", ref, j)
			}

			answer, ok := recordedAt(provenance, orig, name, i, j)

			var original *string
			if ok && !changed {
				original = &answer.Text
			}

			pairs = append(pairs, Pair{
				Ref: answerRef, Label: fmt.Sprintf("%s: answer %d", label, j+1), Original: original, Cleaned: a.Text,
				Note: answerNote(changed, ok),
			})
		}
	}

	return pairs
}

// recordedAt returns the recorded call a response of a fixture came from.
func recordedAt(provenance []Provenance, orig originals, source string, fixture, response int) (investigator.RecordedCall, bool) {
	for _, p := range provenance {
		if p.Source == source && p.Fixture == fixture && p.Response == response {
			c, ok := orig.calls[p.Seq]

			return c, ok
		}
	}

	return investigator.RecordedCall{}, false
}

// fixtureCount returns how many fixtures the provenance says a source had.
func fixtureCount(provenance []Provenance, source string) int {
	n := 0

	for _, p := range provenance {
		if p.Source == source {
			n = max(n, p.Fixture+1)
		}
	}

	return n
}

// prettyJSON indents raw JSON, or returns nil for nothing.
func prettyJSON(raw []byte) *string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", jsonIndent); err != nil {
		text := string(raw)

		return &text
	}

	text := buf.String()

	return &text
}

// nodeJSON writes a node as indented JSON, keeping the order of the keys.
func nodeJSON(n *yaml.Node) string {
	if n == nil {
		return ""
	}

	var compact bytes.Buffer

	writeJSON(&compact, content(n))

	var out bytes.Buffer
	if err := json.Indent(&out, compact.Bytes(), "", jsonIndent); err != nil {
		return compact.String()
	}

	return out.String()
}

// writeJSON writes a node as compact JSON.
func writeJSON(b *bytes.Buffer, n *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode:
		b.WriteByte('{')

		for i := 0; i+1 < len(n.Content); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}

			key, _ := json.Marshal(n.Content[i].Value)
			b.Write(key)
			b.WriteByte(':')
			writeJSON(b, n.Content[i+1])
		}

		b.WriteByte('}')
	case yaml.SequenceNode:
		b.WriteByte('[')

		for i, c := range n.Content {
			if i > 0 {
				b.WriteByte(',')
			}

			writeJSON(b, c)
		}

		b.WriteByte(']')
	case yaml.ScalarNode:
		writeScalar(b, n)
	default:
		b.WriteString("null")
	}
}

// writeScalar writes a scalar node as a JSON value.
func writeScalar(b *bytes.Buffer, n *yaml.Node) {
	switch n.Tag {
	case "!!int", "!!float", "!!bool":
		if _, err := strconv.ParseFloat(n.Value, 64); err == nil || n.Tag == "!!bool" {
			b.WriteString(n.Value)

			return
		}
	case "!!null":
		b.WriteString("null")

		return
	}

	text, _ := json.Marshal(n.Value)
	b.Write(text)
}

// noteIf returns note when cond holds, and "" otherwise.
func noteIf(cond bool, note string) string {
	if cond {
		return note
	}

	return ""
}

// answerNote says why the original of an answer is not shown.
func answerNote(changed, found bool) string {
	switch {
	case changed:
		return noteChanged
	case !found:
		return noteGone
	}

	return ""
}

// textIf returns a pointer to text when cond holds, and nil otherwise.
func textIf(cond bool, text string) *string {
	if !cond {
		return nil
	}

	return &text
}
