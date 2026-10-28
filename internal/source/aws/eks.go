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
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Bounds of get_eks_cluster.
const (
	// maxClusters is the most cluster names one list holds, one page of
	// ListClusters.
	maxClusters = 100
	// maxNodegroups is the most node groups get_eks_cluster describes.
	maxNodegroups = 10
	// maxIssues bounds the health issues of a cluster or a node group.
	maxIssues = 10
)

// eksNamePattern is the shape of an EKS cluster name. ARNs are not taken,
// since one can name another account.
var eksNamePattern = regexp.MustCompile(`^[0-9A-Za-z][A-Za-z0-9_-]{0,99}$`)

// eksCluster is the result of get_eks_cluster for one cluster.
type eksCluster struct {
	Account         string         `json:"account"`
	Region          string         `json:"region"`
	Name            string         `json:"name"`
	Status          string         `json:"status"`
	Version         string         `json:"version,omitempty"`
	PlatformVersion string         `json:"platformVersion,omitempty"`
	SupportType     string         `json:"supportType,omitempty"`
	Created         time.Time      `json:"created,omitzero"`
	Issues          []eksIssue     `json:"issues,omitempty"`
	Nodegroups      []eksNodegroup `json:"nodegroups"`
	URL             string         `json:"url"`
	NodegroupsCut   bool           `json:"nodegroupsCut,omitempty"`
}

// eksNodegroup is one managed node group of a cluster.
type eksNodegroup struct {
	Name           string     `json:"name"`
	Status         string     `json:"status"`
	Version        string     `json:"version,omitempty"`
	ReleaseVersion string     `json:"releaseVersion,omitempty"`
	CapacityType   string     `json:"capacityType,omitempty"`
	InstanceTypes  []string   `json:"instanceTypes,omitempty"`
	Desired        *int32     `json:"desired,omitempty"`
	Min            *int32     `json:"min,omitempty"`
	Max            *int32     `json:"max,omitempty"`
	Modified       time.Time  `json:"modified,omitzero"`
	Issues         []eksIssue `json:"issues,omitempty"`
}

// eksIssue is one health issue EKS reports.
type eksIssue struct {
	Code      string   `json:"code"`
	Message   string   `json:"message,omitempty"`
	Resources []string `json:"resources,omitempty"`
}

// eksClusters is the result of get_eks_cluster without cluster.
type eksClusters struct {
	Account  string   `json:"account"`
	Region   string   `json:"region"`
	Clusters []string `json:"clusters"`
}

