// Package gitchange holds the parts of the pull request and merge request
// actions that do not depend on the Git host: path globs and refused paths,
// the limits and parameters of a change, text cleaning, line counting, the
// plan for validating files, and the daily job that reads the state of the
// requests the actions opened. A host package, such as githubpr, adds what
// talks to its host.
package gitchange
