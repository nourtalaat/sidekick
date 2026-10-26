// Package prompt assembles the prompt of an investigation and marks the
// untrusted data in it.
//
// Text that comes from outside Sidekick, such as tool results and the text
// of an event, is wrapped with WrapUntrusted before it reaches the model.
// The system prompt tells the model that text inside those blocks is data
// to analyze, never instructions.
package prompt
