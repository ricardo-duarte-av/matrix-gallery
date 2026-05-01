package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct {
	store *Store
	proxy *Proxy
}

func newHandler(store *Store, proxy *Proxy) *Handler {
	return &Handler{store: store, proxy: proxy}
}

func (h *Handler) handleAPIMedia(w http.ResponseWriter, r *http.Request) {
	offset := queryInt(r, "offset", 0)
	limit := queryInt(r, "limit", 50)
	if limit > 250 {
		limit = 250
	}
	if limit < 1 {
		limit = 1
	}

	if h.store.NeedsMore(offset, limit) {
		h.store.TriggerLoad(context.Background())
	}

	items, hasMore, total := h.store.GetPage(offset, limit)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"items":    items,
		"has_more": hasMore,
		"loading":  h.store.IsLoading(),
		"total":    total,
		"offset":   offset,
	})
}

func (h *Handler) handleThumb(w http.ResponseWriter, r *http.Request) {
	server, mediaID, ok := parseMXCPath(r.URL.Path, "/media/thumb/")
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	h.proxy.ServeThumb(w, r, server, mediaID)
}

func (h *Handler) handleOriginal(w http.ResponseWriter, r *http.Request) {
	server, mediaID, ok := parseMXCPath(r.URL.Path, "/media/original/")
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	h.proxy.ServeOriginal(w, r, server, mediaID)
}

// parseMXCPath extracts {server} and {mediaId} from a path like
// "{prefix}{server}/{mediaId}".
func parseMXCPath(urlPath, prefix string) (server, mediaID string, ok bool) {
	rest := strings.TrimPrefix(urlPath, prefix)
	idx := strings.Index(rest, "/")
	if idx < 1 || idx == len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
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
