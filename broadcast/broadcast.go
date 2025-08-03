package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type BroadcastMonitor struct {
	cmd             *exec.Cmd
	ctx             context.Context
	cancel          context.CancelFunc
	restartCount    int
	maxRestarts     int
	healthTimeout   time.Duration
	lastOutput      time.Time
	mutex           sync.RWMutex
	isRunning       bool
	startTime       time.Time
	timestampErrors int
	lastErrorTime   time.Time
	forceRestart    bool // Flag to prevent lastOutput updates during forced restart

	// Frame progression monitoring
	lastFrameNumber   int
	lastFrameUpdate   time.Time
	frameStallTimeout time.Duration
}

type Config struct {
	MaxRestarts          int      `json:"max_restarts"`
	HealthTimeoutSeconds int      `json:"health_timeout_seconds"`
	RestartDelaySeconds  int      `json:"restart_delay_seconds"`
	LogFile              string   `json:"log_file"`
	FFmpegArgs           []string `json:"ffmpeg_args"`

	// Recording configuration
	EnableLocalRecording bool   `json:"enable_local_recording"`
	RecordingPath        string `json:"recording_path"`
	SegmentDuration      int    `json:"segment_duration_seconds"` // 3600 for hourly
	MaxRecordingDays     int    `json:"max_recording_days"`       // Auto-delete after N days
	CreatePlaylist       bool   `json:"create_playlist"`          // Generate m3u8 playlist
}

// loadConfig loads configuration from broadcast_config.json
func loadConfig(filename string) (Config, error) {
	var config Config

	configFile, err := os.ReadFile(filename)
	if err != nil {
		return config, fmt.Errorf("failed to read config file %s: %v", filename, err)
	}

	if err := json.Unmarshal(configFile, &config); err != nil {
		return config, fmt.Errorf("failed to parse config file %s: %v", filename, err)
	}

	return config, nil
}

func NewBroadcastMonitor(config Config) *BroadcastMonitor {
	ctx, cancel := context.WithCancel(context.Background())

	fmt.Println("🚀 DEBUG: Creating new BroadcastMonitor instance")
	fmt.Printf("🔧 DEBUG: Unlimited restarts enabled, Health timeout: %ds\n",
		config.HealthTimeoutSeconds)

	return &BroadcastMonitor{
		ctx:               ctx,
		cancel:            cancel,
		maxRestarts:       config.MaxRestarts,
		healthTimeout:     time.Duration(config.HealthTimeoutSeconds) * time.Second,
		lastOutput:        time.Now(),
		frameStallTimeout: 11 * time.Second, // Restart if frame count doesn't increase for 11 seconds
		lastFrameUpdate:   time.Now(),       // Initialize to prevent false positive on startup
	}
}

