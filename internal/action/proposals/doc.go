// Package proposals stores the actions the model proposes and moves them
// from proposed to a result: a person approves or rejects each one, the
// execute_action job runs an approved one, and a proposal that nobody
// decides expires. It is the one place that changes the action_proposals
// table.
package proposals
