package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Bounds of get_ecs_service.
const (
	// maxServices is the most service names one list holds, one page of
	// ListServices.
	maxServices = 100
	// maxDeployments bounds the deployments of a service.
	maxDeployments = 10
)

// ecsNamePattern is the shape of an ECS cluster or service name. ARNs are
// not taken, since one can name another account.
var ecsNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,254}$`)

// ecsService is the result of get_ecs_service for one service.
type ecsService struct {
	Account        string          `json:"account"`
	Region         string          `json:"region"`
	Cluster        string          `json:"cluster"`
	Name           string          `json:"name"`
	Status         string          `json:"status"`
	Desired        int32           `json:"desired"`
	Running        int32           `json:"running"`
	Pending        int32           `json:"pending"`
	LaunchType     string          `json:"launchType,omitempty"`
	TaskDefinition string          `json:"taskDefinition,omitempty"`
	Deployments    []ecsDeployment `json:"deployments"`
	Events         []ecsEvent      `json:"events"`
	URL            string          `json:"url"`
}

// ecsDeployment is one deployment of a service.
type ecsDeployment struct {
	ID             string    `json:"id"`
	Status         string    `json:"status"`
	Rollout        string    `json:"rollout,omitempty"`
	RolloutReason  string    `json:"rolloutReason,omitempty"`
	TaskDefinition string    `json:"taskDefinition,omitempty"`
	Desired        int32     `json:"desired"`
	Running        int32     `json:"running"`
	Pending        int32     `json:"pending"`
	Failed         int32     `json:"failed"`
	Created        time.Time `json:"created,omitzero"`
	Updated        time.Time `json:"updated,omitzero"`
}

// ecsEvent is one service event.
type ecsEvent struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

// ecsServices is the result of get_ecs_service without service.
type ecsServices struct {
	Account  string   `json:"account"`
	Region   string   `json:"region"`
	Cluster  string   `json:"cluster"`
	Services []string `json:"services"`
}

// getECSService runs get_ecs_service.
func (s *Source) getECSService(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Account string `json:"account"`
		Region  string `json:"region"`
		Cluster string `json:"cluster"`
		Service string `json:"service"`
		Limit   int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	t, err := s.target(in.Account, in.Region)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	cluster, service := strings.TrimSpace(in.Cluster), strings.TrimSpace(in.Service)

	switch {
	case !ecsNamePattern.MatchString(cluster):
		return source.Result{}, &source.ToolError{Message: fmt.Sprintf("cluster: %q is not an ECS cluster name", source.CutText(cluster, maxNameLen))}
	case service != "" && !ecsNamePattern.MatchString(service):
		return source.Result{}, &source.ToolError{Message: fmt.Sprintf("service: %q is not an ECS service name", source.CutText(service, maxNameLen))}
	}

	if err := t.account.ready(ctx); err != nil {
		return source.Result{}, fmt.Errorf("get ready: %w", err)
	}

	client := s.ecs(t)

	if service == "" {
		return listECSServices(ctx, client, t, cluster)
	}

	return describeECSService(ctx, client, t, cluster, service, limitOf(in.Limit, defaultServiceEvents, maxServiceEvents))
}

// listECSServices returns the names of the services of a cluster.
func listECSServices(ctx context.Context, client *ecs.Client, t target, cluster string) (source.Result, error) {
	answer, err := client.ListServices(ctx, &ecs.ListServicesInput{Cluster: awssdk.String(cluster), MaxResults: awssdk.Int32(maxServices)})
	if err != nil {
		return source.Result{}, fmt.Errorf("list the services of ECS cluster %s: %w", cluster, err)
	}

	out := ecsServices{Account: t.account.id, Region: t.region, Cluster: cluster, Services: make([]string, 0, len(answer.ServiceArns))}
	for _, arn := range answer.ServiceArns {
		out.Services = append(out.Services, source.CutText(arn[strings.LastIndex(arn, "/")+1:], maxNameLen))
	}

	evidence := []source.Evidence{{Kind: source.EvidenceResource, Title: "ECS cluster " + cluster, URL: ecsURL(t.region, cluster, "")}}
	empty := fmt.Sprintf("ECS cluster %s in account %s, Region %s has no services.", cluster, t.account.id, t.region)

	return structured(out, evidence, answer.NextToken != nil, len(out.Services) == 0, empty)
}

// describeECSService returns one service with its deployments and its
// latest limit events.
func describeECSService(ctx context.Context, client *ecs.Client, t target, cluster, service string, limit int) (source.Result, error) {
	answer, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: awssdk.String(cluster), Services: []string{service}})
	if err != nil {
		return source.Result{}, fmt.Errorf("describe ECS service %s in cluster %s: %w", service, cluster, err)
	}

	if len(answer.Services) == 0 {
		return source.Result{NoData: true, Text: fmt.Sprintf("ECS cluster %s in account %s, Region %s has no service %s.",
			cluster, t.account.id, t.region, service)}, nil
	}

	svc := answer.Services[0]
	out := ecsService{
		Account:        t.account.id,
		Region:         t.region,
		Cluster:        cluster,
		Name:           service,
		Status:         awssdk.ToString(svc.Status),
		Desired:        svc.DesiredCount,
		Running:        svc.RunningCount,
		Pending:        svc.PendingCount,
		LaunchType:     string(svc.LaunchType),
		TaskDefinition: source.CutText(awssdk.ToString(svc.TaskDefinition), maxNameLen),
		Deployments:    make([]ecsDeployment, 0, min(len(svc.Deployments), maxDeployments)),
		Events:         make([]ecsEvent, 0, min(len(svc.Events), limit)),
		URL:            ecsURL(t.region, cluster, service),
	}

	for _, d := range svc.Deployments[:min(len(svc.Deployments), maxDeployments)] {
		out.Deployments = append(out.Deployments, deploymentOf(d))
	}

	b := budget{}
	cutEvents := len(svc.Events) > limit

	for _, e := range svc.Events[:min(len(svc.Events), limit)] {
		event := ecsEvent{Time: awssdk.ToTime(e.CreatedAt).UTC(), Message: source.CutText(awssdk.ToString(e.Message), maxMessageLen)}
		if !b.fits(event) {
			break
		}

		out.Events = append(out.Events, event)
	}

	evidence := []source.Evidence{{Kind: source.EvidenceResource, Title: "ECS service " + service + " in cluster " + cluster, URL: out.URL}}

	return structured(out, evidence, cutEvents || b.cut || len(svc.Deployments) > maxDeployments, false, "")
}

// deploymentOf returns the part of a deployment the result keeps.
func deploymentOf(d types.Deployment) ecsDeployment {
	return ecsDeployment{
		ID:             awssdk.ToString(d.Id),
		Status:         awssdk.ToString(d.Status),
		Rollout:        string(d.RolloutState),
		RolloutReason:  source.CutText(awssdk.ToString(d.RolloutStateReason), maxMessageLen),
		TaskDefinition: source.CutText(awssdk.ToString(d.TaskDefinition), maxNameLen),
		Desired:        d.DesiredCount,
		Running:        d.RunningCount,
		Pending:        d.PendingCount,
		Failed:         d.FailedTasks,
		Created:        awssdk.ToTime(d.CreatedAt).UTC(),
		Updated:        awssdk.ToTime(d.UpdatedAt).UTC(),
	}
}

// ecsURL returns the ECS console link of a cluster, or of one of its
// services.
func ecsURL(region, cluster, service string) string {
	path := "ecs/v2/clusters/" + url.PathEscape(cluster)
	if service != "" {
		path += "/services/" + url.PathEscape(service) + "/health"
	}

	return consoleRoot(region) + "/" + path + "?region=" + region
}
