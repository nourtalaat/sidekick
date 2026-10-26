// Package adapterstest holds the integration tests of the sender adapters
// (signoz, datadog, sentry and grafana): each sends the adapter's recorded
// payloads to POST /hooks/{name} on a real webhook handler and database,
// and checks what is stored. The package has no code of its own.
package adapterstest
