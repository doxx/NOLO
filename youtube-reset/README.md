# YouTube Broadcast Reset Utility

Automates YouTube Live broadcast creation to avoid the manual "Go Live" step in YouTube Studio.

## The Problem

When the NOLO broadcast restarts after a failure, YouTube requires you to manually:
1. Go to YouTube Studio
2. Click "+ Create" → "Go Live"
3. Wait for the stream to connect

This utility automates that process using the YouTube Live Streaming API.

## Setup

### 1. Create Google Cloud Project

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create a new project or select existing
3. Enable the **YouTube Data API v3**
4. Go to **APIs & Services** → **Credentials**
5. Click **Create Credentials** → **OAuth client ID**
6. Select **Desktop app** as application type
7. Download the JSON file and save as `client_secret.json`

### 2. Configure OAuth Consent Screen

1. Go to **APIs & Services** → **OAuth consent screen**
2. Add your Google account as a test user (if in testing mode)
3. Add scopes:
   - `https://www.googleapis.com/auth/youtube`
   - `https://www.googleapis.com/auth/youtube.force-ssl`

### 3. Install and Build

```bash
cd youtube-reset
go mod tidy
go build -o youtube-reset .
```

### 4. First-Time Authorization

```bash
# This will open a browser for OAuth authorization
./youtube-reset -create-stream
```

Follow the prompts to authorize. A `token.json` will be saved for future use.

## Usage

### Create a Reusable Stream (One-Time)

```bash
./youtube-reset -create-stream
```

This creates a reusable RTMP stream and saves the stream ID to `config.json`.
Use the provided RTMP URL and stream key in your broadcast config.

### Reset Broadcast Before Streaming

```bash
./youtube-reset -reset
```

Run this before starting FFmpeg. It will:
1. End any active/live broadcasts
2. Create a new broadcast with `enableAutoStart: true`
3. Bind it to your reusable stream

When video starts flowing, YouTube auto-transitions to live.

### Check Status

```bash
./youtube-reset -status
```

Shows current stream and broadcast status.

### End Broadcast

```bash
./youtube-reset -end
```

Ends any active broadcasts.

### List Streams

```bash
./youtube-reset -list-streams
```

Shows all streams on your channel with their RTMP URLs.

## Integration with NOLO Broadcast

Update your broadcast systemd service or startup script:

```bash
#!/bin/bash
# Pre-broadcast reset
cd /home/blyon/NOLO/youtube-reset
./youtube-reset -reset

# Wait for YouTube to be ready
sleep 5

# Start broadcast
cd /home/blyon/NOLO/broadcast
./broadcast -c broadcast_config_nvidia_nodrawtext.json
```

Or modify the broadcast Go code to call the reset before starting FFmpeg.

## Configuration

Edit `config.json`:

```json
{
  "title": "Miami River Camera",
  "description": "AI-powered PTZ camera tracking",
  "privacy": "public",
  "stream_id": "YOUR_STREAM_ID"
}
```

## Files

- `client_secret.json` - OAuth credentials (download from Google Cloud Console)
- `token.json` - OAuth token (auto-generated after first auth)
- `config.json` - Broadcast settings and stream ID

## Troubleshooting

### "liveStreamingNotEnabled" Error

Your YouTube channel needs live streaming enabled:
1. Go to YouTube Studio
2. Settings → Channel → Feature eligibility
3. Enable live streaming (may require phone verification)

### "quotaExceeded" Error

YouTube API has daily quotas. The reset operation uses ~100 quota units.
Default quota is 10,000 units/day which allows ~100 resets.

### Token Expired

Delete `token.json` and re-run to re-authorize.
