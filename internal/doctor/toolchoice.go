package doctor

import (
	"context"
	"slices"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/openai"
)

// checkToolChoice warns about each OpenAI-compatible connector that fills
// the investigate role, in model.roles or in a route, but cannot be forced
// to call a tool. Its investigations then rely on a reminder to end with
// submit_verdict. Other providers are left out: whether their models take
// a forced tool choice depends on the model alone, not on a server the
// user can change, and the default Claude model takes none.
func (d *doctor) checkToolChoice(ctx context.Context) []Result {
	var results []Result

	for _, name := range d.investigateConnectors() {
		c, ok := d.cfg.Model.Connectors[name]
		if !ok || c.Type != openai.Provider {
			continue
		}

		decoded, err := d.regs.Models.Check(ctx, c.Type, name, c.Section)
		if err != nil {
			continue
		}

		cfg, ok := decoded.(*openai.Config)
		if !ok || cfg.Capabilities().ToolChoice {
			continue
		}

		results = append(results, Result{
			Check: "tool choice " + name, Status: StatusWarn,
			Message: "the connector fills the investigate role but cannot be forced to call a tool, so each investigation relies on a reminder to end with submit_verdict",
			Fix:     toolChoiceFix(cfg),
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
func toolChoiceFix(cfg *openai.Config) string {
	if _, ok := openAIPreset(cfg); ok {
		return "serve the model with vLLM, started with --enable-auto-tool-choice, and set `server: vllm` on the connector; investigations still work without it"
	}

	return "if the model and its server honour a forced tool choice, set `capabilities.toolChoice: true` on the connector; otherwise use a model that does for the investigate role"
}
