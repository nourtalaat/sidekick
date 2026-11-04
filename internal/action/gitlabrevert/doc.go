// Package gitlabrevert is the gitlab.revert action: it opens a draft merge
// request that reverts one named commit or one merged merge request, in a
// project the config allows, on gitlab.com or a self-managed GitLab. It sends
// a project, group or personal access token with the api scope, and the
// token's user needs the Developer role on the allowed projects, with the
// branches sidekick/* left unprotected.
//
// GitLab has a call that reverts a commit, so the action uses it. The branch
// sidekick/<proposal id> is made from the head of the base branch, the revert
// call writes one commit on it, and a draft merge request opens from it. The
// action never merges, approves, assigns, asks for a review or turns on
// auto-merge. A repeat after a crash finds the branch and the merge request it
// made. Proposing writes nothing to the project: the checks read, and the
// revert call at execution is the final check that the change applies.
//
// Before it reverts, the action compares each file the commit touched with the
// head of the base branch. A file that is not what the commit wrote refuses
// the whole revert and names the file, as github.revert does, so what the
// snapshot shows is exactly what the revert writes.
//
// What the action relies on from GitLab, as the public source at the tag
// v19.5.1-ee of gitlab.com/gitlab-org/gitlab and v19.5.1 of
// gitlab.com/gitlab-org/gitaly (paths are under doc/ unless noted) show:
//
//   - lib/api/commits.rb, app/services/commits/revert_service.rb and
//     api/commits.md: POST /projects/:id/repository/commits/:sha/revert takes
//     a branch, which must exist and which the token's user must be allowed to
//     push to, and a dry_run, which the action does not use because proposing
//     must not write. It takes no message. It answers the revert commit, or
//     400 with a message and an error_code of conflict or empty. A revert
//     that cannot apply cleanly is that 400, and the Commits API answers every
//     other failure of the same call with a 400 too.
//   - app/models/commit.rb: GitLab writes the message itself, Revert "<title>"
//     and "This reverts commit <sha>", or "This reverts merge request !<iid>"
//     for the commit of a merged merge request. It cannot carry a mark of
//     Sidekick, so the mark is in the description of the merge request.
//   - Gitaly internal/gitaly/service/operations/revert.go: the revert is a
//     three-way merge of the head of the branch with the first parent of the
//     commit, with the commit as the merge base, so a merge commit is reverted
//     against its first parent. It writes one commit whose parent is the head
//     of the branch.
//   - app/services/commits/change_service.rb: a conflict is the message
//     "Commit revert failed: Can't perform this action automatically. It may
//     have already been done, or a more recent commit may have updated some of
//     this content. Please perform this action locally." (the text in the
//     docs differs; the source is trusted). The error body is {message,
//     error_code}, and the API client keeps the message.
//   - lib/api/commits.rb, lib/gitlab/user_access.rb and lib/api/branches.rb:
//     the call needs push access to the branch, which the Developer role has
//     on an unprotected branch, and a branch is made with the same role.
//   - lib/api/entities/merge_request_basic.rb and
//     app/models/merge_request.rb: a merge request gives merge_commit_sha (set
//     for a merge commit, also with semi-linear history), squash_commit_sha
//     (set when it was squashed), merged_at, source_branch, target_branch,
//     description and draft. A merge request merged with fast-forward has
//     neither SHA, and the commits of GET .../merge_requests/:iid/commits are
//     the commits it brought.
//   - lib/api/commits.rb and lib/api/entities/diff.rb: GET
//     .../repository/commits/:sha/diff is paged and gives old_path, new_path,
//     a_mode, b_mode, collapsed and too_large for each file, against the first
//     parent of the commit (lib/gitlab/git/commit.rb, parent_id).
//     GET .../repository/commits/:sha/merge_requests lists the merge requests
//     that hold a commit.
//   - lib/api/repositories.rb: GET .../repository/merge_base with refs[] of
//     a commit and a branch answers the common ancestor, which is the commit
//     itself exactly when the commit is on the branch, and 404 when there is
//     none. GET .../repository/compare lists the commits of one ref that the
//     other lacks.
package gitlabrevert
