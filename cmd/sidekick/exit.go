package main

import "errors"

// Exit codes besides 0 for success and 1 for any other failure.
const (
	// exitThresholdFailed is a benchmark score below one of its
	// thresholds. It is the code of any other failure too.
	exitThresholdFailed = 1
	// exitBadInput is a bad flag, config file or scenario file.
	exitBadInput = 2
	// exitInfrastructure is a failure of what a command runs on rather
	// than of its input: a model provider that is unreachable after its
	// retries, Docker or the database.
	exitInfrastructure = 3
)

// exitFailure is the exit code of an error without an exitError.
const exitFailure = 1

// exitError is an error that ends the process with code.
type exitError struct {
	code int
	err  error
}

// Error returns the message of the error.
func (e *exitError) Error() string {
	return e.err.Error()
}

// Unwrap returns the error.
func (e *exitError) Unwrap() error {
	return e.err
}

// exitCode returns the exit code of err: the code of the exitError it
// wraps, or exitFailure.
func exitCode(err error) int {
	if e, ok := errors.AsType[*exitError](err); ok {
		return e.code
	}

	return exitFailure
}
