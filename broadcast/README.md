# Broadcast Monitor

A Go-based FFmpeg process monitor that ensures your streaming encoding stays healthy and automatically restarts on failures.

## Features

- **Process Health Monitoring**: Tracks FFmpeg output and process status
- **Automatic Restart**: Restarts encoding when process crashes or becomes unresponsive
- **Configurable Timeouts**: Customizable health check intervals and restart delays
- **Detailed Logging**: Comprehensive logging of process status and errors
- **Graceful Shutdown**: Handles SIGINT/SIGTERM signals properly
- **Resource Management**: Properly manages pipes and process resources

## Usage

### Basic Usage

1. Build the monitor:
   ```bash
   cd broadcast
   go build -o broadcast-monitor
   ```

2. Run the monitor:
   ```bash
   ./broadcast-monitor
   ```

### Configuration

The monitor uses built-in default configuration, but you can customize it by modifying the `defaultConfig` in `main.go` or by loading from a JSON file.

Configuration options:
- `max_restarts`: Maximum number of restart attempts (default: 100)
- `health_timeout_seconds`: Seconds without output before considering unhealthy (default: 30)
- `restart_delay_seconds`: Delay between restart attempts (default: 5)
- `log_file`: Path to log file (default: "broadcast.log")
- `ffmpeg_args`: FFmpeg command arguments

### Health Monitoring

The monitor considers FFmpeg unhealthy if:
- The process crashes or exits unexpectedly
- No output is received within the health timeout period
- Process signals fail (process is unresponsive)

### Logging

The monitor logs:
- Process start/stop events
- Frame processing milestones (every 30 seconds at 30fps)
- Encoding statistics
- Error messages and restart attempts
- Health check failures

### Stopping the Monitor

Send SIGINT (Ctrl+C) or SIGTERM to gracefully stop the monitor. It will:
1. Stop the FFmpeg process with SIGTERM
2. Wait up to 10 seconds for graceful shutdown
3. Force kill if necessary
4. Clean up resources

## Differences from Original Script

The original `pub.sh` used an infinite loop with basic restart logic. This Go monitor provides:

- **Better Error Handling**: Distinguishes between different types of failures
- **Health Monitoring**: Proactive detection of hanging processes
- **Resource Management**: Proper cleanup of processes and file handles
- **Structured Logging**: Better visibility into system behavior
- **Signal Handling**: Graceful shutdown capabilities
- **Configurable Parameters**: Easy tuning of restart behavior

## FFmpeg Command

The monitor runs the following FFmpeg command (extracted from your original script):

```bash
ffmpeg -re \
  -i "rtmp://192.168.0.12/live/stream" \
  -i "rtsp://admin:password1@192.168.0.59:554/Streaming/Channels/201" \
  -map 0:v:0 -map 1:a:0 -shortest \
  -fflags +genpts+discardcorrupt \
  -r 30 -g 60 -keyint_min 60 -sc_threshold 0 \
  -c:v libx264 -preset veryfast -tune zerolatency -profile:v high -level 4.0 \
  -b:v 16000k -maxrate 16000k -bufsize 9000k -pix_fmt yuv420p \
  -c:a aac -b:a 128k -af "volume=0.1" \
  -f flv "rtmp://a.rtmp.youtube.com/live2/hv9f-xv8d-8vq3-fg7f-3gub"
```

## Troubleshooting

- **High restart frequency**: Check network connectivity to input streams
- **Process not starting**: Verify FFmpeg is installed and accessible in PATH
- **Permission errors**: Ensure write access to log file location
- **Resource exhaustion**: Monitor system resources if restarts become frequent 