func (bm *BroadcastMonitor) startFFmpeg(config Config) error {
	bm.mutex.Lock()
	defer bm.mutex.Unlock()

	if bm.isRunning {
		return fmt.Errorf("FFmpeg is already running")
	}

	// Reset timestamp error counter on new start
	bm.timestampErrors = 0
	bm.lastErrorTime = time.Time{}
	bm.forceRestart = false // Reset force restart flag

	// Reset frame progression tracking on new start
	bm.lastFrameNumber = 0
	bm.lastFrameUpdate = time.Now()

	bm.startTime = time.Now()
	fmt.Printf("\n🎬 DEBUG: Starting FFmpeg process (attempt %d) at %s\n",
		bm.restartCount+1, bm.startTime.Format("15:04:05"))

	// Use the configured ffmpeg args directly
	ffmpegArgs := config.FFmpegArgs
	fmt.Printf("🔄 DEBUG: Using configured FFmpeg arguments directly\n")

	// Show the full command being executed
	fmt.Printf("📝 DEBUG: FFmpeg command: ffmpeg %s\n", strings.Join(ffmpegArgs, " "))

	log.Printf("Starting FFmpeg process (attempt %d)", bm.restartCount+1)

	bm.cmd = exec.CommandContext(bm.ctx, "ffmpeg", ffmpegArgs...)

	// Create pipes for stdout and stderr
	stdout, err := bm.cmd.StdoutPipe()
	if err != nil {
		fmt.Printf("❌ DEBUG: Failed to create stdout pipe: %v\n", err)
		return fmt.Errorf("failed to create stdout pipe: %v", err)
	}

	stderr, err := bm.cmd.StderrPipe()
	if err != nil {
		fmt.Printf("❌ DEBUG: Failed to create stderr pipe: %v\n", err)
		return fmt.Errorf("failed to create stderr pipe: %v", err)
	}

	fmt.Println("🔗 DEBUG: Created stdout and stderr pipes successfully")

	// Start the process
	if err := bm.cmd.Start(); err != nil {
		fmt.Printf("❌ DEBUG: Failed to start FFmpeg process: %v\n", err)
		return fmt.Errorf("failed to start FFmpeg: %v", err)
	}

	fmt.Printf("✅ DEBUG: FFmpeg process started with PID: %d\n", bm.cmd.Process.Pid)
	bm.isRunning = true
	bm.lastOutput = time.Now()

	// Monitor stdout and stderr in separate goroutines
	go bm.monitorOutput(stdout, "STDOUT")
	go bm.monitorOutput(stderr, "STDERR")

	return nil
}

// isNonFatalWarning checks if a warning/error message is non-fatal and should not trigger alerts
func isNonFatalWarning(line string) bool {
	lowerLine := strings.ToLower(line)

	// Common non-fatal RTSP warnings
	if strings.Contains(lowerLine, "rtsp") && (strings.Contains(lowerLine, "max delay reached") ||
		strings.Contains(lowerLine, "missed") && strings.Contains(lowerLine, "packets")) {
		return true
	}

	// Common FLV format warnings (normal during streaming)
	if strings.Contains(lowerLine, "flv") && strings.Contains(lowerLine, "failed to update header") {
		return true
	}

	// Other common non-fatal warnings can be added here

	return false
}

func (bm *BroadcastMonitor) monitorOutput(pipe io.ReadCloser, source string) {
	defer pipe.Close()

	fmt.Printf("👁️  DEBUG: Starting output monitor for %s\n", source)

	frameRegex := regexp.MustCompile(`frame=\s*(\d+)`)

	// Pattern to detect DTS/PTS timestamp errors
	timestampErrorRegex := regexp.MustCompile(`(?i)((DTS|PTS)\s+\d+,\s+next:\d+.*invalid dropping|Non-monotonic DTS.*previous:.*current:.*changing to)`)

	lineCount := 0
	charCount := 0
	buffer := make([]byte, 1)
	currentLine := strings.Builder{}

	// Read character by character to capture carriage return updates
	for {
		n, err := pipe.Read(buffer)
		if err != nil {
			if err != io.EOF {
				fmt.Printf("❌ DEBUG: [%s] Read error: %v\n", source, err)
			}
			break
		}

		if n == 0 {
			continue
		}

		charCount++
		char := buffer[0]

		// Update health check for ANY character received (FFmpeg is alive)
		// But only if we're not forcing a restart
		bm.mutex.Lock()
		if !bm.forceRestart {
			bm.lastOutput = time.Now()
		}
		bm.mutex.Unlock()

		// Handle different line endings
		if char == '\n' || char == '\r' {
			if currentLine.Len() > 0 {
				line := currentLine.String()
				lineCount++

				// Process the line
				bm.processOutputLine(line, source, frameRegex, timestampErrorRegex, lineCount)

				// Reset buffer
				currentLine.Reset()
			}
		} else {
			currentLine.WriteByte(char)
		}

		// Periodically show we're receiving data
		if charCount%10000 == 0 {
			fmt.Printf("📊 DEBUG: [%s] Received %d characters, %d lines\n", source, charCount, lineCount)
		}
	}

	// Process any remaining line
	if currentLine.Len() > 0 {
		line := currentLine.String()
		bm.processOutputLine(line, source, frameRegex, timestampErrorRegex, lineCount+1)
	}

	fmt.Printf("👁️  DEBUG: Output monitor for %s finished. Total chars: %d, lines: %d\n", source, charCount, lineCount)
}

