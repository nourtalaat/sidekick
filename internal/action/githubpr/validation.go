package githubpr

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// validateFiles checks the changes with the validator and counts each
// validation in the metrics.
func (p *PR) validateFiles(ctx context.Context, r *reader, changes []gitchange.PlannedFile) error {
	if err := gitchange.ValidateFiles(ctx, p.validator, r, changes, p.metrics.validated); err != nil {
		return fmt.Errorf("run the validator: %w", err)
	}

	return nil
}
