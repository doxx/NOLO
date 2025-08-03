# AI Commentary Application

A standalone application that captures images directly from a PTZ camera and generates humorous commentary using OpenAI's GPT-4o model.

## Overview

This application embodies "Captain BlackEye", a pirate trapped inside a camera who provides sarcastic and entertaining commentary about what he sees in the Miami River area. The commentary is written to `/tmp/commentary.txt` for integration with streaming applications like FFmpeg.

## Features

- **Direct Camera Capture**: Uses Hikvision ISAPI with digest authentication to capture high-quality JPEG images
- **AI-Powered Commentary**: GPT-4o generates contextual, humorous commentary as Captain BlackEye
- **Configurable Intervals**: Adjustable commentary generation frequency
- **Conversation Memory**: Maintains conversation history for context-aware responses
- **FFmpeg Integration**: Outputs commentary text in a format suitable for video overlays

## Installation

1. Navigate to the ai_commentary directory:
```bash
cd ai_commentary
```

2. Build the application:
```bash
go build -o ai_commentary main.go
```

## Usage

### Basic Usage
```bash
./ai_commentary
```

### Custom Configuration
```bash
./ai_commentary -ip 192.168.1.100 -user admin -pass mypassword -interval 1m
```

### Command Line Options

| Option | Description | Default |
|--------|-------------|---------|
| `-ip <address>` | Camera IP address | 192.168.1.160 |
| `-user <username>` | Camera username | admin |
| `-pass <password>` | Camera password | Miami2024 |
| `-interval <time>` | Commentary generation interval | 30s |
| `-h, -help` | Show help message | - |

### Time Format Examples
- `30s` - 30 seconds
- `1m` - 1 minute  
- `2m30s` - 2 minutes 30 seconds
- `1h` - 1 hour

## Output

The application writes commentary to `/tmp/commentary.txt`. This file is:
- **Size-limited**: Maximum 1KB to prevent FFmpeg memory issues
- **Atomically updated**: Uses temporary files to prevent partial reads
- **Text-wrapped**: Lines wrapped to 80 characters for readability

## Character: Captain BlackEye

Captain BlackEye is a pirate trapped inside the camera who:
- Uses colorful pirate language
- Makes fun of people on boats
- Provides sarcastic observations about Miami River
- Knows about local landmarks (Billy the Pelican, Brickell Key, etc.)
- Responds like Robin Williams would describe boat activities
- Keeps responses to 1-2 sentences unless providing facts

## Technical Details

### Camera Integration
- Uses curl with digest authentication
- Captures JPEG images via Hikvision ISAPI endpoint
- Validates image data before processing
- Handles network timeouts and errors gracefully

### AI Integration  
- OpenAI GPT-4o vision model
- Maintains conversation history (last 10 exchanges)
- Base64 image encoding for API transmission
- Robust error handling and retry logic

### File Safety
- Atomic file writes prevent FFmpeg reading corruption
- Size limits prevent memory allocation failures
- Temporary file cleanup on errors

## Logging

The application provides detailed logging:
- Camera connection status
- Image capture success/failure
- OpenAI API interactions
- Commentary generation results
- File write operations

## Examples

### Starting with custom camera
```bash
./ai_commentary -ip 10.0.1.50 -user operator -pass secret123
```

### High frequency commentary (every 10 seconds)
```bash
./ai_commentary -interval 10s
```

### Different credentials
```bash
./ai_commentary -user mycamerauser -pass mycamerapass
```

## Integration with Streaming

The commentary file can be used with FFmpeg for video overlays:

```bash
ffmpeg -i input.mp4 -vf "drawtext=textfile=/tmp/commentary.txt:fontsize=24:fontcolor=white:x=10:y=10" output.mp4
```

## Error Handling

The application handles various error conditions:
- **Camera connection failures**: Retries and logs errors
- **OpenAI API errors**: Continues operation on next interval
- **File write errors**: Logs issues but continues running
- **Invalid images**: Validates JPEG headers and size

## Stopping the Application

Press `Ctrl+C` to gracefully stop the application. The current commentary will remain in `/tmp/commentary.txt`.

## Troubleshooting

### Camera Connection Issues
1. Verify camera IP address and credentials
2. Check network connectivity
3. Ensure camera supports ISAPI endpoints
4. Test with: `curl --digest -u user:pass http://CAMERA_IP/ISAPI/Streaming/channels/1/picture -o test.jpg`

### OpenAI API Issues
1. Check API key is valid
2. Verify internet connectivity
3. Monitor API rate limits
4. Check OpenAI service status

### File Permission Issues
1. Ensure write access to `/tmp/` directory
2. Check disk space availability
3. Verify no processes are locking the commentary file 