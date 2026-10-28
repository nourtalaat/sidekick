package slackapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/slack-go/slack"

	"github.com/nourtalaat/sidekick/internal/feedback"
	"github.com/nourtalaat/sidekick/internal/memory"
	"github.com/nourtalaat/sidekick/internal/output"
	slackout "github.com/nourtalaat/sidekick/internal/output/slack"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// callbackFeedbackDetails is the callback ID of the details form.
const callbackFeedbackDetails = "feedback_details"

// The block and action IDs of the details form's inputs.
const (
	blockPart      = "fb_part"
	actionPart     = "part"
	blockToolCall  = "fb_tool_call"
	actionToolCall = "tool_call_id"
	blockRating    = "fb_rating"
	actionRating   = "rating"
	blockReason    = "fb_reason"
	actionReason   = "reason"
	blockVerdict   = "fb_verdict"
	actionVerdict  = "correct_verdict"
	blockCause     = "fb_cause"
	actionCause    = "correct_cause"
)

// partToolCall is the value of the part option for a tool call, which has
// no ref until the person enters a tool call ID.
const partToolCall = "tool_call"

// The words on the form's frame, and the path of the investigation page
// under server.publicURL.
const (
	formTitle         = "Feedback"
	formSubmit        = "Send"
	formClose         = "Cancel"
	investigationPath = "/investigations/"
)

// The most options of the part list, the most findings and fixes it
// offers, and the most characters of an option's text, which Slack limits
// to 100 options, 75 characters and 150 characters of value.
const (
	maxPartOptions    = 90
	maxFindingsInForm = 40
	maxFixesInForm    = 40
	maxOptionChars    = 75
	maxOptionValue    = 150
	optionCutSuffix   = "..."
)

// The messages shown in the form when it cannot be sent.
const (
	errorDenied      = "Only responders can send feedback."
	errorOutOfDate   = "This form is out of date. Close it and press Details again."
	errorNoVerdict   = "This investigation has no verdict yet."
	errorGone        = "This investigation does not exist any more."
	errorNoRating    = "Choose correct or wrong."
	errorNoToolCall  = "Enter the ID of the tool call."
	errorUnknownCall = "No step of this investigation has this tool call ID."
	errorBadVerdict  = "The real verdict does not fit the rating of the verdict."
	errorFailed      = "Sidekick could not save this. Try again in a moment."
)

// detailsOptions say how detailsView looks.
type detailsOptions struct {
	// Rating is chosen in the form when set, as after a click on Wrong.
	Rating output.Rating
	// PublicURL is server.publicURL. With it, the form offers rating a
	// tool call and links to the investigation page where its ID is.
	PublicURL string
}

// detailsValues are what the person filled in.
type detailsValues struct {
	Part, ToolCall, Rating, Reason, Verdict, Cause string
}

// detailsView returns the form for feedback on a part of inv: the verdict,
// each finding and fix of its report, and a tool call, whether it is
// correct or wrong, why, and the real verdict and cause.
func detailsView(inv db.Investigation, opts detailsOptions) slack.ModalViewRequest {
	parts := partOptions(inv, reportOf(inv), opts.PublicURL != "")

	blocks := []slack.Block{
		input(blockPart, "Which part is this about?", "", false,
			slack.NewOptionsSelectBlockElement(slack.OptTypeStatic, plainText("Choose a part"), actionPart, parts...).WithInitialOption(parts[0])),
	}

	if opts.PublicURL != "" {
		blocks = append(blocks,
			slack.NewSectionBlock(markdown("To rate a tool call, copy its ID from the steps on <"+investigationLink(opts.PublicURL, inv)+"|the investigation page>."), nil, nil),
			input(blockToolCall, "Tool call ID (only for a tool call)", "", true,
				slack.NewPlainTextInputBlockElement(plainText("For example toolu_01A..."), actionToolCall)),
		)
	}

	rating := slack.NewRadioButtonsBlockElement(actionRating, ratingOption(output.RatingCorrect), ratingOption(output.RatingWrong))
	if opts.Rating != "" {
		rating.InitialOption = ratingOption(opts.Rating)
	}

	reason := slack.NewPlainTextInputBlockElement(plainText("What was right or wrong about it?"), actionReason)
	reason.Multiline, reason.MaxLength = true, feedback.MaxReasonChars

	verdicts := slack.NewOptionsSelectBlockElement(slack.OptTypeStatic, plainText("Choose a verdict"), actionVerdict, verdictOptions()...)

	cause := slack.NewPlainTextInputBlockElement(plainText("What caused it?"), actionCause)
	cause.Multiline, cause.MaxLength = true, feedback.MaxCauseChars

	blocks = append(blocks,
		input(blockRating, "Is it correct or wrong?", "", false, rating),
		input(blockReason, "Why?", "", true, reason),
		input(blockVerdict, "The real verdict, if you know it", "Giving it confirms the investigation with this verdict.", true, verdicts),
		input(blockCause, "The real cause, if you know it", "", true, cause),
	)

	return slack.ModalViewRequest{
		Type:            slack.VTModal,
		Title:           plainText(formTitle),
		Submit:          plainText(formSubmit),
		Close:           plainText(formClose),
		CallbackID:      callbackFeedbackDetails,
		PrivateMetadata: inv.ID.String(),
		Blocks:          slack.Blocks{BlockSet: blocks},
	}
}

