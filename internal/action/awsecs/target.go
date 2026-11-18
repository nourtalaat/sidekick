package awsecs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/nourtalaat/sidekick/internal/action/awsaction"
)

// What a revision document says when the deployment history could not tell
// whether a revision ever ran on the service.
const (
	deniedWarning = "Sidekick is not allowed to read the deployment history of the service, so it cannot tell whether this revision ever ran on it."
	noneWarning   = "ECS has no deployment history for the service, so Sidekick cannot tell whether this revision ever ran on it."
)

// maxListedRevisions is the most revisions a refusal lists; the rest is
// counted.
const maxListedRevisions = 5

// choice is the revision a rollback goes to, and what is known about it.
type choice struct {
	revision int
	// deployment is the deployment that put the revision on the service. It
	// is nil when the history could not be used.
	deployment *deployment
	// warning says why the history could not be used. It is empty when it
	// was.
	warning string
}

// chooseTarget picks the revision a rollback goes to: the back-th ACTIVE
// revision of the family below the running one that the service deployed
// successfully, newest first. The deployment history ECS keeps is the proof
// of a deployment. When ECS has no history for the service, or Sidekick may
// not read it, the target is the back-th ACTIVE revision below the running
// one, in the order of registration, and the choice carries a warning.
func chooseTarget(ctx context.Context, client *ecs.Client, pos position, family string, running, back int) (choice, error) {
	active, err := activeBelow(ctx, client, family, running)
	if err != nil {
		return choice{}, fmt.Errorf("find the ACTIVE task definitions: %w", err)
	}

	if len(active.revisions) < back {
		return choice{}, tooFewActive(family, running, back, active)
	}

	r := &reader{
		client: client, pos: pos, family: family, running: running, back: back,
		active:          revisionSet(active.revisions),
		taskDefinitions: map[string]string{},
	}

	h, err := r.read(ctx)
	if _, denied := errors.AsType[*types.AccessDeniedException](err); denied {
		return fallback(active, back, deniedWarning), nil
	}

	if err != nil {
		return choice{}, fmt.Errorf("read the deployment history of service %s: %w", pos.service, awsaction.Fail(err))
	}

	if h.none {
		return fallback(active, back, noneWarning), nil
	}

	if len(h.found) < back {
		return choice{}, notDeployed(pos, family, running, back, active, h)
	}

	found := h.found[back-1]

	return choice{revision: found.revision, deployment: &found.deployment}, nil
}

// fallback is the choice of the back-th ACTIVE revision below the running
// one, with the reason the history was not used.
func fallback(active activeSet, back int, warning string) choice {
	return choice{revision: active.revisions[back-1], warning: warning}
}

// tooFewActive is the refusal for a family with fewer ACTIVE revisions below
// the running one than the model asked to go back. When the search was cut
// short, the revisions may be beyond what it read.
func tooFewActive(family string, running, back int, active activeSet) error {
	if active.complete {
		return tooFew(family, running, back, len(active.revisions))
	}

	return awsaction.Refuse("looked through the newest %d task definitions that start with %q and did not find %d ACTIVE revision%s of family %s below %s; "+
		"ask for fewer revisions back, or roll back in the ECS console", active.examined, family, back, plural(back), family, taskDefinitionID(family, running))
}

// notDeployed is the refusal for a service whose deployment history shows
// fewer ACTIVE revisions below the running one than the model asked to go
// back. It names the deployed revisions that are INACTIVE and the ACTIVE ones
// the history does not show, so a person can see why they were not chosen.
func notDeployed(pos position, family string, running, back int, active activeSet, h history) error {
	id := taskDefinitionID(family, running)
	have := len(h.found)

	if h.cut {
		return awsaction.Refuse("read the newest %d successful deployments of service %s and found %d ACTIVE task definition%s of family %s below %s, not %d; "+
			"ask for fewer revisions back, or roll back in the ECS console", h.read, pos.service, have, plural(have), family, id, back)
	}

	var b strings.Builder

	if have == 0 {
		fmt.Fprintf(&b, "service %s has no ACTIVE task definition of family %s below %s that it deployed successfully in the deployment history ECS keeps (about 90 days), so there is nothing to go back to",
			pos.service, family, id)
	} else {
		fmt.Fprintf(&b, "service %s deployed only %d ACTIVE task definition%s of family %s below %s successfully in the deployment history ECS keeps (about 90 days), so it cannot go %d back",
			pos.service, have, plural(have), family, id, back)
	}

	if len(h.inactive) > 0 {
		fmt.Fprintf(&b, ". Earlier task definitions it did deploy are INACTIVE, and ECS cannot deploy an INACTIVE task definition: %s", listRevisions(family, h.inactive))
	}

	if never := neverDeployed(active, h); len(never) > 0 {
		fmt.Fprintf(&b, ". These ACTIVE revisions are not in its history of successful deployments, so Sidekick does not choose them: %s", listRevisions(family, never))
	}

	return awsaction.Refuse("%s", b.String())
}

// neverDeployed returns the ACTIVE revisions that the history does not show
// as deployed, newest first.
func neverDeployed(active activeSet, h history) []int {
	return slices.DeleteFunc(slices.Clone(active.revisions), func(revision int) bool { return h.deployed[revision] })
}

// listRevisions writes revisions as family:revision, separated by commas, up
// to maxListedRevisions of them.
func listRevisions(family string, revisions []int) string {
	var ids []string

	for i, revision := range revisions {
		if i == maxListedRevisions {
			ids = append(ids, "and "+strconv.Itoa(len(revisions)-maxListedRevisions)+" more")

			break
		}

		ids = append(ids, taskDefinitionID(family, revision))
	}

	return strings.Join(ids, ", ")
}
