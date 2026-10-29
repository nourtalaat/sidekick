package feedback

import (
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Change is how a rating changed an investigation's confirmed verdict.
type Change string

// Changes.
const (
	// Unchanged: the confirmed verdict stays as it was.
	Unchanged Change = ""
	// Confirmed: a Correct confirmed a verdict nobody had confirmed.
	Confirmed Change = "confirmed"
	// Revised: a Correct confirmed a verdict other than the confirmed one.
	Revised Change = "revised"
	// Withdrawn: a Wrong on the confirmed verdict took the confirmation
	// back, so the investigation is no longer confirmed.
	Withdrawn Change = "withdrawn"
)

// confirmation is an investigation's confirmed verdict. All nil means it
// has none.
type confirmation struct {
	verdict     *string
	cause       *string
	by          *uuid.UUID
	bySlackUser *string
	at          *time.Time
}

// confirmationView is what the audit log records of a confirmed verdict.
type confirmationView struct {
	Verdict     string `json:"verdict,omitempty"`
	Cause       string `json:"cause,omitempty"`
	BySlackUser string `json:"by_slack_user,omitempty"`
	At          string `json:"at,omitempty"`
}

// actor is who gave feedback: a signed-in user or a Slack user.
type actor struct {
	userID    *uuid.UUID
	slackUser *string
}

// view returns c for the audit log, or nil when nothing is confirmed, so
// the audit row leaves that side out.
func (c confirmation) view() any {
	if c.verdict == nil {
		return nil
	}

	v := &confirmationView{Verdict: *c.verdict}
	if c.cause != nil {
		v.Cause = *c.cause
	}

	if c.bySlackUser != nil {
		v.BySlackUser = *c.bySlackUser
	}

	if c.at != nil {
		v.At = c.at.UTC().Format(time.RFC3339)
	}

	return v
}

// current returns the confirmed verdict of inv.
func current(inv db.Investigation) confirmation {
	return confirmation{
		verdict:     inv.ConfirmedVerdict,
		cause:       inv.ConfirmedCause,
		by:          inv.ConfirmedBy,
		bySlackUser: inv.ConfirmedBySlackUser,
		at:          inv.ConfirmedAt,
	}
}

// nextConfirmation returns the confirmed verdict after who rated verdict,
// whose cause is cause, at now, and how it changed. A Correct confirms
// verdict unless it is the confirmed one already. A Wrong takes back a
// confirmation of verdict, and changes nothing otherwise.
func nextConfirmation(cur confirmation, rating output.Rating, verdict string, cause *string, who actor, now time.Time) (confirmation, Change) {
	same := cur.verdict != nil && *cur.verdict == verdict

	switch {
	case rating == output.RatingCorrect && same:
		return cur, Unchanged
	case rating == output.RatingCorrect:
		change := Confirmed
		if cur.verdict != nil {
			change = Revised
		}

		return who.confirmedBy(verdict, cause, now), change
	case rating == output.RatingWrong && same:
		return confirmation{}, Withdrawn
	default:
		return cur, Unchanged
	}
}

// nextRealVerdict returns the confirmed verdict after who said the real
// verdict is verdict, with cause when they gave one, at now, and how it
// changed. The real verdict is confirmed whatever the verdict of the
// report is. The same verdict with no new cause changes nothing.
func nextRealVerdict(cur confirmation, verdict string, cause *string, who actor, now time.Time) (confirmation, Change) {
	same := cur.verdict != nil && *cur.verdict == verdict
	sameCause := cause == nil || (cur.cause != nil && *cur.cause == *cause)

	switch {
	case same && sameCause:
		return cur, Unchanged
	case cur.verdict != nil:
		return who.confirmedBy(verdict, cause, now), Revised
	default:
		return who.confirmedBy(verdict, cause, now), Confirmed
	}
}

// confirmedBy returns a confirmation of verdict and cause by a at now.
func (a actor) confirmedBy(verdict string, cause *string, now time.Time) confirmation {
	return confirmation{verdict: &verdict, cause: cause, by: a.userID, bySlackUser: a.slackUser, at: &now}
}

// id names a for the audit log.
func (a actor) id() string {
	if a.slackUser != nil {
		return *a.slackUser
	}

	if a.userID != nil {
		return a.userID.String()
	}

	return ""
}
