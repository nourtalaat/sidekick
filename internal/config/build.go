package config

import (
	"context"
	"errors"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Instances are the plugin instances built from a config, by name.
type Instances struct {
	Models  map[string]model.Connector
	Sources map[string]source.Source
	Intakes map[string]intake.Adapter
	Outputs map[string]output.Sink
	// SourceSettings are the shared settings of each built source, from
	// configs that embed source.Settings; other sources have the zero value,
	// which means the defaults.
	SourceSettings map[string]source.Settings
	// ModelSettings are the shared settings of each built connector, such
	// as its model ID, from configs that embed model.Settings; other
	// connectors have the zero value.
	ModelSettings map[string]model.Settings
}

// Build builds every configured plugin instance through the registries,
// calling each plugin's constructor. Run it on a config from Load. Start-up
// always runs it, and config validate runs it with --check-secrets, because
// constructors may read secrets and connect to services. Afterwards it
// checks the roles against the capabilities the built connectors report,
// for connectors whose config did not declare them. When deps.Secrets is nil
// it reads the files under the config's secrets.dir. deps.SlackBotToken is
// set from slack.botTokenSecret and deps.TeamsBot from the teams section.
// On problems it closes what it built and
// returns Errors.
func Build(ctx context.Context, cfg *Config, regs Registries, deps registry.Deps) (*Instances, error) {
	regs = regs.WithDefaults()
	if deps.Secrets == nil {
		deps.Secrets = secrets.NewDir(cfg.Secrets.Dir)
	}

	if cfg.Slack != nil {
		deps.SlackBotToken = cfg.Slack.BotTokenSecret
	}

	if t := cfg.Teams; t != nil {
		deps.TeamsBot = t.Bot()
	}

	var errs errorList

	connectors := make(map[string]Section, len(cfg.Model.Connectors))
	for name, c := range cfg.Model.Connectors {
		connectors[name] = c.Section
	}

	webhooks := make(map[string]Section, len(cfg.Intake.Webhooks))
	for name, w := range cfg.Intake.Webhooks {
		webhooks[name] = w.Section
	}

	in := &Instances{
		Models:  buildAll(ctx, regs.Models, connectors, deps, &errs),
		Sources: buildAll(ctx, regs.Sources, cfg.Sources, deps, &errs),
		Intakes: buildAll(ctx, regs.Intakes, webhooks, deps, &errs),
		Outputs: buildAll(ctx, regs.Outputs, cfg.Outputs, deps, &errs),
	}

	in.SourceSettings = sharedSettings(ctx, regs.Sources, cfg.Sources, in.Sources, source.Configured.SourceSettings, &errs)
	in.ModelSettings = sharedSettings(ctx, regs.Models, connectors, in.Models, model.Configured.ModelSettings, &errs)

	checkBuiltRoles(&errs, "model.roles", cfg.Model.Roles, in.Models)

	for i, r := range cfg.Routes {
		checkBuiltRoles(&errs, fmt.Sprintf("routes[%d].roles", i), r.Roles, in.Models)
	}

	if err := errs.err(); err != nil {
		if cerr := in.Close(); cerr != nil {
			return nil, errors.Join(err, fmt.Errorf("clean up after failed build: %w", cerr))
		}

		return nil, err
	}

	return in, nil
}

// Close closes the sources, the only instances that hold connections or
// child processes.
func (in *Instances) Close() error {
	var errs []error

	for _, name := range sortedKeys(in.Sources) {
		if err := in.Sources[name].Close(); err != nil {
			errs = append(errs, fmt.Errorf("close source %q: %w", name, err))
		}
	}

	return errors.Join(errs...)
}

// buildAll builds the instance of every section in name order, recording
// failures at the section's path.
func buildAll[T any](ctx context.Context, reg *registry.Registry[T], sections map[string]Section, deps registry.Deps, errs *errorList) map[string]T {
	out := make(map[string]T, len(sections))

	for _, name := range sortedKeys(sections) {
		s := sections[name]

		v, err := reg.Build(ctx, s.Type, name, s, deps)
		if err != nil {
			errs.addAll(pluginErrors(s.Path(), err))

			continue
		}

		out[name] = v
	}

	return out
}

// sharedSettings decodes the shared settings of each built instance from
// its section, without building it again. Configs that do not implement C
// get the zero value of S.
func sharedSettings[T, C, S any](ctx context.Context, reg *registry.Registry[T], sections map[string]Section, built map[string]T, get func(C) S, errs *errorList) map[string]S {
	out := make(map[string]S, len(built))

	for _, name := range sortedKeys(built) {
		s := sections[name]

		decoded, err := reg.Check(ctx, s.Type, name, s)
		if err != nil {
			errs.addAll(pluginErrors(s.Path(), err))

			continue
		}

		var settings S
		if c, ok := decoded.(C); ok {
			settings = get(c)
		}

		out[name] = settings
	}

	return out
}

// checkBuiltRoles checks the roles in a role map against the capabilities
// of the built connectors.
func checkBuiltRoles(errs *errorList, path string, roles map[model.Role]string, models map[string]model.Connector) {
	for _, role := range sortedKeys(roles) {
		if c, ok := models[roles[role]]; ok {
			checkRoleCaps(errs, joinPath(path, string(role)), role, roles[role], c.Capabilities())
		}
	}
}
