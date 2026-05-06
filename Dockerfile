# Builder stage compiles a static Linux binary.
FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY app/go.mod ./
COPY app/ ./
# Tidy in build stage to keep module locks reproducible in CI/container builds.
# Omit GOARCH so the binary matches the builder image (works on amd64 and arm64 hosts).
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux go build -o /out/swift-api ./main.go

# Runtime stage keeps final image small and non-root.
FROM alpine:3.20

WORKDIR /app
COPY --from=builder /out/swift-api /app/swift-api

# wget is used by container health checks in generated compose.
RUN addgroup -S appgroup && adduser -S appuser -G appgroup \
  && apk add --no-cache ca-certificates wget

EXPOSE 3000
USER appuser:appgroup
CMD ["/app/swift-api"]
