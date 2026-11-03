package awslambda

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/awsaction"
	"github.com/nourtalaat/sidekick/internal/source"
)

// maxShownLen is the most bytes of a value from the model that an error
// message shows.
const maxShownLen = 512

// functionNamePattern is the shape of a Lambda function name in a call. ARNs
// are not taken, since one can name another account.
var functionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Params are what the model fills in when it proposes the action.
type Params struct {
	Account       string `json:"account"`
	Region        string `json:"region"`
	Function      string `json:"function"`
	Alias         string `json:"alias"`
	RevisionsBack *int   `json:"revisionsBack"`
}

// back is the number of versions to go back: one when the model left it out.
func (p Params) back() int {
	if p.RevisionsBack == nil {
		return action.DefaultMaxRevisionsBack
	}

	return *p.RevisionsBack
}

// position is the alias a call is about, with the account and Region
// resolved against the config.
type position struct {
	target   awsaction.Target
	function string
	alias    string
}

// name is the target of a snapshot.
func (p position) name() string {
	return "aws:lambda/" + p.target.Account + "/" + p.target.Region + "/" + p.function + "/" + p.alias
}

// paramsSchema returns the JSON Schema of Params for a config. The maximum of
// revisionsBack is the config's limit.
func paramsSchema(maxBack int) json.RawMessage {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"function", "alias"},
		"properties": map[string]any{
			"account":  map[string]any{"type": "string", "description": "The 12-digit AWS account ID. It may be left out when the action works in only one account."},
			"region":   map[string]any{"type": "string", "description": "The AWS Region, for example us-east-1. It may be left out when the action works in only one Region."},
			"function": map[string]any{"type": "string", "minLength": 1, "description": "The name of the Lambda function, not its ARN."},
			"alias":    map[string]any{"type": "string", "minLength": 1, "description": "The name of the alias to point at an earlier version."},
			"revisionsBack": map[string]any{
				"type": "integer", "minimum": 1, "maximum": maxBack, "default": action.DefaultMaxRevisionsBack,
				"description": "How many published versions of the function to go back from the one the alias serves. Default 1.",
			},
		},
	}

	raw, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("encode the parameters schema: %v", err))
	}

	return raw
}

// parse decodes the parameters and checks them against the limits, including
// the number of versions back.
func (r *Rollback) parse(raw json.RawMessage) (Params, position, error) {
	p, pos, err := r.decode(raw)
	if err != nil {
		return Params{}, position{}, fmt.Errorf("check the parameters: %w", err)
	}

	if err := r.CheckRequest(p.back()); err != nil {
		return Params{}, position{}, fmt.Errorf("check the request: %w", err)
	}

	return p, pos, nil
}

// decode decodes the parameters and checks that the config lists the account,
// Region, function and alias. Undo uses it alone, because an undo puts back a
// change that was allowed when it was made.
func (r *Rollback) decode(raw json.RawMessage) (Params, position, error) {
	var p Params
	if err := action.DecodeParams(raw, &p); err != nil {
		return Params{}, position{}, fmt.Errorf("decode the parameters: %w", err)
	}

	p.Function, p.Alias = strings.TrimSpace(p.Function), strings.TrimSpace(p.Alias)

	target, err := r.clients.Target(p.Account, p.Region)
	if err != nil {
		return Params{}, position{}, fmt.Errorf("pick the account and Region: %w", err)
	}

	if err := r.checkReach(p); err != nil {
		return Params{}, position{}, fmt.Errorf("check the alias: %w", err)
	}

	return p, position{target: target, function: p.Function, alias: p.Alias}, nil
}

// checkReach checks that limits.functions lists the function and
// limits.aliases the alias.
func (r *Rollback) checkReach(p Params) error {
	if !functionNamePattern.MatchString(p.Function) || !slices.ContainsFunc(r.limits.Functions, func(pattern string) bool { return matches(pattern, p.Function) }) {
		return fmt.Errorf("function %q is not one the action may change; allowed: %s: %w",
			source.CutText(p.Function, maxShownLen), strings.Join(r.limits.Functions, ", "), action.ErrLimit)
	}

	if !slices.Contains(r.limits.Aliases, p.Alias) {
		return fmt.Errorf("alias %q is not one the action may change; allowed: %s: %w",
			source.CutText(p.Alias, maxShownLen), strings.Join(r.limits.Aliases, ", "), action.ErrLimit)
	}

	return nil
}

// matches reports whether the function name matches a pattern of
// limits.functions, in which "*" stands for any run of characters.
func matches(pattern, function string) bool {
	ok, err := path.Match(pattern, function)

	return err == nil && ok
}
