package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/nourtalaat/sidekick/internal/source"
)

// managedCluster is the part of an AKS cluster the source reads. It never
// reads credentials: the source has no call that lists them.
type managedCluster struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Location   string `json:"location"`
	Properties struct {
		ProvisioningState        string     `json:"provisioningState"`
		PowerState               powerState `json:"powerState"`
		KubernetesVersion        string     `json:"kubernetesVersion"`
		CurrentKubernetesVersion string     `json:"currentKubernetesVersion"`
		NodeResourceGroup        string     `json:"nodeResourceGroup"`
		AutoUpgradeProfile       *struct {
			UpgradeChannel       string `json:"upgradeChannel"`
			NodeOSUpgradeChannel string `json:"nodeOSUpgradeChannel"`
		} `json:"autoUpgradeProfile"`
		AgentPoolProfiles []struct {
			Name                       string     `json:"name"`
			Mode                       string     `json:"mode"`
			Count                      int        `json:"count"`
			VMSize                     string     `json:"vmSize"`
			OSType                     string     `json:"osType"`
			ProvisioningState          string     `json:"provisioningState"`
			PowerState                 powerState `json:"powerState"`
			OrchestratorVersion        string     `json:"orchestratorVersion"`
			CurrentOrchestratorVersion string     `json:"currentOrchestratorVersion"`
			EnableAutoScaling          bool       `json:"enableAutoScaling"`
			MinCount                   *int       `json:"minCount"`
			MaxCount                   *int       `json:"maxCount"`
		} `json:"agentPoolProfiles"`
	} `json:"properties"`
}

// powerState says whether a cluster or node pool is running or stopped.
type powerState struct {
	Code string `json:"code"`
}

// poolItem is one node pool in a result.
type poolItem struct {
	Name          string `json:"name"`
	Mode          string `json:"mode,omitempty"`
	Count         int    `json:"count"`
	VMSize        string `json:"vmSize,omitempty"`
	OSType        string `json:"osType,omitempty"`
	State         string `json:"provisioningState,omitempty"`
	Power         string `json:"powerState,omitempty"`
	Version       string `json:"version,omitempty"`
	Autoscaling   bool   `json:"autoscaling,omitempty"`
	MinCount      *int   `json:"minCount,omitempty"`
	MaxCount      *int   `json:"maxCount,omitempty"`
	TargetVersion string `json:"targetVersion,omitempty"`
}

// clusterItem is one cluster in a result.
type clusterItem struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Location          string     `json:"location,omitempty"`
	State             string     `json:"provisioningState,omitempty"`
	Power             string     `json:"powerState,omitempty"`
	Version           string     `json:"version,omitempty"`
	TargetVersion     string     `json:"targetVersion,omitempty"`
	UpgradeChannel    string     `json:"upgradeChannel,omitempty"`
	NodeOSChannel     string     `json:"nodeOSUpgradeChannel,omitempty"`
	NodeResourceGroup string     `json:"nodeResourceGroup,omitempty"`
	NodePools         []poolItem `json:"nodePools,omitempty"`
}

// clustersResult is the result of get_aks_cluster without a cluster.
type clustersResult struct {
	Subscription string        `json:"subscription"`
	Clusters     []clusterItem `json:"clusters"`
}

// getAKSCluster runs get_aks_cluster.
func (s *Source) getAKSCluster(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Subscription  string `json:"subscription"`
		ResourceGroup string `json:"resourceGroup"`
		Cluster       string `json:"cluster"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	sub, err := s.subscription(in.Subscription)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	group := strings.TrimSpace(in.ResourceGroup)
	name := strings.TrimSpace(in.Cluster)

	if name == "" {
		return s.listAKSClusters(ctx, sub)
	}

	if group == "" {
		return source.Result{}, &source.ToolError{Message: "resourceGroup: required with cluster"}
	}

	if err := checkGroup(group); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if !clusterPattern.MatchString(name) {
		return source.Result{}, &source.ToolError{Message: fmt.Sprintf("cluster: %q is not an AKS cluster name", source.CutText(name, maxNameLen))}
	}

	id := "/subscriptions/" + sub + "/resourceGroups/" + group + "/providers/Microsoft.ContainerService/managedClusters/" + name

	var c managedCluster
	if err := s.client.get(ctx, armScope, s.apis.arm+id, query(aksVersion, nil), &c); err != nil {
		return source.Result{}, fmt.Errorf("get cluster %s: %w", name, err)
	}

	evidence := []source.Evidence{{Kind: source.EvidenceResource, Title: "AKS cluster " + name, URL: portal(id, "/overview")}}

	return structured(clusterOf(c, true), evidence, false, false, "")
}

// listAKSClusters returns the clusters of a subscription, without their
// node pools.
func (s *Source) listAKSClusters(ctx context.Context, sub string) (source.Result, error) {
	var list struct {
		Value    []managedCluster `json:"value"`
		NextLink string           `json:"nextLink"`
	}

	target := s.apis.arm + "/subscriptions/" + url.PathEscape(sub) + "/providers/Microsoft.ContainerService/managedClusters"
	if err := s.client.get(ctx, armScope, target, query(aksVersion, nil), &list); err != nil {
		return source.Result{}, fmt.Errorf("list the AKS clusters of %s: %w", sub, err)
	}

	out := clustersResult{Subscription: sub, Clusters: make([]clusterItem, 0, len(list.Value))}
	b := budget{}

	for _, c := range list.Value {
		item := clusterOf(c, false)
		if !b.fits(item) {
			break
		}

		out.Clusters = append(out.Clusters, item)
	}

	evidence := []source.Evidence{{
		Kind:  source.EvidenceResource,
		Title: "AKS clusters of subscription " + sub,
		URL:   portalURL + "/#browse/Microsoft.ContainerService%2FmanagedClusters",
	}}

	return structured(out, evidence, list.NextLink != "" || b.cut, len(out.Clusters) == 0, "The subscription has no AKS clusters.")
}

// clusterOf returns the result item of a cluster, with its node pools
// when pools is true.
func clusterOf(c managedCluster, pools bool) clusterItem {
	p := c.Properties
	item := clusterItem{
		ID:                c.ID,
		Name:              c.Name,
		Location:          c.Location,
		State:             p.ProvisioningState,
		Power:             p.PowerState.Code,
		Version:           p.CurrentKubernetesVersion,
		NodeResourceGroup: p.NodeResourceGroup,
	}

	if p.KubernetesVersion != p.CurrentKubernetesVersion {
		item.TargetVersion = p.KubernetesVersion
	}

	if p.AutoUpgradeProfile != nil {
		item.UpgradeChannel = p.AutoUpgradeProfile.UpgradeChannel
		item.NodeOSChannel = p.AutoUpgradeProfile.NodeOSUpgradeChannel
	}

	if !pools {
		return item
	}

	for _, ap := range p.AgentPoolProfiles {
		pool := poolItem{
			Name:        ap.Name,
			Mode:        ap.Mode,
			Count:       ap.Count,
			VMSize:      ap.VMSize,
			OSType:      ap.OSType,
			State:       ap.ProvisioningState,
			Power:       ap.PowerState.Code,
			Version:     ap.CurrentOrchestratorVersion,
			Autoscaling: ap.EnableAutoScaling,
			MinCount:    ap.MinCount,
			MaxCount:    ap.MaxCount,
		}

		if ap.OrchestratorVersion != ap.CurrentOrchestratorVersion {
			pool.TargetVersion = ap.OrchestratorVersion
		}

		item.NodePools = append(item.NodePools, pool)
	}

	return item
}
