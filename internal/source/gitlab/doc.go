// Package gitlab is the native GitLab source. It registers itself as the
// source type "gitlab".
//
// A source reads the projects its config lists through the GitLab REST API
// of gitlab.com or a self-managed GitLab, with one access token that has the
// read_api scope and the Reporter role. Every tool only reads, and every
// result links to the GitLab pages a person can open to check it.
//
// What the source relies on from GitLab, as its public documentation and
// OpenAPI file show them at the tag v19.5.1-ee of gitlab.com/gitlab-org/gitlab
// (paths are under doc/ unless noted):
//
//   - api/rest/authentication.md: a personal, project or group access token
//     is sent in the PRIVATE-TOKEN header. api/releases/_index.md names only
//     that header for the Releases API, so the source uses it for every call.
//   - security/tokens/access_token_scopes.md: read_api gives read access to
//     the API for the token's scope; read_repository covers only Git and the
//     repository files API.
//   - user/permissions.md: the Reporter role can view project code, merge
//     requests, pipelines, environments and releases. The Guest role cannot
//     read the code of a private project.
//   - api/rest/_index.md: a project is addressed by its URL-encoded full path
//     (group%2Fsubgroup%2Fproject) and a path or branch with a slash is
//     encoded the same way; a path that is not encoded gives 404. Lists page
//     by offset (page and per_page, at most 100) and answer with x-next-page
//     and Link headers. Keyset paging exists only for a short list of
//     resources that has none of the lists used here. A project that moved
//     answers a GET with a redirect.
//   - api/rest/troubleshooting.md: 404 also means the token may not see the
//     resource, 401 is a bad or expired token, 429 carries Retry-After, and
//     an error body is {"message": ...} where message is a string or an
//     object.
//   - api/commits.md, api/repositories.md, api/repository_files.md,
//     api/merge_requests.md, api/pipelines.md, api/deployments.md,
//     api/releases/_index.md, api/tags.md and api/openapi/openapi_v2.yaml:
//     the parameters and fields of each endpoint the tools call. The commit
//     diff pages by offset, a diff has no line counts so the source counts
//     them from the patch, and a deployment has no web_url.
//   - config/routes/repository.rb, config/routes/project.rb and
//     app/controllers/projects/deployments_controller.rb: the web pages
//     /-/blob/<ref>/<path> and /-/environments/<id>/deployments/<iid>, which
//     the source links to where an answer has no web_url.
package gitlab
