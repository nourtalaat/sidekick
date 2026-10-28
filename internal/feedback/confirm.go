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

// nextConfirmation returns the confirmed verdict after slackUser rated
// verdict, whose cause is cause, at now, and how it changed. A Correct
// confirms verdict unless it is the confirmed one already. A Wrong takes
// back a confirmation of verdict, and changes nothing otherwise.
func nextConfirmation(cur confirmation, rating output.Rating, verdict string, cause *string, slackUser string, now time.Time) (confirmation, Change) {
	same := cur.verdict != nil && *cur.verdict == verdict

	switch {
	case rating == output.RatingCorrect && same:
		return cur, Unchanged
	case rating == output.RatingCorrect:
		change := Confirmed
		if cur.verdict != nil {
			change = Revised
		}

		return confirmation{verdict: &verdict, cause: cause, bySlackUser: &slackUser, at: &now}, change
	case rating == output.RatingWrong && same:
		return confirmation{}, Withdrawn
	default:
		return cur, Unchanged
	}
}
