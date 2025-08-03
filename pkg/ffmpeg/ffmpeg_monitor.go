package ffmpeg

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OutputBuffer stores recent output lines for crash dump analysis
type OutputBuffer struct {
	lines    []string
	maxLines int
	index    int
	full     bool
	mutex    sync.RWMutex
}

// NewOutputBuffer creates a circular buffer for storing recent output
func NewOutputBuffer(maxLines int) *OutputBuffer {
	return &OutputBuffer{
		lines:    make([]string, maxLines),
		maxLines: maxLines,
		index:    0,
		full:     false,
	}
}

// Add stores a new line in the circular buffer
func (ob *OutputBuffer) Add(line string) {
	ob.mutex.Lock()
	defer ob.mutex.Unlock()

	ob.lines[ob.index] = fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05.000"), line)
	ob.index = (ob.index + 1) % ob.maxLines
	if ob.index == 0 {
		ob.full = true
	}
}

// GetRecent returns the most recent lines (oldest first)
func (ob *OutputBuffer) GetRecent() []string {
	ob.mutex.RLock()
	defer ob.mutex.RUnlock()

	if !ob.full && ob.index == 0 {
		return []string{} // No lines yet
	}

	var result []string
	if ob.full {
		// Buffer is full, start from current index (oldest)
		for i := 0; i < ob.maxLines; i++ {
			idx := (ob.index + i) % ob.maxLines
			if ob.lines[idx] != "" {
				result = append(result, ob.lines[idx])
			}
		}
	} else {
		// Buffer not full, start from beginning
		for i := 0; i < ob.index; i++ {
			if ob.lines[i] != "" {
				result = append(result, ob.lines[i])
			}
		}
	}
	return result
}

// FFmpegHealthMonitor provides comprehensive health monitoring for FFmpeg processes
type FFmpegHealthMonitor struct {
	// Core health tracking
	cmd             *exec.Cmd
	isRunning       bool
	lastOutput      time.Time
	lastFrameNumber int
	lastFrameUpdate time.Time
	healthTimeout   time.Duration
	frameTimeout    time.Duration // 200 frames timeout

	// Error tracking
	timestampErrors int
	lastErrorTime   time.Time
	forceUnhealthy  bool

	// Output buffers for crash dump
	stderrBuffer *OutputBuffer
	stdoutBuffer *OutputBuffer

	// Callbacks
	onUnhealthy func(reason string)

	// Synchronization
	mutex sync.RWMutex

	// Monitoring goroutines
	stderrPipe       io.ReadCloser
	stdoutPipe       io.ReadCloser
	monitoringActive bool
}

// NewFFmpegHealthMonitor creates a new health monitor instance
func NewFFmpegHealthMonitor() *FFmpegHealthMonitor {
	return &FFmpegHealthMonitor{
		healthTimeout:    30 * time.Second,       // 30 seconds for general output
		frameTimeout:     200 * time.Second / 30, // 200 frames at 30fps = ~6.7 seconds
		timestampErrors:  0,
		monitoringActive: false,
		stderrBuffer:     NewOutputBuffer(100), // Keep last 100 stderr lines
		stdoutBuffer:     NewOutputBuffer(100), // Keep last 100 stdout lines
	}
}

// Start begins monitoring the FFmpeg process
func (fhm *FFmpegHealthMonitor) Start(cmd *exec.Cmd, onUnhealthy func(reason string)) error {
	fhm.mutex.Lock()
	defer fhm.mutex.Unlock()

	fhm.cmd = cmd
	fhm.onUnhealthy = onUnhealthy
	fhm.isRunning = true
	fhm.lastOutput = time.Now()
	fhm.lastFrameUpdate = time.Now()
	fhm.lastFrameNumber = 0
	fhm.timestampErrors = 0
	fhm.forceUnhealthy = false

	fmt.Printf("[FFMPEG_MONITOR] Setting up health monitoring (process will start after pipe setup)\n")

	// Create pipes for stderr and stdout monitoring
	var err error
	fhm.stderrPipe, err = cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %v", err)
	}

	fhm.stdoutPipe, err = cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %v", err)
	}

	fmt.Printf("[FFMPEG_MONITOR] Health monitor pipes created successfully\n")

	return nil
}

