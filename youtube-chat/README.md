# NOLO YouTube Chat Controller

YouTube Live Chat bot that reads camera control commands from chat and sends them to the NOLO PTZ tracking system.

## Features

- Parses hashtag commands from YouTube Live Chat
- Rate limiting: 1 command per 10 seconds globally, 2 commands per user per 30 seconds
- Command queue to prevent rapid camera movements
- Automatic reconnection to chat

## Supported Commands

### Movement Commands
- `#up` - Tilt camera up
- `#down` - Tilt camera down
- `#left` - Pan camera left
- `#right` - Pan camera right

### Zoom Commands
- `#zoomin` - Zoom in one step
- `#zoomout` - Zoom out one step
- `#zoomfull` - Maximum zoom
- `#zoommid` - 50% zoom
- `#zoom1` through `#zoom10` - Set specific zoom level

### Preset Commands
- `#bridge1` - Bridge view preset 1
- `#bridge2` - Bridge view preset 2
- `#bridge3` - Bridge view preset 3
- `#river` - River view preset

### Control Commands
- `#auto` - Resume AI tracking
- `#pause` - Pause AI tracking for 30 seconds
- `#help` - Display available commands

## Rate Limiting

To prevent abuse and jerky camera movements:

- **Global limit**: Only 1 command can be executed every 10 seconds
- **Per-user limit**: Each user can send at most 2 commands per 30 seconds

Commands that exceed limits are silently dropped.

## Setup

### Prerequisites

1. OAuth credentials from the `youtube-reset` utility (same `client_secret.json` and `token.json`)
2. An active YouTube Live broadcast

### Building

```bash
cd youtube-chat
go build -o gchat .
```

### Testing (Simulation Mode)

```bash
./gchat -test
```

This runs a simulated test of command parsing and rate limiting without connecting to YouTube.

### Running

```bash
./gchat -credentials ../youtube-reset/client_secret.json -token ../youtube-reset/token.json
```

Or if credentials are in the same directory:

```bash
./gchat
```

## Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `-credentials` | `client_secret.json` | Path to OAuth credentials file |
| `-token` | `token.json` | Path to token file |
| `-test` | `false` | Run in test mode (simulate commands) |

## Future Integration

This bot is designed to integrate with NOLO's HTTP API (to be implemented). Currently it logs commands that would be sent. The API endpoint will be:

```
POST http://localhost:8080/api/ptz/{command}
```

Commands will be sent as HTTP requests to control the camera.

## Architecture

```
YouTube Live Chat
       |
       v
  [gchat.go]
       |
   (parses #commands)
       |
       v
  [Rate Limiter]
       |
   (10s global, 2/30s per user)
       |
       v
  [Command Queue]
       |
       v
  [NOLO HTTP API] (future)
       |
       v
  [PTZ Camera]
```
