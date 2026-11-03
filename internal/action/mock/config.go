package mock

import "errors"

// Config is one mock action: a scenario's entry under actions.
type Config struct {
	// Spec says where the action's description and schema come from.
	Spec Spec `yaml:"spec"`
}

// Spec says where the spec of the action comes from.
type Spec struct {
	// Snapshot is a JSON file of the form {"name", "description",
	// "paramsSchema"}, as the files in bench/actions are.
	Snapshot string `yaml:"snapshot"`
}

// Validate checks the config without reading the snapshot. Each error
// starts with the key path of its field.
func (c Config) Validate() error {
	if c.Spec.Snapshot == "" {
		return errors.New("spec.snapshot: required")
	}

	return nil
}
