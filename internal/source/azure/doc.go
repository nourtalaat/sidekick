// Package azure is the native Azure source. It registers itself as the
// source type "azure".
//
// A source reads the Azure subscriptions and Log Analytics workspaces its
// config lists, through the Log Analytics query API and Azure Resource
// Manager. It gets Microsoft Entra tokens through AKS Workload Identity,
// or with a client secret it reads from a secret for each new token.
// azidentity and azcore are used only for those tokens; the API calls are
// plain HTTP, with every api-version pinned as a constant.
//
// Every tool only reads. A call for a subscription, resource or workspace
// the config does not allow is a tool error before anything is sent, and
// every result links to the Azure portal pages a person can open to check
// it.
package azure
