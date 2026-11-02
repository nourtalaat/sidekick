// Package slack holds the actions that reply in a Slack thread (slack.reply)
// and add a reaction to the alert message (slack.reaction). They post as the
// bot of the slack section. The model gives only the text or the emoji; the
// message is found from the investigation, and the channel it is in must be
// one the config allows. The model's text is posted as plain text: it is
// never read as Slack markup, so it cannot mention anyone or add a link.
package slack

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/actionhttp"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// The action types this package registers. A config selects one with
// "type: slack.reply" or "type: slack.reaction".
const (
	TypeReply    = "slack.reply"
	TypeReaction = "slack.reaction"
)

// DefaultAPIURL is the base URL of the Slack Web API.
const DefaultAPIURL = "https://slack.com/api/"

// Limits on the parameters the model may give.
const (
	maxReplyChars = 1500
)

// emojiPattern is what the name of an emoji looks like, without colons.
var emojiPattern = regexp.MustCompile(`^[a-z0-9_+-]{1,100}$`)

var (
	errNoChannels = errors.New("limits.channels: list at least one channel ID, such as C0123456789")
	errNoEmoji    = errors.New("limits.emoji: list at least one emoji name, such as eyes")
	errNoToken    = errors.New("the slack section with botTokenSecret is required")
	errNoSecrets  = errors.New("no secrets resolver to read the bot token with")
)

// init registers the action types when the package is linked in.
func init() {
	registry.Actions.Register(TypeReply, registry.Typed(NewReply))
	registry.Actions.Register(TypeReaction, registry.Typed(NewReaction))
}

// Connection is how an action reaches Slack. The bot token is the one of the
// slack section and needs no key of its own.
type Connection struct {
	// APIURL is the base URL of the Web API, ending in a slash. Empty means
	// DefaultAPIURL. It must be https, except on localhost.
	APIURL string `yaml:"apiURL"`
}

// validate checks the connection without secrets or network calls.
func (c Connection) validate() []error {
	if c.APIURL == "" {
		return nil
	}

	return []error{actionhttp.CheckURL("apiURL", c.APIURL)}
}

// validateChannels checks the allowed channels.
func validateChannels(channels []string) []error {
	if len(channels) == 0 {
		return []error{errNoChannels}
	}

	var errs []error

	for i, ch := range channels {
		if !channelPattern.MatchString(ch) {
			errs = append(errs, fmt.Errorf("limits.channels[%d]: %q is not a channel ID, such as C0123456789", i, ch))
		}
	}

	return errs
}

// service is what both action types share.
type service struct {
	name      string
	channels  []string
	client    *client
	targets   Targets
	publicURL string
}

// newService builds the part both actions share.
func newService(name string, conn Connection, channels []string, deps registry.Deps) (service, error) {
	if deps.SlackBotToken == (registry.SecretRef{}) {
		return service{}, errNoToken
	}

	if deps.Secrets == nil {
		return service{}, errNoSecrets
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	apiURL := conn.APIURL
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}

	return service{
		name:      name,
		channels:  channels,
		client:    &client{apiURL: strings.TrimSuffix(apiURL, "/") + "/", http: hc, secrets: deps.Secrets, token: deps.SlackBotToken},
		targets:   dbTargets{db: deps.DB},
		publicURL: deps.PublicURL,
	}, nil
}

// channelAllowed returns an error wrapping action.ErrLimit when channel is
// not one of the allowed channels.
func (s service) channelAllowed(channel string) error {
	if !slices.Contains(s.channels, channel) {
		return action.LimitError(errChannelLimit, "channel %s", channel)
	}

	return nil
}

var errChannelLimit = errors.New("the message is in a channel that is not one of limits.channels")

// escape writes the three characters Slack treats as control characters of
// its markup as entities, so text cannot add a mention such as <!channel>
// or a link.
func escape(s string) string {
	return escaper.Replace(s)
}

var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
