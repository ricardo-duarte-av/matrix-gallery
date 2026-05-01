package main

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type MediaItem struct {
	EventID      string `json:"event_id"`
	Type         string `json:"type"` // "image", "video", "file"
	ThumbnailURL string `json:"thumbnail_url"`
	OriginalURL  string `json:"original_url"`
	Filename     string `json:"filename"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	Sender       string `json:"sender,omitempty"`
	Timestamp    int64  `json:"timestamp,omitempty"`
}

type MatrixFetcher struct {
	client *mautrix.Client
	roomID id.RoomID
}

func newMatrixFetcher(cfg *Config) (*MatrixFetcher, error) {
	client, err := mautrix.NewClient(cfg.Homeserver, id.UserID(""), cfg.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("creating matrix client: %w", err)
	}
	return &MatrixFetcher{
		client: client,
		roomID: id.RoomID(cfg.RoomID),
	}, nil
}

// FetchBatch retrieves up to limit message events going backward from fromToken.
// Pass an empty fromToken to start from the latest events in the room.
// Returns the media items found and the next pagination token.
func (f *MatrixFetcher) FetchBatch(ctx context.Context, fromToken string, limit int) ([]MediaItem, string, error) {
	resp, err := f.client.Messages(ctx, f.roomID, fromToken, "", mautrix.DirectionBackward, nil, limit)
	if err != nil {
		return nil, "", fmt.Errorf("fetching messages: %w", err)
	}

	var items []MediaItem
	for _, evt := range resp.Chunk {
		if evt.Type != event.EventMessage && evt.Type != event.EventSticker {
			continue
		}
		if err := evt.Content.ParseRaw(evt.Type); err != nil {
			continue
		}
		msg := evt.Content.AsMessage()
		item, ok := extractMediaItem(evt, msg)
		if !ok {
			continue
		}
		items = append(items, item)
	}

	return items, resp.End, nil
}

func extractMediaItem(evt *event.Event, msg *event.MessageEventContent) (MediaItem, bool) {
	var itemType string
	switch msg.MsgType {
	case event.MsgImage:
		itemType = "image"
	case event.MsgVideo:
		itemType = "video"
	case event.MsgFile:
		itemType = "file"
	case "":
		// m.sticker events have no msgtype; treat as image
		itemType = "image"
	default:
		return MediaItem{}, false
	}

	if msg.URL == "" {
		return MediaItem{}, false
	}
	origURI := msg.URL.ParseOrIgnore()
	if origURI.IsEmpty() {
		return MediaItem{}, false
	}

	originalURL := mxcToProxy(origURI, "original")

	// Use the event's thumbnail_url if present; otherwise the proxy will
	// generate a thumbnail from the original on first request.
	thumbURL := mxcToProxy(origURI, "thumb")
	if msg.Info != nil && msg.Info.ThumbnailURL != "" {
		thumbURI := msg.Info.ThumbnailURL.ParseOrIgnore()
		if !thumbURI.IsEmpty() {
			thumbURL = mxcToProxy(thumbURI, "thumb")
		}
	}

	var width, height int
	var mimeType string
	if msg.Info != nil {
		width = msg.Info.Width
		height = msg.Info.Height
		mimeType = msg.Info.MimeType
	}

	return MediaItem{
		EventID:      evt.ID.String(),
		Type:         itemType,
		ThumbnailURL: thumbURL,
		OriginalURL:  originalURL,
		Filename:     msg.GetFileName(),
		Width:        width,
		Height:       height,
		MimeType:     mimeType,
		Sender:       evt.Sender.String(),
		Timestamp:    evt.Timestamp,
	}, true
}

func mxcToProxy(uri id.ContentURI, kind string) string {
	return fmt.Sprintf("/media/%s/%s/%s", kind, uri.Homeserver, uri.FileID)
}
