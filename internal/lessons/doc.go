// Package lessons keeps the lessons that prompts show the model: short
// pieces of guidance that people reviewed.
//
// A lesson is drafted from feedback, or written by an admin. Its scope
// says which investigations it applies to, and a lesson with no scope is
// global. Choose picks the lessons of one prompt under the limits of the
// config, and a draft job turns feedback with a reason into a proposed
// lesson.
package lessons
