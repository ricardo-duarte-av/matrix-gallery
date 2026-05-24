package main

import (
	"context"
	"fmt"
	"log"
	"sync"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type MediaItem struct {
	EventID         string         `json:"event_id"`
	Type            string         `json:"type"` // "image", "video", "file"
	ThumbnailURL    string         `json:"thumbnail_url"`
	OriginalURL     string         `json:"original_url"`
	Filename        string         `json:"filename"`
	Width           int            `json:"width,omitempty"`
	Height          int            `json:"height,omitempty"`
	MimeType        string         `json:"mime_type,omitempty"`
	Sender          string         `json:"sender,omitempty"`
	DisplayName     string         `json:"display_name,omitempty"`
	SenderAvatarURL string         `json:"sender_avatar_url,omitempty"`
	Timestamp       int64          `json:"timestamp,omitempty"`
	Reactions       map[string]int `json:"reactions,omitempty"`

	// Original MXC details for the thumbnail, used for pre-caching.
	ThumbServer  string `json:"-"`
	ThumbMediaID string `json:"-"`
}

type memberInfo struct {
	DisplayName string
	AvatarURL   string // proxy URL, empty if none
}

type RoomInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type MatrixFetcher struct {
	client      *mautrix.Client
	memberCache sync.Map // map[string]memberInfo
}

func newMatrixFetcher(cfg *Config) (*MatrixFetcher, error) {
	client, err := mautrix.NewClient(cfg.Homeserver, id.UserID(""), cfg.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("creating matrix client: %w", err)
	}
	return &MatrixFetcher{
		client: client,
	}, nil
}

// getMember returns cached or freshly fetched display name and avatar for a user.
func (f *MatrixFetcher) getMember(ctx context.Context, roomID id.RoomID, userID id.UserID) memberInfo {
	key := userID.String()
	if v, ok := f.memberCache.Load(key); ok {
		return v.(memberInfo)
	}

	var content event.MemberEventContent
	err := f.client.StateEvent(ctx, roomID, event.StateMember, key, &content)

	info := memberInfo{DisplayName: content.Displayname}
	if err == nil && content.AvatarURL != "" {
		uri := content.AvatarURL.ParseOrIgnore()
		if !uri.IsEmpty() {
			info.AvatarURL = mxcToProxy(uri, "thumb")
		}
	}

	f.memberCache.Store(key, info)
	return info
}

// FetchBatch retrieves up to limit message events going backward from fromToken.
// Pass an empty fromToken to start from the latest events in the room.
// Returns the media items found, a reactions map (target event ID -> emoji -> count),
// the next backward token, and the forward token (only on first call).
func (f *MatrixFetcher) FetchBatch(ctx context.Context, roomID id.RoomID, fromToken string, limit int) ([]MediaItem, map[string]map[string]int, string, string, error) {
	resp, err := f.client.Messages(ctx, roomID, fromToken, "", mautrix.DirectionBackward, nil, limit)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("fetching messages: %w", err)
	}

	var items []MediaItem
	reactions := make(map[string]map[string]int)
	for _, evt := range resp.Chunk {
		if evt.Type == event.EventReaction {
			if err := evt.Content.ParseRaw(evt.Type); err != nil {
				continue
			}
			r := evt.Content.AsReaction()
			rel := r.RelatesTo
			if rel.Type == event.RelAnnotation && rel.EventID != "" && rel.Key != "" {
				targetID := rel.EventID.String()
				if reactions[targetID] == nil {
					reactions[targetID] = make(map[string]int)
				}
				reactions[targetID][rel.Key]++
			}
			continue
		}
		if evt.Type != event.EventMessage && evt.Type != event.EventSticker {
			if evt.Type == event.EventEncrypted {
				log.Printf("[%s] Found encrypted event - gallery does not support E2EE yet", roomID)
			}
			continue
		}
		if err := evt.Content.ParseRaw(evt.Type); err != nil {
			continue
		}
		msg := evt.Content.AsMessage()
		
		if msg.MsgType == event.MsgImage || msg.MsgType == event.MsgVideo || msg.MsgType == event.MsgFile {
			log.Printf("DEBUG: Found media message msgtype=%q, url=%q, file=%v", msg.MsgType, msg.URL, msg.File != nil)
		}

		item, ok := extractMediaItem(evt, msg)
		if !ok {
			log.Printf("DEBUG: extractMediaItem returned false for evt ID %s", evt.ID)
			continue
		}
		m := f.getMember(ctx, roomID, evt.Sender)
		item.DisplayName = m.DisplayName
		item.SenderAvatarURL = m.AvatarURL
		items = append(items, item)
	}

	return items, reactions, resp.End, resp.Start, nil
}

