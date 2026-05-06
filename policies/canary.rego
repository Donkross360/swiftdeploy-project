package policy.canary

import rego.v1

default allow := false

# Canary domain answers one question:
# "Is it safe to proceed with pre-promote based on canary health?"

violations contains v if {
  # Policy questions are context-specific; reject mismatched callers explicitly.
  input.context != "pre-promote"
  v := {
    "code": "INVALID_CONTEXT",
    "message": "expected context=pre-promote",
    "actual": input.context
  }
}

violations contains v if {
  # Ensure promote decision uses the expected observation window.
  input.window_seconds != data.canary.window_seconds
  v := {
    "code": "WINDOW_MISMATCH",
    "message": sprintf("metrics window %v seconds does not match required %v seconds", [input.window_seconds, data.canary.window_seconds]),
    "actual": input.window_seconds,
    "threshold": data.canary.window_seconds
  }
}

violations contains v if {
  # Block promotion when observed error rate exceeds configured threshold.
  input.error_rate > data.canary.max_error_rate
  v := {
    "code": "HIGH_ERROR_RATE",
    "message": sprintf("error rate %v exceeds maximum %v", [input.error_rate, data.canary.max_error_rate]),
    "actual": input.error_rate,
    "threshold": data.canary.max_error_rate
  }
}

violations contains v if {
  # Block promotion when p99 latency exceeds configured threshold.
  input.p99_latency_ms > data.canary.max_p99_latency_ms
  v := {
    "code": "HIGH_P99_LATENCY",
    "message": sprintf("p99 latency %vms exceeds maximum %vms", [input.p99_latency_ms, data.canary.max_p99_latency_ms]),
    "actual": input.p99_latency_ms,
    "threshold": data.canary.max_p99_latency_ms
  }
}

allow if {
  count(violations) == 0
}

summary := "canary policy passed" if {
  allow
}

summary := sprintf("canary policy denied with %d violation(s)", [count(violations)]) if {
  not allow
}

violation_list := [v | some v in violations]

decision := {
  "allow": allow,
  "violations": violation_list,
  "summary": summary,
  "domain": "canary"
}