// StartMonitoring begins the actual monitoring after the process has started
func (fhm *FFmpegHealthMonitor) StartMonitoring() {
	fhm.mutex.Lock()
	defer fhm.mutex.Unlock()

	if fhm.cmd != nil && fhm.cmd.Process != nil {
		fmt.Printf("[FFMPEG_MONITOR] Starting health monitoring for PID: %d\n", fhm.cmd.Process.Pid)
	}

	// Start monitoring goroutines
	fhm.monitoringActive = true
	go fhm.monitorOutput(fhm.stderrPipe, "STDERR", fhm.stderrBuffer)
	go fhm.monitorOutput(fhm.stdoutPipe, "STDOUT", fhm.stdoutBuffer)
	go fhm.healthCheckLoop()
}

// isHealthy checks if FFmpeg is currently healthy (keeping broadcast.go interface)
func (fhm *FFmpegHealthMonitor) isHealthy() bool {
	fhm.mutex.RLock()
	defer fhm.mutex.RUnlock()

	if !fhm.isRunning || fhm.forceUnhealthy {
		return false
	}

	// Check general output timeout
	if time.Since(fhm.lastOutput) > fhm.healthTimeout {
		return false
	}

	// Check frame progress timeout (specific to encoding)
	if time.Since(fhm.lastFrameUpdate) > fhm.frameTimeout {
		return false
	}

	return true
}

// Stop stops the health monitoring
func (fhm *FFmpegHealthMonitor) Stop() {
	fhm.mutex.Lock()
	defer fhm.mutex.Unlock()

	fmt.Printf("[FFMPEG_MONITOR] Stopping health monitoring\n")
	fhm.monitoringActive = false
	fhm.isRunning = false
}

// DumpCrashInfo dumps recent stdout/stderr for crash analysis
func (fhm *FFmpegHealthMonitor) DumpCrashInfo() {
	fmt.Printf("\n" + strings.Repeat("=", 80) + "\n")
	fmt.Printf("💥 FFMPEG CRASH DUMP - RECENT OUTPUT\n")
	fmt.Printf(strings.Repeat("=", 80) + "\n")

	fmt.Printf("\n📋 RECENT STDERR (last %d lines):\n", len(fhm.stderrBuffer.GetRecent()))
	fmt.Printf(strings.Repeat("-", 50) + "\n")
	stderrLines := fhm.stderrBuffer.GetRecent()
	if len(stderrLines) == 0 {
		fmt.Printf("(no stderr output captured)\n")
	} else {
		for _, line := range stderrLines {
			fmt.Printf("%s\n", line)
		}
	}

	fmt.Printf("\n📋 RECENT STDOUT (last %d lines):\n", len(fhm.stdoutBuffer.GetRecent()))
	fmt.Printf(strings.Repeat("-", 50) + "\n")
	stdoutLines := fhm.stdoutBuffer.GetRecent()
	if len(stdoutLines) == 0 {
		fmt.Printf("(no stdout output captured)\n")
	} else {
		for _, line := range stdoutLines {
			fmt.Printf("%s\n", line)
		}
	}

	fmt.Printf("\n" + strings.Repeat("=", 80) + "\n")
	fmt.Printf("End of crash dump\n")
	fmt.Printf(strings.Repeat("=", 80) + "\n\n")
}

// monitorOutput monitors FFmpeg stdout/stderr with real-time console output and crash buffer
func (fhm *FFmpegHealthMonitor) monitorOutput(pipe io.ReadCloser, source string, buffer *OutputBuffer) {
	defer pipe.Close()

	fmt.Printf("[FFMPEG_MONITOR] 👁️ Starting output monitor for %s\n", source)

	// Regex patterns for parsing FFmpeg output
	frameRegex := regexp.MustCompile(`frame=\s*(\d+)`)
	timestampErrorRegex := regexp.MustCompile(`(?i)((DTS|PTS)\s+\d+,\s+next:\d+.*invalid dropping|Non-monotonic DTS.*previous:.*current:.*changing to)`)

	scanner := bufio.NewScanner(pipe)
	// Increase buffer size to handle long FFmpeg lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	lineCount := 0

	for scanner.Scan() && fhm.monitoringActive {
		line := scanner.Text()
		lineCount++

		// Store line in crash dump buffer
		buffer.Add(line)

		// Update health check for ANY output received
		fhm.mutex.Lock()
		if !fhm.forceUnhealthy {
			fhm.lastOutput = time.Now()
		}
		fhm.mutex.Unlock()

		// Process the line for health indicators
		fhm.processOutputLine(line, source, frameRegex, timestampErrorRegex)

		// 🎯 REAL-TIME CONSOLE OUTPUT: Show ALL lines with proper formatting
		fmt.Printf("[FFMPEG_%s] %s\n", source, line)
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("[FFMPEG_MONITOR] ❌ Scanner error for %s: %v\n", source, err)
		// Store error in buffer too
		buffer.Add(fmt.Sprintf("SCANNER_ERROR: %v", err))
	}

	fmt.Printf("[FFMPEG_MONITOR] 👁️ Output monitor for %s finished (%d lines)\n", source, lineCount)
}

