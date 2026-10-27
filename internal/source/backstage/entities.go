package backstage

import (
	"slices"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits on what a result keeps.
const (
	// maxListLen is the most entries one list in a result keeps, such as
	// the dependencies of a service.
	maxListLen = 100
	// maxTitleLen bounds a title, name or short text from Backstage.
	maxTitleLen = 512
	// maxDescriptionLen bounds a description from Backstage.
	maxDescriptionLen = 4 << 10
	// maxURLLen bounds a link from an entity's metadata.
	maxURLLen = 2 << 10
)

// Relation types of the Backstage catalog the tools report.
const (
	relationOwnedBy      = "ownedBy"
	relationOwnerOf      = "ownerOf"
	relationPartOf       = "partOf"
	relationHasPart      = "hasPart"
	relationDependsOn    = "dependsOn"
	relationDependencyOf = "dependencyOf"
	relationProvidesAPI  = "providesApi"
	relationConsumesAPI  = "consumesApi"
	relationChildOf      = "childOf"
	relationParentOf     = "parentOf"
	relationMemberOf     = "memberOf"
	relationHasMember    = "hasMember"
)

// entity is a catalog entity as the API returns it, with the fields the
// tools read.
type entity struct {
	Kind     string   `json:"kind"`
	Metadata metadata `json:"metadata"`
	Spec     struct {
		Type      string `json:"type"`
		Lifecycle string `json:"lifecycle"`
		Profile   struct {
			DisplayName string `json:"displayName"`
			Email       string `json:"email"`
		} `json:"profile"`
	} `json:"spec"`
	Relations []relation `json:"relations"`
}

// metadata is the metadata of an entity.
type metadata struct {
	Name        string   `json:"name"`
	Namespace   string   `json:"namespace"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Links       []struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	} `json:"links"`
}

// relation is one relation of an entity to another.
type relation struct {
	Type      string `json:"type"`
	TargetRef string `json:"targetRef"`
}

// link is a link in a result.
type link struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
}

// header is what every result says about its entity.
type header struct {
	Ref         string `json:"ref"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// serviceResult is the result of get_service.
type serviceResult struct {
	header

	Type         string   `json:"type,omitempty"`
	Lifecycle    string   `json:"lifecycle,omitempty"`
	Owners       []string `json:"owners,omitempty"`
	Systems      []string `json:"systems,omitempty"`
	DependsOn    []string `json:"dependsOn,omitempty"`
	DependencyOf []string `json:"dependencyOf,omitempty"`
	ProvidesAPIs []string `json:"providesApis,omitempty"`
	ConsumesAPIs []string `json:"consumesApis,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Links        []link   `json:"links,omitempty"`
}

// ownerResult is the result of get_owner.
type ownerResult struct {
	header

	Type        string   `json:"type,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	Email       string   `json:"email,omitempty"`
	Parents     []string `json:"parents,omitempty"`
	Children    []string `json:"children,omitempty"`
	Members     []string `json:"members,omitempty"`
	MemberOf    []string `json:"memberOf,omitempty"`
	Owns        []string `json:"owns,omitempty"`
}

// systemResult is the result of get_system.
type systemResult struct {
	header

	Owners  []string `json:"owners,omitempty"`
	Domains []string `json:"domains,omitempty"`
	Parts   []string `json:"parts,omitempty"`
}

// cutter builds one result and notes whether anything was cut from it.
type cutter struct {
	truncated bool
}

// service returns the get_service result of e.
func (c *cutter) service(e entity, r ref) serviceResult {
	res := serviceResult{
		header:       c.header(e, r),
		Type:         c.text(e.Spec.Type, maxTitleLen),
		Lifecycle:    c.text(e.Spec.Lifecycle, maxTitleLen),
		Owners:       c.targets(e, relationOwnedBy),
		Systems:      c.targets(e, relationPartOf),
		DependsOn:    c.targets(e, relationDependsOn),
		DependencyOf: c.targets(e, relationDependencyOf),
		ProvidesAPIs: c.targets(e, relationProvidesAPI),
		ConsumesAPIs: c.targets(e, relationConsumesAPI),
		Tags:         c.list(e.Metadata.Tags),
	}

	for _, l := range e.Metadata.Links {
		if len(res.Links) == maxListLen {
			c.truncated = true

			break
		}

		res.Links = append(res.Links, link{Title: c.text(l.Title, maxTitleLen), URL: c.text(l.URL, maxURLLen)})
	}

	return res
}

// owner returns the get_owner result of e.
func (c *cutter) owner(e entity, r ref) ownerResult {
	return ownerResult{
		header:      c.header(e, r),
		Type:        c.text(e.Spec.Type, maxTitleLen),
		DisplayName: c.text(e.Spec.Profile.DisplayName, maxTitleLen),
		Email:       c.text(e.Spec.Profile.Email, maxTitleLen),
		Parents:     c.targets(e, relationChildOf),
		Children:    c.targets(e, relationParentOf),
		Members:     c.targets(e, relationHasMember),
		MemberOf:    c.targets(e, relationMemberOf),
		Owns:        c.targets(e, relationOwnerOf),
	}
}

// system returns the get_system result of e.
func (c *cutter) system(e entity, r ref) systemResult {
	return systemResult{
		header:  c.header(e, r),
		Owners:  c.targets(e, relationOwnedBy),
		Domains: c.targets(e, relationPartOf),
		Parts:   c.targets(e, relationHasPart),
	}
}

// header returns the header of e, which was read for r.
func (c *cutter) header(e entity, r ref) header {
	return header{
		Ref:         r.String(),
		Title:       c.text(e.Metadata.Title, maxTitleLen),
		Description: c.text(e.Metadata.Description, maxDescriptionLen),
	}
}

// targets returns the sorted target refs of e's relations of type typ.
func (c *cutter) targets(e entity, typ string) []string {
	var out []string

	for _, rel := range e.Relations {
		if rel.Type == typ {
			out = append(out, rel.TargetRef)
		}
	}

	slices.Sort(out)

	return c.list(slices.Compact(out))
}

// list returns at most maxListLen entries of items, each cut to
// maxTitleLen.
func (c *cutter) list(items []string) []string {
	if len(items) > maxListLen {
		c.truncated = true
		items = items[:maxListLen]
	}

	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, c.text(item, maxTitleLen))
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// text returns s cut to n bytes.
func (c *cutter) text(s string, n int) string {
	if len(s) > n {
		c.truncated = true
	}

	return source.CutText(s, n)
}
