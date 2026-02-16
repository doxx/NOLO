# NOLO TODO

## Active / In Progress

### Recorder Video Description Enrichment
- [ ] Recorder reads `events_*.json` files matching segment time window on upload
- [ ] Merge vessel sightings, weather, and tide data into video description
- [ ] Generate timecodes relative to segment start time
- [ ] Clean up event files after successful upload
- [ ] Add OpenAI summary generation (needs API key configured as runtime flag)
- [ ] Anonymized chat topic extraction for richer summaries (no usernames)

### YouTube API Quota & Multi-Project Upload (COMPLETED Feb 15, 2026)
- [x] Created 4 new Google Cloud projects: rivercam-pub1 through rivercam-pub4
- [x] Enabled YouTube Data API v3, created OAuth credentials, authorized all against channel
- [x] Recorder v2: round-robin rotation across 5 projects (pub0-pub4)
- [x] Switched to 1-hour segments (24/day): ~6.5GB each, 31 uploads/day capacity
- [x] 16MB chunked resumable uploads with progress logging
- [x] Upload-in-progress tracking with /ready endpoint (returns 503 during upload)
- [x] Graceful shutdown waits up to 60s for in-progress upload
- [x] Leftover detection on restart uploads orphaned segments
- [x] 48-hour rolling retention of raw recordings, auto-cleanup of old files
- [x] Retry with project rotation (failed upload retries on different project)
- [ ] Still waiting for Google quota increase approval (requested 1M units/day)
- [ ] Once approved: switch chat reading from scrape to official API for lower latency
- [ ] Once approved: enable `#commands` reply without quota concerns

