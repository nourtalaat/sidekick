package awslambda

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/awsaction"
	"github.com/nourtalaat/sidekick/internal/source"
)

// What a revision document says about a published version.
const (
	revisionKind = "Lambda version"
	// hashLen is how many hex characters of the code hash a revision shows.
	hashLen = 12
	// maxDescriptionLen is the longest version description shown, in bytes.
	maxDescriptionLen = 200
)

// lastModifiedLayout is how Lambda writes LastModified, for example
// 2020-04-10T19:06:32.563+0000.
const lastModifiedLayout = "2006-01-02T15:04:05.000-0700"

// restoreData is what Undo needs to put an alias back, and what Execute needs
// to tell that the alias is as it was when the rollback was proposed. It is
// kept as JSON in Snapshot.Version, so nothing is read back from the text a
// person sees.
type restoreData struct {
	// RevisionID is the alias's RevisionId when the rollback was proposed.
	RevisionID string `json:"revisionId"`
	// Weights is the traffic split the alias had: the extra version and its
	// share of the traffic. Empty when the alias had no split.
	Weights map[string]float64 `json:"weights,omitempty"`
}

// encodeRestore writes the restore data of an alias as a snapshot version.
func encodeRestore(st aliasState) (string, error) {
	raw, err := json.Marshal(restoreData{RevisionID: st.revisionID, Weights: st.weights})
	if err != nil {
		return "", fmt.Errorf("encode the restore data: %w", err)
	}

	return string(raw), nil
}

// decodeRestore reads the restore data of a snapshot. It refuses, for good, a
// snapshot that has none.
func decodeRestore(version string) (restoreData, error) {
	var data restoreData

	if err := json.Unmarshal([]byte(version), &data); err != nil {
		return restoreData{}, awsaction.Refuse("the snapshot has no readable alias data to restore: %v", err)
	}

	if data.RevisionID == "" {
		return restoreData{}, awsaction.Refuse("the snapshot has no alias revision to compare with")
	}

	return data, nil
}

// revisionOf describes a published version as a revision. Facts Lambda does
// not give are left out, and screens show "unknown": Lambda does not record
// who published a version, so DeployedBy is always empty.
func revisionOf(version string, cfg *lambda.GetFunctionConfigurationOutput, notes string) action.Revision {
	rev := action.Revision{Kind: revisionKind, ID: version, Version: codeOf(cfg), Notes: joinNotes(notes, descriptionOf(cfg))}

	if at, err := time.Parse(lastModifiedLayout, aws.ToString(cfg.LastModified)); err == nil {
		utc := at.UTC()
		rev.DeployedAt = &utc
	}

	return rev
}

// codeOf says what the version runs: the first characters of the hash of its
// code and its runtime, or "container image".
func codeOf(cfg *lambda.GetFunctionConfigurationOutput) string {
	var parts []string

	if hash := hashPrefix(aws.ToString(cfg.CodeSha256)); hash != "" {
		parts = append(parts, "sha256:"+hash)
	}

	switch {
	case cfg.PackageType == types.PackageTypeImage:
		parts = append(parts, "container image")
	case cfg.Runtime != "":
		parts = append(parts, string(cfg.Runtime))
	}

	return strings.Join(parts, " ")
}

// hashPrefix is the first hex characters of a base64 SHA-256 hash.
func hashPrefix(b64 string) string {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return ""
	}

	return hex.EncodeToString(raw)[:min(hashLen, 2*len(raw))]
}

// descriptionOf is the description of the version, cut.
func descriptionOf(cfg *lambda.GetFunctionConfigurationOutput) string {
	if d := aws.ToString(cfg.Description); d != "" {
		return "Description: " + source.CutText(d, maxDescriptionLen)
	}

	return ""
}

// joinNotes joins the non-empty notes with a space.
func joinNotes(notes ...string) string {
	return strings.Join(slices.DeleteFunc(slices.Clone(notes), func(n string) bool { return n == "" }), " ")
}

// splitNote says, for the revision of a running version, that the alias
// splits traffic, and what the rollback does about it. It is empty when there
// is no split.
func splitNote(st aliasState) string {
	if !st.split() {
		return ""
	}

	return "The alias also sends " + shares(st) + ". The rollback clears this split and sends all requests to one version; Undo restores it."
}

// shares writes the traffic split of an alias, for example "10% of requests to
// version 43".
func shares(st aliasState) string {
	versions := slices.Sorted(maps.Keys(st.weights))
	parts := make([]string, 0, len(versions))

	for _, v := range versions {
		parts = append(parts, fmt.Sprintf("%s of requests to version %s", percent(st.weights[v]), v))
	}

	return strings.Join(parts, " and ")
}

// percent writes a share of traffic, 0 to 1, as a percentage.
func percent(weight float64) string {
	return strconv.FormatFloat(math.Round(weight*10000)/100, 'f', -1, 64) + "%"
}