// processOutputLine analyzes FFmpeg output for health indicators
func (fhm *FFmpegHealthMonitor) processOutputLine(line, source string, frameRegex, timestampErrorRegex *regexp.Regexp) {
	// Check for critical timestamp errors
	if timestampErrorRegex.MatchString(line) {
		fhm.mutex.Lock()
		now := time.Now()

		// Reset counter if it's been more than 30 seconds since last error
		if now.Sub(fhm.lastErrorTime) > 30*time.Second {
			fhm.timestampErrors = 0
		}

		fhm.timestampErrors++
		fhm.lastErrorTime = now

		fmt.Printf("[FFMPEG_MONITOR] 🚨 CRITICAL TIMESTAMP ERROR #%d: %s\n", fhm.timestampErrors, line)
		log.Printf("[%s CRITICAL] Timestamp error #%d: %s", source, fhm.timestampErrors, line)

		// Trigger unhealthy state if we see 3+ timestamp errors within 30 seconds
		if fhm.timestampErrors >= 3 {
			fmt.Printf("[FFMPEG_MONITOR] 🚨 TIMESTAMP ERROR THRESHOLD REACHED (%d errors), marking unhealthy!\n", fhm.timestampErrors)
			fhm.forceUnhealthy = true
			fhm.lastOutput = time.Now().Add(-fhm.healthTimeout - time.Second)
			fhm.timestampErrors = 0
			fhm.mutex.Unlock()
			return
		}
		fhm.mutex.Unlock()
	}

	// Check for frame progress updates
	if matches := frameRegex.FindStringSubmatch(line); len(matches) > 1 {
		if frameNum, err := strconv.Atoi(matches[1]); err == nil {
			fhm.mutex.Lock()
			if frameNum > fhm.lastFrameNumber {
				fhm.lastFrameNumber = frameNum
				fhm.lastFrameUpdate = time.Now()
			}
			fhm.mutex.Unlock()
		}
	}
}

// healthCheckLoop continuously monitors FFmpeg health status
func (fhm *FFmpegHealthMonitor) healthCheckLoop() {
	fmt.Printf("[FFMPEG_MONITOR] ⚙️ Starting health check loop (interval: 5s)\n")

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !fhm.monitoringActive {
				fmt.Printf("[FFMPEG_MONITOR] ⚙️ Health check loop stopping\n")
				return
			}

			if !fhm.isHealthy() {
				reason := fhm.getUnhealthyReason()
				fmt.Printf("[FFMPEG_MONITOR] 🚨 FFmpeg became unhealthy: %s\n", reason)

				// Dump crash info before triggering unhealthy callback
				fhm.DumpCrashInfo()

				if fhm.onUnhealthy != nil {
					fhm.onUnhealthy(reason)
				}
				return
			}
		}
	}
}

// getUnhealthyReason determines why FFmpeg is considered unhealthy
func (fhm *FFmpegHealthMonitor) getUnhealthyReason() string {
	fhm.mutex.RLock()
	defer fhm.mutex.RUnlock()

	if !fhm.isRunning {
		return "process not running"
	}

	if fhm.forceUnhealthy {
		return "forced unhealthy due to critical errors"
	}

	if time.Since(fhm.lastOutput) > fhm.healthTimeout {
		return fmt.Sprintf("no output received for %v (last: %v ago)",
			fhm.healthTimeout, time.Since(fhm.lastOutput))
	}

	if time.Since(fhm.lastFrameUpdate) > fhm.frameTimeout {
		return fmt.Sprintf("no frame progress for %v (last frame: %d, %v ago)",
			fhm.frameTimeout, fhm.lastFrameNumber, time.Since(fhm.lastFrameUpdate))
	}

	return "unknown reason"
}
