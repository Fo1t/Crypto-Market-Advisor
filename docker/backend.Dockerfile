# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /src

# Dependencies are cached separately from the source so that code edits do not
# re-download the module graph.
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/server ./cmd/server

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata wget && \
    adduser -D -u 10001 advisor

COPY --from=builder /out/server /usr/local/bin/server

USER advisor
WORKDIR /app

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=5 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/api/health/db || exit 1

ENTRYPOINT ["/usr/local/bin/server"]
CMD ["serve"]
