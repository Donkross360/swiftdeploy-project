# SwiftDeploy Project

`swiftdeploy` is a manifest-driven deployment CLI for a Go API behind Nginx, extended with Prometheus metrics and Open Policy Agent (OPA) gates plus a lightweight audit trail.

## Prerequisites

Required to run CLI workflows:

- Docker Engine (daemon running)
- Docker Compose plugin (`docker compose`)
- Bash
- `curl`
- `ss` (from `iproute2`)
- `python3` (metrics/policy parsing, YAML validation fallback, audit reports)
- One YAML parser path for strict validation:
  - `python3` + `PyYAML`, or
  - `ruby` with stdlib YAML

Optional for local app development outside Docker:

- Go toolchain (`go`, `gofmt`)

## Project Layout

- `manifest.yaml` - single source of truth
- `swiftdeploy` - executable CLI script
- `app/` - Go API (`/`, `/healthz`, `/chaos`, `/metrics`)
- `templates/` - Nginx and Compose templates (app, nginx, OPA)
- `policies/` - Rego (`policy.infrastructure`, `policy.canary`)
- `policy-data/` - external thresholds (`thresholds.json`) loaded by OPA
- `template-output/` - default generated output (nginx.conf, docker-compose.yml, audit history)
- `Dockerfile` - API image build

## Output Directory Configuration

Default output directory: `template-output/`.

Override with:

```bash
SWIFTDEPLOY_OUT_DIR=swiftdeploy-output ./swiftdeploy init
```

Relative paths resolve from the project root; absolute paths are used as given.

## Core Features

- **`GET /metrics`** — Prometheus text format with `http_requests_total`, `http_request_duration_seconds`, `app_uptime_seconds`, `app_mode`, `chaos_active`.
- **OPA sidecar** — Compose binds OPA only to **`127.0.0.1:<opa.port>`** (see `manifest.yaml`). No Nginx route forwards to OPA.
- **Policies** — Structured decisions at `policy/infrastructure/decision` and `policy/canary/decision`; thresholds read from **`data.infrastructure`** and **`data.canary`** (mounted JSON).
- **Pre-deploy gate** (`deploy`) — Host disk/CPU snapshot → infrastructure policy → blocks with violation messages on deny.
- **Pre-promote gate** (`promote stable` when current mode is **canary**) — Two `/metrics` scrapes through Nginx separated by **`canary.window_seconds`** → windowed error rate and p99 → canary policy → blocks on deny.
- **`status [interval_seconds]`** — Loop: scrape metrics, evaluate both policy domains with current inputs, append JSON Lines to `audit.history_file`.
- **`audit`** — Reads history, writes `audit.report_file` (Markdown timeline and policy notes).
- **OPA failure handling** — Connection errors, timeouts, malformed JSON, undefined documents.

## Quick Usage

```bash
./swiftdeploy build          # build API image from Dockerfile
./swiftdeploy init           # render nginx.conf + docker-compose.yml
./swiftdeploy validate       # five preflight checks
./swiftdeploy deploy         # init → OPA → pre-deploy gate → validate → stack up → wait for health

./swiftdeploy promote canary    # manifest + app restart; chaos available in canary
./swiftdeploy promote stable    # when already canary: pre-promote gate → then mode change

./swiftdeploy status 5         # optional: live samples + history append (requires stack + OPA)
./swiftdeploy audit            # generate Markdown report from history

./swiftdeploy teardown --clean
```

Replace `8181` / `8080` below if your `manifest.yaml` uses different `opa.port` / `nginx.port`.

## Quick manual checks (copy/paste)

After a successful `./swiftdeploy deploy` (stack healthy):

```bash
# Ingress: Prometheus text (must go through nginx port, not app port exposed to host).
curl -sS "http://127.0.0.1:8080/metrics" | grep -E "http_requests_total|chaos_active|app_uptime_seconds" | head

# Policy allow (requires `policy/` in the URL — omitting it returns empty `{}`).
curl -sS -X POST "http://127.0.0.1:8181/v1/data/policy/infrastructure/decision" \
  -H "Content-Type: application/json" \
  -d '{"input":{"context":"pre-deploy","disk_free_gb":50,"cpu_load":0.3}}'

curl -sS -X POST "http://127.0.0.1:8181/v1/data/policy/canary/decision" \
  -H "Content-Type: application/json" \
  -d '{"input":{"context":"pre-promote","window_seconds":30,"error_rate":0.001,"p99_latency_ms":100}}'

# Isolation: OPA-shaped path on nginx → 404 (see section below).
curl -sS -o /dev/null -w "%{http_code}\n" "http://127.0.0.1:8080/v1/data/policy/infrastructure/decision"
```

