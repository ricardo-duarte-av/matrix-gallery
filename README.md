# Matrix Room Gallery

A lightning-fast, highly responsive web gallery for rendering and interacting with media from an entire Matrix room's history. It seamlessly creates a visual grid of all images and videos shared in a specific room.

## Features

- **Infinite Scrolling**: Scroll through tens of thousands of media items smoothly. Thumbnails are eagerly batch-loaded via an Intersection Observer tailored for zero-wait scrolling.
- **Proactive Thumbnail Pre-Caching**: The backend actively monitors your Matrix homserver and fetches/resizes thumbnails for new and historical items via 5 asynchronous parallel workers before your browser even requests them.
- **Real-time Synchronization**: Implements the official Matrix `/sync` API by maintaining long-polling background connections. New media shared in the room appears in the gallery instantly.
- **Deduplication & Sorting**: Robust backend and frontend handling guarantees media items never duplicate and are always displayed in precise descending chronological order.
- **Built-in Lightbox**: View full-size original images and videos natively without leaving the gallery.
- **Minimalist Architecture**: Highly optimized Go backend with a zero-dependency Vanilla JS/CSS frontend structure mapping to raw CSS grids.

## Prerequisites

- Go 1.25+
- Access to a Matrix homeserver
- A Matrix user access token (can be retrieved from Element via `Settings` → `Help & About` → `Access Token`)
- The internal ID of the room you want to read from (e.g., `!abc1234:matrix.example.com`)

## Installation

1. Clone or copy the project into a folder.
2. Build the application using Go:
   ```bash
   go build -o matrix-gallery .
   ```

## Configuration

Copy the sample configuration file to create your local setup:

```bash
cp sample.config.yaml config.yaml
```

Edit `config.yaml` with your Matrix details:
```yaml
# Matrix homeserver URL (no trailing slash)
homeserver: "https://matrix.example.com"

# Access token from your Matrix client
access_token: "your-access-token"

# Room ID to generate the gallery from
room_id: "!your-room-id:matrix.example.com"

# HTTP listen address and port
listen_address: "0.0.0.0"
listen_port: 63301

# Directory used to cache generated thumbnails
cache_dir: "./cache"
```

## Usage

Run the server:
```bash
./matrix-gallery
```

Then, open your browser and navigate to the address configured (e.g., `http://localhost:63301`). 

### How it works 

1. On startup, the backend establishes a connection to the Matrix server and requests the most recent event history of the room.
2. Any identified media objects (Photos, Videos, Stickers) have their metadata logged.
3. A background queuing system instantly starts pulling and locally caching the thumbnails of these events concurrently.
4. The backend then attaches to the Matrix `/sync` long-polling event listener. Any newly published media in the room is instantly pulled and prepended to the memory block, ready for the browser API.
5. In the browser, Vanilla JS dynamically injects cards using a responsive CSS grid, utilizing an `IntersectionObserver` to trigger batches before you hit the bottom of the page.

## License

MIT