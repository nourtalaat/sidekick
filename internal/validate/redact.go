package validate

import "regexp"

// redacted takes the place of a token in messages.
const redacted = "<redacted>"

// jwtLike matches text that looks like a JWT: three base64url parts joined
// by dots, each long enough that version numbers and host names do not
// match.
var jwtLike = regexp.MustCompile(`[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)

// RedactTokens replaces everything in s that looks like a JWT with
// <redacted>. The modules the validator checks are written by the model,
// and an OpenTofu function such as file() can read a token the container
// can see into an error message. The chart keeps the tokens out of the
// validator's container; this is a second line of defence for every
// message that leaves the validator.
func RedactTokens(s string) string {
	return jwtLike.ReplaceAllString(s, redacted)
}

// redactResult returns res with RedactTokens applied to every message.
func redactResult(res Result) Result {
	res.Errors = redactProblems(res.Errors)
	res.NotChecked = redactProblems(res.NotChecked)

	return res
}

// redactProblems returns a copy of problems with their messages redacted.
func redactProblems(problems []Problem) []Problem {
	if problems == nil {
		return nil
	}

	out := make([]Problem, len(problems))
	for i, p := range problems {
		p.Message = RedactTokens(p.Message)
		out[i] = p
	}

	return out
}
