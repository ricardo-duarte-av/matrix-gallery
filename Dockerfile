# syntax=docker/dockerfile:1

# --- build ---------------------------------------------------------------
FROM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies first so the layer is cached across source-only changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# No cgo dependencies; the frontend is embedded via //go:embed static.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/matrix-gallery .

# --- runtime -------------------------------------------------------------
FROM alpine:3.22

# HTTPS to the Matrix homeserver needs root certs; tzdata keeps timestamps
# sane if TZ is set.
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 -h /data gallery

COPY --from=build /out/matrix-gallery /usr/local/bin/matrix-gallery

# Pre-create the cache directory so the default (non-root) user can write
# thumbnails when no host directory is mounted over it.
RUN mkdir -p /data/cache && chown -R gallery:gallery /data

# The binary reads ./config.yaml and writes ./cache/thumbs, so /data is the
# mount point: config.yaml in, thumbnail cache out. No VOLUME declaration —
# docker-compose.yaml mounts the two paths explicitly, and an anonymous
# volume over /data would shadow them.
WORKDIR /data
USER gallery

EXPOSE 8008

ENTRYPOINT ["/usr/local/bin/matrix-gallery"]