func (bm *BroadcastMonitor) processOutputLine(line, source string, frameRegex, timestampErrorRegex *regexp.Regexp, lineCount int) {
	// Output FFmpeg output to console
	fmt.Printf("[%s] %s\n", source, line)

	// Check for critical timestamp errors that require immediate restart
	if timestampErrorRegex.MatchString(line) {
		bm.mutex.Lock()
		now := time.Now()

		// Reset counter if it's been more than 30 seconds since last error
		if now.Sub(bm.lastErrorTime) > 30*time.Second {
			bm.timestampErrors = 0
		}

		bm.timestampErrors++
		bm.lastErrorTime = now

		fmt.Printf("CRITICAL TIMESTAMP ERROR #%d: %s\n", bm.timestampErrors, line)
		log.Printf("[%s CRITICAL] Timestamp error #%d: %s", source, bm.timestampErrors, line)

		// Trigger restart if we see 3 or more timestamp errors within 30 seconds
		if bm.timestampErrors >= 3 {
			fmt.Printf("TIMESTAMP ERROR THRESHOLD REACHED (%d errors), forcing restart!\n", bm.timestampErrors)
			log.Printf("[%s CRITICAL] Timestamp error threshold reached (%d errors), forcing restart", source, bm.timestampErrors)
			bm.mutex.Unlock()

			// Force an unhealthy state by setting lastOutput to long ago and preventing updates
			bm.mutex.Lock()
			bm.forceRestart = true
			bm.lastOutput = time.Now().Add(-bm.healthTimeout - time.Second)
			bm.timestampErrors = 0 // Reset counter
			bm.mutex.Unlock()

			return // Exit the monitor loop to trigger health check failure
		}
		bm.mutex.Unlock()
	}

	// Log important events and patterns, but filter out non-fatal warnings
	if (strings.Contains(strings.ToLower(line), "error") ||
		strings.Contains(strings.ToLower(line), "failed") ||
		strings.Contains(strings.ToLower(line), "warning")) &&
		!isNonFatalWarning(line) {
		log.Printf("[%s ERROR] %s", source, line)
	} else if frameRegex.MatchString(line) {
		// Extract frame number for health monitoring
		matches := frameRegex.FindStringSubmatch(line)
		if len(matches) > 1 {
			frame, _ := strconv.Atoi(matches[1])

			// Check for frame progression
			bm.mutex.Lock()
			if frame > bm.lastFrameNumber {
				// Frame progressed - update tracking
				bm.lastFrameNumber = frame
				bm.lastFrameUpdate = time.Now()
			} else if frame == bm.lastFrameNumber {
				// Frame stuck - check timeout
				timeSinceUpdate := time.Since(bm.lastFrameUpdate)
				if timeSinceUpdate > bm.frameStallTimeout {
					fmt.Printf("CRITICAL: Frame stuck at %d for %v - forcing restart!\n", frame, timeSinceUpdate)
					log.Printf("[%s CRITICAL] Frame stalled at %d for %v, forcing restart", source, frame, timeSinceUpdate)

					// Force an unhealthy state to trigger restart
					bm.forceRestart = true
					bm.lastOutput = time.Now().Add(-bm.healthTimeout - time.Second)
					bm.mutex.Unlock()
					return // Exit to trigger health check failure
				}
			}
			bm.mutex.Unlock()

			if frame > 0 && frame%900 == 0 { // Log every 30 seconds at 30fps
				uptime := time.Since(bm.startTime).Round(time.Second)
				log.Printf("[%s] Processing frame %d (uptime: %v)", source, frame, uptime)
			}
		}
	} else if strings.Contains(line, "fps=") {
		// Extract and display encoding stats
		log.Printf("[%s STATS] %s", source, line)
	}
}

