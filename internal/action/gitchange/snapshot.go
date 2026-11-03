package gitchange

import (
	"encoding/json"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action"
)

// The statuses of a file in the snapshot of a pull request or merge request,
// as an approver reads them.
const (
	FileChanged = "changed"
	FileAdded   = "added"
	FileDeleted = "deleted"
)

// BlobRef names the content of a file by its Git blob SHA.
type BlobRef struct {
	Blob string `json:"blob"`
}

// AfterEntry is a file as the pull request leaves it. A deleted file has no
// blob.
type AfterEntry struct {
	Blob         string `json:"blob,omitempty"`
	Status       string `json:"status"`
	LinesAdded   *int   `json:"linesAdded,omitempty"`
	LinesRemoved *int   `json:"linesRemoved,omitempty"`
}

// SnapshotFiles are the two sides of a snapshot, by path.
type SnapshotFiles struct {
	// Before maps a path to the blob it had, or nil for a file that is new.
	Before map[string]*BlobRef
	// After maps a path to what the pull request leaves.
	After map[string]AfterEntry
}

// ReadSnapshot decodes the two sides of the snapshot of a pull request or
// merge request action.
func ReadSnapshot(snap action.Snapshot) (SnapshotFiles, error) {
	var sides SnapshotFiles

	if err := json.Unmarshal(snap.Before, &sides.Before); err != nil {
		return SnapshotFiles{}, fmt.Errorf("%w: decode the before side of the snapshot: %w", action.ErrPermanent, err)
	}

	if err := json.Unmarshal(snap.After, &sides.After); err != nil {
		return SnapshotFiles{}, fmt.Errorf("%w: decode the after side of the snapshot: %w", action.ErrPermanent, err)
	}

	return sides, nil
}

// BlobOf returns the blob of a reference, or "" for a file that is not
// there.
func BlobOf(ref *BlobRef) string {
	if ref == nil {
		return ""
	}

	return ref.Blob
}
