package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"k8s.io/client-go/kubernetes"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Tool names, as the source names them. The model sees them with the
// source prefix, for example k8s__list_events.
const (
	toolListEvents        = "list_events"
	toolGetWorkload       = "get_workload"
	toolListPodProblems   = "list_pod_problems"
	toolGetRolloutHistory = "get_rollout_history"
	toolGetPodLogs        = "get_pod_logs"
	toolListNodes         = "list_nodes"
)

// Limits of the tool inputs: the default and the most a call may ask for.
const (
	defaultEvents    = 50
	maxEvents        = 500
	defaultPods      = 50
	maxPods          = 200
	defaultRevisions = 10
	maxRevisions     = 50
	defaultLogLines  = 200
	maxLogLines      = 2000
	defaultNodes     = 100
	maxNodes         = 500
)

// Descriptions that several inputs share.
const (
	sinceDescription   = "Only what happened after this: an RFC 3339 time, or a duration back from now such as 30m or 2h."
	labelSelectorUsage = "A label selector, for example app=checkout or tier in (web,api)."
)

// definition is one tool and the function that runs it.
type definition struct {
	name        string
	description string
	schema      *jsonschema.Schema
	run         handler
}

// toolset returns the tools the config turns on, in a fixed order, and
// their handlers by name.
func (s *Source) toolset() ([]source.Tool, map[string]handler, error) {
	defs := []definition{
		{
			name: toolListEvents,
			description: "List Kubernetes events, newest first, for a namespace or for one object, " +
				"such as back-offs, failed scheduling, OOM kills, image pull errors and scaling.",
			schema: objectSchema(map[string]*jsonschema.Schema{
				"namespace": s.namespaceSchema("The namespace. Empty means every namespace Sidekick may read."),
				"kind":      stringSchema("Only events about objects of this kind, for example Pod or Deployment."),
				"name":      stringSchema("Only events about the object with this name."),
				"type":      enumSchema("Only events of this type.", "Normal", "Warning"),
				"since":     stringSchema(sinceDescription),
				"limit":     intSchema("The most events to return.", defaultEvents, maxEvents),
			}),
			run: decoded(s.listEvents),
		},
		{
			name: toolGetWorkload,
			description: "Get the status of a Deployment, StatefulSet or DaemonSet: replicas, conditions, " +
				"container images, rollout strategy and any horizontal pod autoscaler that scales it.",
			schema: objectSchema(map[string]*jsonschema.Schema{
				"namespace": s.namespaceSchema("The namespace of the workload."),
				"kind":      enumSchema("The kind of workload.", kindDeployment, kindStatefulSet, kindDaemonSet),
				"name":      stringSchema("The name of the workload."),
			}, "namespace", "kind", "name"),
			run: decoded(s.getWorkload),
		},
		{
			name: toolListPodProblems,
			description: "List pods that have problems: restarts, OOM kills, CrashLoopBackOff and other waiting " +
				"reasons, pods that cannot be scheduled, failed or evicted pods, and containers that are not ready.",
			schema: objectSchema(map[string]*jsonschema.Schema{
				"namespace":     s.namespaceSchema("The namespace. Empty means every namespace Sidekick may read."),
				"labelSelector": stringSchema(labelSelectorUsage),
				"limit":         intSchema("The most pods to return.", defaultPods, maxPods),
			}),
			run: decoded(s.listPodProblems),
		},
		{
			name: toolGetRolloutHistory,
			description: "Get the rollout history of a Deployment: its ReplicaSet revisions, newest first, with " +
				"creation times, images and what changed from the revision before, to see recent changes.",
			schema: objectSchema(map[string]*jsonschema.Schema{
				"namespace": s.namespaceSchema("The namespace of the Deployment."),
				"name":      stringSchema("The name of the Deployment."),
				"limit":     intSchema("The most revisions to return.", defaultRevisions, maxRevisions),
			}, "namespace", "name"),
			run: decoded(s.getRolloutHistory),
		},
	}

	if s.cfg.Logs {
		defs = append(defs, definition{
			name:        toolGetPodLogs,
			description: "Get the last lines of the logs of one container of a pod, with timestamps.",
			schema: objectSchema(map[string]*jsonschema.Schema{
				"namespace": s.namespaceSchema("The namespace of the pod."),
				"pod":       stringSchema("The name of the pod."),
				"container": stringSchema("The container. Required when the pod has more than one."),
				"tailLines": intSchema("How many lines to return, from the end.", defaultLogLines, maxLogLines),
				"previous":  boolSchema("Return the logs of the container's previous run, for example before a crash."),
				"since":     stringSchema(sinceDescription),
			}, "namespace", "pod"),
			run: decoded(s.getPodLogs),
		})
	}

	defs = append(defs, definition{
		name:        toolListNodes,
		description: "List nodes with their conditions (ready, memory, disk and PID pressure), cordons, taints, roles, kubelet versions and allocatable resources.",
		schema: objectSchema(map[string]*jsonschema.Schema{
			"name":         stringSchema("Only the node with this name."),
			"problemsOnly": boolSchema("Only nodes that are not ready, are under pressure or are cordoned."),
			"limit":        intSchema("The most nodes to return.", defaultNodes, maxNodes),
		}),
		run: decoded(s.listNodes),
	})

	tools := make([]source.Tool, 0, len(defs))
	handlers := make(map[string]handler, len(defs))

	for _, d := range defs {
		schema, err := json.Marshal(d.schema)
		if err != nil {
			return nil, nil, fmt.Errorf("encode the schema of %s: %w", d.name, err)
		}

		tools = append(tools, source.Tool{Name: d.name, Description: d.description, InputSchema: schema, ReadOnly: true})
		handlers[d.name] = d.run
	}

	return tools, handlers, nil
}

