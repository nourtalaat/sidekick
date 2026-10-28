// Package slack is the Slack output: it posts an investigation as one
// message and edits it in place as the investigation runs, through the
// Slack Web API with the bot token of the shared Slack app.
//
// The message goes in the thread of the Slack message the event came from,
// or, for webhook events, as a new message in the output's channel. While
// the investigation runs, the message's bar is brand orange; the final edit
// takes the verdict's color and always shows the verdict's emoji and label
// too. Each new verdict also posts a short thread reply, because editing a
// message notifies nobody, and a report too long for the main message
// continues in a thread reply.
//
// The message shows a line for each watcher of the investigation's group,
// with a Stop watching button for each one that runs, and each change of a
// watcher's state posts a short thread reply too. A watcher line gives
// times of day rather than durations up to now, so it stays true between
// edits.
//
// Edits of one message are at most one every 3 seconds, and an update with
// a Seq no newer than the one the message shows is dropped. Each replica
// limits its calls per channel, and a 429 is retried after its
// Retry-After.
//
// Text from the report and the event is escaped, so it cannot add
// mentions or links; the only links are evidence links from tool results.
package slack
