# SwiftDeploy Project (Stage 4A)

`swiftdeploy` is a manifest-driven CLI that generates deployment configs, validates preflight requirements, and manages stack lifecycle commands for a Go API behind Nginx.

## Prerequisites

Required to run CLI workflows:

- Docker Engine (daemon running)
- Docker Compose plugin (`docker compose`)
- Bash
- `curl`
- `ss` (from `iproute2`)
- One YAML parser path for strict validation:
  - `python3` + `pyyaml`, or
  - `ruby` with stdlib YAML

Optional for local app development (outside Docker):

- Go toolchain (`go`, `gofmt`)

## Project Layout

- `manifest.yaml` - single source of truth
- `swiftdeploy` - executable CLI script
- `app/` - Go API service
- `templates/` - source templates for Nginx and Compose
- `template-output/` - default render output directory
- `Dockerfile` - service image build

## Output Directory Configuration

By default, generated files are written to `template-output/`.

You can override that directory with `SWIFTDEPLOY_OUT_DIR`:

```bash
SWIFTDEPLOY_OUT_DIR=swiftdeploy-output ./swiftdeploy init
```

If the value is relative, it is resolved from the project root.  
If it is absolute, it is used directly.

## Quick Usage

```bash
# Build service image referenced by manifest.yaml
./swiftdeploy build

# Generate nginx.conf and docker-compose.yml from templates
./swiftdeploy init

# Run 5 preflight checks (non-zero on any failure)
./swiftdeploy validate

# Generate -> validate -> start stack -> wait for health
./swiftdeploy deploy

# Switch runtime mode and restart only app container
./swiftdeploy promote canary
./swiftdeploy promote stable

# Stop stack; --clean also removes generated config files
./swiftdeploy teardown --clean
```

## End-to-End Walkthrough

Run from project root:

```bash
# 1) Build the app image declared in manifest.yaml
./swiftdeploy build

# 2) Render deployment configs from manifest.yaml
./swiftdeploy init

# 3) Run preflight checks
./swiftdeploy validate

# 4) Deploy stack and wait until /healthz is ready
./swiftdeploy deploy

# 5) Verify baseline response
curl -s http://127.0.0.1:8080/
curl -s http://127.0.0.1:8080/healthz

# 6) Promote to canary and verify mode
./swiftdeploy promote canary
curl -i http://127.0.0.1:8080/healthz

# 7) Exercise chaos in canary mode
curl -s -X POST http://127.0.0.1:8080/chaos \
  -H "Content-Type: application/json" \
  -d '{"mode":"slow","duration":2}'
curl -s -X POST http://127.0.0.1:8080/chaos \
  -H "Content-Type: application/json" \
  -d '{"mode":"error","rate":0.5}'
curl -s -X POST http://127.0.0.1:8080/chaos \
  -H "Content-Type: application/json" \
  -d '{"mode":"recover"}'

# 8) Promote back to stable and verify header disappears
./swiftdeploy promote stable
curl -i http://127.0.0.1:8080/healthz

# 9) Tear down resources (and optionally generated files)
./swiftdeploy teardown --clean
```

## Command Behavior

- `init`
  - reads `manifest.yaml`
  - generates `nginx.conf` and `docker-compose.yml` in output directory
- `validate`
  - runs 5 checks and exits non-zero on any failure:
    1. manifest exists + valid YAML
    2. required fields present/non-empty
    3. app image exists locally
    4. nginx host port is free
    5. generated nginx config passes `nginx -t`
- `deploy`
  - runs init + validate
  - starts stack with Compose
  - waits for healthy `/healthz` up to manifest timeout
- `promote <canary|stable>`
  - updates `services.mode` in `manifest.yaml`
  - regenerates compose config
  - restarts app container only
  - confirms mode via `/healthz` response header behavior
- `teardown [--clean]`
  - removes containers, network, and volumes for the stack
  - with `--clean`, also removes generated config files

## Troubleshooting

### Nginx port already in use

If `./swiftdeploy validate` fails at check `4) nginx host port not already in use`:

```bash
ss -ltnp | rg ":8080"
```

Resolve with one of these:

1. Stop the conflicting process currently bound to the port:

```bash
# If it is a Docker container publishing 8080
docker ps --format "{{.ID}}\t{{.Ports}}\t{{.Names}}" | rg "0.0.0.0:8080|:::8080"
docker stop <container_id_or_name>

# If it is a system service
sudo systemctl stop <service-name>

# If you only have a PID from ss output
sudo kill <pid>
```

2. Change `nginx.port` in `manifest.yaml` to an unused port (for example `8081`), then rerun:

```bash
./swiftdeploy init
./swiftdeploy validate
./swiftdeploy deploy
```

