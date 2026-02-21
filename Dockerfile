# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

# CGO dependencies for sqlite3 + sqlite-vec
RUN apk add --no-cache gcc musl-dev

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -tags "fts5" -o engram ./cmd/engram

# ── Final stage ───────────────────────────────────────────────────────────────
FROM alpine:3.21

# ca-certificates for TLS to Anthropic / Ollama APIs
RUN apk add --no-cache ca-certificates

WORKDIR /data

COPY --from=builder /build/engram /usr/local/bin/engram

# Default port; override with ENGRAM_SERVER_PORT or config file
EXPOSE 8080

# Mount a volume at /data for the database and config file.
# Pass -config /data/engram.yaml or use ENGRAM_* env vars.
VOLUME ["/data"]

ENTRYPOINT ["/usr/local/bin/engram"]
CMD ["-config", "/data/engram.yaml"]
