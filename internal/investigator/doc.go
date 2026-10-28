// Package investigator runs investigations: the investigate job, its agent
// loop, and what the router shares with it, which are the job's payload
// and unique key and the settings stored with each investigation.
//
// The router creates each investigation in the queued status, with its
// event group, and enqueues one investigate job for it in the same
// transaction. The Handler runs that job. It takes over the run with the
// job's claim token as the fence, then loops: it asks the model for a
// turn, runs the tools the model calls through the source guard, and
// saves every prompt, model turn, tool result and note as a step before
// the next one starts. Each step write takes the next seq in the same
// transaction as a check of the fence, so a worker that lost its lease
// writes nothing more, and the next worker resumes from the last saved
// step with no gaps and no repeats.
//
// When the investigation's route has a triage role, the run starts with
// triage: one call to the triage model, which answers with submit_triage.
// Its request, its answer and Sidekick's decision are saved as steps
// before the prompt, and are not part of the conversation. The decision
// either picks the strategy of the full investigation, or ends the run
// with a triage-only result (duplicate, known_issue or flapping). An
// answer that cannot be used leads to a full investigation with the
// route's strategy. InvestigateAnyway sets a triage-only investigation
// back to queued for a full run, which follows the triage's strategy.
//
// The run ends when the model submits a report that passes its checks,
// when it gives no such report, or when a limit is reached; the last two
// end as inconclusive, with a report built from the saved steps. Progress
// and the final report go to the route's outputs through the publish job.
//
// A responder can stop a run from any replica with RequestCancel, which
// sets the investigation's stop flag and sends a notification. The
// Canceller of the replica that owns the run hears it and ends the context
// of the model or tool call in flight; each step write also returns the
// flag, so a run sees the stop at its next step even when the notification
// is lost. The run then ends as cancelled, with a report built from the
// saved steps.
package investigator