// submission returns the feedback the form asks for from the Slack user
// slackUser on investigation id, or the errors to show in the form, by
// block ID.
func (d detailsValues) submission(id uuid.UUID, slackUser string, mem memory.SyncOptions) (feedback.Submission, map[string]string) {
	target, ok := targetOf(d.Part, d.ToolCall)

	switch {
	case !ok:
		return feedback.Submission{}, map[string]string{blockPart: errorOutOfDate}
	case target.Kind == feedback.TargetToolCall && target.Ref == "":
		return feedback.Submission{}, map[string]string{blockToolCall: errorNoToolCall}
	case d.Rating != string(output.RatingCorrect) && d.Rating != string(output.RatingWrong):
		return feedback.Submission{}, map[string]string{blockRating: errorNoRating}
	}

	return feedback.Submission{
		InvestigationID: id,
		Target:          target,
		Rating:          output.Rating(d.Rating),
		Reason:          d.Reason,
		CorrectVerdict:  report.Verdict(d.Verdict),
		CorrectCause:    d.Cause,
		Author:          feedback.Author{SlackUserID: slackUser},
		Memory:          mem,
	}, nil
}

// targetOf returns the target a part option stands for. The option of the
// verdict, a finding or a fix holds the target ref; a tool call is
// partToolCall, and its ref is toolCall.
func targetOf(part, toolCall string) (feedback.Target, bool) {
	if part == partToolCall {
		return feedback.Target{Kind: feedback.TargetToolCall, Ref: strings.TrimSpace(toolCall)}, true
	}

	_, rest, ok := strings.Cut(part, "/")

	switch {
	case !ok:
		return feedback.Target{}, false
	case rest == feedback.TargetVerdict:
		return feedback.Target{Kind: feedback.TargetVerdict, Ref: part}, true
	case strings.HasPrefix(rest, feedback.TargetFinding+":"):
		return feedback.Target{Kind: feedback.TargetFinding, Ref: part}, true
	case strings.HasPrefix(rest, feedback.TargetFix+":"):
		return feedback.Target{Kind: feedback.TargetFix, Ref: part}, true
	}

	return feedback.Target{}, false
}

// readDetails reads the values of a submitted form.
func readDetails(state *slack.ViewState) detailsValues {
	if state == nil {
		return detailsValues{}
	}

	text := func(block, action string) string { return state.Values[block][action].Value }
	chosen := func(block, action string) string { return state.Values[block][action].SelectedOption.Value }

	return detailsValues{
		Part:     chosen(blockPart, actionPart),
		ToolCall: text(blockToolCall, actionToolCall),
		Rating:   chosen(blockRating, actionRating),
		Reason:   text(blockReason, actionReason),
		Verdict:  chosen(blockVerdict, actionVerdict),
		Cause:    text(blockCause, actionCause),
	}
}

// submissionErrors returns the errors to show in the form for an error of
// feedback.Submit, by block ID, or nil for an error the form cannot help
// with.
func submissionErrors(target feedback.Target, err error) map[string]string {
	if invalid, ok := errors.AsType[*feedback.InvalidError](err); ok {
		return map[string]string{blockPart: invalid.Detail + "."}
	}

	switch {
	case errors.Is(err, feedback.ErrNotFound):
		return map[string]string{blockPart: errorGone}
	case errors.Is(err, feedback.ErrNotRatable):
		return map[string]string{blockPart: errorNoVerdict}
	case errors.Is(err, feedback.ErrBadVerdict):
		return map[string]string{blockVerdict: errorBadVerdict}
	case errors.Is(err, feedback.ErrBadTarget) && target.Kind == feedback.TargetToolCall:
		return map[string]string{blockToolCall: errorUnknownCall}
	case errors.Is(err, feedback.ErrBadTarget):
		return map[string]string{blockPart: errorOutOfDate}
	}

	return nil
}

