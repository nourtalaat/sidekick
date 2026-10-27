package gcp

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/source"
)

// maxOperations is the most recent operations get_gke_cluster returns of
// one cluster.
const maxOperations = 10

// anyLocation is the location that lists the clusters of every location.
const anyLocation = "-"

// cluster is the part of a GKE cluster the source reads.
type cluster struct {
	Name                 string      `json:"name"`
	Location             string      `json:"location"`
	Status               string      `json:"status"`
	StatusMessage        string      `json:"statusMessage,omitempty"`
	CurrentMasterVersion string      `json:"currentMasterVersion,omitempty"`
	CurrentNodeVersion   string      `json:"currentNodeVersion,omitempty"`
	CreateTime           string      `json:"createTime,omitempty"`
	Conditions           []condition `json:"conditions,omitempty"`
	NodePools            []nodePool  `json:"nodePools,omitempty"`
	ReleaseChannel       *struct {
		Channel string `json:"channel"`
	} `json:"releaseChannel,omitempty"`
	Autopilot *struct {
		Enabled bool `json:"enabled"`
	} `json:"autopilot,omitempty"`
}

// condition is a problem GKE reports on a cluster or node pool.
type condition struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// nodePool is the part of a GKE node pool the source reads.
type nodePool struct {
	Name          string      `json:"name"`
	Status        string      `json:"status"`
	StatusMessage string      `json:"statusMessage,omitempty"`
	Version       string      `json:"version,omitempty"`
	Conditions    []condition `json:"conditions,omitempty"`
	Autoscaling   *struct {
		Enabled      bool `json:"enabled,omitempty"`
		MinNodeCount int  `json:"minNodeCount,omitempty"`
		MaxNodeCount int  `json:"maxNodeCount,omitempty"`
	} `json:"autoscaling,omitempty"`
}

// operation is one GKE operation, such as an upgrade.
type operation struct {
	Name          string `json:"name"`
	OperationType string `json:"operationType"`
	Status        string `json:"status"`
	StatusMessage string `json:"statusMessage,omitempty"`
	TargetLink    string `json:"targetLink,omitempty"`
	StartTime     string `json:"startTime,omitempty"`
	EndTime       string `json:"endTime,omitempty"`
}

// clusterSummary is one cluster in the list get_gke_cluster returns
// without a cluster.
type clusterSummary struct {
	Name          string `json:"name"`
	Location      string `json:"location"`
	Status        string `json:"status"`
	StatusMessage string `json:"statusMessage,omitempty"`
	Version       string `json:"version,omitempty"`
}

// clusterResult is the result of get_gke_cluster for one cluster.
type clusterResult struct {
	Project    string      `json:"project"`
	Cluster    cluster     `json:"cluster"`
	Operations []operation `json:"operations"`
}

// clustersResult is the result of get_gke_cluster without a cluster.
type clustersResult struct {
	Project  string           `json:"project"`
	Clusters []clusterSummary `json:"clusters"`
}

// getGKECluster runs get_gke_cluster.
func (s *Source) getGKECluster(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Project  string `json:"project"`
		Location string `json:"location"`
		Cluster  string `json:"cluster"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	project, err := s.project(in.Project)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	location := strings.TrimSpace(in.Location)
	name := strings.TrimSpace(in.Cluster)

	switch {
	case name != "" && location == "":
		return source.Result{}, &source.ToolError{Message: "location: required with cluster"}
	case location != "":
		if err := checkName("location", location); err != nil {
			return source.Result{}, fmt.Errorf("read the input: %w", err)
		}
	}

	if name == "" {
		return s.listClusters(ctx, project, cmp.Or(location, anyLocation))
	}

	if err := checkName("cluster", name); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	return s.getCluster(ctx, project, location, name)
}

// getCluster returns one cluster and its most recent operations.
func (s *Source) getCluster(ctx context.Context, project, location, name string) (source.Result, error) {
	base := s.apis.container + "/projects/" + url.PathEscape(project) + "/locations/" + location

	var c cluster
	if err := s.client.get(ctx, base+"/clusters/"+name, nil, &c); err != nil {
		return source.Result{}, fmt.Errorf("get cluster %s in %s: %w", name, location, err)
	}

	var ops struct {
		Operations []operation `json:"operations"`
	}

	if err := s.client.get(ctx, base+"/operations", nil, &ops); err != nil {
		return source.Result{}, fmt.Errorf("list the operations in %s: %w", location, err)
	}

	out := clusterResult{Project: project, Cluster: cutCluster(c), Operations: clusterOperations(ops.Operations, name)}
	evidence := []source.Evidence{{
		Kind:  source.EvidenceResource,
		Title: fmt.Sprintf("GKE cluster %s in %s", name, project),
		URL:   console("/kubernetes/clusters/details/"+location+"/"+name+"/details", project),
	}}

	return structured(out, evidence, false, false, "")
}

// listClusters returns the clusters of project in location, or in every
// location for anyLocation.
func (s *Source) listClusters(ctx context.Context, project, location string) (source.Result, error) {
	var list struct {
		Clusters []cluster `json:"clusters"`
	}

	target := s.apis.container + "/projects/" + url.PathEscape(project) + "/locations/" + location + "/clusters"
	if err := s.client.get(ctx, target, nil, &list); err != nil {
		return source.Result{}, fmt.Errorf("list the clusters of %s: %w", project, err)
	}

	out := clustersResult{Project: project, Clusters: make([]clusterSummary, 0, len(list.Clusters))}

	for _, c := range list.Clusters {
		out.Clusters = append(out.Clusters, clusterSummary{
			Name:          c.Name,
			Location:      c.Location,
			Status:        c.Status,
			StatusMessage: source.CutText(c.StatusMessage, maxMessageLen),
			Version:       c.CurrentMasterVersion,
		})
	}

	evidence := []source.Evidence{{
		Kind:  source.EvidenceResource,
		Title: "GKE clusters in " + project,
		URL:   console("/kubernetes/list/overview", project),
	}}

	return structured(out, evidence, false, len(out.Clusters) == 0, "The project has no GKE clusters there.")
}

// clusterOperations returns the most recent operations whose target is the
// cluster name, newest first.
func clusterOperations(ops []operation, name string) []operation {
	suffix := "/clusters/" + name

	mine := make([]operation, 0, len(ops))

	for _, op := range ops {
		if strings.HasSuffix(op.TargetLink, suffix) || strings.Contains(op.TargetLink, suffix+"/") {
			op.StatusMessage = source.CutText(op.StatusMessage, maxMessageLen)
			mine = append(mine, op)
		}
	}

	slices.SortStableFunc(mine, func(a, b operation) int { return strings.Compare(b.StartTime, a.StartTime) })

	return mine[:min(len(mine), maxOperations)]
}

// cutCluster cuts the messages of a cluster and its node pools.
func cutCluster(c cluster) cluster {
	c.StatusMessage = source.CutText(c.StatusMessage, maxMessageLen)
	c.Conditions = cutConditions(c.Conditions)

	for i := range c.NodePools {
		c.NodePools[i].StatusMessage = source.CutText(c.NodePools[i].StatusMessage, maxMessageLen)
		c.NodePools[i].Conditions = cutConditions(c.NodePools[i].Conditions)
	}

	return c
}

// cutConditions cuts the message of each condition.
func cutConditions(conds []condition) []condition {
	for i := range conds {
		conds[i].Message = source.CutText(conds[i].Message, maxMessageLen)
	}

	return conds
}
