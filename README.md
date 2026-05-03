# SwiftDeploy Project (Stage 4A)

This folder contains the Stage 4A scaffold.

## Included

- `manifest.yaml` - single source of truth
- `swiftdeploy` - CLI entrypoint (Bash)
- `app/main.go` - Go API service (`/`, `/healthz`, `/chaos`)
- `templates/` - config templates
- `generated/` - rendered outputs from templates
- `Dockerfile` - multi-stage Go image build definition

## Next

1. Build image: `docker build -t swiftdeploy-api-go:latest .`
2. Run `./swiftdeploy init` to render deployment artifacts.
3. Add preflight checks in `./swiftdeploy validate`.
4. Wire deploy, promote, and teardown lifecycle commands.
