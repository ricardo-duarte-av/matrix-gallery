# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build
go build ./...

# Run (requires config.yaml)
go run .

# Run compiled binary
./matrix-gallery
```

There are no tests and no linter configuration. `go vet ./...` is the closest available static check.

## Configuration

Copy `sample.config.yaml` to `config.yaml` before running. Required fields: `homeserver`, `access_token`, `room_id`. The server defaults to `0.0.0.0:8008` with thumbnails cached under `./cache/thumbs/`.

## Architecture

The application is a single Go binary that serves a media gallery from a Matrix room. The frontend is a single-page app embedded in the binary via `//go:embed static`.

### Data flow

```
Matrix homeserver
    │
    ├─ /messages (backward pagination) ──► MatrixFetcher.FetchBatch
    └─ /sync     (long-poll forward)   ──► MatrixFetcher.SyncOnce
                                               │
                                               ▼
                                            Store
                                         (in-memory)
                                               │
                                    GET /api/media?offset=&limit=
                                               │
                                               ▼
                                        index.html (JS)
                                      infinite-scroll masonry
```

### Key design decisions

**Backward pagination + deferred reactions.** `FetchBatch` walks room history newest-first. Because `m.reaction` events always have a later timestamp than the event they annotate, reactions are encountered *before* their target media events when paginating backward. The `Store` keeps a persistent `reactions map[string]map[string]int` (target event ID → emoji → count) that accumulates across batches. Each time a batch is processed, `mergeReactions` updates any already-loaded items, and newly loaded items are stamped with whatever reactions have accumulated so far. This means reactions are populated correctly even across batch boundaries.

**Two loading paths.** `Store.TriggerLoad` does incremental backward pagination (called on demand by the API handler when the client scrolls near the end). `Store.SyncLoop` runs as a goroutine doing 30-second long-poll sync calls to receive new events in real time. Both paths go through `mergeReactions`.

**Media proxy.** The browser never talks directly to the homeserver. All `mxc://` URIs are rewritten to `/media/thumb/{server}/{mediaID}` or `/media/original/{server}/{mediaID}`. The proxy tries the Matrix v1.11 authenticated endpoint first (`/_matrix/client/v1/media/…`), then falls back to the legacy v3 path. Thumbnails are generated at 400×400 JPEG and cached to disk; originals are streamed without caching. Five goroutines pre-cache thumbnails in the background as items are loaded.

**Masonry layout.** Column placement is done in JS by measuring each card's `offsetHeight` immediately after DOM insertion (images are pre-sized from known `width`/`height` metadata so the container height is correct before the image loads). A debounced relayout fires on window resize.

### File map

| File | Responsibility |
|---|---|
| `matrix.go` | `MediaItem` type, `MatrixFetcher` (API calls, member cache, event parsing) |
| `store.go` | `Store` (in-memory state, pagination loop, sync loop, reaction merging) |
| `proxy.go` | Thumbnail fetching/caching, original media proxying, local JPEG generation |
| `handlers.go` | HTTP handlers wiring store and proxy to routes |
| `config.go` | YAML config loading |
| `main.go` | Wires everything together, starts sync goroutine, serves embedded static files |
| `static/index.html` | Entire frontend: masonry engine, infinite scroll, lightbox, card rendering |
