package prometheusrule

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
)

// Parts of a rule path: the group, the rule, and then the field.
const (
	// ruleDepth is the length of the path that names one rule in a
	// definition: the group name and the rule name.
	ruleDepth = 2
	// validationFilePath is the name of the file sent to the validator.
	validationFilePath = "rules.yaml"
)

var (
	errRuleAmbiguous = errors.New("more than one rule has that name")
	errBadEdit       = errors.New("the change does not name a field of a rule")
)

// groupFields are the fields of a rule group that promtool reads. The CRD
// has more, such as partial_response_strategy for Thanos, which promtool
// refuses in a rules file.
var groupFields = []string{"name", "interval", "query_offset", "limit", "labels"}

// ruleRef names one rule of a PrometheusRule: by its group and its alert or
// record name, not by its position, which changes when someone adds a rule.
type ruleRef struct {
	group  string
	name   string
	record bool
}

// String describes the rule for a person.
func (r ruleRef) String() string {
	kind := "alert"
	if r.record {
		kind = "record"
	}

	return fmt.Sprintf("%s %q in group %q", kind, r.name, r.group)
}

// groupList is the spec.groups of a PrometheusRule. Each group is kept as
// raw fields, so the fields this package does not know are written back
// as they were read.
type groupList []map[string]json.RawMessage

// parseGroups reads a JSON array of rule groups. Empty input is no groups.
func parseGroups(raw json.RawMessage) (groupList, error) {
	if len(bytes.TrimSpace(raw)) == 0 || isNull(raw) {
		return groupList{}, nil
	}

	var groups groupList
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil, fmt.Errorf("decode the rule groups: %w", err)
	}

	return groups, nil
}

// marshal writes the groups as a JSON array.
func (g groupList) marshal() (json.RawMessage, error) {
	raw, err := json.Marshal(g)
	if err != nil {
		return nil, fmt.Errorf("encode the rule groups: %w", err)
	}

	return raw, nil
}

// view returns the definition of the rules in refs: an object from group
// name to rule name to the editable fields of the rule, with expr as text.
// A rule that is not there is listed in missing. A name that two rules of a
// group share is an error, because the rule cannot be told apart.
func (g groupList) view(refs []ruleRef) (json.RawMessage, []ruleRef, error) {
	out := map[string]map[string]map[string]json.RawMessage{}

	var missing []ruleRef

	for _, ref := range refs {
		gi, ri, err := g.locate(ref)
		if err != nil {
			return nil, nil, fmt.Errorf("find the %s: %w", ref, err)
		}

		if gi < 0 || ri < 0 {
			missing = append(missing, ref)

			continue
		}

		rules, err := g.rules(gi)
		if err != nil {
			return nil, nil, fmt.Errorf("read the rules of group %q: %w", ref.group, err)
		}

		fields, err := ruleFields(rules[ri])
		if err != nil {
			return nil, nil, fmt.Errorf("read the %s: %w", ref, err)
		}

		if out[ref.group] == nil {
			out[ref.group] = map[string]map[string]json.RawMessage{}
		}

		out[ref.group][ref.name] = fields
	}

	doc, err := json.Marshal(out)
	if err != nil {
		return nil, nil, fmt.Errorf("encode the definition: %w", err)
	}

	return doc, missing, nil
}

// edit returns the groups with the edits made. Each edit's path starts with
// the group name and the rule name of one of refs, followed by a field path
// within the rule. Everything else in the groups stays as it is.
func (g groupList) edit(refs []ruleRef, edits []action.Edit) (groupList, error) {
	out := make(groupList, len(g))
	for i, group := range g {
		out[i] = maps.Clone(group)
	}

	for _, e := range edits {
		if len(e.Path) <= ruleDepth {
			return nil, fmt.Errorf("%q: %w", e.Path, errBadEdit)
		}

		i := slices.IndexFunc(refs, func(r ruleRef) bool { return r.group == e.Path[0] && r.name == e.Path[1] })
		if i < 0 {
			return nil, fmt.Errorf("%q: %w", e.Path, errBadEdit)
		}

		if err := out.editRule(refs[i], action.Edit{Path: e.Path[ruleDepth:], Value: e.Value}); err != nil {
			return nil, fmt.Errorf("edit the %s: %w", refs[i], err)
		}
	}

	return out, nil
}