func (bm *BroadcastMonitor) stopFFmpeg() error {
	bm.mutex.Lock()
	defer bm.mutex.Unlock()

	if !bm.isRunning || bm.cmd == nil {
		fmt.Println("🛑 DEBUG: FFmpeg is not running, nothing to stop")
		return nil
	}

	uptime := time.Since(bm.startTime).Round(time.Second)
	fmt.Printf("🛑 DEBUG: Stopping FFmpeg process (PID: %d, uptime: %v)\n", bm.cmd.Process.Pid, uptime)
	log.Printf("Stopping FFmpeg process (PID: %d, uptime: %v)", bm.cmd.Process.Pid, uptime)

	// Send SIGTERM first
	fmt.Println("📡 DEBUG: Sending SIGTERM to FFmpeg process")
	if err := bm.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		fmt.Printf("❌ DEBUG: Failed to send SIGTERM: %v\n", err)
		log.Printf("Failed to send SIGTERM: %v", err)
	}

	// Wait for graceful shutdown with timeout
	done := make(chan error, 1)
	go func() {
		done <- bm.cmd.Wait()
	}()

	fmt.Println("⏳ DEBUG: Waiting for graceful shutdown (3s timeout)")
	select {
	case <-time.After(3 * time.Second):
		fmt.Println("💀 DEBUG: FFmpeg did not stop gracefully, killing process...")
		log.Println("FFmpeg did not stop gracefully, killing process...")
		if err := bm.cmd.Process.Kill(); err != nil {
			fmt.Printf("❌ DEBUG: Failed to kill process: %v\n", err)
			log.Printf("Failed to kill process: %v", err)
		}
		<-done // Wait for process to actually end
		fmt.Println("💀 DEBUG: Process killed")
	case err := <-done:
		if err != nil {
			fmt.Printf("⚠️  DEBUG: FFmpeg stopped with error: %v\n", err)
			log.Printf("FFmpeg stopped with error: %v", err)
		} else {
			fmt.Println("✅ DEBUG: FFmpeg stopped gracefully")
			log.Println("FFmpeg stopped gracefully")
		}
	}

	bm.isRunning = false
	fmt.Println("🏁 DEBUG: FFmpeg process cleanup complete")
	return nil
}

func (bm *BroadcastMonitor) isHealthy() bool {
	bm.mutex.RLock()
	defer bm.mutex.RUnlock()

	if !bm.isRunning || bm.cmd == nil {
		fmt.Println("💔 DEBUG: Health check failed - process not running")
		return false
	}

	// Check if process is still alive
	if bm.cmd.Process != nil {
		if err := bm.cmd.Process.Signal(syscall.Signal(0)); err != nil {
			fmt.Printf("💔 DEBUG: Process health check failed (signal test): %v\n", err)
			log.Printf("Process health check failed: %v", err)
			return false
		}
	}

	// Check if we've received output recently
	timeSinceOutput := time.Since(bm.lastOutput)
	if timeSinceOutput > bm.healthTimeout {
		fmt.Printf("💔 DEBUG: No output received for %v (timeout: %v), considering unhealthy\n",
			timeSinceOutput, bm.healthTimeout)
		log.Printf("No output received for %v, considering unhealthy", timeSinceOutput)
		return false
	}

	// More frequent health status updates to show we're receiving data
	if int(timeSinceOutput.Seconds())%5 == 0 && timeSinceOutput.Seconds() >= 5 {
		errorInfo := ""
		if bm.timestampErrors > 0 {
			errorInfo = fmt.Sprintf(" (timestamp errors: %d)", bm.timestampErrors)
		}

		// Add frame progression info
		frameInfo := ""
		if bm.lastFrameNumber > 0 {
			timeSinceFrameUpdate := time.Since(bm.lastFrameUpdate)
			frameInfo = fmt.Sprintf(" (frame: %d, last update: %v ago)", bm.lastFrameNumber, timeSinceFrameUpdate.Round(time.Second))
		}

		fmt.Printf("💚 DEBUG: Health check OK - receiving FFmpeg output (last: %v ago, timeout: %v)%s%s\n",
			timeSinceOutput.Round(time.Second), bm.healthTimeout, errorInfo, frameInfo)
	}

	return true
}

