// Package teamsapp is the Microsoft Teams bot's receiving side: the Bot
// Framework posts each activity to POST /teams/messages, and the channel
// messages that matter become stored events.
//
// Every activity's Bot Framework token is checked first, and an activity
// from any tenant but the bot's own is refused before anything is
// stored. A channel message that a Teams listener matches becomes an alert
// (or a recovery event, when the listener's recovery pattern matches); a
// mention of the bot becomes a mention event, for the message the mention
// is in or, in a thread, for the thread's first message. That message is
// taken from the event a listener stored for it, or read through
// Microsoft Graph. Events are stored through package ingest, keyed by the
// channel and the message ID, before the activity is answered, so an
// activity sent again is stored once.
//
// The bot receives every message of the channels of the teams it is in
// only when its app manifest asks for the resource-specific consent
// permission ChannelMessage.Read.Group and the team's owner grants it;
// without it, only mentions arrive.
//
// Message text is untrusted: it is redacted before it is stored and never
// logged.
package teamsapp
