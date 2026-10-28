// Package flags holds what the feature flag sources share: the two tools
// they offer, list_flag_changes and get_flag, with their input; the
// allowlist of projects and environments a source may read; the time
// range of a call; and the shape of their results, cut to a bounded size.
//
// A flag source, such as the LaunchDarkly source, reads its service and
// fills the results; this package keeps the tools and results the same
// for every flag service, so a strategy written for one works with the
// others.
package flags
