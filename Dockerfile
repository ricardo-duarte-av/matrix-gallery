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

# The binary reads ./config.yaml and writes ./cache/thumbs, so /data is the
# host-mounted directory: config.yaml in, thumbnail cache out.
VOLUME /data
WORKDIR /data
USER gallery

EXPOSE 8008

ENTRYPOINT ["/usr/local/bin/matrix-gallery"]
