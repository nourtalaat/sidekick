package awslambda

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/awsaction"
)

// Bounds of the search for earlier versions.
const (
	// listPageSize is the most versions ListVersionsByFunction returns in a
	// page.
	listPageSize = 50
	// maxListPages bounds the pages read. Lambda does not promise an order, so
	// every page must be read before the versions can be put in order.
	maxListPages = 20
)

// latest is the version name of the unpublished code of a function.
const latest = "$LATEST"

// aliasState is what the action needs to know about an alias: the version it
// serves, the extra version that gets part of its traffic, if any, and the
// RevisionId that changes whenever the alias does.
type aliasState struct {
	version    string
	revisionID string
	weights    map[string]float64
}

// split reports whether the alias sends part of its traffic to another
// version.
func (s aliasState) split() bool {
	return len(s.weights) > 0
}

// sameRouting reports whether two aliases serve the same version and split
// traffic the same way.
func (s aliasState) sameRouting(version string, weights map[string]float64) bool {
	return s.version == version && maps.Equal(s.weights, weights)
}

// client returns the Lambda client for the position, after getting the
// credentials of its account.
func (r *Rollback) client(ctx context.Context, pos position) (*lambda.Client, error) {
	if err := pos.target.Ready(ctx); err != nil {
		return nil, fmt.Errorf("get ready: %w", err)
	}

	return lambda.NewFromConfig(pos.target.Config, func(o *lambda.Options) {
		if r.endpoint != "" {
			o.BaseEndpoint = aws.String(r.endpoint)
		}
	}), nil
}

// readAlias reads the alias. It refuses, for good, when the function or alias
// does not exist.
func readAlias(ctx context.Context, client *lambda.Client, pos position) (aliasState, error) {
	out, err := client.GetAlias(ctx, &lambda.GetAliasInput{FunctionName: aws.String(pos.function), Name: aws.String(pos.alias)})
	if _, ok := errors.AsType[*types.ResourceNotFoundException](err); ok {
		return aliasState{}, awsaction.Refuse("Lambda function %s in account %s, Region %s has no alias %s", pos.function, pos.target.Account, pos.target.Region, pos.alias)
	}

	if err != nil {
		return aliasState{}, fmt.Errorf("get alias %s of function %s: %w", pos.alias, pos.function, awsaction.Fail(err))
	}

	st := aliasState{version: aws.ToString(out.FunctionVersion), revisionID: aws.ToString(out.RevisionId)}
	if out.RoutingConfig != nil && len(out.RoutingConfig.AdditionalVersionWeights) > 0 {
		st.weights = maps.Clone(out.RoutingConfig.AdditionalVersionWeights)
	}

	return st, nil
}

// publishedVersion returns the number of a version name, and refuses, for
// good, $LATEST and anything that is not a published version.
func publishedVersion(name string, pos position) (int, error) {
	n, err := strconv.Atoi(name)
	if err != nil || n < 1 {
		return 0, awsaction.Refuse("alias %s of function %s serves %q, which is not a published version; the action rolls back only an alias that serves a published version", pos.alias, pos.function, name)
	}

	return n, nil
}

// earlier returns the target of a rollback: the published version that is
// back versions below running. It reads every page of ListVersionsByFunction
// and puts the numbers in order itself.
func earlier(ctx context.Context, client *lambda.Client, pos position, running, back int) (int, error) {
	var (
		below []int
		token *string
	)

	for range maxListPages {
		out, err := client.ListVersionsByFunction(ctx, &lambda.ListVersionsByFunctionInput{
			FunctionName: aws.String(pos.function),
			Marker:       token,
			MaxItems:     aws.Int32(listPageSize),
		})
		if err != nil {
			return 0, fmt.Errorf("list the versions of function %s: %w", pos.function, awsaction.Fail(err))
		}

		for _, v := range out.Versions {
			if n, err := strconv.Atoi(aws.ToString(v.Version)); err == nil && n < running {
				below = append(below, n)
			}
		}

		if out.NextMarker == nil || *out.NextMarker == "" {
			return nth(pos, below, running, back)
		}

		token = out.NextMarker
	}

	return 0, awsaction.Refuse("function %s has more than %d versions, and Sidekick does not read them all; roll back in the Lambda console", pos.function, maxListPages*listPageSize)
}

