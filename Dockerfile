# ── Stage 1: build ───────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Download dependencies first (cached layer)
COPY go.mod go.sum ./
RUN go mod download

# Build a static binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /medbook ./cmd/

# ── Stage 2: runtime ─────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /medbook /medbook

EXPOSE 8080

# Default: start the web server.
# Override CMD to run "migrate" or "sync" as a one-off Job/initContainer.
ENTRYPOINT ["/medbook"]
CMD ["serve"]
