package awsecs

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/awsaction"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Bounds of the search for earlier task definitions.
const (
	// listPageSize is the most ARNs ListTaskDefinitions returns in a page.
	listPageSize = 100
	// maxListPages bounds the pages read. ListTaskDefinitions cannot filter by
	// revision, and in descending order the families that share the prefix
	// come first, so the search is cut.
	maxListPages = 10
)

// primaryStatus is the status of the most recent deployment of a service.
const primaryStatus = "PRIMARY"

// state is what the action needs to know about a service: its primary
// deployment and the task definition that deployment runs, as family:revision.
type state struct {
	deploymentID   string
	taskDefinition string
}

// refusal is an error that says, for the model or a person to read, why the
// action will not do something, and its text is only the reason. It matches
// action.ErrLimit, because Sidekick shows the model only a Snapshot error that
// wraps it as a refusal with its reason, and anything else as a failure to read
// the service, to try again later. ErrLimit counts as permanent, as does
// action.ErrPermanent, which it also matches, so Execute and Undo fail for good.
type refusal struct {
	text string
}

// Error implements error.
func (e *refusal) Error() string {
	return e.text
}

// Is makes the error match action.ErrLimit and action.ErrPermanent.
func (e *refusal) Is(target error) bool {
	return target == action.ErrLimit || target == action.ErrPermanent
}

// refuse makes a refusal from format and args.
func refuse(format string, args ...any) error {
	return &refusal{text: fmt.Sprintf(format, args...)}
}

// client returns the ECS client for the position, after getting the
// credentials of its account.
func (r *Rollback) client(ctx context.Context, pos position) (*ecs.Client, error) {
	if err := pos.target.Ready(ctx); err != nil {
		return nil, fmt.Errorf("get ready: %w", err)
	}

	return ecs.NewFromConfig(pos.target.Config, func(o *ecs.Options) {
		if r.endpoint != "" {
			o.BaseEndpoint = aws.String(r.endpoint)
		}
	}), nil
}

// readService reads the service and checks that the action can roll it back:
// it exists, is ACTIVE, uses the ECS deployment controller and has a primary
// deployment. It returns the state of the primary deployment.
func readService(ctx context.Context, client *ecs.Client, pos position) (state, error) {
	out, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(pos.cluster), Services: []string{pos.service}})
	if _, ok := errors.AsType[*types.ClusterNotFoundException](err); ok {
		return state{}, refuse("ECS cluster %s does not exist in account %s, Region %s", pos.cluster, pos.target.Account, pos.target.Region)
	}

	if err != nil {
		return state{}, fmt.Errorf("describe service %s in cluster %s: %w", pos.service, pos.cluster, awsaction.Fail(err))
	}

	if len(out.Services) == 0 {
		return state{}, refuse("ECS cluster %s in account %s, Region %s has no service %s", pos.cluster, pos.target.Account, pos.target.Region, pos.service)
	}

	return stateOf(out.Services[0], pos)
}

// stateOf checks a described service and returns the state of its primary
// deployment.
func stateOf(svc types.Service, pos position) (state, error) {
	if status := aws.ToString(svc.Status); status != "ACTIVE" {
		return state{}, refuse("service %s is %s, not ACTIVE, so ECS cannot update it", pos.service, status)
	}

	// ECS leaves deploymentController out for a service that uses the ECS
	// controller, so none means ECS.
	if c := svc.DeploymentController; c != nil && c.Type != types.DeploymentControllerTypeEcs {
		return state{}, refuse("service %s uses the %s deployment controller; this action rolls back only services that use the ECS deployment controller",
			pos.service, c.Type)
	}

	for _, d := range svc.Deployments {
		if aws.ToString(d.Status) != primaryStatus {
			continue
		}

		arn := aws.ToString(d.TaskDefinition)

		family, revision, ok := parseTaskDefinition(arn)
		if !ok {
			return state{}, refuse("the PRIMARY deployment of service %s names task definition %q, which is not a task definition ARN",
				pos.service, source.CutText(arn, maxShownLen))
		}

		return state{deploymentID: aws.ToString(d.Id), taskDefinition: taskDefinitionID(family, revision)}, nil
	}

	return state{}, refuse("service %s has no PRIMARY deployment, so Sidekick cannot tell which task definition it runs", pos.service)
}