func (bm *BroadcastMonitor) Run(config Config) error {
	// Setup signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("🎯 DEBUG: Setting up signal handlers for graceful shutdown")

	// Setup logging
	if config.LogFile != "" {
		logFile, err := os.OpenFile(config.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			fmt.Printf("❌ DEBUG: Failed to open log file %s: %v\n", config.LogFile, err)
			return fmt.Errorf("failed to open log file: %v", err)
		}
		defer logFile.Close()
		log.SetOutput(logFile)
		fmt.Printf("📝 DEBUG: Logging to file: %s\n", config.LogFile)
	}

	fmt.Println("🚀 DEBUG: Starting Broadcast Monitor main loop...")
	log.Println("Starting Broadcast Monitor...")

	restartDelay := time.Duration(config.RestartDelaySeconds) * time.Second
	healthCheckInterval := 5 * time.Second

	fmt.Printf("⚙️  DEBUG: Restart delay: %v, Health check interval: %v\n", restartDelay, healthCheckInterval)

	for {
		select {
		case <-bm.ctx.Done():
			fmt.Println("🛑 DEBUG: Context cancelled, shutting down...")
			log.Println("Context cancelled, shutting down...")
			bm.stopFFmpeg()
			return nil

		case sig := <-sigCh:
			fmt.Printf("📡 DEBUG: Received signal %v, shutting down gracefully...\n", sig)
			log.Printf("Received signal %v, shutting down gracefully...", sig)
			bm.cancel()
			bm.stopFFmpeg()
			return nil

		default:
			// Check if we need to start or restart FFmpeg
			if !bm.isRunning {
				// Unlimited restarts - no max restart check

				if bm.restartCount > 0 {
					fmt.Printf("⏰ DEBUG: Waiting %v before restart attempt...\n", restartDelay)
					time.Sleep(restartDelay)
				}

				fmt.Printf("🔄 DEBUG: Attempting to start FFmpeg (attempt %d)\n", bm.restartCount+1)
				if err := bm.startFFmpeg(config); err != nil {
					fmt.Printf("❌ DEBUG: Failed to start FFmpeg: %v\n", err)
					log.Printf("Failed to start FFmpeg: %v", err)
					bm.restartCount++
					continue
				}
				bm.restartCount++
			}

			// Health check
			time.Sleep(healthCheckInterval)
			if !bm.isHealthy() {
				fmt.Println("💔 DEBUG: FFmpeg appears unhealthy, initiating restart...")
				log.Println("FFmpeg appears unhealthy, restarting...")
				bm.stopFFmpeg()
			}
		}
	}
}

