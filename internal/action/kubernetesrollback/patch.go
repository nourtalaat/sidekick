package kubernetesrollback

import (
	"encoding/json"
	"errors"
	"fmt"
)

// replaceDirective is the strategic merge patch directive that replaces a
// map whole instead of merging into it.
const replaceDirective = `"replace"`

// patchBody returns the strategic merge patch that gives the workload w the
// pod template of revision r, with the resourceVersion of w so the API
// server refuses it when the workload changed since it was read.
//
// A Deployment gets the template of the ReplicaSet without the
// pod-template-hash label, which revision already has left out. A
// StatefulSet or DaemonSet gets the data of its ControllerRevision, which is
// already such a patch.
func patchBody(w workload, r revision) ([]byte, error) {
	var spec json.RawMessage

	if w.kind == kindDeployment {
		template, err := templateReplacement(r)
		if err != nil {
			return nil, fmt.Errorf("make the template patch: %w", err)
		}

		spec = template
	} else {
		stored := map[string]json.RawMessage{}
		if err := json.Unmarshal(r.data, &stored); err != nil {
			return nil, fmt.Errorf("read the stored data: %w", err)
		}

		if len(stored["spec"]) == 0 {
			return nil, errors.New("the stored data has no spec")
		}

		spec = stored["spec"]
	}

	body, err := json.Marshal(map[string]any{
		"metadata": map[string]string{"resourceVersion": w.version},
		"spec":     spec,
	})
	if err != nil {
		return nil, fmt.Errorf("encode the patch: %w", err)
	}

	return body, nil
}

// templateReplacement returns {"template": {...}} with the pod template of r
// and the directive that makes the API server replace the template whole.
func templateReplacement(r revision) (json.RawMessage, error) {
	raw, err := json.Marshal(r.template)
	if err != nil {
		return nil, fmt.Errorf("encode the pod template: %w", err)
	}

	template := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &template); err != nil {
		return nil, fmt.Errorf("read the pod template: %w", err)
	}

	template["$patch"] = json.RawMessage(replaceDirective)

	out, err := json.Marshal(map[string]any{"template": template})
	if err != nil {
		return nil, fmt.Errorf("encode the template patch: %w", err)
	}

	return out, nil
}
