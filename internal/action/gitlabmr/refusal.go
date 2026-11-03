package gitlabmr

import (
	"errors"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// fileRefusals are the answers of the reader about a file or a directory that
// the action will not change or read, whatever the model proposes.
var fileRefusals = []error{
	gitlabgit.ErrNotFile,
	gitlabgit.ErrTooBig,
	gitlabgit.ErrNotText,
	gitlabgit.ErrUnsupportedHash,
	gitlabgit.ErrTooManyEntries,
}

// readError wraps an error of reading files at the base with what was being
// done. One of the reader's answers about a file is a refusal, which the model
// reads with its reason, so it also matches action.ErrLimit and
// action.ErrPermanent. Any other error, such as a failed call, is left as it
// is, so Sidekick tries again later.
func readError(doing string, err error) error {
	wrapped := fmt.Errorf("%s: %w", doing, err)

	for _, refusal := range fileRefusals {
		if errors.Is(err, refusal) {
			return gitchange.Refuse(wrapped)
		}
	}

	return wrapped
}
