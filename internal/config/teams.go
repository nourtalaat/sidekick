package config

import (
	"cmp"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// TeamsMentionIntake is the intake name of events that start because
// someone mentioned Sidekick in Microsoft Teams. It is reserved like
// SlackMentionIntake.
const TeamsMentionIntake = "teams-mention"

// ListenerSource is the chat app a listener watches.
type ListenerSource string

// Listener sources.
const (
	// ListenerSourceSlack watches a Slack channel through the Slack app.
	ListenerSourceSlack ListenerSource = "slack"
	// ListenerSourceTeams watches a Teams channel through the Teams bot.
	ListenerSourceTeams ListenerSource = "teams"
)

// knownListenerSources are the values a listener's source can take; empty
// means slack.
var knownListenerSources = []ListenerSource{ListenerSourceSlack, ListenerSourceTeams}

var (
	// guidPattern is a Microsoft Entra ID: an app (client) ID, a tenant ID
	// or an object ID.
	guidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// teamsChannelPattern is a Teams channel's conversation ID, such as
	// 19:abc123@thread.tacv2.
	teamsChannelPattern = regexp.MustCompile(`^19:[A-Za-z0-9_.=-]+@thread\.(tacv2|skype)$`)
	// teamsBotPattern is a bot's Teams ID: "28:" and its app ID.
	teamsBotPattern = regexp.MustCompile(`^28:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Teams holds the Microsoft Teams bot's identity and how it gets tokens
// (IN-8). The bot is an Azure Bot resource, of type single tenant or
// user-assigned managed identity, whose messaging endpoint is Sidekick's
// /teams/messages.
type Teams struct {
	// AppID is the bot's Microsoft App ID: the client ID of its Microsoft
	// Entra app registration or user-assigned managed identity. Tokens
	// that Teams sends must be for it.
	AppID string `yaml:"appID"`
	// TenantID is the Microsoft Entra tenant the bot and the teams it
	// serves are in. Activities from any other tenant are refused.
	TenantID string `yaml:"tenantID"`
	// ClientSecret holds a client secret of the app registration. Set it
	// or WorkloadIdentity, not both.
	ClientSecret registry.SecretRef `yaml:"clientSecret"`
	// WorkloadIdentity gets tokens with the federated token of Microsoft
	// Entra Workload ID on AKS, from the file AZURE_FEDERATED_TOKEN_FILE
	// names, instead of a client secret. The app registration or managed
	// identity needs a federated credential for Sidekick's service
	// account.
	WorkloadIdentity bool `yaml:"workloadIdentity"`
	// TrustedBots are the Teams IDs of bots, such as
	// 28:00000000-0000-0000-0000-000000000000, whose messages count as
	// trusted in listeners marked trusted.
	TrustedBots []string `yaml:"trustedBots"`
	// ServiceURL is the Bot Framework service URL for posts that do not
	// answer a Teams message, such as the result of a webhook alert,
	// ending in "/". Empty means https://smba.trafficmanager.net/teams/.
	ServiceURL string `yaml:"serviceURL"`
}

// OnTeams reports whether the listener watches a Teams channel. A listener
// with no source watches Slack.
func (l Listener) OnTeams() bool {
	return l.Source == ListenerSourceTeams
}

// checkTeams checks the teams section.
func (v *validator) checkTeams() {
	t := v.cfg.Teams
	if t == nil {
		return
	}

	switch {
	case t.AppID == "":
		v.errs.add("teams.appID", "required")
	case !guidPattern.MatchString(t.AppID):
		v.errs.add("teams.appID", "expected a client ID such as 00000000-1111-2222-3333-444444444444, got %q", t.AppID)
	}

	switch {
	case t.TenantID == "":
		v.errs.add("teams.tenantID", "required")
	case !guidPattern.MatchString(t.TenantID):
		v.errs.add("teams.tenantID", "expected a tenant ID such as 00000000-1111-2222-3333-444444444444, got %q", t.TenantID)
	}

	hasSecret := t.ClientSecret != (registry.SecretRef{})

	switch {
	case hasSecret && t.WorkloadIdentity:
		v.errs.add("teams", "set exactly one of clientSecret and workloadIdentity, not both")
	case !hasSecret && !t.WorkloadIdentity:
		v.errs.add("teams", "set clientSecret or workloadIdentity: true, so the bot can get tokens to post with")
	}

	v.checkIDs("teams.trustedBots", t.TrustedBots, teamsBotPattern, "a bot ID such as 28:00000000-1111-2222-3333-444444444444")

	if t.ServiceURL != "" && !isServiceURL(t.ServiceURL) {
		v.errs.add("teams.serviceURL", `expected an https URL ending in "/", such as https://smba.trafficmanager.net/teams/, got %q`, t.ServiceURL)
	}
}

// checkTeamsListener checks the fields of a listener on a Teams channel
// whose rules differ from Slack's.
func (v *validator) checkTeamsListener(at string, l Listener) {
	switch {
	case l.Channel == "":
		v.errs.add(at+".channel", "required")
	case !teamsChannelPattern.MatchString(l.Channel):
		v.errs.add(at+".channel", "expected a Teams channel ID such as 19:abc123@thread.tacv2, got %q", l.Channel)
	}

	v.checkIDs(at+".match.sender.bots", l.Match.Sender.Bots, teamsBotPattern, "a bot ID such as 28:00000000-1111-2222-3333-444444444444")
	v.checkIDs(at+".match.sender.users", l.Match.Sender.Users, guidPattern, "a user's Microsoft Entra object ID such as 00000000-1111-2222-3333-444444444444")

	if l.Trusted && (v.cfg.Teams == nil || len(v.cfg.Teams.TrustedBots) == 0) {
		v.errs.add(at+".trusted", "has no effect without teams.trustedBots, the bots whose messages are trusted")
	}
}

// checkListenerSource checks a listener's source and reports whether it
// is known.
func (v *validator) checkListenerSource(at string, l Listener) bool {
	if l.Source == "" || slices.Contains(knownListenerSources, l.Source) {
		return true
	}

	v.errs.add(at+".source", "expected slack or teams, got %q%s", l.Source, didYouMean(string(l.Source), asStrings(knownListenerSources)))

	return false
}

// hasListeners reports whether any listener watches source.
func (v *validator) hasListeners(source ListenerSource) bool {
	for _, l := range v.cfg.Listeners {
		if cmp.Or(l.Source, ListenerSourceSlack) == source {
			return true
		}
	}

	return false
}

// isServiceURL reports whether s is an absolute https URL ending in "/",
// or an http one on a loopback host.
func isServiceURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || !strings.HasSuffix(u.Path, "/") {
		return false
	}

	return u.Scheme == "https" || (u.Scheme == "http" && httpclient.IsLoopbackHost(u.Hostname()))
}
