package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"maunium.net/go/mautrix/id"
)

//go:embed static
var staticFiles embed.FS

func main() {
	cfg, err := loadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Config error: %v\n\nCopy sample.config.yaml to config.yaml and fill in your values.", err)
	}

	fetcher, err := newMatrixFetcher(cfg)
	if err != nil {
		log.Fatalf("Matrix client error: %v", err)
	}
	log.Println("Matrix client initialized")

	proxy, err := newProxy(cfg)
	if err != nil {
		log.Fatalf("Proxy setup error: %v", err)
	}
	log.Println("Proxy initialized")

	manager := NewStoreManager(fetcher, proxy)
	h := newHandler(manager, proxy, id.RoomID(cfg.RoomID))
	if cfg.RoomID != "" {
		log.Printf("Room lock enabled: serving only %s", cfg.RoomID)
	}

	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("Static files error: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/media", h.handleAPIMedia)
	mux.HandleFunc("GET /api/rooms", h.handleAPIRooms)
	mux.HandleFunc("GET /api/config", h.handleAPIConfig)
	mux.HandleFunc("/media/thumb/", h.handleThumb)
	mux.HandleFunc("/media/original/", h.handleOriginal)

	fileServer := http.FileServer(http.FS(staticFS))
	mux.Handle("/", noCacheHTML(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If the path starts with /room/ or is exactly /, serve the SPA (index.html)
		if strings.HasPrefix(r.URL.Path, "/room/") {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})))

	log.Printf("Gallery server listening on http://%s", cfg.ListenAddr())

	// Continuous long-poll sync for new items.
	go manager.SyncLoop(context.Background())

	if err := http.ListenAndServe(cfg.ListenAddr(), mux); err != nil {
		log.Fatalf("Server: %v", err)
	}
}
