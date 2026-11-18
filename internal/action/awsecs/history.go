package awsecs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// Bounds of the read of the deployment history of a service.
const (
	// deploymentsPageSize is the most deployments ListServiceDeployments
	// returns in a page.
	deploymentsPageSize = 100
	// maxDeploymentPages bounds the pages read, so a service with a very long
	// history costs a fixed number of calls.
	maxDeploymentPages = 3
	// revisionsPerDescribe is the most service revision ARNs
	// DescribeServiceRevisions takes in one call.
	revisionsPerDescribe = 20
)

// deployment is a deployment that put a task definition on the service.
type deployment struct {
	id string
	at *time.Time
}

// deployedRevision is a task definition revision of the family that the
// service deployed successfully, and the newest deployment that did it.
type deployedRevision struct {
	revision int
	deployment
}

// history is what the deployment history of a service says about the
// revisions of one family below the running one.
type history struct {
	// found are the ACTIVE revisions that were deployed, newest first, up to
	// the number asked for.
	found []deployedRevision
	// inactive are the deployed revisions that are not ACTIVE, newest first.
	inactive []int
	// deployed holds every revision below the running one that was seen.
	deployed map[int]bool
	// none is true when ECS has no deployment of any status for the service.
	none bool
	// cut is true when the read stopped at its bound before the end of the
	// history, with fewer revisions found than asked for.
	cut bool
	// read is the number of successful deployments read.
	read int
}

// reader reads the successful deployments of one service, newest first, and
// sorts the task definitions they deployed.
type reader struct {
	client  *ecs.Client
	pos     position
	family  string
	running int
	// active holds the ACTIVE revisions of the family below the running one.
	active map[int]bool
	back   int
	// taskDefinitions maps a service revision ARN to the family:revision of
	// its task definition.
	taskDefinitions map[string]string
}

// read lists the successful deployments page by page, until it has found
// back ACTIVE revisions, the history ends or the bound is reached. A call
// ECS refuses is returned as it is, so the caller can tell a denied call
// from other failures.
func (r *reader) read(ctx context.Context) (history, error) {
	h := history{deployed: map[int]bool{}}

	var token *string

	for page := range maxDeploymentPages {
		out, err := r.client.ListServiceDeployments(ctx, &ecs.ListServiceDeploymentsInput{
			Cluster:    aws.String(r.pos.cluster),
			Service:    aws.String(r.pos.service),
			Status:     []types.ServiceDeploymentStatus{types.ServiceDeploymentStatusSuccessful},
			MaxResults: aws.Int32(deploymentsPageSize),
			NextToken:  token,
		})
		if err != nil {
			return history{}, fmt.Errorf("list the deployments of service %s in cluster %s: %w", r.pos.service, r.pos.cluster, err)
		}

		h.read += len(out.ServiceDeployments)

		if err := r.resolve(ctx, out.ServiceDeployments); err != nil {
			return history{}, fmt.Errorf("read the task definitions of the deployments: %w", err)
		}

		if r.sort(&h, out.ServiceDeployments) {
			return h, nil
		}

		if out.NextToken == nil {
			if page == 0 && len(out.ServiceDeployments) == 0 {
				none, err := r.noDeployments(ctx)
				if err != nil {
					return history{}, fmt.Errorf("check for any deployment: %w", err)
				}

				h.none = none
			}

			return h, nil
		}

		token = out.NextToken
	}

	h.cut = true

	return h, nil
}

// resolve reads the task definitions of the service revisions that the
// deployments target and that it has not read yet.
func (r *reader) resolve(ctx context.Context, deployments []types.ServiceDeploymentBrief) error {
	var arns []string

	for _, d := range deployments {
		arn := aws.ToString(d.TargetServiceRevisionArn)
		if _, known := r.taskDefinitions[arn]; arn != "" && !known {
			r.taskDefinitions[arn] = ""
			arns = append(arns, arn)
		}
	}

	for len(arns) > 0 {
		chunk := arns[:min(revisionsPerDescribe, len(arns))]
		arns = arns[len(chunk):]

		out, err := r.client.DescribeServiceRevisions(ctx, &ecs.DescribeServiceRevisionsInput{ServiceRevisionArns: chunk})
		if err != nil {
			return fmt.Errorf("describe %d service revisions of service %s: %w", len(chunk), r.pos.service, err)
		}

		for _, rev := range out.ServiceRevisions {
			family, revision, ok := parseTaskDefinition(aws.ToString(rev.TaskDefinition))
			if ok {
				r.taskDefinitions[aws.ToString(rev.ServiceRevisionArn)] = taskDefinitionID(family, revision)
			}
		}
	}

	return nil
}

// sort puts the revisions the deployments deployed into h, and reports
// whether it has found as many ACTIVE ones as asked for.
func (r *reader) sort(h *history, deployments []types.ServiceDeploymentBrief) bool {
	for _, d := range deployments {
		family, revision, ok := parseTaskDefinition(r.taskDefinitions[aws.ToString(d.TargetServiceRevisionArn)])
		if !ok || family != r.family || revision >= r.running || h.deployed[revision] {
			continue
		}

		h.deployed[revision] = true

		if !r.active[revision] {
			h.inactive = append(h.inactive, revision)

			continue
		}

		h.found = append(h.found, deployedRevision{revision: revision, deployment: deploymentOf(d)})

		if len(h.found) == r.back {
			return true
		}
	}

	return false
}

// noDeployments reports whether ECS has no deployment of any status for the
// service, as opposed to none that succeeded.
func (r *reader) noDeployments(ctx context.Context) (bool, error) {
	out, err := r.client.ListServiceDeployments(ctx, &ecs.ListServiceDeploymentsInput{
		Cluster:    aws.String(r.pos.cluster),
		Service:    aws.String(r.pos.service),
		MaxResults: aws.Int32(1),
	})
	if err != nil {
		return false, fmt.Errorf("list one deployment of service %s: %w", r.pos.service, err)
	}

	return len(out.ServiceDeployments) == 0, nil
}

// deploymentOf is the ID and the time of a deployment. The time is when it
// finished, or when it started when ECS gives no end.
func deploymentOf(d types.ServiceDeploymentBrief) deployment {
	id := aws.ToString(d.ServiceDeploymentArn)
	id = id[strings.LastIndex(id, "/")+1:]

	for _, at := range []*time.Time{d.FinishedAt, d.StartedAt, d.CreatedAt} {
		if at != nil {
			utc := at.UTC()

			return deployment{id: id, at: &utc}
		}
	}

	return deployment{id: id}
}

// revisionSet makes a set of revision numbers.
func revisionSet(revisions []int) map[int]bool {
	m := make(map[int]bool, len(revisions))

	for _, revision := range revisions {
		m[revision] = true
	}

	return m
}