// namespaceSchema is a namespace input. When the config lists namespaces,
// the input may only be one of them.
func (s *Source) namespaceSchema(description string) *jsonschema.Schema {
	schema := stringSchema(description)
	for _, ns := range s.cfg.Namespaces {
		schema.Enum = append(schema.Enum, ns)
	}

	return schema
}

// decoded adapts a tool function with a typed input into a handler.
func decoded[I any](run func(ctx context.Context, c kubernetes.Interface, in I) (source.Result, error)) handler {
	return func(ctx context.Context, c kubernetes.Interface, raw json.RawMessage) (source.Result, error) {
		var in I

		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				return source.Result{}, &source.ToolError{Message: "the input does not match the tool's schema: " + err.Error()}
			}
		}

		return run(ctx, c, in)
	}
}

// objectSchema is a JSON Schema object with no other properties. Required
// string properties may not be empty.
func objectSchema(props map[string]*jsonschema.Schema, required ...string) *jsonschema.Schema {
	nonEmpty := 1

	for _, name := range required {
		if p := props[name]; p != nil && p.Type == "string" && len(p.Enum) == 0 {
			p.MinLength = &nonEmpty
		}
	}

	return &jsonschema.Schema{
		Type:                 "object",
		Properties:           props,
		Required:             required,
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}
}

// stringSchema is a string input.
func stringSchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Description: description}
}

// enumSchema is a string input with a fixed set of values.
func enumSchema(description string, values ...string) *jsonschema.Schema {
	schema := stringSchema(description)
	for _, v := range values {
		schema.Enum = append(schema.Enum, v)
	}

	return schema
}

// intSchema is an integer input from 1 to maxValue.
func intSchema(description string, defaultValue, maxValue int) *jsonschema.Schema {
	low, high := 1.0, float64(maxValue)

	return &jsonschema.Schema{
		Type:        "integer",
		Description: fmt.Sprintf("%s Default %d, at most %d.", description, defaultValue, maxValue),
		Minimum:     &low,
		Maximum:     &high,
	}
}

// boolSchema is a boolean input.
func boolSchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "boolean", Description: description}
}
