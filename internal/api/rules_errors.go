package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/problem"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// The codes of the problems that listener and route changes answer with.
const (
	codeValidation = "validation"
	codeFileRule   = "file_rule"
	codeExists     = "exists"
	codeStale      = "stale"
	codeConflict   = "conflict"
	codeNotFound   = "not_found"
)

// configFileName is the name of the config file in messages. The config
// does not know the path it was loaded from; Helm renders it as this.
const configFileName = "sidekick.yaml"

// configPathRoots are the first parts of the field paths that the config
// checks give to runtime rules, by kind.
var configPathRoots = map[string]string{
	rules.KindListener: "listeners",
	rules.KindRoute:    "routes",
}

// ruleError is the answer to a failed change of the listener or route of
// kind named name. An error it does not know answers 500.
func ruleError(err error, kind, name string) error {
	var problems config.Errors

	switch {
	case errors.As(err, &problems):
		return validationError(problems, kind, name)
	case errors.Is(err, rules.ErrFileRule):
		return &Error{
			Status: http.StatusConflict, Code: codeFileRule,
			Detail: fmt.Sprintf("The %s %s is defined in the config file %s, which only a config change can alter.", kind, name, configFileName),
		}
	case errors.Is(err, rules.ErrExists):
		return &Error{Status: http.StatusConflict, Code: codeExists, Detail: fmt.Sprintf("A %s named %s exists already.", kind, name)}
	case errors.Is(err, rules.ErrStale):
		return &Error{
			Status: http.StatusConflict, Code: codeStale,
			Detail: fmt.Sprintf("The %s %s was changed since you read it. Reload it to see the new version.", kind, name),
		}
	case errors.Is(err, rules.ErrConflict):
		return &Error{
			Status: http.StatusConflict, Code: codeConflict,
			Detail: fmt.Sprintf("The %s %s was changed at the same moment by someone else. Try again.", kind, name),
		}
	case errors.Is(err, rules.ErrNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeNotFound, Detail: fmt.Sprintf("No runtime %s is named %s.", kind, name)}
	default:
		return fmt.Errorf("change %s %s: %w", kind, name, err)
	}
}

// validationError is the 422 answer to a rule that fails the config
// checks, with a field for each problem.
func validationError(problems config.Errors, kind, name string) *Error {
	fields := make([]problem.FieldError, len(problems))

	for i, p := range problems {
		field := fieldOf(p.Path, configPathRoots[kind], name)
		if field == "" && strings.Contains(p.Message, strconv.Quote(name)) {
			field = "name"
		}

		fields[i] = problem.FieldError{Field: field, Message: p.Message}
	}

	return &Error{
		Status: http.StatusUnprocessableEntity, Code: codeValidation, Fields: fields,
		Detail: fmt.Sprintf("The %s does not pass the config checks:\n%s", kind, problems.Error()),
	}
}

// fieldOf turns the path of a config problem, such as
// routes.checkout.budgets.maxTokens, into the field of the request body,
// budgets.maxTokens. A problem with the whole rule has the empty field.
func fieldOf(path, root, name string) string {
	for _, prefix := range []string{root + "." + name, root + "[" + strconv.Quote(name) + "]"} {
		switch {
		case path == prefix:
			return ""
		case strings.HasPrefix(path, prefix+"."):
			return path[len(prefix)+1:]
		case strings.HasPrefix(path, prefix+"["):
			return path[len(prefix):]
		}
	}

	return path
}