// nth returns the back-th highest of the versions below running, or a refusal
// when there are fewer.
func nth(pos position, below []int, running, back int) (int, error) {
	slices.SortFunc(below, func(a, b int) int { return b - a })
	below = slices.Compact(below)

	switch {
	case len(below) >= back:
		return below[back-1], nil
	case len(below) == 0:
		return 0, awsaction.Refuse("function %s has no published version below %d, so there is nothing to go back to", pos.function, running)
	default:
		return 0, awsaction.Refuse("function %s has only %d published version(s) below %d, so alias %s cannot go %d back", pos.function, len(below), running, pos.alias, back)
	}
}

// describeVersion reads the configuration of a published version. It refuses,
// for good, a version that does not exist, so the alias cannot be put on it.
func describeVersion(ctx context.Context, client *lambda.Client, pos position, version string) (*lambda.GetFunctionConfigurationOutput, error) {
	out, err := client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(pos.function), Qualifier: aws.String(version)})
	if _, ok := errors.AsType[*types.ResourceNotFoundException](err); ok {
		return nil, awsaction.Refuse("version %s of Lambda function %s does not exist, so the alias cannot be put on it", version, pos.function)
	}

	if err != nil {
		return nil, fmt.Errorf("get the configuration of version %s of function %s: %w", version, pos.function, awsaction.Fail(err))
	}

	return out, nil
}

// updateAlias points the alias at version, with weights as its traffic split
// (none clears it), if its RevisionId is still revisionID. It is tried once:
// the SDK does not repeat it, because Sidekick tries again from a fresh read
// of the alias, which is the one that knows whether the call took effect.
// A precondition that fails gives an error that matches action.ErrConflict.
// It checks the answer, so a split that Lambda did not clear is not reported
// as a success.
func updateAlias(ctx context.Context, client *lambda.Client, pos position, version string, weights map[string]float64, revisionID string) error {
	if weights == nil {
		weights = map[string]float64{}
	}

	out, err := client.UpdateAlias(ctx, &lambda.UpdateAliasInput{
		FunctionName:    aws.String(pos.function),
		Name:            aws.String(pos.alias),
		FunctionVersion: aws.String(version),
		RoutingConfig:   &types.AliasRoutingConfiguration{AdditionalVersionWeights: weights},
		RevisionId:      aws.String(revisionID),
	}, func(o *lambda.Options) { o.RetryMaxAttempts = 1 })

	switch {
	case isPrecondition(err):
		return fmt.Errorf("alias %s of function %s changed after it was read: %w", pos.alias, pos.function, action.ErrConflict)
	case isBusy(err):
		return fmt.Errorf("update alias %s of function %s: %w", pos.alias, pos.function, err)
	case err != nil:
		return fmt.Errorf("update alias %s of function %s to version %s: %w", pos.alias, pos.function, version, awsaction.Fail(err))
	}

	var got map[string]float64
	if out.RoutingConfig != nil {
		got = out.RoutingConfig.AdditionalVersionWeights
	}

	if aws.ToString(out.FunctionVersion) != version || !maps.Equal(got, weights) {
		return fmt.Errorf("alias %s serves version %s with traffic split %v after the update, not version %s with split %v: %w",
			pos.alias, aws.ToString(out.FunctionVersion), got, version, weights, action.ErrPermanent)
	}

	return nil
}

// isPrecondition reports whether Lambda refused the RevisionId.
func isPrecondition(err error) bool {
	_, ok := errors.AsType[*types.PreconditionFailedException](err)

	return ok
}

// isBusy reports whether Lambda says another operation on the alias is in
// progress. awsaction.Fail would call it permanent, but trying again passes.
func isBusy(err error) bool {
	_, ok := errors.AsType[*types.ResourceConflictException](err)

	return ok
}

// aliasLink points at the alias in the Lambda console.
func aliasLink(pos position) action.Link {
	return action.Link{
		Title: "Lambda alias " + pos.alias + " of function " + pos.function,
		URL: awsaction.ConsoleRoot(pos.target.Region) + "/lambda/home?region=" + pos.target.Region +
			"#/functions/" + url.PathEscape(pos.function) + "/aliases/" + url.PathEscape(pos.alias),
	}
}