// SyncOnce performs a single long-poll sync from the given token.
// The server holds the connection until new events arrive or timeout elapses.
func (f *MatrixFetcher) SyncOnce(ctx context.Context, since string, roomID id.RoomID) ([]MediaItem, map[string]map[string]int, string, error) {
	// 30 seconds is the Matrix-recommended long-poll timeout
	resp, err := f.client.SyncRequest(ctx, 30000, since, "", false, "")
	if err != nil {
		return nil, nil, "", err
	}

	var items []MediaItem
	reactions := make(map[string]map[string]int)
	room, ok := resp.Rooms.Join[roomID]
	if ok {
		for _, evt := range room.Timeline.Events {
			if evt.Type == event.EventReaction {
				if err := evt.Content.ParseRaw(evt.Type); err != nil {
					continue
				}
				r := evt.Content.AsReaction()
				rel := r.RelatesTo
				if rel.Type == event.RelAnnotation && rel.EventID != "" && rel.Key != "" {
					targetID := rel.EventID.String()
					if reactions[targetID] == nil {
						reactions[targetID] = make(map[string]int)
					}
					reactions[targetID][rel.Key]++
				}
				continue
			}
		}
		// Timeline events are chronological; we want newest-first for the store.
		for i := len(room.Timeline.Events) - 1; i >= 0; i-- {
			evt := room.Timeline.Events[i]
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
			m := f.getMember(ctx, roomID, evt.Sender)
			item.DisplayName = m.DisplayName
			item.SenderAvatarURL = m.AvatarURL
			items = append(items, item)
		}
	}

	return items, reactions, resp.NextBatch, nil
}

// GetNowToken returns the latest sync token.
func (f *MatrixFetcher) GetNowToken(ctx context.Context) (string, error) {
	resp, err := f.client.SyncRequest(ctx, 0, "", "", false, "")
	if err != nil {
		return "", err
	}
	return resp.NextBatch, nil
}

// GetJoinedRooms retrieves all rooms joined by the user and their display names.
func (f *MatrixFetcher) GetJoinedRooms(ctx context.Context) ([]RoomInfo, error) {
	resp, err := f.client.SyncRequest(ctx, 0, "", "", false, "")
	if err != nil {
		return nil, err
	}

	var rooms []RoomInfo
	for roomID, joinedRoom := range resp.Rooms.Join {
		name := string(roomID)
		isSpace := false
		
		// Determine name and filter out spaces
		for _, evt := range joinedRoom.State.Events {
			if evt.Type == event.StateRoomName {
				if err := evt.Content.ParseRaw(evt.Type); err == nil {
					name = evt.Content.AsRoomName().Name
				}
			} else if evt.Type == event.StateCreate {
				if err := evt.Content.ParseRaw(evt.Type); err == nil {
					if evt.Content.AsCreate().Type == event.RoomTypeSpace {
						isSpace = true
					}
				}
			}
		}
		
		if isSpace {
			continue
		}
		// If no name found, we could try to fetch it via separate state request if needed,
		// but sync usually provides it if it changed recently or if we use certain filters.
		// For now, let's keep it simple or try a fallback fetch.
		if name == string(roomID) {
			var content event.RoomNameEventContent
			err := f.client.StateEvent(ctx, roomID, event.StateRoomName, "", &content)
			if err == nil && content.Name != "" {
				name = content.Name
			}
		}

		rooms = append(rooms, RoomInfo{
			ID:          roomID.String(),
			DisplayName: name,
		})
	}
	return rooms, nil
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
	thumbServer := origURI.Homeserver
	thumbMediaID := origURI.FileID

	if msg.Info != nil && msg.Info.ThumbnailURL != "" {
		thumbURI := msg.Info.ThumbnailURL.ParseOrIgnore()
		if !thumbURI.IsEmpty() {
			thumbURL = mxcToProxy(thumbURI, "thumb")
			thumbServer = thumbURI.Homeserver
			thumbMediaID = thumbURI.FileID
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
		ThumbServer:  thumbServer,
		ThumbMediaID: thumbMediaID,
	}, true
}

func mxcToProxy(uri id.ContentURI, kind string) string {
	return fmt.Sprintf("/media/%s/%s/%s", kind, uri.Homeserver, uri.FileID)
}