// editRule makes one edit, with a path within the rule, to the rule ref
// names.
func (g groupList) editRule(ref ruleRef, e action.Edit) error {
	gi, ri, err := g.locate(ref)
	if err != nil {
		return fmt.Errorf("find the rule: %w", err)
	}

	if gi < 0 || ri < 0 {
		return fmt.Errorf("the rule is not there: %w", errBadEdit)
	}

	rules, err := g.rules(gi)
	if err != nil {
		return fmt.Errorf("read the rules: %w", err)
	}

	rules[ri], err = action.Apply(rules[ri], []action.Edit{e})
	if err != nil {
		return fmt.Errorf("apply the edit: %w", err)
	}

	raw, err := json.Marshal(rules)
	if err != nil {
		return fmt.Errorf("encode the rules: %w", err)
	}

	g[gi]["rules"] = raw

	return nil
}

// validationFile returns a rules file for promtool with the groups that
// hold the rules of refs, whole, in the order they have in the resource.
// It holds the fields promtool reads, and expr as text.
func (g groupList) validationFile(refs []ruleRef) ([]byte, error) {
	var groups []map[string]json.RawMessage

	for gi, group := range g {
		name, err := groupName(group)
		if err != nil {
			return nil, fmt.Errorf("read group %d: %w", gi, err)
		}

		if !slices.ContainsFunc(refs, func(r ruleRef) bool { return r.group == name }) {
			continue
		}

		file, err := g.validationGroup(gi)
		if err != nil {
			return nil, fmt.Errorf("prepare group %q: %w", name, err)
		}

		groups = append(groups, file)
	}

	var b bytes.Buffer

	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	if err := enc.Encode(map[string]any{"groups": groups}); err != nil {
		return nil, fmt.Errorf("encode the rules file: %w", err)
	}

	return b.Bytes(), nil
}

// validationGroup returns group gi with the fields promtool reads.
func (g groupList) validationGroup(gi int) (map[string]json.RawMessage, error) {
	file := map[string]json.RawMessage{}

	for _, key := range groupFields {
		if value, ok := g[gi][key]; ok && !isNull(value) {
			file[key] = value
		}
	}

	rules, err := g.rules(gi)
	if err != nil {
		return nil, fmt.Errorf("read the rules: %w", err)
	}

	kept := make([]map[string]json.RawMessage, 0, len(rules))

	for _, rule := range rules {
		fields, err := ruleFields(rule)
		if err != nil {
			return nil, fmt.Errorf("read a rule: %w", err)
		}

		var id map[string]json.RawMessage
		if err := json.Unmarshal(rule, &id); err != nil {
			return nil, fmt.Errorf("decode a rule: %w", err)
		}

		for _, key := range []string{"alert", "record"} {
			if value, ok := id[key]; ok && !isNull(value) {
				fields[key] = value
			}
		}

		kept = append(kept, fields)
	}

	raw, err := json.Marshal(kept)
	if err != nil {
		return nil, fmt.Errorf("encode the rules: %w", err)
	}

	file["rules"] = raw

	return file, nil
}

// locate returns the position of the group and of the rule ref names, or -1
// for what is not there. It fails when two rules of the group have the name.
func (g groupList) locate(ref ruleRef) (int, int, error) {
	gi := -1

	for i, group := range g {
		name, err := groupName(group)
		if err != nil {
			return -1, -1, fmt.Errorf("read group %d: %w", i, err)
		}

		if name == ref.group {
			gi = i

			break
		}
	}

	if gi < 0 {
		return -1, -1, nil
	}

	rules, err := g.rules(gi)
	if err != nil {
		return -1, -1, fmt.Errorf("read the rules of group %q: %w", ref.group, err)
	}

	ri := -1

	for i, rule := range rules {
		match, err := ref.matches(rule)
		if err != nil {
			return -1, -1, fmt.Errorf("read rule %d of group %q: %w", i, ref.group, err)
		}

		if !match {
			continue
		}

		if ri >= 0 {
			return -1, -1, fmt.Errorf("group %q: %w", ref.group, errRuleAmbiguous)
		}

		ri = i
	}

	return gi, ri, nil
}

