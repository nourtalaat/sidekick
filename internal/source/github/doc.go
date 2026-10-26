// Package github is the native GitHub source (SRC-4). It registers itself
// as the source type "github".
//
// A source reads the repositories its config lists through the GitHub REST
// API, authenticated as a GitHub App installation. It asks GitHub for
// installation tokens that can only read, whatever the app itself may do,
// and reads the app's private key from its secret on each token exchange,
// so a rotated key needs no restart. Every tool only reads, and every
// result links to the GitHub pages a person can open to check it.
package github
