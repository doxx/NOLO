# NOLO TODO

## Active / In Progress

### Recorder Video Description Enrichment
- [ ] Recorder reads `events_*.json` files matching segment time window on upload
- [ ] Merge vessel sightings, weather, and tide data into video description
- [ ] Generate timecodes relative to segment start time
- [ ] Clean up event files after successful upload
- [ ] Add OpenAI summary generation (needs API key configured as runtime flag)
- [ ] Anonymized chat topic extraction for richer summaries (no usernames)

### YouTube API Quota
- [ ] Waiting for Google quota increase approval (requested 1M units/day)
- [ ] Record screencast demo for YouTube API compliance review
- [ ] Once approved: switch chat reading from scrape to official API for lower latency
- [ ] Once approved: enable `#commands` reply without quota concerns

### NOLO API Integration with Chat Bot
- [ ] Test chat camera control end-to-end with viewers (#up, #down, #bridge1, etc.)
- [ ] Tune override duration (currently 15s for moves, 30s for #stay)
- [ ] Verify overlay toggles work (#show.target, #show.pip, etc.)

## Planned

### Direct-to-YouTube FFmpeg (Bypass SRS + Broadcast)
- [ ] Add `-youtube-direct` flag to NOLO's setupFFmpeg()
- [ ] Add camera audio RTSP input with safety flags (timeout, reconnect)
- [ ] Add background music input (track.aac with stream_loop)
- [ ] Add audio filter_complex for volume mixing
- [ ] Change output from SRS to YouTube RTMP URL
- [ ] Test audio RTSP hang resilience
- [ ] This eliminates SRS + broadcast services, reduces latency by ~15-20 seconds

### YOLO Model Upgrade
- [ ] Evaluate YOLOv8/v10 for improved boat detection accuracy
- [ ] Test with captured frames from `/tmp/nolo-analysis/frames/`
- [ ] Compare detection confidence and size thresholds vs YOLOv3-tiny
- [ ] Custom training on Miami River boat dataset (extract from recordings)

### AIS Vessel Data Improvements
- [ ] Cross-reference AIS vessel type codes for richer announcements (tug, cargo, passenger, etc.)
- [ ] Track vessel direction over time for better approach predictions
- [ ] Announce bridge openings based on large vessel approach patterns
- [ ] Historical vessel traffic stats per hour/day

### Chat Bot Enhancements
- [ ] Add `#sunrise` / `#sunset` commands with actual Miami times
- [ ] Add `#subscribe` reminder message (configurable interval)
- [ ] Moderator commands with elevated rate limits
- [ ] Per-user command tracking and abuse detection
- [ ] Auto-respond to common questions ("what bridge is this?", "where is this?")

## Completed

### v1.0.0 (Feb 14, 2026)
- [x] YouTube chat bot (gchat) with command parsing and rate limiting
- [x] Chat reading via internal YouTube scrape endpoint (zero API quota for reads)
- [x] Chat replies via official YouTube API (liveChatMessages.insert)
- [x] PTZ camera control commands (#up, #down, #left, #right, #zoomin, #zoomout, etc.)
- [x] Preset positions from scanning.json (#bridge1, #bridge2, #bridge3, #river)
- [x] Manual override system in NOLO (15s per command, 30s for #stay/#linger)
- [x] Overlay toggles via API (#show.target, #show.pip, #show.console)
- [x] NOLO HTTP API on 127.0.0.1:8080 (status, PTZ, overlays)
- [x] NWS weather API integration (temp, humidity, wind, conditions)
- [x] NOAA tide API integration (water level, high/low predictions)
- [x] AIS vessel tracking via AISStream.io websocket (0.3nm radius)
- [x] Real-time vessel approach announcements with direction
- [x] Vessel name title case formatting (JEAN RUTH -> Jean Ruth)
- [x] Event logging to JSON files for video description enrichment
- [x] Weather/tide announcements every 4 hours
- [x] 8-hour MP4 segment recording from SRS
- [x] Auto-upload recordings to YouTube with generated titles
- [x] youtube-reset broadcast reuse (preserves URL across restarts)
- [x] EnableAutoStop=false for persistent broadcasts
- [x] Ultra-low latency setting for future broadcasts
- [x] Systemd services for all components (srs, nolo, nolo-broadcast, nolo-chat, nolo-recorder)
- [x] GitHub Actions release workflow (cross-platform binaries)
- [x] Tracking fix: scanning hysteresis (3s delay before switching to scan mode)
- [x] Tracking fix: less aggressive recovery (20% zoom-out instead of 50%)
- [x] Tracking fix: clamp release after 3 consecutive limit hits
- [x] Post-lock holdover extended to 15 seconds

## Architecture

```
Camera (RTSP) -> NOLO (FFmpeg + YOLO + PTZ) -> SRS (RTMP relay) -> Broadcast (FFmpeg) -> YouTube
                    |                                                    |
                    +-- HTTP API :8080                                   +-- YouTube RTMP
                    |
                    +-- gchat (YouTube chat + AIS + weather + tides)
                    |     |
                    |     +-- Scrape chat (zero quota)
                    |     +-- Official API (replies only)
                    |     +-- AISStream.io websocket
                    |     +-- NWS + NOAA APIs
                    |     +-- Event logging to JSON
                    |
                    +-- recorder (8hr MP4 segments -> YouTube upload)
```

## Production Server

- Host: ops1.mia2.doxx.net
- Services: srs, nolo, nolo-broadcast, nolo-chat, nolo-recorder
- Camera: Hikvision PTZ at 192.168.0.59
- Location: 200 Biscayne Blvd Way, Miami FL 33131 (Brickell Bridge)
- Live stream: https://www.youtube.com/@MiamiRiverCam
