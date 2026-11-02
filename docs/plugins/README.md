# Extending Sidekick

Sidekick has five kinds of plugin. Each one is a Go package that registers
itself with the plugin registry, and each has a guide that walks through
the steps and a small example that CI builds and tests.

## Guides

- [Writing a source](sources.md): gives investigations tools to call. Example: [`source-hello`](../../examples/source-hello).
- [Writing an intake adapter](intake-adapters.md): reads the webhook payload of one kind of sender. Example: [`intake-uptime`](../../examples/intake-uptime).
- [Writing a model connector](model-connectors.md): talks to one kind of model provider. Example: [`model-echo`](../../examples/model-echo).
- [Writing an output](outputs.md): shows investigations somewhere people look. Example: [`output-board`](../../examples/output-board).
- [Writing an action](actions.md): a change Sidekick may make outside itself. Example: [`action-note`](../../examples/action-note).

## Related

- [`internal/plugins/plugins.go`](../../internal/plugins/plugins.go): the list of built-in plugins that every build links in.
- [Package dependencies](../../CONTRIBUTING.md#package-dependencies): which plugin packages may import which.
- [Integrations](../integrations.md): the plugins that ship with Sidekick.
