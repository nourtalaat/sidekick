package linear

import (
	"context"
	"fmt"
	"strings"
)

// The GraphQL documents of creating an issue.
const (
	queryTeamByID = `query SidekickTeam($id: String!) {
  team(id: $id) { id key }
}`
	queryTeamByKey = `query SidekickTeamByKey($key: String!) {
  teams(filter: { key: { eq: $key } }, first: 1) { nodes { id key } }
}`
	queryLabels = `query SidekickLabels($names: [String!]!, $team: ID!) {
  issueLabels(
    filter: { name: { in: $names }, or: [{ team: { id: { eq: $team } } }, { team: { null: true } }] }
    first: 100
  ) { nodes { id name team { id } } }
}`
	queryIssueByMarker = `query SidekickIssueByMarker($team: ID!, $marker: String!) {
  issues(filter: { team: { id: { eq: $team } }, description: { contains: $marker } }, first: 1) {
    nodes { id identifier url team { id key } }
  }
}`
	mutationIssueCreate = `mutation SidekickIssueCreate($input: IssueCreateInput!) {
  issueCreate(input: $input) { success issue { id identifier url team { id key } } }
}`
)

type labelRef struct {
	ID   string     `json:"id"`
	Name string     `json:"name"`
	Team *labelTeam `json:"team"`
}

// labelTeam is the team of a label.
type labelTeam struct {
	ID string `json:"id"`
}

// issueInput is the input of issueCreate.
type issueInput struct {
	ID          string   `json:"id"`
	TeamID      string   `json:"teamId"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	LabelIDs    []string `json:"labelIds,omitempty"`
}

// team looks up a team by its key or its ID. It returns an error wrapping
// errNotFound when there is none.
func (c *client) team(ctx context.Context, ref string) (teamRef, error) {
	if isUUID(ref) {
		var out struct {
			Team *teamRef `json:"team"`
		}

		if err := c.call(ctx, queryTeamByID, map[string]any{"id": ref}, &out); err != nil {
			return teamRef{}, fmt.Errorf("look up team: %w", err)
		}

		if out.Team == nil {
			return teamRef{}, fmt.Errorf("look up team: %w", errMissing)
		}

		return *out.Team, nil
	}

	var out struct {
		Teams struct {
			Nodes []teamRef `json:"nodes"`
		} `json:"teams"`
	}

	if err := c.call(ctx, queryTeamByKey, map[string]any{"key": strings.ToUpper(ref)}, &out); err != nil {
		return teamRef{}, fmt.Errorf("look up team: %w", err)
	}

	if len(out.Teams.Nodes) == 0 {
		return teamRef{}, fmt.Errorf("look up team: %w", errMissing)
	}

	return out.Teams.Nodes[0], nil
}

// labelIDs returns the IDs of the labels with these names that the team can
// use: its own and the workspace's. A name without a label is an error
// wrapping errNotFound.
func (c *client) labelIDs(ctx context.Context, names []string, teamID string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}

	var out struct {
		IssueLabels struct {
			Nodes []labelRef `json:"nodes"`
		} `json:"issueLabels"`
	}

	if err := c.call(ctx, queryLabels, map[string]any{"names": names, "team": teamID}, &out); err != nil {
		return nil, fmt.Errorf("look up labels: %w", err)
	}

	ids := make([]string, 0, len(names))

	for _, name := range names {
		id := pickLabel(out.IssueLabels.Nodes, name, teamID)
		if id == "" {
			return nil, fmt.Errorf("label %q: %w", name, errMissing)
		}

		ids = append(ids, id)
	}

	return ids, nil
}

// issueByMarker finds the issue of the team whose description holds the
// marker. It returns an error wrapping errNotFound when there is none.
func (c *client) issueByMarker(ctx context.Context, teamID, marker string) (issueRef, error) {
	var out struct {
		Issues struct {
			Nodes []issueRef `json:"nodes"`
		} `json:"issues"`
	}

	if err := c.call(ctx, queryIssueByMarker, map[string]any{"team": teamID, "marker": marker}, &out); err != nil {
		return issueRef{}, fmt.Errorf("search for the marker: %w", err)
	}

	if len(out.Issues.Nodes) == 0 {
		return issueRef{}, fmt.Errorf("search for the marker: %w", errMissing)
	}

	return out.Issues.Nodes[0], nil
}

// createIssue creates an issue.
func (c *client) createIssue(ctx context.Context, input issueInput) (issueRef, error) {
	var out struct {
		IssueCreate struct {
			Success bool      `json:"success"`
			Issue   *issueRef `json:"issue"`
		} `json:"issueCreate"`
	}

	if err := c.call(ctx, mutationIssueCreate, map[string]any{"input": input}, &out); err != nil {
		return issueRef{}, fmt.Errorf("create the issue: %w", err)
	}

	if !out.IssueCreate.Success {
		return issueRef{}, fmt.Errorf("create the issue: %w", errUnsuccessful)
	}

	if out.IssueCreate.Issue == nil || out.IssueCreate.Issue.ID == "" {
		return issueRef{}, fmt.Errorf("create the issue: %w", errNoID)
	}

	return *out.IssueCreate.Issue, nil
}

// pickLabel returns the ID of the label called name that the team can use,
// preferring the team's own label to the workspace's, or "" for none. Linear
// names are not case sensitive.
func pickLabel(nodes []labelRef, name, teamID string) string {
	workspace := ""

	for _, n := range nodes {
		if !strings.EqualFold(n.Name, name) {
			continue
		}

		switch {
		case n.Team != nil && n.Team.ID == teamID:
			return n.ID
		case n.Team == nil && workspace == "":
			workspace = n.ID
		}
	}

	return workspace
}
