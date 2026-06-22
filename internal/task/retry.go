// Retry policy. Pure functions only — no clock, no I/O, no goroutines. The
// caller decides when to attach `time.Now()` to the returned delay.
//
// The policy lives in the spider's free-form `config` JSONB blob under the
// `retry` key:
//
//   retry:
//     max_attempts: 3          # 1 = no retries (default)
//     backoff: exp             # "exp" | "linear"
//     base_delay_s: 30
//     max_delay_s: 1800        # 0 = no cap
//     retry_on: [timeout, exit]
//
// Missing or malformed fields silently fall back to defaults — a typo in a
// spider config should never crash the master.
package task

import (
	"math"
	"strings"
	"time"
)

// RetryPolicy is the evaluator. Fields are exported so tests can build one
// without round-tripping through PolicyFromSpiderConfig.
type RetryPolicy struct {
	MaxAttempts int
	BackoffKind string // "linear" or "exp"
	BaseDelay   time.Duration
	MaxDelay    time.Duration // 0 = no cap
	RetryOn     []string      // subset of {"timeout", "exit", "deps", "build_assign"}
}

// retryDefault is the policy returned when a spider config has no retry
// block. max_attempts=1 means "no retries" — a single attempt total.
var retryDefault = RetryPolicy{
	MaxAttempts: 1,
	BackoffKind: "exp",
	BaseDelay:   30 * time.Second,
	MaxDelay:    30 * time.Minute,
	RetryOn:     []string{"timeout", "exit"},
}

// retryableClasses caps what callers can put in retry_on. Anything outside
// this set is dropped during parsing. captcha_blocked is intentionally
// absent — a captcha is operator-blocked, not transient.
var retryableClasses = map[string]struct{}{
	"timeout":      {},
	"exit":         {},
	"deps":         {},
	"build_assign": {},
}

// PolicyFromSpiderConfig reads spider.Config["retry"] and returns a policy.
// Always returns a valid policy — never errors. Invalid fields are skipped.
func PolicyFromSpiderConfig(cfg map[string]any) RetryPolicy {
	p := retryDefault
	raw, ok := cfg["retry"].(map[string]any)
	if !ok {
		return p
	}

	if v, ok := numberAsInt(raw["max_attempts"]); ok && v >= 1 && v <= 100 {
		p.MaxAttempts = v
	}
	if v, ok := raw["backoff"].(string); ok {
		switch strings.ToLower(v) {
		case "exp", "exponential":
			p.BackoffKind = "exp"
		case "linear":
			p.BackoffKind = "linear"
		}
	}
	if v, ok := numberAsInt(raw["base_delay_s"]); ok && v >= 0 {
		p.BaseDelay = time.Duration(v) * time.Second
	}
	if v, ok := numberAsInt(raw["max_delay_s"]); ok && v >= 0 {
		p.MaxDelay = time.Duration(v) * time.Second
	}
	if list, ok := raw["retry_on"].([]any); ok {
		out := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				continue
			}
			s = strings.ToLower(strings.TrimSpace(s))
			if _, allowed := retryableClasses[s]; allowed {
				out = append(out, s)
			}
		}
		p.RetryOn = out // explicit empty list disables retry, matches author intent
	}
	return p
}

// Decide answers "should this just-failed attempt be retried, and after how
// long?". `attempt` is 1-indexed: the parent task that just failed is on
// attempt N, so the (potential) child is attempt N+1.
//
// Returns (false, 0) when:
//   - we've hit MaxAttempts (no further child)
//   - errClass is not in RetryOn
//   - errClass is "captcha" (operator state, never retried by policy)
//   - errClass is "" (we don't know what failed; play it safe)
func (p RetryPolicy) Decide(attempt int, errClass string) (bool, time.Duration) {
	if p.MaxAttempts <= 1 {
		return false, 0
	}
	if attempt >= p.MaxAttempts {
		return false, 0
	}
	errClass = strings.ToLower(strings.TrimSpace(errClass))
	if errClass == "" || errClass == "captcha" {
		return false, 0
	}
	if !contains(p.RetryOn, errClass) {
		return false, 0
	}
	return true, p.backoffFor(attempt)
}

// backoffFor returns the delay before attempt N+1 (where N == attempt arg).
// linear: base*N. exp: base * 2^(N-1). Capped at MaxDelay if set.
func (p RetryPolicy) backoffFor(attempt int) time.Duration {
	if p.BaseDelay <= 0 {
		return 0
	}
	var d time.Duration
	switch p.BackoffKind {
	case "linear":
		d = time.Duration(attempt) * p.BaseDelay
	default: // "exp" (default)
		// math.Pow on small ints; bounded by the MaxAttempts<=100 check on
		// parse so we won't overflow. Clamp the exponent defensively anyway.
		exp := attempt - 1
		if exp < 0 {
			exp = 0
		}
		if exp > 30 {
			exp = 30
		}
		mult := math.Pow(2, float64(exp))
		d = time.Duration(float64(p.BaseDelay) * mult)
	}
	if p.MaxDelay > 0 && d > p.MaxDelay {
		d = p.MaxDelay
	}
	return d
}

// numberAsInt is a small helper for JSON-derived configs where numbers are
// likely float64 (encoding/json default) but might be int when authored in
// Go tests.
func numberAsInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return int(n), true
	case float32:
		return int(n), true
	default:
		return 0, false
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
