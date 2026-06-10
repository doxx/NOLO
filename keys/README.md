# NOLO External Keys

This directory holds API keys, credentials, and secrets used by NOLO services.
All .txt files in this directory are gitignored and NEVER committed to the repo.

## Required Key Files

Create these files with your own values:

### camera-ip.txt
Camera IP address (e.g., `192.168.0.59`)

### camera-user.txt
Camera HTTP/RTSP username (e.g., `admin`)

### camera-pass.txt
Camera HTTP/RTSP password

### youtube-stream-key.txt
YouTube RTMP stream key from YouTube Studio > Stream > Stream Key

### openai-key.txt
OpenAI API key (`sk-proj-...`) for GPT-5.2 video descriptions.
Used by: recorder (hourly summaries), sunrise (timelapse descriptions), clipper

### ais-key.txt
AISStream.io API key for vessel tracking.
Get one at https://aisstream.io (free tier available)

## Usage

Services read these files at startup via flags or scripts. If a key file is missing, the feature is disabled gracefully.

## Security

- These files should be readable only by the service user (`chmod 600 keys/*.txt`)
- Never commit .txt key files to git
- The .gitignore in this directory ensures only this README is tracked