// errorsResponse is the answer that keeps the form open and shows errors,
// by block ID.
func errorsResponse(errs map[string]string) ([]byte, error) {
	body, err := json.Marshal(slack.NewErrorsViewSubmissionResponse(errs))
	if err != nil {
		return nil, fmt.Errorf("encode the form errors: %w", err)
	}

	return body, nil
}

// partOptions are the parts of inv that the form can be about, the verdict
// first.
func partOptions(inv db.Investigation, rep report.Report, toolCalls bool) []*slack.OptionBlockObject {
	verdict := "The verdict"
	if inv.Verdict != nil {
		verdict += ": " + slackout.VerdictLabel(report.Verdict(*inv.Verdict))
	}

	options := []*slack.OptionBlockObject{option(feedback.VerdictRef(inv.Run), verdict)}

	for _, f := range rep.Findings[:min(len(rep.Findings), maxFindingsInForm)] {
		options = appendOption(options, feedback.FindingRef(inv.Run, f.ID), "Finding "+f.ID+": "+f.Claim)
	}

	for _, f := range rep.Fixes[:min(len(rep.Fixes), maxFixesInForm)] {
		options = appendOption(options, feedback.FixRef(inv.Run, f.ID), "Fix "+f.ID+": "+f.Title)
	}

	if toolCalls {
		options = append(options, option(partToolCall, "A tool call"))
	}

	return options[:min(len(options), maxPartOptions)]
}

// reportOf returns the report of inv, or an empty one when it has none that
// can be read.
func reportOf(inv db.Investigation) report.Report {
	var rep report.Report
	if err := json.Unmarshal(inv.Report, &rep); err != nil {
		return report.Report{}
	}

	return rep
}

// verdictOptions are the verdicts a report can have.
func verdictOptions() []*slack.OptionBlockObject {
	options := make([]*slack.OptionBlockObject, 0, len(report.Verdicts))

	for _, v := range report.Verdicts {
		options = append(options, option(string(v), slackout.VerdictLabel(v)))
	}

	return options
}

// ratingOption is the radio button of rating.
func ratingOption(rating output.Rating) *slack.OptionBlockObject {
	label := "Correct"
	if rating == output.RatingWrong {
		label = "Wrong"
	}

	return option(string(rating), label)
}

// appendOption adds an option to options, unless its value is longer than
// Slack takes.
func appendOption(options []*slack.OptionBlockObject, value, text string) []*slack.OptionBlockObject {
	if len(value) > maxOptionValue {
		return options
	}

	return append(options, option(value, text))
}

// option returns an option with value, whose text is cut to what Slack
// shows.
func option(value, text string) *slack.OptionBlockObject {
	return slack.NewOptionBlockObject(value, plainText(cutChars(oneLine(text), maxOptionChars)), nil)
}

// input returns an input block with a label, an optional hint, and the
// element.
func input(blockID, label, hint string, optional bool, element slack.BlockElement) *slack.InputBlock {
	var hintText *slack.TextBlockObject
	if hint != "" {
		hintText = plainText(hint)
	}

	block := slack.NewInputBlock(blockID, plainText(label), hintText, element)
	block.Optional = optional

	return block
}

// investigationLink is the address of inv's page in the web UI.
func investigationLink(publicURL string, inv db.Investigation) string {
	return strings.TrimRight(publicURL, "/") + investigationPath + url.PathEscape(inv.ID.String())
}

// plainText is a plain text object that shows text as it is.
func plainText(text string) *slack.TextBlockObject {
	return slack.NewTextBlockObject(slack.PlainTextType, text, false, false)
}

// markdown is a text object in Slack's markup. text must be escaped.
func markdown(text string) *slack.TextBlockObject {
	return slack.NewTextBlockObject(slack.MarkdownType, text, false, true)
}

// oneLine replaces each run of white space in text with one space.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// cutChars cuts text to at most n characters, ending in "..." when it cut.
func cutChars(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}

	runes := []rune(text)

	return string(runes[:n-len(optionCutSuffix)]) + optionCutSuffix
}
