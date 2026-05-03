FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY app/go.mod ./
RUN go mod download
COPY app/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/swift-api ./main.go

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=builder /out/swift-api /app/swift-api

EXPOSE 3000
USER nonroot:nonroot
CMD ["/app/swift-api"]
