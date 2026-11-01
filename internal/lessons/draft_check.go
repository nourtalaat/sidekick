package lessons

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// drafted is a lesson the model drafted and Sidekick accepted.
type drafted struct {
	text  string
	scope Scope
}

// answer is the input of submit_lesson.
type answer struct {
	Text  string `json:"text"`
	Scope Scope  `json:"scope"`
}

// check reads the input of submit_lesson. It returns the lesson, or the
// problems that make the answer unusable, in words for the model. The text
// is cut into one line and redacted; the scope may name only the
// investigation's own service, alert name, route and strategy, and when it
// names none it is the service and alert name, or the route when the
// investigation has neither.
func (j *DraftJob) check(input json.RawMessage, facts draftFacts) (drafted, []string) {
	var a answer
	if err := json.Unmarshal(input, &a); err != nil {
		return drafted{}, []string{"the input is not the JSON the tool takes: " + err.Error()}
	}

	var problems []string

	text, textProblems := j.checkText(a.Text)
	problems = append(problems, textProblems...)

	scope, scopeProblems := defaultScope(a.Scope, facts.scope)
	problems = append(problems, scopeProblems...)

	if len(problems) > 0 {
		return drafted{}, problems
	}

	return drafted{text: text, scope: scope}, nil
}

// checkText returns text as one redacted line, or the problems with it.
func (j *DraftJob) checkText(text string) (string, []string) {
	text = strings.Join(strings.Fields(text), " ")

	if j.redactor != nil {
		text = j.redactor.Text(text).Text
	}

	switch {
	case text == "":
		return "", []string{"text is empty"}
	case strings.ContainsFunc(text, unicode.IsControl):
		return "", []string{"text has control characters"}
	case utf8.RuneCountInString(text) > MaxTextChars:
		return "", []string{fmt.Sprintf("text has %d characters; at most %d are allowed", utf8.RuneCountInString(text), MaxTextChars)}
	default:
		return text, nil
	}
}

// defaultScope returns the scope a draft gets: asked, when every field it
// sets is the investigation's own value, and otherwise the problems with
// it. An empty scope becomes the service and alert name of the
// investigation, or its route when it has neither.
func defaultScope(asked, own Scope) (Scope, []string) {
	var problems []string

	for _, f := range []struct{ name, asked, own string }{
		{"service", asked.Service, own.Service},
		{"alert", asked.Alert, own.Alert},
		{"route", asked.Route, own.Route},
		{"strategy", asked.Strategy, own.Strategy},
	} {
		switch {
		case f.asked == "" || f.asked == f.own:
		case f.own == "":
			problems = append(problems, fmt.Sprintf("scope.%s is set, but this investigation has no %s; leave it out", f.name, f.name))
		default:
			problems = append(problems, fmt.Sprintf("scope.%s must be %q, the %s of this investigation, or left out", f.name, f.own, f.name))
		}
	}

	if len(problems) > 0 {
		return Scope{}, problems
	}

	if !asked.Global() {
		return asked, nil
	}

	if own.Service != "" || own.Alert != "" {
		return Scope{Service: own.Service, Alert: own.Alert}, nil
	}

	return Scope{Route: own.Route}, nil
}
