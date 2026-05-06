package policy.infrastructure

import rego.v1

default allow := false

# Infrastructure domain answers one question:
# "Is it safe to proceed with pre-deploy based on host capacity?"

violations contains v if {
  # Policy questions are context-specific; reject mismatched callers explicitly.
  input.context != "pre-deploy"
  v := {
    "code": "INVALID_CONTEXT",
    "message": "expected context=pre-deploy",
    "actual": input.context
  }
}

violations contains v if {
  # Hard guardrail: block deploy when free disk falls below configured minimum.
  input.disk_free_gb < data.infrastructure.min_disk_free_gb
  v := {
    "code": "LOW_DISK",
    "message": sprintf("disk free %vGB is below minimum %vGB", [input.disk_free_gb, data.infrastructure.min_disk_free_gb]),
    "actual": input.disk_free_gb,
    "threshold": data.infrastructure.min_disk_free_gb
  }
}

violations contains v if {
  # Hard guardrail: block deploy when host load is above configured maximum.
  input.cpu_load > data.infrastructure.max_cpu_load
  v := {
    "code": "HIGH_CPU_LOAD",
    "message": sprintf("cpu load %v exceeds maximum %v", [input.cpu_load, data.infrastructure.max_cpu_load]),
    "actual": input.cpu_load,
    "threshold": data.infrastructure.max_cpu_load
  }
}

allow if {
  count(violations) == 0
}

summary := "infrastructure policy passed" if {
  allow
}

summary := sprintf("infrastructure policy denied with %d violation(s)", [count(violations)]) if {
  not allow
}

violation_list := [v | some v in violations]

decision := {
  "allow": allow,
  "violations": violation_list,
  "summary": summary,
  "domain": "infrastructure"
}
