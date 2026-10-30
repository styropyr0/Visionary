# Visionary: React frontend + Go server in one static binary.
#   docker build -t visionary .

# 1. Frontend, built on the build machine's platform; the output is static files.
FROM --platform=$BUILDPLATFORM node:22.11.0-alpine3.20 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN mkdir -p /src/internal/analytics/web/dist && npm run build

# 2. Go server, cross-compiled for the target platform with the frontend embedded.
FROM --platform=$BUILDPLATFORM golang:1.26.2-alpine3.22 AS server
ARG TARGETOS
ARG TARGETARCH
# Release tag and commit, shown in the About panel.
ARG VERSION=
ARG COMMIT=
WORKDIR /src
COPY go.mod go.sum ./
COPY sdks/go/go.mod sdks/go/go.sum sdks/go/
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY sdks/go/ sdks/go/
COPY --from=web /src/internal/analytics/web/dist/ internal/analytics/web/dist/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X github.com/styropyr0/visionary/internal/analytics.Version=$VERSION -X github.com/styropyr0/visionary/internal/analytics.Commit=$COMMIT" -o /out/visionary ./cmd/visionary

# Optional runtime with Chromium for browser uptime checks (~400 MB more):
#   docker build --target browser -t visionary:browser .
FROM debian:12.12-slim AS browser
RUN apt-get update \
 && apt-get install -y --no-install-recommends chromium fonts-liberation fonts-noto-color-emoji ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 65532 --create-home --home-dir /home/nonroot nonroot
COPY --from=server /out/visionary /visionary
ENV VISIONARY_ADDR=:8090 VISIONARY_CHROME=/usr/bin/chromium
EXPOSE 8090 4317
USER nonroot:nonroot
ENTRYPOINT ["/visionary"]

# 3. Runtime: no shell, no package manager, non-root. The default target.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=server /out/visionary /visionary
ENV VISIONARY_ADDR=:8090
# 4317 is OTLP/gRPC, served once Settings → Server → Ingest turns it on.
EXPOSE 8090 4317
USER nonroot:nonroot
ENTRYPOINT ["/visionary"]
