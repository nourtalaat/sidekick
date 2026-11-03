package action

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// revisionMarker is the value of the sidekickKind field that RevisionSnapshot
// writes in each revision document, for a person who reads the stored
// snapshot. Nothing reads it back: Snapshot.Revision says what a snapshot is.
const revisionMarker = "revision"

// Revision is one revision of a workload on a platform, as a rollback action
// reads it. Every text field is text from outside, such as an image tag or
// the name of who deployed, so a screen shows it as plain text.
type Revision struct {
	// Kind is what the platform calls it, for example "ReplicaSet revision",
	// "Helm release revision", "task definition" or "Lambda version".
	Kind string `json:"kind"`
	// ID is the platform's identifier, for example "7" or "checkout:42".
	ID string `json:"id"`
	// Version is what a person recognises: an image tag, the chart and app
	// version, or a git revision. Empty when unknown.
	Version string `json:"version,omitempty"`
	// DeployedAt is nil when unknown.
	DeployedAt *time.Time `json:"deployedAt,omitempty"`
	// DeployedBy is empty when the platform does not record it.
	DeployedBy string `json:"deployedBy,omitempty"`
	// Notes is short text, for example the images of several containers.
	Notes string `json:"notes,omitempty"`
}

// Revisions are the two revisions of a rollback: the one running now and the
// one it goes back to. Snapshot.Revisions returns the text as the action
// wrote it; Display returns it for a person.
type Revisions struct {
	Running    Revision `json:"running"`
	GoesBackTo Revision `json:"goesBackTo"`
}

// revisionDocument is a revision as stored in Snapshot.Before and After.
type revisionDocument struct {
	SidekickKind string `json:"sidekickKind"`
	Revision
}

// RevisionSnapshot returns the snapshot of a rollback, with Revision set.
// Before holds the revision running now and After the one it goes back to,
// each as a JSON document that starts {"sidekickKind":"revision", ...}.
// version is the provider's version of the running revision, or its ID, and
// is what the conflict check compares. It fails when a revision has no Kind
// or no ID.
func RevisionSnapshot(target, version string, running, goesBackTo Revision) (Snapshot, error) {
	before, err := revisionJSON(running)
	if err != nil {
		return Snapshot{}, fmt.Errorf("write the running revision: %w", err)
	}

	after, err := revisionJSON(goesBackTo)
	if err != nil {
		return Snapshot{}, fmt.Errorf("write the revision it goes back to: %w", err)
	}

	return Snapshot{Target: target, Before: before, After: after, Version: version, Revision: true}, nil
}

// Revisions returns the two revisions of a rollback snapshot, or nil and no
// error when Revision is false, whatever Before and After hold. For a
// snapshot with Revision set it fails unless both sides are revision
// documents with a Kind and an ID. A rollback's Undo reads the revision to
// restore from the Before of the snapshot it is given.
func (s Snapshot) Revisions() (*Revisions, error) {
	if !s.Revision {
		return nil, nil
	}

	var out Revisions

	var err error

	if out.Running, err = readRevision(s.Before); err != nil {
		return nil, fmt.Errorf("read the running revision: %w", err)
	}

	if out.GoesBackTo, err = readRevision(s.After); err != nil {
		return nil, fmt.Errorf("read the revision it goes back to: %w", err)
	}

	return &out, nil
}

// Display returns the revisions with every text field written as \u escapes,
// as Change values are, and nothing cut. A newline shows as \n and a
// backslash as two. Proposal.Revisions holds this form.
func (r Revisions) Display() Revisions {
	return Revisions{Running: r.Running.display(), GoesBackTo: r.GoesBackTo.display()}
}

// display writes the text fields of r for a person to read.
func (r Revision) display() Revision {
	r.Kind, r.ID, r.Version = displayText(r.Kind), displayText(r.ID), displayText(r.Version)
	r.DeployedBy, r.Notes = displayText(r.DeployedBy), displayText(r.Notes)

	return r
}

// displayText spells out the characters of s that could hide something, and
// writes a newline and a backslash as \n and \\, so a value cannot push other
// lines out of sight or pass for an escape.
func displayText(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)

	return spell(strings.ReplaceAll(s, "\n", `\n`))
}

// revisionJSON writes r as a revision document.
func revisionJSON(r Revision) (json.RawMessage, error) {
	if r.Kind == "" || r.ID == "" {
		return nil, errors.New("a revision needs a kind and an ID")
	}

	raw, err := json.Marshal(revisionDocument{SidekickKind: revisionMarker, Revision: r})
	if err != nil {
		return nil, fmt.Errorf("encode the revision: %w", err)
	}

	return raw, nil
}

// readRevision decodes a revision document and checks it has a kind and an ID.
func readRevision(raw json.RawMessage) (Revision, error) {
	var doc revisionDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Revision{}, fmt.Errorf("decode the revision: %w", err)
	}

	if doc.Kind == "" || doc.ID == "" {
		return Revision{}, errors.New("a revision needs a kind and an ID")
	}

	return doc.Revision, nil
}
