package model

import "context"

// KeyChecker is implemented by connectors that can check their credentials
// with a call that costs nothing, such as listing the provider's models.
// `sidekick doctor` calls it; nothing on the investigation path does.
type KeyChecker interface {
	// CheckKey calls the provider with the configured credentials. A
	// failure is a *Error, so its Kind tells a rejected key (ErrAuth) from
	// a provider that cannot be reached.
	CheckKey(ctx context.Context) error
}
