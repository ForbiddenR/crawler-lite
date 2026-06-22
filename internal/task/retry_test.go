package task

import (
	"testing"
	"time"
)

func TestPolicyFromSpiderConfig_NoRetryBlock_ReturnsDefault(t *testing.T) {
	got := PolicyFromSpiderConfig(map[string]any{})
	if got.MaxAttempts != 1 {
		t.Errorf("default MaxAttempts should be 1, got %d", got.MaxAttempts)
	}
	if got.BackoffKind != "exp" {
		t.Errorf("default backoff should be exp, got %q", got.BackoffKind)
	}
	if got.BaseDelay != 30*time.Second {
		t.Errorf("default base delay should be 30s, got %v", got.BaseDelay)
	}
}

func TestPolicyFromSpiderConfig_FullyPopulated(t *testing.T) {
	// JSON-derived numbers come through as float64; keep that fidelity here.
	cfg := map[string]any{
		"retry": map[string]any{
			"max_attempts": float64(5),
			"backoff":      "linear",
			"base_delay_s": float64(10),
			"max_delay_s":  float64(120),
			"retry_on":     []any{"timeout", "exit"},
		},
	}
	p := PolicyFromSpiderConfig(cfg)
	if p.MaxAttempts != 5 {
		t.Errorf("max_attempts: %d", p.MaxAttempts)
	}
	if p.BackoffKind != "linear" {
		t.Errorf("backoff: %q", p.BackoffKind)
	}
	if p.BaseDelay != 10*time.Second {
		t.Errorf("base_delay: %v", p.BaseDelay)
	}
	if p.MaxDelay != 120*time.Second {
		t.Errorf("max_delay: %v", p.MaxDelay)
	}
	if len(p.RetryOn) != 2 || p.RetryOn[0] != "timeout" || p.RetryOn[1] != "exit" {
		t.Errorf("retry_on: %v", p.RetryOn)
	}
}

func TestPolicyFromSpiderConfig_GarbageIsIgnored(t *testing.T) {
	cfg := map[string]any{
		"retry": map[string]any{
			"max_attempts": "not a number",
			"backoff":      42, // wrong type
			"retry_on":     []any{"timeout", "captcha", 99, "deps"},
		},
	}
	p := PolicyFromSpiderConfig(cfg)
	// Bad max_attempts falls back to default.
	if p.MaxAttempts != 1 {
		t.Errorf("expected default MaxAttempts=1 when garbage, got %d", p.MaxAttempts)
	}
	if p.BackoffKind != "exp" {
		t.Errorf("expected default backoff=exp when garbage, got %q", p.BackoffKind)
	}
	// "captcha" and 99 should have been filtered out; "timeout" and "deps" kept.
	if len(p.RetryOn) != 2 || p.RetryOn[0] != "timeout" || p.RetryOn[1] != "deps" {
		t.Errorf("retry_on filtering wrong: %v", p.RetryOn)
	}
}

func TestPolicyFromSpiderConfig_ExplicitEmptyRetryOn_DisablesEverything(t *testing.T) {
	cfg := map[string]any{
		"retry": map[string]any{
			"max_attempts": float64(5),
			"retry_on":     []any{},
		},
	}
	p := PolicyFromSpiderConfig(cfg)
	if len(p.RetryOn) != 0 {
		t.Errorf("empty retry_on should yield empty slice, got %v", p.RetryOn)
	}
	// And Decide must respect the empty list — no retries even though
	// max_attempts > 1.
	ok, _ := p.Decide(1, "exit")
	if ok {
		t.Errorf("empty retry_on must block all retries")
	}
}

func TestPolicyFromSpiderConfig_ClampsAbsurdMaxAttempts(t *testing.T) {
	cfg := map[string]any{
		"retry": map[string]any{"max_attempts": float64(99999)},
	}
	p := PolicyFromSpiderConfig(cfg)
	if p.MaxAttempts != 1 {
		t.Errorf("oversize max_attempts should fall back to default 1, got %d", p.MaxAttempts)
	}
}

func TestDecide_NoRetryWhenMaxAttemptsIsOne(t *testing.T) {
	p := retryDefault // MaxAttempts: 1
	ok, _ := p.Decide(1, "exit")
	if ok {
		t.Errorf("default policy must not retry")
	}
}

