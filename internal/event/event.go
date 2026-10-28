// Package event defines the common event format that every intake produces.
package event

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Kind says what an event represents.
type Kind string

// Event kinds.
const (
	KindAlert    Kind = "alert"
	KindResolved Kind = "resolved"
	KindMessage  Kind = "message"
	KindMention  Kind = "mention"
)

// Severity is the sender's severity, normalized.
type Severity string

// Severities.
const (
	SeverityUnknown  Severity = "unknown"
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// SenderKind tells a human sender from a bot or another system.
type SenderKind string

// Sender kinds.
const (
	SenderHuman  SenderKind = "human"
	SenderBot    SenderKind = "bot"
	SenderSystem SenderKind = "system"
)

// Trust is decided by the intake configuration, never by the payload.
type Trust string

// Trust levels. Untrusted is the zero value on purpose.
const (
	Untrusted Trust = ""
	Trusted   Trust = "trusted"
)

// Sender identifies who or what produced the event.
type Sender struct {
	Kind SenderKind
	ID   string
	Name string
}

// Link is a URL the sender attached, such as a dashboard or runbook.
type Link struct {
	Title string
	URL   string
}

// SlackRef locates a Slack message that produced or carries the event.
// Its fields are stored without redaction, so replies reach the message;
// a new field is redacted unless the event writer lists it as an ID.
type SlackRef struct {
	TeamID   string
	Channel  string
	TS       string
	ThreadTS string
	UserID   string
}

// TeamsRef locates a Microsoft Teams channel message that produced or
// carries the event. Its fields are stored without redaction, so replies
// reach the message; a new field is redacted unless the event writer lists
// it as an ID.
type TeamsRef struct {
	// TenantID is the Microsoft Entra tenant of the team.
	TenantID string
	// TeamID is the team's Microsoft 365 group ID, which Graph names it by.
	TeamID string
	// ChannelID is the channel's conversation ID, such as
	// 19:abc@thread.tacv2, without a ";messageid=" part.
	ChannelID string
	// MessageID is the ID of the message.
	MessageID string
	// ThreadID is the ID of the first message of the message's thread. It
	// is MessageID for a message that starts a thread.
	ThreadID string
	// UserID is the sender's Teams user ID, such as 29:1abc.
	UserID string
	// ServiceURL is the Bot Framework service URL the message came from,
	// which replies to it are sent to.
	ServiceURL string
}

// Event is one thing that may need investigating.
type Event struct {
	// Key makes repeated delivery safe: (Intake, Key) is unique.
	Key         string
	Intake      string
	Kind        Kind
	Sender      Sender
	Trust       Trust
	AlertName   string
	Title       string
	Text        string
	Service     string
	Severity    Severity
	Labels      map[string]string
	Fingerprint string
	StartsAt    time.Time
	EndsAt      time.Time
	ReceivedAt  time.Time
	Links       []Link
	Slack       *SlackRef
	Teams       *TeamsRef
	// Raw is the redacted original payload, kept for debugging and scenario export.
	Raw json.RawMessage
}

// fingerprintBytes is how much of the SHA-256 sum a fingerprint keeps: 128 bits,
// shown as 32 hex characters.
const fingerprintBytes = 16

// Fingerprint builds the grouping key from the sender type, the alert identity
// and the label values named in keys. Missing labels count as empty.
func Fingerprint(senderType, alertName string, labels map[string]string, keys []string) string {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)

	var b strings.Builder
	b.WriteString(senderType)
	b.WriteByte(0)
	b.WriteString(alertName)
	for _, k := range sorted {
		b.WriteByte(0)
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
	}

	sum := sha256.Sum256([]byte(b.String()))

	return hex.EncodeToString(sum[:fingerprintBytes])
}
