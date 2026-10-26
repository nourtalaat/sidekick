package watch

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/jsonpath"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Op compares the values a condition's JSON path finds with its value.
type Op string

// Comparison operators.
const (
	OpEquals    Op = "equals"
	OpNotEquals Op = "notEquals"
	OpLT        Op = "lt"
	OpLTE       Op = "lte"
	OpGT        Op = "gt"
	OpGTE       Op = "gte"
	OpContains  Op = "contains"
	// OpExists passes when the path finds anything, even null. It takes no
	// value.
	OpExists Op = "exists"
)

// Ops lists every operator, for checks and error messages.
var Ops = []Op{OpEquals, OpNotEquals, OpLT, OpLTE, OpGT, OpGTE, OpContains, OpExists}

// maxShownValue is the longest found value a check detail shows, in bytes.
const maxShownValue = 64

// Condition says when the result of a tool check passes. It has one of two
// forms:
//
//   - JSONPath with Op and Value: a path into the result's structured
//     content, compared with Value. Numbers compare as numbers when both
//     sides are numbers (a JSON number, or text that holds one), anything
//     else as text. When the path finds several values, as with [*], the
//     condition passes if any of them passes. A path that finds nothing
//     fails.
//   - TextRegex, with Negate: a regular expression on the result's text,
//     which passes when it matches, or when it does not and Negate is set.
type Condition struct {
	JSONPath  jsonpath.Path `yaml:"jsonPath" json:"jsonPath,omitzero"`
	Op        Op            `yaml:"op" json:"op,omitempty"`
	Value     Scalar        `yaml:"value" json:"value,omitempty"`
	TextRegex string        `yaml:"textRegex" json:"textRegex,omitempty"`
	Negate    bool          `yaml:"negate" json:"negate,omitempty"`
}

// Scalar is a condition's value: text, or a JSON number or boolean kept as
// its text, so "value: 0.01" in YAML and {"value": 0.01} in JSON both work.
type Scalar string

// UnmarshalJSON accepts a JSON string, number or boolean.
func (s *Scalar) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*s = Scalar(text)

		return nil
	}

	var v any

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("decode condition value: %w", err)
	}

	switch v := v.(type) {
	case json.Number:
		*s = Scalar(v.String())
	case bool:
		*s = Scalar(strconv.FormatBool(v))
	default:
		return errors.New("a condition value must be a string, a number or a boolean")
	}

	return nil
}

// Validate checks that the condition has exactly one form, with a known
// operator, a value where the operator needs one, and a regular expression
// that compiles.
func (c Condition) Validate() error {
	_, err := c.compile()

	return err
}

// compiled is a condition ready to evaluate.
type compiled struct {
	cond  Condition
	regex *regexp.Regexp
}

// compile checks the condition and compiles its regular expression.
func (c Condition) compile() (compiled, error) {
	hasPath := !c.JSONPath.IsZero()
	hasRegex := c.TextRegex != ""

	switch {
	case hasPath && hasRegex:
		return compiled{}, errors.New("set jsonPath or textRegex, not both")
	case hasRegex:
		return c.compileRegex()
	case hasPath:
		return compiled{cond: c}, c.checkComparison()
	default:
		return compiled{}, errors.New("set jsonPath with op, or textRegex")
	}
}

// compileRegex compiles a textRegex condition.
func (c Condition) compileRegex() (compiled, error) {
	if c.Op != "" || c.Value != "" {
		return compiled{}, errors.New("op and value go with jsonPath, not textRegex")
	}

	re, err := regexp.Compile(c.TextRegex)
	if err != nil {
		return compiled{}, fmt.Errorf("textRegex: %w", err)
	}

	return compiled{cond: c, regex: re}, nil
}

// checkComparison checks the operator and value of a jsonPath condition.
func (c Condition) checkComparison() error {
	switch {
	case c.Negate:
		return errors.New("negate goes with textRegex; use notEquals with jsonPath")
	case c.Op == "":
		return errors.New("op: required with jsonPath")
	case !slices.Contains(Ops, c.Op):
		return fmt.Errorf("op: unknown operator %q; expected one of %s", c.Op, joinOps())
	case c.Op == OpExists && c.Value != "":
		return errors.New("value: exists takes no value")
	case c.Value == "" && c.Op != OpExists && c.Op != OpEquals && c.Op != OpNotEquals:
		return fmt.Errorf("value: required with %s", c.Op)
	default:
		return nil
	}
}

