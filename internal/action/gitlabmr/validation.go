package gitlabmr

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// validateFiles checks the changes with the validator and counts each
// validation in the metrics.
func (m *MR) validateFiles(ctx context.Context, r *gitlabgit.Reader, changes []gitchange.PlannedFile) error {
	if err := gitchange.ValidateFiles(ctx, m.validator, r, changes, m.metrics.validated); err != nil {
		return fmt.Errorf("run the validator: %w", err)
	}

	return nil
}
