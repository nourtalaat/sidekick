package doctor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
)

// checkModels builds each model connector and checks its API key with a
// call that costs nothing.
func (d *doctor) checkModels(ctx context.Context) []Result {
	connectors := d.cfg.Model.Connectors
	if len(connectors) == 0 {
		return []Result{{Check: "models", Status: StatusSkip, Message: "no model connectors"}}
	}

	results := make([]Result, 0, len(connectors))

	for _, name := range slices.Sorted(maps.Keys(connectors)) {
		results = append(results, d.checkModel(ctx, name))
	}

	return results
}

// checkPrices warns about each connector whose model has no price: its
// calls cost nothing as far as the cost budgets and daily caps can tell.
func (d *doctor) checkPrices(ctx context.Context) []Result {
	models := map[string]string{}

	for name, c := range d.cfg.Model.Connectors {
		decoded, err := d.regs.Models.Check(ctx, c.Type, name, c.Section)
		if err != nil {
			continue
		}

		if configured, ok := decoded.(model.Configured); ok {
			models[name] = configured.ModelSettings().Model
		}
	}

	if embeddings := d.cfg.Memory.Vector; embeddings.Enabled {
		decoded, err := d.regs.Models.Check(ctx, embeddings.Embeddings.Type, config.EmbeddingsName, embeddings.Embeddings.Section)
		if configured, ok := decoded.(model.Configured); err == nil && ok {
			models[config.EmbeddingsName] = configured.ModelSettings().Model
		}
	}

	unpriced := pricing.New(d.cfg.Model.PriceTable()).Unpriced(models)
	if len(unpriced) == 0 {
		if len(models) == 0 {
			return nil
		}

		return []Result{{Check: "prices", Status: StatusOK, Message: "every configured model has a price"}}
	}

	results := make([]Result, 0, len(unpriced))
	for _, u := range unpriced {
		if u.Connector == config.EmbeddingsName {
			results = append(results, Result{
				Check: "price " + u.Connector, Status: StatusWarn,
				Message: fmt.Sprintf("the embedding model %s has no price, so its calls show no cost in the metrics", u.Model),
				Fix:     fmt.Sprintf("add model.prices.%q with inputPerMTok, in US dollars per million tokens", u.Model),
			})

			continue
		}

		results = append(results, Result{
			Check: "price " + u.Connector, Status: StatusWarn,
			Message: fmt.Sprintf("the model %s has no price, so its calls show no cost and count nothing toward the cost budgets and daily caps", u.Model),
			Fix:     fmt.Sprintf("add model.prices.%q with inputPerMTok and outputPerMTok, in US dollars per million tokens", u.Model),
		})
	}

	return results
}

// checkModel builds one connector and checks its key.
func (d *doctor) checkModel(ctx context.Context, name string) Result {
	check := "model " + name
	c := d.cfg.Model.Connectors[name]

	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	connector, err := d.regs.Models.Build(ctx, c.Type, name, c.Section, d.deps())
	if err != nil {
		return Result{
			Check: check, Status: StatusFail,
			Message: "cannot set up the connector: " + err.Error(),
			Fix:     "mount every secret the connector names; `sidekick config validate --check-secrets -f <file>` points at the field",
		}
	}

	checker, ok := connector.(model.KeyChecker)
	if !ok {
		return Result{Check: check, Status: StatusSkip, Message: fmt.Sprintf("the %s provider has no API key check", c.Type)}
	}

	err = checker.CheckKey(ctx)
	if err == nil {
		return Result{Check: check, Status: StatusOK, Message: fmt.Sprintf("the %s API accepted the key", c.Type)}
	}

	if merr, isModel := errors.AsType[*model.Error](err); isModel && merr.Kind == model.ErrAuth {
		return Result{
			Check: check, Status: StatusFail,
			Message: "the API key was rejected or cannot be read: " + err.Error(),
			Fix:     fmt.Sprintf("put a valid %s API key in the Secret the connector's apiKeySecret names, and check the account's billing", c.Type),
		}
	}

	return Result{
		Check: check, Status: StatusFail,
		Message: "cannot check the key: " + err.Error(),
		Fix:     fmt.Sprintf("check that Sidekick's pod can reach the %s API, through any network policy or proxy", c.Type),
	}
}
