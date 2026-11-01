package slackcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/slack-go/slack"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/lessons"
	"github.com/nourtalaat/sidekick/internal/roles"
)

// The scope options of /sidekick lesson add.
const (
	optService  = "service"
	optAlert    = "alert"
	optRoute    = "route"
	optStrategy = "strategy"
)

// lessonUsage is the usage of /sidekick lesson add.
const lessonUsage = "Usage: `/sidekick lesson add \"<text>\" [service:<name>] [alert:<name>] [route:<name>] [strategy:<name>]`. " +
	"Put the text in double quotes. A lesson needs at least one scope option; to write a lesson for every investigation, use the web UI."

// lesson runs /sidekick lesson.
func (h *Handler) lesson(ctx context.Context, cmd slack.SlashCommand, args []string) (string, error) {
	if len(args) == 0 || !strings.EqualFold(args[0], "add") {
		return "", &userError{msg: lessonUsage}
	}

	return h.addLesson(ctx, cmd, args[1:])
}

// addLesson stores an active lesson that an admin wrote.
func (h *Handler) addLesson(ctx context.Context, cmd slack.SlashCommand, args []string) (string, error) {
	if err := h.require(ctx, cmd, roles.Admin); err != nil {
		return "", err
	}

	text, scope, err := parseLesson(args)
	if err != nil {
		return "", err
	}

	stored, err := h.lessons.Create(ctx, lessons.Actor{Kind: audit.ActorUser, ID: cmd.UserID}, lessons.New{
		Text: text, Scope: scope, Origin: lessons.OriginSlack, Status: lessons.StatusActive,
	})
	if invalid, ok := errors.AsType[*lessons.InvalidError](err); ok {
		return "", &userError{msg: invalid.Detail + ". " + lessonUsage}
	}

	if err != nil {
		return "", fmt.Errorf("add a lesson: %w", err)
	}

	h.log.InfoContext(ctx, "lesson added", "lesson_id", stored.ID.String(), "user", cmd.UserID)

	return fmt.Sprintf("Lesson added for %s: %s\nPrompts use it from now on. The web UI can edit, disable or delete it.",
		escape(stored.Scope.Describe()), escape(stored.Text)), nil
}

// parseLesson reads the arguments of /sidekick lesson add: the text and the
// scope options.
func parseLesson(args []string) (string, lessons.Scope, error) {
	if len(args) == 0 {
		return "", lessons.Scope{}, &userError{msg: lessonUsage}
	}

	var scope lessons.Scope

	for _, arg := range args[1:] {
		key, value, ok := option(arg)
		if !ok || value == "" {
			return "", lessons.Scope{}, &userError{msg: fmt.Sprintf("%s is not a scope option. %s", code(arg), lessonUsage)}
		}

		var field *string

		switch key {
		case optService:
			field = &scope.Service
		case optAlert:
			field = &scope.Alert
		case optRoute:
			field = &scope.Route
		case optStrategy:
			field = &scope.Strategy
		default:
			return "", lessons.Scope{}, &userError{msg: fmt.Sprintf("%s is not a scope option. %s", code(key), lessonUsage)}
		}

		if *field != "" {
			return "", lessons.Scope{}, &userError{msg: fmt.Sprintf("%s is given twice. %s", code(key), lessonUsage)}
		}

		*field = value
	}

	if scope.Global() {
		return "", lessons.Scope{}, &userError{msg: "A lesson from Slack needs at least one scope option. " + lessonUsage}
	}

	return args[0], scope, nil
}
