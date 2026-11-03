// Package argocd is the native Argo CD source. It registers itself as the
// source type "argocd".
//
// A source reads the applications of one Argo CD server through its REST
// API with an API token, and only reads. Its three tools list applications
// with their sync status, health and whether auto-sync is on, get one
// application's source, sync policy, last operation and resources that are
// not in order, and get one application's deployment history with the
// commit of each deploy. The config says which applications the tools may
// read, as project/name globs; a call for any other application is a tool
// error before anything is sent. Every result links to the application's
// page in the Argo CD UI.
//
// The API facts the source relies on are listed in package
// github.com/nourtalaat/sidekick/internal/argocd, which holds the client.
package argocd
