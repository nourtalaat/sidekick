// Package gitlabgit holds the calls to the GitLab REST API that the actions
// opening merge requests share: reading the head of a branch, a file, a
// directory and a blob at a commit, and the helpers for their config. An
// action, such as gitlabmr, adds what is its own: which files it may change
// and how it checks them. The client is made by internal/gitlabapi.
//
// What it relies on from GitLab, as the public source and API documentation
// at the tag v19.5.1-ee of gitlab.com/gitlab-org/gitlab show them (paths are
// under doc/ unless noted):
//
//   - api/repository_files.md, api/repositories.md and lib/api/files.rb: a file
//     is read at a commit with GET .../repository/files/:path?ref=<sha>, which
//     answers its blob_id, its size and its base64 content, or 404 for a path
//     that is no file; a directory is listed with GET .../repository/tree, which
//     has no sizes, and a blob is read by its ID with GET
//     .../repository/blobs/:sha. Text that is not stored as UTF-8 is converted
//     before it is encoded, so a file whose decoded length is not its size is
//     refused. A branch is read with GET .../repository/branches/:branch,
//     which answers its commit.
package gitlabgit
