package doctor

import (
	"context"
	"slices"

	"github.com/nourtalaat/sidekick/internal/model"
)

// checkToolChoice warns about each connector that fills the investigate
// role, in model.roles or in a route, but cannot be forced to call a tool.
// Its investigations then rely on a reminder to end with submit_verdict.
func (d *doctor) checkToolChoice(ctx context.Context) []Result {
	var results []Result

	for _, name := range d.investigateConnectors() {
		c, ok := d.cfg.Model.Connectors[name]
		if !ok {
			continue
		}

		decoded, err := d.regs.Models.Check(ctx, c.Type, name, c.Section)
		if err != nil {
			continue
		}

		declarer, ok := decoded.(model.CapabilityDeclarer)
		if !ok || declarer.Capabilities().ToolChoice {
			continue
		}

		results = append(results, Result{
			Check: "tool choice " + name, Status: StatusWarn,
			Message: "the connector fills the investigate role but cannot be forced to call a tool, so each investigation relies on a reminder to end with submit_verdict",
			Fix:     toolChoiceFix(decoded),
		})
	}

	return results
}

// investigateConnectors returns the names of the connectors that fill the
// investigate role in model.roles or in any route, sorted.
func (d *doctor) investigateConnectors() []string {
	var names []string

	add := func(roles map[model.Role]string) {
		if name := roles[model.RoleInvestigate]; name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}

	add(d.cfg.Model.Roles)

	for _, r := range d.cfg.Routes {
		add(r.Roles)
	}

	slices.Sort(names)

	return names
}

// toolChoiceFix is the fix for a connector without forced tool choice. A
// local model preset gets it on vLLM; any other model needs its
// capabilities block to say the server honours it.
func toolChoiceFix(decoded any) string {
	if _, ok := openAIPreset(decoded); ok {
		return "serve the model with vLLM, started with --enable-auto-tool-choice, and set `server: vllm` on the connector; investigations still work without it"
	}

	return "if the model and its server honour a forced tool choice, set `capabilities.toolChoice: true` on the connector; otherwise use a model that does for the investigate role"
}
