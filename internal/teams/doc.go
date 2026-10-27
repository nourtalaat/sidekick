// Package teams holds what the Microsoft Teams intake and output share:
// the bot's tokens for the Bot Framework and Graph, and the check of the
// tokens that the Bot Framework sends with each activity.
//
// Both talk to Microsoft over plain REST with the HTTP client they are
// given. Tokens come from Microsoft Entra ID with the client credentials
// grant, using a client secret or the federated token of AKS workload
// identity. An activity's token must be signed with one of the Bot
// Framework's published keys, which are cached and refreshed every
// KeysRefreshInterval.
package teams