// evaluate reports whether a result with this text and structured content
// passes, with a short detail. It fails with an error when the structured
// content is not JSON.
func (c compiled) evaluate(text string, structured json.RawMessage) (bool, string, error) {
	if c.regex != nil {
		matched := c.regex.MatchString(text)
		pass := matched != c.cond.Negate

		if matched {
			return pass, "the text matches textRegex", nil
		}

		return pass, "the text does not match textRegex", nil
	}

	return c.evaluatePath(structured)
}

// evaluatePath evaluates a jsonPath condition.
func (c compiled) evaluatePath(structured json.RawMessage) (bool, string, error) {
	path := c.cond.JSONPath.String()
	if len(structured) == 0 {
		return false, fmt.Sprintf("%s found nothing: the result has no structured content", path), nil
	}

	dec := json.NewDecoder(bytes.NewReader(structured))
	dec.UseNumber()

	var doc any
	if err := dec.Decode(&doc); err != nil {
		return false, "", fmt.Errorf("decode the structured content: %w", err)
	}

	found := c.cond.JSONPath.Find(doc)
	if len(found) == 0 {
		return false, path + " found nothing", nil
	}

	for _, v := range found {
		if compare(c.cond.Op, v, string(c.cond.Value)) {
			return true, fmt.Sprintf("%s is %s (%s)", path, shown(v), c.describe()), nil
		}
	}

	if len(found) == 1 {
		return false, fmt.Sprintf("%s is %s (not %s)", path, shown(found[0]), c.describe()), nil
	}

	return false, fmt.Sprintf("none of the %d values %s found is %s", len(found), path, c.describe()), nil
}

// describe returns the comparison as text, such as "lt 0.01".
func (c compiled) describe() string {
	if c.cond.Op == OpExists {
		return string(OpExists)
	}

	return fmt.Sprintf("%s %q", c.cond.Op, string(c.cond.Value))
}

// compare reports whether the found value v passes op with want.
func compare(op Op, v any, want string) bool {
	switch op {
	case OpExists:
		return true
	case OpContains:
		return strings.Contains(asText(v), want)
	case OpEquals, OpNotEquals, OpLT, OpLTE, OpGT, OpGTE:
	}

	order := compareValues(v, want)

	switch op {
	case OpEquals:
		return order == 0
	case OpNotEquals:
		return order != 0
	case OpLT:
		return order < 0
	case OpLTE:
		return order <= 0
	case OpGT:
		return order > 0
	case OpGTE:
		return order >= 0
	case OpContains, OpExists:
	}

	return false
}

// compareValues orders v against want: as numbers when both are numbers,
// as text otherwise.
func compareValues(v any, want string) int {
	if a, ok := asNumber(v); ok {
		if b, ok := parseNumber(want); ok {
			return cmp.Compare(a, b)
		}
	}

	return strings.Compare(asText(v), want)
}

// asNumber returns v as a number when it is a JSON number or text that
// holds a finite number.
func asNumber(v any) (float64, bool) {
	switch v := v.(type) {
	case json.Number:
		return parseNumber(v.String())
	case float64:
		return v, !math.IsInf(v, 0) && !math.IsNaN(v)
	case string:
		return parseNumber(strings.TrimSpace(v))
	default:
		return 0, false
	}
}

// parseNumber parses s as a finite number.
func parseNumber(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}

	return f, true
}

// asText returns v as text: a string as it is, null as "null", and any
// other value as compact JSON.
func asText(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(v)
	case json.Number:
		return v.String()
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}

		return string(data)
	}
}

// shown returns v for a check detail: quoted when it is text, and cut to
// maxShownValue bytes.
func shown(v any) string {
	text := asText(v)
	if len(text) > maxShownValue {
		text = source.CutText(text, maxShownValue) + "..."
	}

	if _, ok := v.(string); ok {
		return strconv.Quote(text)
	}

	return text
}

// joinOps lists the operators for an error message.
func joinOps() string {
	names := make([]string, len(Ops))
	for i, op := range Ops {
		names[i] = string(op)
	}

	return strings.Join(names, ", ")
}
