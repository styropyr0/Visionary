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
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY sdks/go/ sdks/go/
COPY --from=web /src/internal/analytics/web/dist/ internal/analytics/web/dist/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X github.com/styropyr0/visionary/internal/analytics.Version=$VERSION -X github.com/styropyr0/visionary/internal/analytics.Commit=$COMMIT" -o /out/visionary ./cmd/visionary

# 3. Runtime: no shell, no package manager, non-root.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=server /out/visionary /visionary
ENV VISIONARY_ADDR=:8090
EXPOSE 8090
USER nonroot:nonroot
ENTRYPOINT ["/visionary"]
