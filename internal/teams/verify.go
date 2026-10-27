package teams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/intake"
)

// DefaultOpenIDMetadataURL is where the Bot Framework publishes the
// OpenID metadata that names its signing keys.
const DefaultOpenIDMetadataURL = "https://login.botframework.com/v1/.well-known/openidconfiguration"

// BotFrameworkIssuer is the issuer of the tokens the Bot Framework sends
// with activities.
const BotFrameworkIssuer = "https://api.botframework.com"

// ChannelTeams is the channel ID of Microsoft Teams activities.
const ChannelTeams = "msteams"

// ClockLeeway is how far a token's validity times may be off from
// Sidekick's clock, the industry-standard 5 minutes the Bot Framework
// documents.
const ClockLeeway = 5 * time.Minute

// signingMethod is the only algorithm Bot Framework tokens are signed
// with.
const signingMethod = "RS256"

// bearerScheme is the scheme of the Authorization header of an activity.
const bearerScheme = "Bearer"

// Verifier checks the tokens the Bot Framework sends with activities. It
// is safe for concurrent use.
type Verifier struct {
	parser *jwt.Parser
	keys   *keyCache
	// now is the clock. Tests replace it.
	now func() time.Time
}

// VerifierOptions set up a Verifier. AppID is required.
type VerifierOptions struct {
	// AppID is the bot's Microsoft App ID, the audience tokens must have.
	AppID string
	// MetadataURL is the OpenID metadata document. Empty means
	// DefaultOpenIDMetadataURL. It must be https, except on a loopback
	// host, and so must the key set it names.
	MetadataURL string
	// HTTP fetches the metadata and the keys. Nil means httpclient.New().
	HTTP *http.Client
	// Logger receives a warning when the keys could not be refreshed and
	// the cached ones are kept. Nil logs nothing.
	Logger *slog.Logger
}

// botClaims are the claims of a Bot Framework token that Sidekick checks.
type botClaims struct {
	jwt.RegisteredClaims

	// ServiceURL is the service URL the token is for; it must be the
	// activity's.
	ServiceURL string `json:"serviceurl"`
}

// Verify checks the Authorization header value of an activity whose
// channelId is channelID and whose serviceUrl is serviceURL. The token
// must be a Bearer token signed with RS256 by one of the Bot Framework's
// keys, issued by BotFrameworkIssuer for the bot's app ID, valid now
// within ClockLeeway, with a serviceurl claim equal to serviceURL. The
// key must be endorsed for channelID. An error that wraps
// intake.ErrUnauthenticated means the request must be refused; any other
// error means the keys could not be fetched, so the request should be
// sent again later. No error says which check failed beyond the log.
func (v *Verifier) Verify(ctx context.Context, authorization, channelID, serviceURL string) error {
	scheme, token, ok := strings.Cut(authorization, " ")
	if !ok || !strings.EqualFold(scheme, bearerScheme) || token == "" {
		return fmt.Errorf("no Bearer token: %w", intake.ErrUnauthenticated)
	}

	if channelID == "" || serviceURL == "" {
		return fmt.Errorf("the activity has no channel or service URL: %w", intake.ErrUnauthenticated)
	}

	var (
		claims botClaims
		key    signingKey
	)

	_, err := v.parser.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)

		k, err := v.keys.key(ctx, kid, v.now())
		if err != nil {
			return nil, fmt.Errorf("find the signing key: %w", err)
		}

		key = k

		return k.public, nil
	})

	switch {
	case errors.Is(err, errKeysUnavailable):
		return fmt.Errorf("check the token: %w", err)
	case err != nil:
		return fmt.Errorf("check the token: %w: %w", intake.ErrUnauthenticated, err)
	case !strings.EqualFold(claims.ServiceURL, serviceURL):
		return fmt.Errorf("the token is for another service URL: %w", intake.ErrUnauthenticated)
	case !slices.Contains(key.endorsements, channelID):
		return fmt.Errorf("the signing key is not endorsed for channel %q: %w", channelID, intake.ErrUnauthenticated)
	}

	return nil
}

// NewVerifier checks opts and returns a Verifier. It fetches no keys yet;
// the first Verify does.
func NewVerifier(opts VerifierOptions) (*Verifier, error) {
	if opts.AppID == "" {
		return nil, errNoAppID
	}

	metadataURL := opts.MetadataURL
	if metadataURL == "" {
		metadataURL = DefaultOpenIDMetadataURL
	}

	if err := checkSecureURL(metadataURL); err != nil {
		return nil, fmt.Errorf("OpenID metadata URL: %w", err)
	}

	client := opts.HTTP
	if client == nil {
		client = httpclient.New()
	}

	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	v := &Verifier{
		keys: &keyCache{metadataURL: metadataURL, client: client, log: log},
		now:  time.Now,
	}

	v.parser = jwt.NewParser(
		jwt.WithValidMethods([]string{signingMethod}),
		jwt.WithIssuer(BotFrameworkIssuer),
		jwt.WithAudience(opts.AppID),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(ClockLeeway),
		jwt.WithTimeFunc(func() time.Time { return v.now() }),
	)

	return v, nil
}
