package teamsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/teams"
)

// DefaultGraphURL is the Microsoft Graph API root of the public cloud.
const DefaultGraphURL = "https://graph.microsoft.com/v1.0/"

// maxGraphResponseBytes is the largest Graph answer read: 1 MiB.
const maxGraphResponseBytes = 1 << 20

var (
	// errMessageUnreadable is wrapped when Graph answers 403 or 404: the
	// bot may not read the message, usually because the team did not
	// grant ChannelMessage.Read.Group, or the message is gone. Asking
	// again cannot help.
	errMessageUnreadable = errors.New("the message cannot be read")
	errGraphTooLarge     = errors.New("the Graph response is larger than 1 MiB")
)

// graph reads channel messages through Microsoft Graph with the bot's
// token, which the team's resource-specific consent to
// ChannelMessage.Read.Group lets it read.
type graph struct {
	baseURL string
	client  *http.Client
	tokens  *teams.Tokens
}

// chatMessage is the part of a Graph channel message Sidekick reads.
type chatMessage struct {
	ID   string `json:"id"`
	From struct {
		User *struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"user"`
		Application *struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"application"`
	} `json:"from"`
	Body struct {
		Content string `json:"content"`
	} `json:"body"`
	Attachments []attachment `json:"attachments"`
}

// message reads the message with id in channel of team. An error that
// wraps errMessageUnreadable means Graph will not give it.
func (g *graph) message(ctx context.Context, team, channel, id string) (chatMessage, error) {
	u := g.baseURL + "teams/" + url.PathEscape(team) + "/channels/" + url.PathEscape(channel) + "/messages/" + url.PathEscape(id)

	token, err := g.tokens.Token(ctx)
	if err != nil {
		return chatMessage{}, fmt.Errorf("get a Graph token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return chatMessage{}, fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return chatMessage{}, fmt.Errorf("get the message: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(httpclient.LimitBody(resp.Body, maxGraphResponseBytes, errGraphTooLarge))
	if err != nil {
		return chatMessage{}, fmt.Errorf("read the message: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusNotFound:
		return chatMessage{}, fmt.Errorf("graph answered HTTP %d: %w", resp.StatusCode, errMessageUnreadable)
	case http.StatusUnauthorized:
		g.tokens.Invalidate(token)

		return chatMessage{}, fmt.Errorf("graph refused the token (HTTP %d)", resp.StatusCode)
	default:
		return chatMessage{}, fmt.Errorf("graph answered HTTP %d", resp.StatusCode)
	}

	var m chatMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return chatMessage{}, fmt.Errorf("decode the message: %w", err)
	}

	return m, nil
}

// activityOf returns m as the activity of a message in the channel and
// thread of a, so listeners can match it.
func activityOf(m chatMessage, a activity) activity {
	out := activity{
		Type:         activityMessage,
		ID:           m.ID,
		ServiceURL:   a.ServiceURL,
		ChannelID:    a.ChannelID,
		Recipient:    a.Recipient,
		Conversation: conversation{ID: a.channel() + messageIDMarker + m.ID, ConversationType: conversationKind, TenantID: a.tenant()},
		Text:         m.Body.Content,
		Attachments:  m.Attachments,
		ChannelData:  a.ChannelData,
	}

	switch {
	case m.From.Application != nil:
		out.From = account{ID: botIDPrefix + m.From.Application.ID, Name: m.From.Application.DisplayName, Role: roleBot}
	case m.From.User != nil:
		out.From = account{AADObjectID: m.From.User.ID}
	}

	return out
}
