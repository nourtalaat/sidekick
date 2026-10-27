// Package gcp is the native Google Cloud source. It registers itself as the
// source type "gcp".
//
// A source reads the Google Cloud projects its config lists through the
// REST APIs of Cloud Logging, Cloud Monitoring, Google Kubernetes Engine
// and Personalized Service Health. It gets access tokens from Application
// Default Credentials, which on GKE come from Workload Identity, or from a
// credential file in a secret that it reads again for each new token.
// golang.org/x/oauth2 is used only for those tokens; the API calls are
// plain HTTP, so the source needs no gRPC client libraries.
//
// Every tool only reads. A call for a project the config does not list is
// a tool error before anything is sent, and every result links to the
// Google Cloud console pages a person can open to check it.
package gcp