### NOLO API Integration with Chat Bot
- [ ] Test chat camera control end-to-end with viewers (#up, #down, #bridge1, etc.)
- [ ] Tune override duration (currently 15s for moves, 30s for #stay)
- [ ] Verify overlay toggles work (#show.target, #show.pip, etc.)

## Planned

### Camera Upgrade: Hikvision DS-2DF8C842IXG-ELW (8MP/4K, 42× zoom)
- [ ] Best value upgrade: $2,678, same Hikvision ISAPI compatibility
- [ ] 8MP (3840x2160) vs current 4MP (2688x1520) — sharper image, better YOLO detection at distance
- [ ] 42× optical zoom vs current 12× — 3.5x more zoom range for boat tracking
- [ ] 1/1.2" sensor vs current 1/2.8" — dramatically better night/low-light performance
- [ ] 500m IR vs current 50m — 10x nighttime range
- [ ] DarkFighter + AcuSense AI built-in
- [ ] IP67/IK10 weatherproof
- [ ] Server can handle it: GPU at 15%, CPU at 15%, 14 cores idle, YOLO input is always 640x640
- [ ] NOLO changes needed: update frame size, PTZ zoom limits (10-420?), re-calibrate pixels-per-unit
- [ ] Also considered DS-2DF8836I5X-AELW ($5,738) — laser IR, 2/3" sensor, 36× zoom. Not worth 2x price for marginal gains. Laser IR not needed for river monitoring.
- [ ] Add timestamp overlay to stream (bottom-left, "Feb 15, 2026 8:30 AM" format)

### Sunrise Time-Lapse Auto-Publisher
- [ ] Standalone Go program (`sunrise/main.go`) that runs daily
- [ ] Fetch exact sunrise time from api.sunrise-sunset.org (free, no key)
- [ ] After the segment MP4 covering sunrise is written, extract 40-min window (sunrise -20min to +20min)
- [ ] FFmpeg extract: `ffmpeg -ss [offset] -t 2400 -i segment.mp4 -c copy sunrise_raw.mp4`
- [ ] FFmpeg timelapse: 20x speed (`setpts=PTS/20`), 30fps output, GPU encode
- [ ] Add background music (track.aac) with fade in/out (3s each)
- [ ] Optional: slight saturation boost (`eq=saturation=1.2`) for sunrise colors
- [ ] Output: 2-minute clip at 20Mbps (~300MB)
- [ ] Upload to YouTube: "Miami River Sunrise - [date]"
- [ ] Uses separate pub credentials (rivercam-pub1/pub2) for quota
- [ ] Run via cron or systemd timer after segment completion (~8 AM)
- [ ] Delete raw extract after upload, keep timelapse for 7 days

### Direct-to-YouTube FFmpeg (Bypass SRS + Broadcast)
- [ ] Add `-youtube-direct` flag to NOLO's setupFFmpeg()
- [ ] Add camera audio RTSP input with safety flags (timeout, reconnect)
- [ ] Add background music input (track.aac with stream_loop)
- [ ] Add audio filter_complex for volume mixing
- [ ] Change output from SRS to YouTube RTMP URL
- [ ] Test audio RTSP hang resilience
- [ ] This eliminates SRS + broadcast services, reduces latency by ~15-20 seconds

### Seawall False Positive (Critical)
- [ ] v8n confidently detects the concrete seawall/riverwalk as a "boat" every frame
- [ ] Once locked, people sitting on benches/walkway near the seawall keep the detection alive
- [ ] Garbage cans, benches, and park objects also get detected as boats when zoomed in
- [ ] Frame edge filter helps at scan zoom but fails once camera zooms in (seawall no longer touches edges)
- [ ] P1 staleness timeout doesn't help — seawall gets ongoing P1 "boat" detections from v8n
- [ ] Stationary + no people check at lock time helps but doesn't maintain for already-locked boats
- [ ] Possible fixes to explore:
  - PTZ exclusion zones (known seawall Pan/Tilt coordinates → reject detections)
  - Require minimum PTZ-space displacement over 5s to maintain lock (seawall never moves in world space)
  - Machine-learning based: train a small classifier on seawall vs real boat crops
  - Fine-tune YOLOv8n on Miami River data to not detect seawall
  - Boat aspect ratio filter (seawall has unusual width:height ratio vs real boats)
  - Require people inside box within 10 seconds to maintain lock (real boats have visible crew)

### Post-Lock Linger Too Long
- [ ] After losing a boat, camera lingers at the last position for too long before scanning
- [ ] Post-lock holdover is 15 seconds — should maybe be 8-10s
- [ ] During holdover the camera shows empty water which is boring for viewers

### Zoom Inheritance on Re-acquisition
- [ ] When a boat is lost and re-detected 1-2s later in the same area, zoom resets to Z18
- [ ] Should carry over zoom level from the previous tracking session
- [ ] If a new boat appears within ~200px of where we just lost one, inherit the old zoom
- [ ] This would eliminate the 1-2 second zoom lag on re-acquisition

### YOLOv8n Completed (Feb 14, 2026)
- [x] Upgraded from YOLOv3-tiny to YOLOv8n (92% confidence vs 44%, 193 FPS vs 128 FPS)
- [x] Optimized v8 parser: bulk DataPtrFloat32 eliminates 705K CGO calls/frame
- [x] Properly exported ONNX with fixed shapes, opset 12, FP32
- [x] Runtime model selection via -yolo-model flag (v3-tiny fallback)
- [x] People-validates-boat: boats with people get instant lock, priority boost
- [x] Progressive zoom by people count (1=Z70, 2=Z85, 3+=Z100)
- [x] Velocity cap at 150 px/s (filters camera-movement artifacts)
- [x] P2 centroid tracking disabled (v8n detects people too well for this)
- [x] P2 lock maintenance removed (prevented seawall→people chain)
- [x] Frame edge filter (rejects detections touching 2+ frame edges)
- [x] P1 staleness unlock after 5s without boat-class detection
- [x] Instant zoom targeting (removed progressive stages and rate limiter)
- [x] Confidence thresholds raised for v8n: P1=0.45, P2=0.35
- [x] Scanning positions bumped to Z18 minimum

### YOLO Model Further Improvements
- [ ] Custom training on Miami River boat dataset (extract from recordings)
- [ ] Fine-tune to reject seawall/riverwalk/bridge structures
- [ ] Evaluate YOLOv8s (small) for even better accuracy with GPU headroom available
- [ ] Evaluate maritime-specific YOLO models (Roboflow/HuggingFace)

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
