# syntax=docker/dockerfile:1

# SendAGift API image.
#
# One image carries two binaries:
#   /app/api      the HTTP server (the default command)
#   /app/migrate  applies database migrations, then exits
# so a deploy can run `/app/migrate` as a one-off task before the new API
# starts, from exactly the same build.
#
# Build:  docker build --build-arg GIT_SHA=$(git rev-parse HEAD) -t sendagift-api .
# The SHA is reported by GET /version.

FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies first, so they stay cached until go.mod or go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG GIT_SHA=dev
# Static binaries (no cgo) so they run on the minimal base image below.
# timetzdata embeds the time zone database: competitions schedule rounds in
# their own time zones, and the runtime image has no zoneinfo of its own.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -tags timetzdata \
      -ldflags "-s -w -X myapp/internal/routes.BuildSHA=${GIT_SHA}" \
      -o /out/api ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -tags timetzdata \
      -ldflags "-s -w" \
      -o /out/migrate ./cmd/migrate

# Distroless: no shell or package manager, just CA certificates (for S3 and
# Google) and an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot

ARG GIT_SHA=dev
LABEL org.opencontainers.image.title="sendagift-api" \
      org.opencontainers.image.revision="${GIT_SHA}"

WORKDIR /app
COPY --from=build /out/api /out/migrate /app/

ENV APP_PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/api"]
