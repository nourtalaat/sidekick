// Package slackapp is the Slack app's receiving side: it takes Slack
// events over a Socket Mode connection, or at POST /slack/events with the
// Events API, and turns the messages that matter into stored events.
//
// Both modes hand each Events API payload to one Handler. A channel message
// that a listener matches becomes an alert (or a recovery event, when the
// listener's recovery pattern matches); a mention of Sidekick becomes a
// mention event, for the message the mention is in or, in a thread, for
// the thread's first message, which is fetched with conversations.replies.
// Events are stored through package ingest, keyed by the Slack event ID,
// before Slack is acknowledged, so a payload that Slack sends again is
// stored once and a payload that could not be stored is sent again.
//
// Each replica opens at most one Socket Mode connection, and only while it
// holds one of SocketSlots advisory locks, because Slack allows 10
// connections per app.
//
// Message text is untrusted: it is redacted before it is stored and never
// logged.
package slackapp
