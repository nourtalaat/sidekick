// Package audit writes the audit log: one row for every change people or
// Sidekick make, with who made it and the old and new values. Rows are
// never updated; the database rejects UPDATE on the table.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The kinds of actor an entry can have.
const (
	// ActorUser is a person, through Slack or the web UI.
	ActorUser = "user"
	// ActorToken is an API token an admin created; its ID is the token's.
	ActorToken = "token"
	// ActorSidekick is Sidekick acting on its own, for example the model
	// proposing an action.
	ActorSidekick = "sidekick"
	// ActorPolicy is a decision made by config, for example a read-write
	// route running an action without approval.
	ActorPolicy = "policy"
	// ActorSystem is Sidekick's own housekeeping, for example an expiry.
	ActorSystem = "system"
)

// ErrInvalidEntry means an entry lacks a field every row needs.
var ErrInvalidEntry = errors.New("invalid audit entry")

// Entry is one change to record.
type Entry struct {
	// ActorKind is one of the Actor constants.
	ActorKind string
	// ActorID names the actor, for example a Slack user ID. It can be
	// empty for ActorSidekick, ActorPolicy and ActorSystem.
	ActorID string
	// Action is what happened, for example listener.created.
	Action string
	// TargetKind and TargetID name what changed, for example route and
	// checkout.
	TargetKind string
	TargetID   string
	// InvestigationID is the investigation the change belongs to, if any.
	InvestigationID *uuid.UUID
	// Old and New are the values before and after the change. Each is
	// stored as JSON; nil is left out.
	Old, New any
}

// details is how Old and New are stored in the details column.
type details struct {
	Old any `json:"old,omitempty"`
	New any `json:"new,omitempty"`
}

// Writer adds entries to the audit log.
type Writer struct {
	now func() time.Time
}

// NewWriter returns a Writer that stamps entries with the current time.
func NewWriter() *Writer {
	return &Writer{now: time.Now}
}

// Write adds e to the audit log through q. Pass the transaction that makes
// the change, so the change and its entry commit or roll back together.
func (w *Writer) Write(ctx context.Context, q db.DBTX, e Entry) error {
	if e.ActorKind == "" || e.Action == "" {
		return fmt.Errorf("%w: actor kind and action are required", ErrInvalidEntry)
	}

	body, err := json.Marshal(details{Old: e.Old, New: e.New})
	if err != nil {
		return fmt.Errorf("encode audit details for %s: %w", e.Action, err)
	}

	err = db.New(q).InsertAuditEntry(ctx, db.InsertAuditEntryParams{
		At:              w.now(),
		ActorKind:       e.ActorKind,
		ActorID:         optional(e.ActorID),
		Action:          e.Action,
		TargetKind:      optional(e.TargetKind),
		TargetID:        optional(e.TargetID),
		InvestigationID: e.InvestigationID,
		Details:         body,
	})
	if err != nil {
		return fmt.Errorf("write audit entry %s: %w", e.Action, err)
	}

	return nil
}

// optional turns an empty string into SQL NULL.
func optional(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}