// getEKSCluster runs get_eks_cluster.
func (s *Source) getEKSCluster(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Account string `json:"account"`
		Region  string `json:"region"`
		Cluster string `json:"cluster"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	t, err := s.target(in.Account, in.Region)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	name := strings.TrimSpace(in.Cluster)
	if name != "" && !eksNamePattern.MatchString(name) {
		return source.Result{}, &source.ToolError{Message: fmt.Sprintf("cluster: %q is not an EKS cluster name", source.CutText(name, maxNameLen))}
	}

	if err := t.account.ready(ctx); err != nil {
		return source.Result{}, fmt.Errorf("get ready: %w", err)
	}

	client := s.eks(t)

	if name == "" {
		return listEKSClusters(ctx, client, t)
	}

	return describeEKSCluster(ctx, client, t, name)
}

// listEKSClusters returns the names of the clusters of the call's account
// and Region.
func listEKSClusters(ctx context.Context, client *eks.Client, t target) (source.Result, error) {
	answer, err := client.ListClusters(ctx, &eks.ListClustersInput{MaxResults: awssdk.Int32(maxClusters)})
	if err != nil {
		return source.Result{}, fmt.Errorf("list the EKS clusters of account %s, Region %s: %w", t.account.id, t.region, err)
	}

	out := eksClusters{Account: t.account.id, Region: t.region, Clusters: make([]string, 0, len(answer.Clusters))}
	for _, c := range answer.Clusters {
		out.Clusters = append(out.Clusters, source.CutText(c, maxNameLen))
	}

	where := fmt.Sprintf("account %s, Region %s", t.account.id, t.region)
	evidence := []source.Evidence{{Kind: source.EvidenceResource, Title: "EKS clusters in " + where, URL: consoleURL(t.region, "eks", "/clusters")}}

	return structured(out, evidence, answer.NextToken != nil, len(out.Clusters) == 0, "No EKS cluster in "+where+".")
}

// describeEKSCluster returns one cluster and its node groups.
func describeEKSCluster(ctx context.Context, client *eks.Client, t target, name string) (source.Result, error) {
	answer, err := client.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: awssdk.String(name)})
	if err != nil {
		return source.Result{}, fmt.Errorf("describe EKS cluster %s: %w", name, err)
	}

	c := answer.Cluster
	if c == nil {
		return source.Result{NoData: true, Text: notFoundText}, nil
	}

	out := eksCluster{
		Account:         t.account.id,
		Region:          t.region,
		Name:            name,
		Status:          string(c.Status),
		Version:         awssdk.ToString(c.Version),
		PlatformVersion: awssdk.ToString(c.PlatformVersion),
		Created:         awssdk.ToTime(c.CreatedAt).UTC(),
		Nodegroups:      []eksNodegroup{},
		URL:             consoleURL(t.region, "eks", "/clusters/"+url.PathEscape(name)),
	}

	if c.UpgradePolicy != nil {
		out.SupportType = string(c.UpgradePolicy.SupportType)
	}

	if c.Health != nil {
		for _, issue := range c.Health.Issues[:min(len(c.Health.Issues), maxIssues)] {
			out.Issues = append(out.Issues, newEKSIssue(string(issue.Code), issue.Message, issue.ResourceIds))
		}
	}

	groups, err := client.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: awssdk.String(name), MaxResults: awssdk.Int32(maxClusters)})
	if err != nil {
		return source.Result{}, fmt.Errorf("list the node groups of EKS cluster %s: %w", name, err)
	}

	out.NodegroupsCut = len(groups.Nodegroups) > maxNodegroups || groups.NextToken != nil

	for _, g := range groups.Nodegroups[:min(len(groups.Nodegroups), maxNodegroups)] {
		ng, err := client.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: awssdk.String(name), NodegroupName: awssdk.String(g)})
		if err != nil {
			return source.Result{}, fmt.Errorf("describe node group %s of EKS cluster %s: %w", g, name, err)
		}

		if ng.Nodegroup != nil {
			out.Nodegroups = append(out.Nodegroups, nodegroupOf(ng.Nodegroup))
		}
	}

	evidence := []source.Evidence{{Kind: source.EvidenceResource, Title: "EKS cluster " + name, URL: out.URL}}

	return structured(out, evidence, out.NodegroupsCut, false, "")
}

// nodegroupOf returns the part of a node group the result keeps.
func nodegroupOf(g *types.Nodegroup) eksNodegroup {
	out := eksNodegroup{
		Name:           source.CutText(awssdk.ToString(g.NodegroupName), maxNameLen),
		Status:         string(g.Status),
		Version:        awssdk.ToString(g.Version),
		ReleaseVersion: awssdk.ToString(g.ReleaseVersion),
		CapacityType:   string(g.CapacityType),
		InstanceTypes:  g.InstanceTypes,
		Modified:       awssdk.ToTime(g.ModifiedAt).UTC(),
	}

	if sc := g.ScalingConfig; sc != nil {
		out.Desired, out.Min, out.Max = sc.DesiredSize, sc.MinSize, sc.MaxSize
	}

	if g.Health != nil {
		for _, issue := range g.Health.Issues[:min(len(g.Health.Issues), maxIssues)] {
			out.Issues = append(out.Issues, newEKSIssue(string(issue.Code), issue.Message, issue.ResourceIds))
		}
	}

	return out
}

// newEKSIssue returns a health issue with its text cut.
func newEKSIssue(code string, message *string, resources []string) eksIssue {
	issue := eksIssue{Code: code, Message: source.CutText(awssdk.ToString(message), maxMessageLen)}
	for _, r := range resources[:min(len(resources), maxIssues)] {
		issue.Resources = append(issue.Resources, source.CutText(r, maxNameLen))
	}

	return issue
}
