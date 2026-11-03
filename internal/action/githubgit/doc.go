// Package githubgit holds the calls to the GitHub REST API that the actions
// opening pull requests on GitHub share: reading the head of a branch,
// writing one commit through the Git data API, opening, finding and closing
// a pull request, reading a blob, the text of a commit and a pull request,
// and the helpers for their config. A pull request action, such as
// githubpr, adds what is its own: which files it may change and how it reads
// them. The GitHub App client is made by internal/githubapp.
//
// What it relies on from GitHub, as the public OpenAPI description
// (github/rest-api-description, commit
// 6348fd455a44a445f9e37a505e981b2c3ed24046, file
// descriptions/api.github.com/api.github.com.2022-11-28.json) and go-github
// v93.0.0 (github/git_trees.go, git_blobs.go, git_refs.go, pulls.go) say:
//
//   - git/create-tree takes entries with a path, a mode, a type and either
//     content or the SHA of a blob. A null SHA deletes the entry, and
//     go-github sends it when both Content and SHA are nil.
//   - git/get-blob serves blobs up to 100 megabytes; with the raw media type
//     the body is the content itself, which GetBlobRaw uses.
//   - pulls/create takes head, base, title, body and draft.
//   - An installation token gets only the permissions its request names, and
//     never more than the app has (apps/create-installation-access-token).
package githubgit
