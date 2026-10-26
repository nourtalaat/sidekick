// Package ids makes the IDs that Sidekick stores in Postgres.
package ids

import "github.com/google/uuid"

// New returns a new UUIDv7. A UUIDv7 starts with a millisecond timestamp,
// so IDs made one after another sort in time order and new rows land close
// together in an index.
//
// New panics only if the system's random number source fails, which the
// Go runtime already treats as fatal.
func New() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}