// rules returns the rules of group gi, as a new slice.
func (g groupList) rules(gi int) ([]json.RawMessage, error) {
	var rules []json.RawMessage
	if raw, ok := g[gi]["rules"]; ok && !isNull(raw) {
		if err := json.Unmarshal(raw, &rules); err != nil {
			return nil, fmt.Errorf("decode the rules: %w", err)
		}
	}

	return rules, nil
}

// names lists the names of the rules of the groups in names, for an error
// that tells the model what is there. It lists at most limit names.
func (g groupList) names(groups []string, limit int) []string {
	var out []string

	for gi, group := range g {
		name, err := groupName(group)
		if err != nil || !slices.Contains(groups, name) {
			continue
		}

		rules, err := g.rules(gi)
		if err != nil {
			continue
		}

		for _, rule := range rules {
			var id struct {
				Alert  string `json:"alert"`
				Record string `json:"record"`
			}

			if json.Unmarshal(rule, &id) != nil {
				continue
			}

			switch {
			case id.Alert != "":
				out = append(out, fmt.Sprintf("alert %q in group %q", id.Alert, name))
			case id.Record != "":
				out = append(out, fmt.Sprintf("record %q in group %q", id.Record, name))
			}

			if len(out) >= limit {
				return out
			}
		}
	}

	return out
}

// matches reports whether rule is the rule ref names.
func (r ruleRef) matches(rule json.RawMessage) (bool, error) {
	var id struct {
		Alert  string `json:"alert"`
		Record string `json:"record"`
	}

	if err := json.Unmarshal(rule, &id); err != nil {
		return false, fmt.Errorf("decode the rule: %w", err)
	}

	if r.record {
		return id.Record == r.name, nil
	}

	return id.Alert == r.name, nil
}

// groupName returns the name of a rule group.
func groupName(group map[string]json.RawMessage) (string, error) {
	var name string
	if err := json.Unmarshal(group["name"], &name); err != nil {
		return "", fmt.Errorf("decode the name: %w", err)
	}

	return name, nil
}

// checkShapes checks the editable fields of every rule of a definition:
// expr is text that is not empty, for and keep_firing_for are text (and
// keep_firing_for is not empty), and labels and annotations map names to
// text. The CRD says the same, but a check here gives the model a
// message about its own change. The error wraps action.ErrLimit.
func checkShapes(doc json.RawMessage) error {
	var rules map[string]map[string]map[string]json.RawMessage
	if err := json.Unmarshal(doc, &rules); err != nil {
		return fmt.Errorf("read the definition: %w: %w", action.ErrLimit, err)
	}

	for _, group := range slices.Sorted(maps.Keys(rules)) {
		for _, name := range slices.Sorted(maps.Keys(rules[group])) {
			if problem := shapeProblem(rules[group][name]); problem != "" {
				return fmt.Errorf("rule %q in group %q: %s: %w", name, group, problem, action.ErrLimit)
			}
		}
	}

	return nil
}

// shapeProblem says what is wrong with the fields of one rule, or "".
func shapeProblem(fields map[string]json.RawMessage) string {
	if text, ok := textOf(fields["expr"]); !ok || strings.TrimSpace(text) == "" {
		return "expr must be text that is not empty"
	}

	for _, key := range []string{"for", "keep_firing_for"} {
		value, present := fields[key]
		if !present {
			continue
		}

		if text, ok := textOf(value); !ok || (key == "keep_firing_for" && text == "") {
			return key + " must be a duration written as text, such as 5m"
		}
	}

	for _, key := range []string{"labels", "annotations"} {
		value, present := fields[key]
		if !present {
			continue
		}

		var m map[string]any
		if err := json.Unmarshal(value, &m); err != nil {
			return key + " must be an object from name to text"
		}

		for name, v := range m {
			if _, ok := v.(string); !ok {
				return fmt.Sprintf("%s.%s must be text", key, name)
			}
		}
	}

	return ""
}

// textOf returns the text of a JSON string, and whether value is one.
func textOf(value json.RawMessage) (string, bool) {
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return "", false
	}

	return text, true
}
