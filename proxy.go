package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/gif"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

type Proxy struct {
	homeserver  string
	accessToken string
	cacheDir    string
	httpClient  *http.Client
}

func newProxy(cfg *Config) (*Proxy, error) {
	thumbDir := filepath.Join(cfg.CacheDir, "thumbs")
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		return nil, fmt.Errorf("creating thumbnail cache dir: %w", err)
	}
	return &Proxy{
		homeserver:  strings.TrimRight(cfg.Homeserver, "/"),
		accessToken: cfg.AccessToken,
		cacheDir:    cfg.CacheDir,
		httpClient:  &http.Client{},
	}, nil
}

func (p *Proxy) thumbCachePath(server, mediaID string) string {
	safe := func(s string) string {
		return strings.NewReplacer("/", "_", ":", "_").Replace(s)
	}
	return filepath.Join(p.cacheDir, "thumbs", safe(server)+"_"+safe(mediaID)+".jpg")
}

// ServeThumb serves a thumbnail for the given mxc media, using the local cache
// when available.
func (p *Proxy) ServeThumb(w http.ResponseWriter, r *http.Request, server, mediaID string) {
	data, err := p.getOrFetchThumb(r.Context(), server, mediaID)
	if err != nil {
		log.Printf("Thumbnail unavailable for %s/%s: %v", server, mediaID, err)
		p.serveFilePlaceholder(w)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

// Precache ensures a thumbnail for the given media is in the local cache.
func (p *Proxy) Precache(server, mediaID string) {
	_, err := p.getOrFetchThumb(context.Background(), server, mediaID)
	if err != nil {
		log.Printf("Pre-cache failed for %s/%s: %v", server, mediaID, err)
	}
}

func (p *Proxy) getOrFetchThumb(ctx context.Context, server, mediaID string) ([]byte, error) {
	cachePath := p.thumbCachePath(server, mediaID)

	if data, err := os.ReadFile(cachePath); err == nil {
		return data, nil
	}

	data, err := p.fetchThumbnail(ctx, server, mediaID)
	if err != nil {
		return nil, err
	}

	if werr := os.WriteFile(cachePath, data, 0644); werr != nil {
		log.Printf("Warning: could not cache thumbnail %s: %v", cachePath, werr)
	}
	return data, nil
}

// fetchThumbnail attempts to obtain JPEG thumbnail bytes for the given media.
// Strategy:
//  1. Matrix /_matrix/client/v1/media/thumbnail (authenticated media, Matrix ≥ v1.11)
//  2. Matrix /_matrix/media/v3/thumbnail        (legacy fallback for older servers)
//  3. Download the original via the same two-path fallback and resize locally.
func (p *Proxy) fetchThumbnail(ctx context.Context, server, mediaID string) ([]byte, error) {
	thumbQuery := "?width=400&height=400&method=scale"

	// Try both API paths for the thumbnail endpoint.
	thumbURLs := []string{
		p.homeserver + "/_matrix/client/v1/media/thumbnail/" + server + "/" + mediaID + thumbQuery,
		p.homeserver + "/_matrix/media/v3/thumbnail/" + server + "/" + mediaID + thumbQuery,
	}
	for _, u := range thumbURLs {
		data, ct, err := p.fetchAuthed(ctx, u)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(ct, "image/") {
			continue
		}
		if !strings.Contains(ct, "jpeg") {
			if data, err = p.toJPEG(data); err != nil {
				continue
			}
		}
		return data, nil
	}

	// Thumbnail endpoint unavailable; download the original and resize locally.
	downloadURLs := []string{
		p.homeserver + "/_matrix/client/v1/media/download/" + server + "/" + mediaID,
		p.homeserver + "/_matrix/media/v3/download/" + server + "/" + mediaID,
	}
	var lastErr error
	for _, u := range downloadURLs {
		origData, origCT, err := p.fetchAuthed(ctx, u)
		if err != nil {
			lastErr = err
			continue
		}
		if !strings.HasPrefix(origCT, "image/") {
			return nil, fmt.Errorf("media is not an image (%s)", origCT)
		}
		return p.generateThumbnail(origData)
	}
	return nil, fmt.Errorf("all media endpoints failed: %w", lastErr)
}

// ServeOriginal proxies the original media from the Matrix server, streaming
// the response without caching. Tries the authenticated v1 endpoint first,
// then falls back to the legacy v3 path.
func (p *Proxy) ServeOriginal(w http.ResponseWriter, r *http.Request, server, mediaID string) {
	urls := []string{
		p.homeserver + "/_matrix/client/v1/media/download/" + server + "/" + mediaID,
		p.homeserver + "/_matrix/media/v3/download/" + server + "/" + mediaID,
	}

	for _, origURL := range urls {
		req, err := http.NewRequestWithContext(r.Context(), "GET", origURL, nil)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		req.Header.Set("Authorization", "Bearer "+p.accessToken)

		resp, err := p.httpClient.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
			// Drain body so the connection can be reused, then try next URL.
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			continue
		}

		for _, h := range []string{"Content-Type", "Content-Length", "Content-Disposition"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	http.Error(w, "media not found", http.StatusNotFound)
}

func (p *Proxy) fetchAuthed(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.accessToken)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%s → %d", url, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	return data, resp.Header.Get("Content-Type"), err
}

func (p *Proxy) generateThumbnail(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decoding image: %w", err)
	}
	thumb := imaging.Fit(img, 400, 400, imaging.Lanczos)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 82}); err != nil {
		return nil, fmt.Errorf("encoding thumbnail: %w", err)
	}
	return buf.Bytes(), nil
}

func (p *Proxy) toJPEG(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// serveFilePlaceholder serves a minimal SVG for non-image or unavailable media.
func (p *Proxy) serveFilePlaceholder(w http.ResponseWriter) {
	const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="400" height="400" viewBox="0 0 400 400">` +
		`<rect width="400" height="400" fill="#1a1a2e"/>` +
		`<text x="200" y="220" font-size="80" text-anchor="middle" fill="#4a4a8a">?</text>` +
		`</svg>`
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	fmt.Fprint(w, svg)
}
