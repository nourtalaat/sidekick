package aws

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf16"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits on the text a result keeps, in bytes.
const (
	// maxResultSize bounds the JSON of one result. Lists stop adding items
	// once they reach it and say they were cut.
	maxResultSize = 256 << 10
	// maxMessageLen bounds one error message.
	maxMessageLen = 2 << 10
	// maxNameLen bounds a name, ARN or other short text from AWS.
	maxNameLen = 512
	// maxCellLen bounds one field of a Logs Insights row, and the request
	// parameters of one CloudTrail event.
	maxCellLen = 1 << 10
	// maxMessages bounds the messages AWS adds to one result.
	maxMessages = 20
)

// notFoundText is the text of a result for something that does not exist.
const notFoundText = "Not found in AWS: the log group or resource does not exist in that account and Region, or Sidekick's role cannot see it."

// Error codes AWS answers with, by what they mean for a call.
var (
	// notFoundCodes say that what the call names does not exist.
	notFoundCodes = map[string]bool{
		"ResourceNotFoundException": true,
		"ResourceNotFound":          true,
		"NotFoundException":         true,
	}
	// sourceFailureCodes are rate limits and refused credentials, which no
	// change to the call fixes.
	sourceFailureCodes = map[string]bool{
		"ThrottlingException":         true,
		"Throttling":                  true,
		"ThrottledException":          true,
		"RequestThrottledException":   true,
		"TooManyRequestsException":    true,
		"RequestLimitExceeded":        true,
		"LimitExceededException":      true,
		"UnrecognizedClientException": true,
		"InvalidClientTokenId":        true,
		"ExpiredToken":                true,
		"ExpiredTokenException":       true,
		"InvalidSignatureException":   true,
		"SignatureDoesNotMatch":       true,
		"IncompleteSignature":         true,
		"MissingAuthenticationToken":  true,
		"RequestExpired":              true,
	}
)

// budget counts the bytes a list result holds so far, so a list stops
// before it grows past maxResultSize.
type budget struct {
	used int
	cut  bool
}

// fits adds the size of item to the budget and reports whether it still
// fits. Once one item does not fit, the budget is spent.
func (b *budget) fits(item any) bool {
	if b.cut {
		return false
	}

	data, err := json.Marshal(item)
	if err != nil || b.used+len(data) > maxResultSize {
		b.cut = true

		return false
	}

	b.used += len(data)

	return true
}

// structured returns a result that holds v as JSON, with its evidence.
// When empty says the call found nothing, it returns a result with NoData
// set, the text and the evidence instead, so the link still reaches the
// report.
func structured(v any, evidence []source.Evidence, truncated, empty bool, emptyText string) (source.Result, error) {
	if len(evidence) == 0 {
		evidence = nil
	}

	if empty {
		return source.Result{NoData: true, Text: emptyText, Evidence: evidence}, nil
	}

	data, err := json.Marshal(v)
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	return source.Result{Structured: data, Evidence: evidence, Truncated: truncated}, nil
}

// failure turns the error of a call into what Call returns. Something that
// does not exist is a result with NoData set. Other refusals of the call
// itself, such as a query AWS cannot parse or a permission the role lacks,
// are tool errors with AWS's message. Rate limits, refused credentials,
// server errors and credentials that cannot be had are failures of the
// source.
func failure(tool string, err error) (source.Result, error) {
	if _, ok := errors.AsType[*credentialError](err); ok {
		return source.Result{}, fmt.Errorf("call %s: %w", tool, err)
	}

	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		code := apiErr.ErrorCode()
		status := 0

		if resp, ok := errors.AsType[*awshttp.ResponseError](err); ok {
			status = resp.HTTPStatusCode()
		}

		ofSource := sourceFailureCodes[code] || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
		refused := status >= http.StatusBadRequest || apiErr.ErrorFault() == smithy.FaultClient

		switch {
		case notFoundCodes[code]:
			return source.Result{NoData: true, Text: notFoundText}, nil
		case refused && !ofSource:
			return source.Result{}, fmt.Errorf("call %s: %w", tool, &source.ToolError{Message: apiMessage(status, apiErr)})
		}
	}

	return source.Result{}, fmt.Errorf("call %s: %w", tool, err)
}

// apiMessage returns the text of an error AWS answered: the status, the
// error code and AWS's own message, cut.
func apiMessage(status int, apiErr smithy.APIError) string {
	text := "AWS answered"
	if status != 0 {
		text += fmt.Sprintf(" %d", status)
	}

	text += " " + source.CutText(apiErr.ErrorCode(), maxNameLen)

	if msg := apiErr.ErrorMessage(); msg != "" {
		text += ": " + source.CutText(msg, maxMessageLen)
	}

	return text
}

// consoleURL returns the link to a page of the AWS console of service in
// region, with fragment after the #.
func consoleURL(region, service, fragment string) string {
	return consoleRoot(region) + "/" + service + "/home?region=" + region + "#" + fragment
}

// consoleRoot returns the address of the AWS console of region, in its
// partition.
func consoleRoot(region string) string {
	switch {
	case strings.HasPrefix(region, "cn-"):
		return "https://" + region + ".console.amazonaws.cn"
	case strings.HasPrefix(region, "us-gov-"):
		return "https://" + region + ".console.amazonaws-us-gov.com"
	default:
		return "https://" + region + ".console.aws.amazon.com"
	}
}

// jsurl encodes s as a string in the JSURL form the CloudWatch console
// reads its query details in: letters, digits, _, - and . stay, $ becomes
// !, and any other UTF-16 unit becomes * and two hex digits, or ** and
// four.
func jsurl(s string) string {
	var b strings.Builder

	for _, u := range utf16.Encode([]rune(s)) {
		switch {
		case u < 0x80 && isJSURLPlain(byte(u)):
			b.WriteByte(byte(u))
		case u == '$':
			b.WriteByte('!')
		case u < 0x100:
			fmt.Fprintf(&b, "*%02x", u)
		default:
			fmt.Fprintf(&b, "**%04x", u)
		}
	}

	return b.String()
}

// isJSURLPlain reports whether c stays as it is in a JSURL string.
func isJSURLPlain(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
}

// cut returns s cut to n bytes, and whether it was cut.
func cut(s string, n int) (string, bool) {
	c := source.CutText(s, n)

	return c, len(c) < len(s)
}
