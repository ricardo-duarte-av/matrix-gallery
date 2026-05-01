package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
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

	proxy, err := newProxy(cfg)
	if err != nil {
		log.Fatalf("Proxy setup error: %v", err)
	}

	store := newStore(fetcher, proxy)
	h := newHandler(store, proxy)

	// Kick off the first batch fetch so media is ready quickly.
	store.TriggerLoad(context.Background())

	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("Static files error: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/media", h.handleAPIMedia)
	mux.HandleFunc("/media/thumb/", h.handleThumb)
	mux.HandleFunc("/media/original/", h.handleOriginal)
	mux.Handle("/", noCacheHTML(http.FileServer(http.FS(staticFS))))

	log.Printf("Gallery server listening on http://%s", cfg.ListenAddr())
	if err := http.ListenAndServe(cfg.ListenAddr(), mux); err != nil {
		log.Fatalf("Server: %v", err)
	}
}