Canary chaos / recover (only when `services.mode` is **canary**):

```bash
curl -sS -X POST "http://127.0.0.1:8080/chaos" \
  -H "Content-Type: application/json" \
  -d '{"mode":"recover"}'
```

If the app container is unhealthy after an image rebuild, inspect logs:

```bash
docker compose -f template-output/docker-compose.yml -p swiftdeploy logs app
```

### Policy isolation (OPA not on the public ingress port)

Nginx only proxies to the app. OPA is not an upstream. The API registers exact routes (`/`, `/healthz`, `/chaos`, `/metrics`), so OPA-style paths are not served by the app.

```bash
# Through Nginx: unknown OPA-style paths should be 404 from the app.
curl -sS -o /dev/null -w "%{http_code}\n" "http://127.0.0.1:8080/v1/data/policy/infrastructure/decision"
```

OPA remains reachable only on the host loopback binding from Compose (`127.0.0.1:8181`).

## Editing Thresholds

Change `policy-data/thresholds.json`, then restart OPA so it reloads the mounted file (for example `docker compose -f template-output/docker-compose.yml -p swiftdeploy restart opa`), or recreate the container. **`deploy`** enforces **`infrastructure`**; **`promote stable`** (from canary) enforces **`canary`**.

## Pre-Promote Testing Notes

- The gate runs **only** for **canary → stable**.
- Metrics are deltas over **`window_seconds`**. With **no** traffic, error rate can be **0** and the gate may pass despite a “broken” story—use realistic load for demos.
- **Chaos** (`POST /chaos` with `mode: error`) stays on until **`{"mode":"recover"}`**. Even without a manual load generator, **Docker health checks** hit `/healthz`, and **`/metrics` scrapes** count as successful requests—so you can still see **non-zero** windowed error rates until you recover.
- To force a **deny**: enable error chaos, add background `GET /` or rely on healthcheck + metrics mix, then `./swiftdeploy promote stable`.
- To **pass** after a deny: `curl -X POST .../chaos -d '{"mode":"recover"}'`, then promote again.

## Command Behavior

- **`init`** — Reads `manifest.yaml`, writes `nginx.conf` and `docker-compose.yml` (includes OPA).
- **`validate`** — (1) manifest YAML (2) required keys (3) image present (4) Nginx port free (5) `nginx -t` via container.
- **`deploy`** — `init` → start **OPA** → **pre-deploy** gate → `validate` → `compose up` → wait for `/healthz` through Nginx.
- **`promote <canary|stable>`** — If current mode is **canary** and target is **stable**, runs **pre-promote gate first** (does not change manifest until policy allows). Then updates `services.mode`, `init`, recreates **app** only, waits for health, checks `X-Mode` on `/healthz`.
- **`status`** — Periodic metrics + policy snapshot; appends to `audit.history_file` (see manifest).
- **`audit`** — Builds `audit.report_file` from history (run `status` at least once to create records).
- **`teardown [--clean]`** — `compose down -v`; `--clean` removes generated compose and nginx files from the output directory.

## Troubleshooting

### Nginx port already in use

```bash
ss -ltnp | rg ":8080"
```

Stop the conflicting container or process, or change `nginx.port` in `manifest.yaml`, then `init` and `validate` again.

### Deploy blocked by infrastructure policy

Thresholds are too strict for this host (CPU load, free disk). Relax `policy-data/thresholds.json` or free resources, restart OPA if needed, then retry.

### Promote stable blocked by canary policy

Clear chaos, ensure the window has acceptable error rate and p99 (see **Pre-Promote Testing Notes**). Shorten `window_seconds` temporarily for faster iteration.

### `audit` says history missing

Run `./swiftdeploy status 5` (or another interval) while the stack and OPA are up so `history.jsonl` is created.

## Checklist

- `validate` output (all checks pass)
- `deploy` output (including pre-deploy policy line)
- `promote canary` then `promote stable` (or deny + recover flow) with pre-promote gate
- Generated `nginx.conf` and `docker-compose.yml` snippets
- Sample `curl` to `/metrics` through Nginx
- Evidence that OPA is **not** exposed via the Nginx port (status code / body as above)
- Optional: `audit_report.md` after running `status` + `audit`
