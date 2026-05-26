package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type Handler struct {
	manager     *StoreManager
	proxy       *Proxy
	lockedRoom  id.RoomID
	mediaSecret string
}

func newHandler(manager *StoreManager, proxy *Proxy, lockedRoom id.RoomID, mediaSecret string) *Handler {
	return &Handler{manager: manager, proxy: proxy, lockedRoom: lockedRoom, mediaSecret: mediaSecret}
}

func (h *Handler) handleAPIConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"locked_room": string(h.lockedRoom),
	})
}

func (h *Handler) handleAPIRooms(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if h.lockedRoom != "" {
		name := string(h.lockedRoom)
		var content event.RoomNameEventContent
		if err := h.manager.fetcher.client.StateEvent(r.Context(), h.lockedRoom, event.StateRoomName, "", &content); err == nil && content.Name != "" {
			name = content.Name
		}
		json.NewEncoder(w).Encode([]RoomInfo{{ID: string(h.lockedRoom), DisplayName: name}})
		return
	}

	rooms, err := h.manager.fetcher.GetJoinedRooms(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(rooms)
}

func (h *Handler) handleAPIMedia(w http.ResponseWriter, r *http.Request) {
	roomID := id.RoomID(r.URL.Query().Get("room_id"))
	if roomID == "" {
		http.Error(w, "missing room_id", http.StatusBadRequest)
		return
	}
	if h.lockedRoom != "" && roomID != h.lockedRoom {
		http.Error(w, "room is locked by server configuration", http.StatusForbidden)
		return
	}

	offset := queryInt(r, "offset", 0)
	limit := queryInt(r, "limit", 50)
	if limit > 250 {
		limit = 250
	}
	if limit < 1 {
		limit = 1
	}

	store := h.manager.GetStore(roomID)

	if store.NeedsMore(offset, limit) {
		store.TriggerLoad(context.Background())
	}

	items, hasMore, total := store.GetPage(offset, limit)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{
		"items":    items,
		"has_more": hasMore,
		"loading":  store.IsLoading(),
		"total":    total,
		"offset":   offset,
	})
}

// handleMedia decodes the HMAC-signed token from /media/<token> and serves
// either a thumbnail or the original file depending on what was encoded.
// Requests with a missing or forged token are rejected with 403.
func (h *Handler) handleMedia(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/media/")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	kind, server, mediaID, err := decodeMediaToken(h.mediaSecret, token)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	switch kind {
	case "thumb":
		h.proxy.ServeThumb(w, r, server, mediaID)
	case "original":
		h.proxy.ServeOriginal(w, r, server, mediaID)
	default:
		http.Error(w, "forbidden", http.StatusForbidden)
	}
}

// noCacheHTML sets Cache-Control: no-store on responses for HTML pages so the
// browser always re-fetches index.html rather than serving a stale version.
func noCacheHTML(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" || strings.HasSuffix(p, ".html") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}
