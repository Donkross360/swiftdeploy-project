FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY app/go.mod ./
RUN go mod download
COPY app/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/swift-api ./main.go

FROM alpine:3.20

WORKDIR /app
COPY --from=builder /out/swift-api /app/swift-api

RUN addgroup -S appgroup && adduser -S appuser -G appgroup \
  && apk add --no-cache ca-certificates wget

EXPOSE 3000
USER appuser:appgroup
CMD ["/app/swift-api"]