// earlier returns the target of a rollback: the revision that is back
// ACTIVE revisions below running in the family. It reads ListTaskDefinitions
// in descending order, and keeps the ARNs of exactly this family, since a
// family prefix also matches other families.
func earlier(ctx context.Context, client *ecs.Client, family string, running, back int) (int, error) {
	var (
		found    []int
		examined int
		token    *string
	)

	for range maxListPages {
		out, err := client.ListTaskDefinitions(ctx, &ecs.ListTaskDefinitionsInput{
			FamilyPrefix: aws.String(family),
			Status:       types.TaskDefinitionStatusActive,
			Sort:         types.SortOrderDesc,
			MaxResults:   aws.Int32(listPageSize),
			NextToken:    token,
		})
		if err != nil {
			return 0, fmt.Errorf("list the task definitions of family %s: %w", family, awsaction.Fail(err))
		}

		examined += len(out.TaskDefinitionArns)

		for _, arn := range out.TaskDefinitionArns {
			if f, revision, ok := parseTaskDefinition(arn); ok && f == family && revision < running {
				found = append(found, revision)
			}

			if len(found) == back {
				return found[back-1], nil
			}
		}

		if out.NextToken == nil {
			return 0, tooFew(family, running, back, len(found))
		}

		token = out.NextToken
	}

	return 0, refuse("looked through the newest %d task definitions that start with %q and did not find %d ACTIVE revision%s of family %s below %s; "+
		"ask for fewer revisions back, or roll back in the ECS console", examined, family, back, plural(back), family, taskDefinitionID(family, running))
}

// tooFew is the refusal for a family that has fewer ACTIVE revisions below
// the running one than the model asked to go back.
func tooFew(family string, running, back, have int) error {
	id := taskDefinitionID(family, running)

	if have == 0 {
		return refuse("task definition family %s has no ACTIVE revision below %s, so there is nothing to go back to", family, id)
	}

	return refuse("task definition family %s has only %d ACTIVE revision%s below %s, so the service cannot go %d back", family, have, plural(have), id, back)
}

// plural is "s" for a count other than one.
func plural(n int) string {
	if n == 1 {
		return ""
	}

	return "s"
}

// activeTaskDefinition describes a task definition and checks that it is
// ACTIVE, so a service can be put on it. forWhat finishes the refusal: "so"
// and what cannot be done with a task definition that is not.
func activeTaskDefinition(ctx context.Context, client *ecs.Client, id, forWhat string) (types.TaskDefinition, error) {
	td, err := describeTaskDefinition(ctx, client, id)
	if _, ok := errors.AsType[*types.ClientException](err); ok {
		return types.TaskDefinition{}, refuse("task definition %s cannot be read from ECS, so %s", id, forWhat)
	}

	if err != nil {
		return types.TaskDefinition{}, fmt.Errorf("check task definition %s: %w", id, err)
	}

	if td.Status != types.TaskDefinitionStatusActive {
		return types.TaskDefinition{}, refuse("task definition %s is %s, so %s", id, td.Status, forWhat)
	}

	return td, nil
}

// describeTaskDefinition describes the task definition with id, a
// family:revision.
func describeTaskDefinition(ctx context.Context, client *ecs.Client, id string) (types.TaskDefinition, error) {
	out, err := client.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: aws.String(id)})
	if err != nil {
		return types.TaskDefinition{}, fmt.Errorf("describe task definition %s: %w", id, awsaction.Fail(err))
	}

	if out.TaskDefinition == nil {
		return types.TaskDefinition{}, refuse("ECS gave no description of task definition %s", id)
	}

	return *out.TaskDefinition, nil
}

// updateService puts the service on the task definition id and changes
// nothing else. It returns the ID of the primary deployment the call
// reports, or "" when it reports none. The call is tried once: the SDK does
// not repeat it, because Sidekick tries again from a fresh read of the
// service, which is the one that knows whether the call took effect.
func updateService(ctx context.Context, client *ecs.Client, pos position, id string) (string, error) {
	out, err := client.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:        aws.String(pos.cluster),
		Service:        aws.String(pos.service),
		TaskDefinition: aws.String(id),
	}, func(o *ecs.Options) { o.RetryMaxAttempts = 1 })
	if err != nil {
		return "", fmt.Errorf("update service %s in cluster %s to task definition %s: %w", pos.service, pos.cluster, id, awsaction.Fail(err))
	}

	if out.Service != nil {
		for _, d := range out.Service.Deployments {
			if aws.ToString(d.Status) == primaryStatus {
				return aws.ToString(d.Id), nil
			}
		}
	}

	return "", nil
}

// serviceLink points at the service in the ECS console.
func serviceLink(pos position) action.Link {
	return action.Link{
		Title: "ECS service " + pos.service + " in cluster " + pos.cluster,
		URL: awsaction.ConsoleRoot(pos.target.Region) + "/ecs/v2/clusters/" + url.PathEscape(pos.cluster) +
			"/services/" + url.PathEscape(pos.service) + "/health?region=" + pos.target.Region,
	}
}

// versionOf is the version of a snapshot: the primary deployment and the
// task definition it runs. A deployment ID has no space, so splitVersion can
// tell them apart.
func versionOf(st state) string {
	return st.deploymentID + " " + st.taskDefinition
}

// splitVersion is the deployment ID and the task definition of a version.
func splitVersion(version string) (deployment, taskDefinition string) {
	deployment, taskDefinition, _ = strings.Cut(version, " ")

	return deployment, taskDefinition
}
