package scenario

import "errors"

var (
	// ErrNotConfirmed means the investigation has no confirmed verdict, so
	// it cannot be exported.
	ErrNotConfirmed = errors.New("the investigation has no confirmed verdict")
	// ErrNoSourceCalls means the investigation called no source tool, so
	// there is nothing to replay.
	ErrNoSourceCalls = errors.New("the investigation called no source tool")
	// ErrNoEvent means the event that opened the investigation has no
	// original payload to replay, for example because it came from Slack.
	ErrNoEvent = errors.New("the investigation's event has no payload to replay")
	// ErrUnknownAdapter means the endpoint the event came in on is no longer
	// in the config, so its adapter is not known.
	ErrUnknownAdapter = errors.New("the event's webhook endpoint is not in the config")
	// ErrNotReviewed means the scenario is a draft, so it cannot be run or
	// downloaded.
	ErrNotReviewed = errors.New("the scenario is not reviewed")
	// ErrNotFound means there is no such scenario.
	ErrNotFound = errors.New("scenario not found")
	// ErrStale means the scenario was changed after the revision the caller
	// read.
	ErrStale = errors.New("the scenario changed since it was read")
	// ErrNotChecked means the reviewer did not tick the sensitive data box.
	ErrNotChecked = errors.New("the reviewer did not confirm the check for sensitive data")
)
