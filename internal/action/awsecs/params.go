package awsecs

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/awsaction"
	"github.com/nourtalaat/sidekick/internal/source"
)

// maxShownLen is the most bytes of a value from the model that an error
// message shows.
const maxShownLen = 512

// Params are what the model fills in when it proposes the action.
type Params struct {
	Account       string `json:"account"`
	Region        string `json:"region"`
	Cluster       string `json:"cluster"`
	Service       string `json:"service"`
	RevisionsBack *int   `json:"revisionsBack"`
}

// back is the number of revisions to go back: one when the model left it
// out.
func (p Params) back() int {
	if p.RevisionsBack == nil {
		return action.DefaultMaxRevisionsBack
	}

	return *p.RevisionsBack
}

// position is the service a call is about, with the account and Region
// resolved against the config.
type position struct {
	target  awsaction.Target
	cluster string
	service string
}

// name is the target of a snapshot.
func (p position) name() string {
	return "aws:ecs/" + p.target.Account + "/" + p.target.Region + "/" + p.cluster + "/" + p.service
}

// paramsSchema returns the JSON Schema of Params for a config. The maximum
// of revisionsBack is the config's limit.
func paramsSchema(maxBack int) json.RawMessage {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"cluster", "service"},
		"properties": map[string]any{
			"account": map[string]any{"type": "string", "description": "The 12-digit AWS account ID. It may be left out when the action works in only one account."},
			"region":  map[string]any{"type": "string", "description": "The AWS Region, for example us-east-1. It may be left out when the action works in only one Region."},
			"cluster": map[string]any{"type": "string", "minLength": 1, "description": "The name of the ECS cluster."},
			"service": map[string]any{"type": "string", "minLength": 1, "description": "The name of the ECS service."},
			"revisionsBack": map[string]any{
				"type": "integer", "minimum": 1, "maximum": maxBack, "default": action.DefaultMaxRevisionsBack,
				"description": "How many ACTIVE task definition revisions of the service's family to go back from the one it runs. Default 1.",
			},
		},
	}

	raw, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("encode the parameters schema: %v", err))
	}

	return raw
}

// parse decodes the parameters and checks them against the limits,
// including the number of revisions back.
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

// decode decodes the parameters and checks that the config lists the
// account, Region, cluster and service. Undo uses it alone, because an undo
// puts back a change that was allowed when it was made.
func (r *Rollback) decode(raw json.RawMessage) (Params, position, error) {
	var p Params
	if err := action.DecodeParams(raw, &p); err != nil {
		return Params{}, position{}, fmt.Errorf("decode the parameters: %w", err)
	}

	p.Cluster, p.Service = strings.TrimSpace(p.Cluster), strings.TrimSpace(p.Service)

	target, err := r.clients.Target(p.Account, p.Region)
	if err != nil {
		return Params{}, position{}, fmt.Errorf("pick the account and Region: %w", err)
	}

	if err := r.checkReach(p); err != nil {
		return Params{}, position{}, fmt.Errorf("check the service: %w", err)
	}

	return p, position{target: target, cluster: p.Cluster, service: p.Service}, nil
}

// checkReach checks that limits.clusters lists the cluster and
// limits.services the service.
func (r *Rollback) checkReach(p Params) error {
	if !namePattern.MatchString(p.Cluster) || !slices.Contains(r.limits.Clusters, p.Cluster) {
		return fmt.Errorf("cluster %q is not one the action may change; allowed: %s: %w",
			source.CutText(p.Cluster, maxShownLen), strings.Join(r.limits.Clusters, ", "), action.ErrLimit)
	}

	if !namePattern.MatchString(p.Service) || !slices.ContainsFunc(r.limits.Services, func(pattern string) bool { return matches(pattern, p.Service) }) {
		return fmt.Errorf("service %q is not one the action may change; allowed: %s: %w",
			source.CutText(p.Service, maxShownLen), strings.Join(r.limits.Services, ", "), action.ErrLimit)
	}

	return nil
}

// matches reports whether the service name matches a pattern of
// limits.services, in which "*" stands for any run of characters.
func matches(pattern, service string) bool {
	ok, err := path.Match(pattern, service)

	return err == nil && ok
}