func TestDecide_StopsAtMaxAttempts(t *testing.T) {
	p := RetryPolicy{
		MaxAttempts: 3, BackoffKind: "linear",
		BaseDelay: time.Second, RetryOn: []string{"exit"},
	}
	if ok, _ := p.Decide(1, "exit"); !ok {
		t.Error("attempt 1 should retry")
	}
	if ok, _ := p.Decide(2, "exit"); !ok {
		t.Error("attempt 2 should retry")
	}
	if ok, _ := p.Decide(3, "exit"); ok {
		t.Error("attempt 3 must NOT spawn attempt 4")
	}
}

func TestDecide_FiltersByErrorClass(t *testing.T) {
	p := RetryPolicy{
		MaxAttempts: 5, BackoffKind: "linear",
		BaseDelay: time.Second, RetryOn: []string{"timeout"},
	}
	if ok, _ := p.Decide(1, "timeout"); !ok {
		t.Error("timeout should match retry_on")
	}
	if ok, _ := p.Decide(1, "exit"); ok {
		t.Error("exit must not match a retry_on=[timeout] policy")
	}
	if ok, _ := p.Decide(1, ""); ok {
		t.Error("empty error class must never retry")
	}
}

func TestDecide_CaptchaNeverRetries(t *testing.T) {
	// Force captcha into retry_on; Decide should still reject it.
	p := RetryPolicy{
		MaxAttempts: 5, BackoffKind: "linear", BaseDelay: time.Second,
		RetryOn: []string{"captcha", "timeout", "exit"},
	}
	if ok, _ := p.Decide(1, "captcha"); ok {
		t.Error("captcha must never be retried by policy")
	}
}

func TestDecide_LinearBackoff(t *testing.T) {
	p := RetryPolicy{
		MaxAttempts: 5, BackoffKind: "linear",
		BaseDelay: 5 * time.Second, RetryOn: []string{"exit"},
	}
	if _, d := p.Decide(1, "exit"); d != 5*time.Second {
		t.Errorf("attempt 1 delay: %v", d)
	}
	if _, d := p.Decide(2, "exit"); d != 10*time.Second {
		t.Errorf("attempt 2 delay: %v", d)
	}
	if _, d := p.Decide(3, "exit"); d != 15*time.Second {
		t.Errorf("attempt 3 delay: %v", d)
	}
}

func TestDecide_ExponentialBackoff(t *testing.T) {
	p := RetryPolicy{
		MaxAttempts: 5, BackoffKind: "exp",
		BaseDelay: 5 * time.Second, RetryOn: []string{"exit"},
	}
	// 5 * 2^(N-1): 5, 10, 20, 40
	if _, d := p.Decide(1, "exit"); d != 5*time.Second {
		t.Errorf("attempt 1 delay: %v", d)
	}
	if _, d := p.Decide(2, "exit"); d != 10*time.Second {
		t.Errorf("attempt 2 delay: %v", d)
	}
	if _, d := p.Decide(3, "exit"); d != 20*time.Second {
		t.Errorf("attempt 3 delay: %v", d)
	}
	if _, d := p.Decide(4, "exit"); d != 40*time.Second {
		t.Errorf("attempt 4 delay: %v", d)
	}
}

func TestDecide_RespectsMaxDelayCap(t *testing.T) {
	p := RetryPolicy{
		MaxAttempts: 10, BackoffKind: "exp",
		BaseDelay: 30 * time.Second, MaxDelay: 60 * time.Second,
		RetryOn: []string{"exit"},
	}
	// Without cap: 30, 60, 120, 240, ...
	// With cap at 60s: 30, 60, 60, 60, ...
	if _, d := p.Decide(1, "exit"); d != 30*time.Second {
		t.Errorf("attempt 1: %v", d)
	}
	if _, d := p.Decide(2, "exit"); d != 60*time.Second {
		t.Errorf("attempt 2 (cap edge): %v", d)
	}
	if _, d := p.Decide(3, "exit"); d != 60*time.Second {
		t.Errorf("attempt 3 (capped): %v", d)
	}
	if _, d := p.Decide(8, "exit"); d != 60*time.Second {
		t.Errorf("attempt 8 (capped): %v", d)
	}
}
