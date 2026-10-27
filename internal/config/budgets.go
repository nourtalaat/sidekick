package config

import (
	"fmt"
	"math"
	"time"

	// The time zone of the daily caps must load on images without a zone
	// database.
	_ "time/tzdata"
)

// Defaults of the budgets that are not set in the file.
const (
	DefaultMaxTokens  = 400_000
	DefaultMaxCostUSD = 1.00
	DefaultTimeZone   = "UTC"
	DefaultOnDailyCap = OnDailyCapWait
	DefaultOverLimit  = OverLimitDelay
)

// OnDailyCap says what happens to a new investigation once a daily
// spending cap is reached.
type OnDailyCap string

// What happens at a daily cap.
const (
	// OnDailyCapWait holds new investigations until the next day starts in
	// budgets.daily.timeZone.
	OnDailyCapWait OnDailyCap = "wait"
	// OnDailyCapSkip ends new investigations without running them.
	OnDailyCapSkip OnDailyCap = "skip"
)

// OverLimit says what happens to an event over a rate limit.
type OverLimit string

// What happens over a rate limit.
const (
	// OverLimitDelay routes the event later, once the window has room.
	OverLimitDelay OverLimit = "delay"
	// OverLimitSkip drops the event without investigating it.
	OverLimitSkip OverLimit = "skip"
)

var (
	knownOnDailyCap = []OnDailyCap{OnDailyCapWait, OnDailyCapSkip}
	knownOverLimit  = []OverLimit{OverLimitDelay, OverLimitSkip}
)

// RouteBudgets are a route's limits: those of one investigation and the
// route's daily spending cap.
type RouteBudgets struct {
	Limits `yaml:",inline"`
	// Daily is the route's daily spending cap.
	Daily RouteDaily `yaml:"daily"`
}

// RouteDaily is a route's daily spending cap. The day is the one of
// budgets.daily.timeZone.
type RouteDaily struct {
	// MaxCostUSD is the most the route's investigations may cost in one
	// day. Zero means only the global cap holds them.
	MaxCostUSD float64 `yaml:"maxCostUSD"`
	// OnDailyCap is what happens to the route's new investigations at its
	// cap. After Load, an unset value holds budgets.daily.onDailyCap.
	OnDailyCap OnDailyCap `yaml:"onDailyCap"`
}

// DailyBudget is the global daily spending cap.
type DailyBudget struct {
	// MaxCostUSD is the most every investigation together may cost in one
	// day. Zero means no global cap.
	MaxCostUSD float64 `yaml:"maxCostUSD"`
	// TimeZone is the IANA time zone whose midnight starts a new day, for
	// every cap.
	TimeZone string `yaml:"timeZone"`
	// OnDailyCap is what happens to new investigations at the global cap,
	// and at a route's cap when the route sets none.
	OnDailyCap OnDailyCap `yaml:"onDailyCap"`
}

// RateLimit limits how many events are routed in a sliding window.
type RateLimit struct {
	// Events is the most events routed within Per.
	Events int `yaml:"events"`
	// Per is the length of the window.
	Per time.Duration `yaml:"per"`
	// OverLimit is what happens to an event over the limit.
	OverLimit OverLimit `yaml:"overLimit"`
}

// MaxCostMicros returns the cost limit in micro-dollars.
func (l Limits) MaxCostMicros() int64 {
	return toMicroDollars(l.MaxCostUSD)
}

// MaxCostMicros returns the route's daily cap in micro-dollars, zero when
// it has none.
func (d RouteDaily) MaxCostMicros() int64 {
	return toMicroDollars(d.MaxCostUSD)
}

// MaxCostMicros returns the global daily cap in micro-dollars, zero when
// there is none.
func (d DailyBudget) MaxCostMicros() int64 {
	return toMicroDollars(d.MaxCostUSD)
}

// Location returns the time zone of the daily caps. Load checks that it
// loads, so after Load it fails only for a config built by hand.
func (d DailyBudget) Location() (*time.Location, error) {
	loc, err := time.LoadLocation(d.TimeZone)
	if err != nil {
		return nil, fmt.Errorf("load time zone %q: %w", d.TimeZone, err)
	}

	return loc, nil
}

// perSecond returns the rate the limit allows, in events per second.
func (r RateLimit) perSecond() float64 {
	return float64(r.Events) / r.Per.Seconds()
}

func (d *DailyBudget) setDefaults() {
	d.TimeZone = DefaultTimeZone
	d.OnDailyCap = DefaultOnDailyCap
}

func (r *RateLimit) setDefaults() {
	r.OverLimit = DefaultOverLimit
}

// resolveRouteBudgets fills a route's unset limits with the global ones.
func resolveRouteBudgets(b *RouteBudgets, global Budgets) {
	limits := global.PerInvestigation

	if b.MaxTokens == 0 {
		b.MaxTokens = limits.MaxTokens
	}

	if b.MaxToolCalls == 0 {
		b.MaxToolCalls = limits.MaxToolCalls
	}

	if b.MaxWallTime == 0 {
		b.MaxWallTime = limits.MaxWallTime
	}

	if b.MaxCostUSD == 0 {
		b.MaxCostUSD = limits.MaxCostUSD
	}

	if b.Daily.OnDailyCap == "" {
		b.Daily.OnDailyCap = global.Daily.OnDailyCap
	}
}

// validUSD reports whether usd is an amount a budget can hold: finite and
// not negative.
func validUSD(usd float64) bool {
	return usd >= 0 && !math.IsInf(usd, 1)
}
