package awsecs

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/source"
)

// What a revision document says about a task definition.
const (
	revisionKind = "task definition"
	// registeredNote says what DeployedAt is, since ECS keeps no deploy time
	// of a task definition.
	registeredNote = "The time is when the task definition was registered. ECS does not say when it was deployed."
	// registeredBy is put before who registered the task definition, because
	// screens label the field "Deployed by", and ECS records who registered
	// the task definition, who may not be who updated the service.
	registeredBy = "registered by "
	// deployedNote says what DeployedAt is for a task definition that the
	// deployment history shows on the service. It takes the deployment ID.
	deployedNote = "The time is when deployment %s of this service, which succeeded, finished."
)

// Bounds of the version of a revision, which lists the images of the task
// definition.
const (
	// maxImages is the most images listed; the rest is counted.
	maxImages = 10
	// maxImageLen is the longest image reference listed, in bytes.
	maxImageLen = 200
)

// revisionOf describes a task definition as a revision. The version is its
// container images as the task definition writes them. Facts ECS does not
// give are left out, and screens show "unknown".
func revisionOf(td types.TaskDefinition) action.Revision {
	rev := action.Revision{
		Kind:    revisionKind,
		ID:      aws.ToString(td.Family) + ":" + strconv.Itoa(int(td.Revision)),
		Version: imagesOf(td.ContainerDefinitions),
		Notes:   registeredNote,
	}

	if td.RegisteredAt != nil {
		at := td.RegisteredAt.UTC()
		rev.DeployedAt = &at
	}

	if who := aws.ToString(td.RegisteredBy); who != "" {
		rev.DeployedBy = registeredBy + who
	}

	return rev
}

// targetRevisionOf describes the task definition a rollback goes to. When
// the deployment history shows the deployment that put it on the service,
// the time is when that deployment finished. Otherwise the time is when the
// task definition was registered, and the note says that the history could
// not be used, when that is why.
func targetRevisionOf(td types.TaskDefinition, c choice) action.Revision {
	rev := revisionOf(td)

	switch {
	case c.deployment != nil:
		rev.DeployedAt = c.deployment.at
		rev.Notes = fmt.Sprintf(deployedNote, c.deployment.id)
	case c.warning != "":
		rev.Notes += " " + c.warning
	}

	return rev
}

// imagesOf lists the images of the containers, in order, separated by commas.
func imagesOf(containers []types.ContainerDefinition) string {
	var images []string

	for i, c := range containers {
		if i == maxImages {
			images = append(images, fmt.Sprintf("+%d more", len(containers)-maxImages))

			break
		}

		images = append(images, source.CutText(aws.ToString(c.Image), maxImageLen))
	}

	return strings.Join(images, ", ")
}

// parseTaskDefinition splits the ARN of a task definition, or its
// family:revision, into the family and the revision number.
func parseTaskDefinition(arn string) (family string, revision int, ok bool) {
	id := arn[strings.LastIndex(arn, "/")+1:]

	family, number, found := strings.Cut(id, ":")
	if !found || family == "" {
		return "", 0, false
	}

	revision, err := strconv.Atoi(number)
	if err != nil || revision < 1 {
		return "", 0, false
	}

	return family, revision, true
}

// taskDefinitionID writes a family and revision as family:revision.
func taskDefinitionID(family string, revision int) string {
	return family + ":" + strconv.Itoa(revision)
}
