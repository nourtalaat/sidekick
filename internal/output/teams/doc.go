// Package teams is the Microsoft Teams output: it posts an investigation
// as one message and edits it in place as the investigation runs, through
// the Bot Framework Connector, as the bot of the teams section.
//
// The message goes in the thread of the Teams message the event came
// from, or, for other events such as webhook alerts, starts a new thread
// in the output's channel. It is an Adaptive Card whose top section takes
// the color of the investigation's state, always with an icon and a label
// too. Each new verdict also posts a short thread reply, because editing a
// message notifies nobody, and a report too long for the main message
// continues in a thread reply.
//
// Edits of one message are at most one every 3 seconds, and an update with
// a Seq no newer than the one the message shows is dropped. Each replica
// limits its calls per thread, and the answers Teams asks bots to retry
// (429, 412, 502 and 504) are retried after their Retry-After or with
// backoff.
//
// All text is in Adaptive Card text runs, which Teams shows as plain text,
// so text from the report and the event cannot add mentions, links or
// formatting; the only links are evidence links from tool results and the
// link to the web UI. The bot's token is sent only to Bot Framework hosts.
package teams