// processFFmpegArgs modifies FFmpeg arguments based on recording settings
func processFFmpegArgs(baseArgs []string, recordingEnabled bool, recordingPath string) []string {
	if !recordingEnabled {
		// Find and remove recording-related arguments
		var filteredArgs []string
		skipNext := false

		for i, arg := range baseArgs {
			if skipNext {
				skipNext = false
				continue
			}

			// Skip recording-related arguments
			if arg == "-c" && i+1 < len(baseArgs) && baseArgs[i+1] == "copy" {
				// Check if this is the recording copy codec (after the main encoding)
				// Look ahead to see if this leads to segment recording
				foundSegment := false
				for j := i + 2; j < len(baseArgs) && j < i+10; j++ {
					if baseArgs[j] == "-f" && j+1 < len(baseArgs) && baseArgs[j+1] == "segment" {
						foundSegment = true
						break
					}
				}
				if foundSegment {
					skipNext = true // Skip the "copy" that follows
					continue
				}
			}

			if arg == "-f" && i+1 < len(baseArgs) && baseArgs[i+1] == "segment" {
				// Skip all remaining args as they're for recording
				break
			}

			filteredArgs = append(filteredArgs, arg)
		}

		fmt.Printf("🚫 RECORDING DISABLED: Removed recording arguments from FFmpeg command\n")
		return filteredArgs
	} else {
		// Recording enabled - update recording path if provided
		if recordingPath != "" {
			modifiedArgs := make([]string, len(baseArgs))
			copy(modifiedArgs, baseArgs)

			// Update the recording path in the last argument (the output path)
			for i := len(modifiedArgs) - 1; i >= 0; i-- {
				if strings.Contains(modifiedArgs[i], "cam_%Y%m%d_%H%M%S.mp4") {
					modifiedArgs[i] = recordingPath + "/cam_%Y%m%d_%H%M%S.mp4"
					fmt.Printf("📁 RECORDING ENABLED: Updated path to %s\n", recordingPath)
					break
				}
			}
			return modifiedArgs
		}

		fmt.Printf("📹 RECORDING ENABLED: Using default recording path from config\n")
		return baseArgs
	}
}

func main() {
	// Parse command line flags
	var recordingPath string
	var configFile string
	flag.StringVar(&recordingPath, "record", "", "Enable recording to specified directory (e.g., -record ./recordings)")
	flag.StringVar(&configFile, "c", "broadcast_config.json", "Config file to use (e.g., -c broadcast_config_nvidia.json)")
	flag.Parse()

	recordingEnabled := recordingPath != ""

	fmt.Println("🎬 ========== BROADCAST MONITOR STARTING ==========")
	fmt.Printf("⏰ DEBUG: Monitor started at %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Printf("📋 DEBUG: Using config file: %s\n", configFile)

	if recordingEnabled {
		fmt.Printf("📹 DEBUG: Recording ENABLED to directory: %s\n", recordingPath)
	} else {
		fmt.Printf("🚫 DEBUG: Recording DISABLED (single encode path - YouTube only)\n")
	}

	// Load configuration from JSON file (required)
	fmt.Printf("📋 DEBUG: Loading configuration from %s...\n", configFile)
	config, err := loadConfig(configFile)
	if err != nil {
		fmt.Printf("❌ FATAL: %v\n", err)
		fmt.Printf("💡 Please ensure %s exists and is valid JSON\n", configFile)
		log.Fatalf("Failed to load configuration: %v", err)
	}

	fmt.Printf("✅ DEBUG: Configuration loaded successfully from %s\n", configFile)
	log.Printf("Loaded configuration from %s", configFile)

	// Validate critical configuration
	if len(config.FFmpegArgs) == 0 {
		fmt.Println("❌ FATAL: No FFmpeg arguments configured")
		log.Fatalf("No FFmpeg arguments configured in broadcast_config.json")
	}

	// Process FFmpeg arguments based on recording flag
	config.FFmpegArgs = processFFmpegArgs(config.FFmpegArgs, recordingEnabled, recordingPath)

	fmt.Printf("🔧 DEBUG: Config loaded - Max restarts: %d, Health timeout: %ds, Restart delay: %ds\n",
		config.MaxRestarts, config.HealthTimeoutSeconds, config.RestartDelaySeconds)

	if recordingEnabled {
		fmt.Printf("🔧 DEBUG: Recording enabled: %v, Path: %s\n", true, recordingPath)
	} else {
		fmt.Printf("🔧 DEBUG: Recording disabled - single encode path for performance testing\n")
	}

	monitor := NewBroadcastMonitor(config)

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	fmt.Println("🎯 DEBUG: Starting monitor.Run()...")
	if err := monitor.Run(config); err != nil {
		fmt.Printf("💥 DEBUG: Monitor failed with error: %v\n", err)
		log.Fatalf("Monitor failed: %v", err)
	}

	fmt.Println("🏁 DEBUG: Monitor shutdown complete")
}
