// Package gitlabgit holds the code that the actions opening merge requests on
// GitLab share: the calls to the GitLab REST API (reading the head of a
// branch, a file, a directory and a blob at a commit, writing one commit,
// opening, finding and closing a merge request, deleting a branch), the text
// sent to GitLab with its quick actions and references made harmless, the
// metrics, and the helpers for their config. An action, such as gitlabmr,
// adds what is its own: which files it may change and how it checks them. The
// client is made by internal/gitlabapi.
//
// What it relies on from GitLab, as the public source and API documentation
// at the tag v19.5.1-ee of gitlab.com/gitlab-org/gitlab show them (paths are
// under doc/ unless noted, and Gitaly is at the tag v19.5.1 of
// gitlab.com/gitlab-org/gitaly):
//
//   - api/commits.md and lib/api/commits.rb: POST /projects/:id/repository/commits
//     takes a branch, a commit_message, a start_sha and actions of create,
//     update, delete, move and chmod, each with a file_path and, except for a
//     delete, a content that is text unless the encoding says base64. A branch
//     that does not exist is created when a start_sha (or start_branch) is
//     given, so the branch and its commit appear together. When the branch
//     exists already and the start differs, the answer is 400 "A branch called
//     ... already exists" unless force is set, which the action never does
//     (app/services/commits/create_service.rb). Every failure of the call is a
//     400 with a message.
//   - Gitaly internal/gitaly/service/operations/commit_files.go and
//     lib/gitlab/gitaly_client/operation_service.rb: a create of a path that
//     exists answers "A file with this name already exists", an update or
//     delete of a path that is not a file answers "A file with this name
//     doesn't exist", an update keeps the file's mode, and a create over a
//     directory replaces it, so the action never creates a file whose name is
//     a directory. The action picks create, update or delete from what it
//     read at the start_sha, so none of these happens unless the files moved.
//   - api/merge_requests.md and lib/api/merge_requests.rb: POST
//     /projects/:id/merge_requests takes a source_branch, a target_branch, a
//     title and a description, among other fields the action never sends, and
//     answers 409 when another open merge request has the same source branch
//     (lib/api/helpers/merge_requests_helpers.rb, app/models/merge_request.rb).
//     It has no parameter for a draft: a merge request is a draft when its title
//     starts with "Draft:", "[Draft]" or "(Draft)" in any case
//     (app/models/merge_request.rb DRAFT_REGEX, lib/gitlab/regex/merge_requests.rb).
//     PUT /projects/:id/merge_requests/:iid takes a state_event of close or
//     reopen. A merge request is opened, closed, locked (being merged) or
//     merged. GET /projects/:id/merge_requests takes source_branch and state=all
//     to find a merge request in any state.
//   - user/project/quick_actions.md, app/services/issuable_base_service.rb and
//     lib/gitlab/quick_actions/extractor.rb: a description is read for quick
//     actions when it is saved, with the rights of the token's user. A command
//     is a slash and a name at the start of a line, matched in any case, after
//     the extractor deletes every carriage return; text in a code span is
//     ignored. text.go puts such a line in a code span.
//   - config/initializers/1_settings.rb: the words that close an issue when a
//     merge request merges (close, fix, resolve and implement, in their forms)
//     and what may follow them. text.go puts the target in a code span.
//   - api/repository_files.md, api/repositories.md and lib/api/files.rb: a file
//     is read at a commit with GET .../repository/files/:path?ref=<sha>, which
//     answers its blob_id, its size and its base64 content, or 404 for a path
//     that is no file; a directory is listed with GET .../repository/tree, which
//     has no sizes, and a blob is read by its ID with GET
//     .../repository/blobs/:sha. Text that is not stored as UTF-8 is converted
//     before it is encoded, so the action refuses a file whose decoded length is
//     not its size. A branch is read with GET .../repository/branches/:branch,
//     which answers its commit, and removed with DELETE on the same path.
package gitlabgit
