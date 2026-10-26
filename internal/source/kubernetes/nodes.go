package kubernetes

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/nourtalaat/sidekick/internal/source"
)

// roleLabelPrefix starts the labels that give a node its roles, such as
// node-role.kubernetes.io/control-plane.
const roleLabelPrefix = "node-role.kubernetes.io/"

// nodesInput is the input of list_nodes.
type nodesInput struct {
	Name         string `json:"name"`
	ProblemsOnly bool   `json:"problemsOnly"`
	Limit        int    `json:"limit"`
}

// nodesOutput is the structured result of list_nodes.
type nodesOutput struct {
	Nodes []nodeView `json:"nodes"`
	// Matched is how many nodes matched, before the limit.
	Matched int `json:"matched"`
	// Checked is how many nodes were looked at.
	Checked int `json:"checked"`
}

// nodeView is one node.
type nodeView struct {
	Name           string          `json:"name"`
	Ready          string          `json:"ready"`
	Unschedulable  bool            `json:"unschedulable,omitempty"`
	Roles          []string        `json:"roles,omitempty"`
	KubeletVersion string          `json:"kubeletVersion,omitempty"`
	Created        time.Time       `json:"created,omitzero"`
	Allocatable    resourceAmounts `json:"allocatable"`
	Taints         []string        `json:"taints,omitempty"`
	Conditions     []conditionView `json:"conditions,omitempty"`
	// Problems says what is wrong, one problem per entry.
	Problems []string `json:"problems,omitempty"`
}

// resourceAmounts are a node's CPU, memory and pod slots.
type resourceAmounts struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
	Pods   string `json:"pods,omitempty"`
}

// listNodes lists the nodes, or the one node the input names.
func (s *Source) listNodes(ctx context.Context, c kubernetes.Interface, in nodesInput) (source.Result, error) {
	var out nodesOutput

	if in.Name != "" {
		n, err := c.CoreV1().Nodes().Get(ctx, in.Name, metav1.GetOptions{})
		if err != nil {
			return source.Result{}, apiError("get", resNodes, "", err)
		}

		out.Checked = 1
		out.Nodes = matchNode(nil, n, in.ProblemsOnly)
	} else {
		err := eachPage(ctx, metav1.ListOptions{}, c.CoreV1().Nodes().List, func(page *corev1.NodeList) {
			out.Checked += len(page.Items)

			for i := range page.Items {
				out.Nodes = matchNode(out.Nodes, &page.Items[i], in.ProblemsOnly)
			}
		})
		if err != nil {
			return source.Result{}, apiError("list", resNodes, "", err)
		}
	}

	slices.SortFunc(out.Nodes, func(a, b nodeView) int {
		return cmp.Or(cmp.Compare(len(b.Problems), len(a.Problems)), cmp.Compare(a.Name, b.Name))
	})

	out.Matched = len(out.Nodes)
	out.Nodes = out.Nodes[:min(len(out.Nodes), limit(in.Limit, defaultNodes, maxNodes))]

	objects := make([]object, 0, len(out.Nodes))
	for _, n := range out.Nodes {
		objects = append(objects, object{Kind: kindNode, Resource: resourceOf(kindNode), Name: n.Name})
	}

	command := kubectl("get", "nodes", "-o", "wide")
	if in.Name != "" {
		command = kubectl("describe", "node", in.Name)
	}

	evidence := s.links.evidence(source.EvidenceResource, objects, command)

	if len(out.Nodes) == 0 {
		text := fmt.Sprintf("No node has a problem (%s checked).\n", plural(out.Checked, "node"))
		if !in.ProblemsOnly {
			text = "The cluster lists no nodes.\n"
		}

		return source.Result{Text: text, NoData: true, Evidence: evidence}, nil
	}

	return result(nodesText(out), out, evidence, false)
}

// matchNode appends the node's view to views, unless problemsOnly is set
// and the node has no problem.
func matchNode(views []nodeView, n *corev1.Node, problemsOnly bool) []nodeView {
	v := nodeView{
		Name:           n.Name,
		Ready:          string(corev1.ConditionUnknown),
		Unschedulable:  n.Spec.Unschedulable,
		KubeletVersion: n.Status.NodeInfo.KubeletVersion,
		Created:        n.CreationTimestamp.Time,
		Allocatable: resourceAmounts{
			CPU:    quantity(n.Status.Allocatable, corev1.ResourceCPU),
			Memory: quantity(n.Status.Allocatable, corev1.ResourceMemory),
			Pods:   quantity(n.Status.Allocatable, corev1.ResourcePods),
		},
	}

	for label := range n.Labels {
		if role, ok := strings.CutPrefix(label, roleLabelPrefix); ok && role != "" {
			v.Roles = append(v.Roles, role)
		}
	}

	slices.Sort(v.Roles)

	for _, t := range n.Spec.Taints {
		v.Taints = append(v.Taints, joinNonEmpty("=", t.Key, t.Value)+":"+string(t.Effect))
	}

	readyReported := false

	for _, c := range n.Status.Conditions {
		v.Conditions = append(v.Conditions, conditionView{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message, LastTransition: c.LastTransitionTime.Time,
		})

		// Ready should be True; every other node condition, such as
		// MemoryPressure, is a problem when it is True.
		bad := c.Status == corev1.ConditionTrue
		if c.Type == corev1.NodeReady {
			v.Ready, readyReported = string(c.Status), true
			bad = c.Status != corev1.ConditionTrue
		}

		if bad {
			v.Problems = append(v.Problems, joinNonEmpty(": ", string(c.Type)+"="+string(c.Status), c.Message))
		}
	}

	if !readyReported {
		v.Problems = append(v.Problems, "Ready: not reported")
	}

	if v.Unschedulable {
		v.Problems = append(v.Problems, "cordoned: new pods are not scheduled here")
	}

	if problemsOnly && len(v.Problems) == 0 {
		return views
	}

	return append(views, v)
}

// nodesText renders the nodes, those with problems first.
func nodesText(out nodesOutput) string {
	var b strings.Builder

	if out.Matched > len(out.Nodes) {
		fmt.Fprintf(&b, "%d of %s (%s checked), those with problems first:\n", len(out.Nodes), plural(out.Matched, "matching node"), plural(out.Checked, "node"))
	} else {
		fmt.Fprintf(&b, "%s (%s checked), those with problems first:\n", plural(out.Matched, "matching node"), plural(out.Checked, "node"))
	}

	for _, n := range out.Nodes {
		fmt.Fprintf(&b, "%s Ready=%s", n.Name, n.Ready)

		if len(n.Roles) > 0 {
			fmt.Fprintf(&b, ", roles %s", strings.Join(n.Roles, ","))
		}

		a := n.Allocatable
		fmt.Fprintf(&b, ", kubelet %s, allocatable cpu %s, memory %s, pods %s, created %s\n",
			cmp.Or(n.KubeletVersion, "unknown"), cmp.Or(a.CPU, "unknown"), cmp.Or(a.Memory, "unknown"), cmp.Or(a.Pods, "unknown"), timestamp(n.Created))

		if len(n.Taints) > 0 {
			fmt.Fprintf(&b, "  Taints: %s\n", strings.Join(n.Taints, ", "))
		}

		for _, p := range n.Problems {
			fmt.Fprintf(&b, "  Problem: %s\n", p)
		}
	}

	return b.String()
}

// quantity returns the amount of one resource, or empty when it is not set.
func quantity(list corev1.ResourceList, name corev1.ResourceName) string {
	q, ok := list[name]
	if !ok {
		return ""
	}

	return q.String()
}
