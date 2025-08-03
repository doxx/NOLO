package main

import (
	"bufio"
	"flag"
	"fmt"
	"image"
	"image/color"
	"io"
	"io/ioutil"
	"math"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"rivercam/detection"
	"rivercam/overlay"
	"rivercam/ptz"
	"rivercam/tracking"

	"gocv.io/x/gocv"
)

const (
	frameRate          = 30               // Target frames per second
	perfReportInterval = 15 * time.Second // Performance reporting interval
	disableYOLO        = false            // Set to true to disable YOLO processing
	maxPendingFrames   = 120              // Maximum pending frames to prevent memory leaks
)

var (
	// Command-line flags
	inputStream          = flag.String("input", "", "RTSP input stream URL (required)\n\t\tExample: rtsp://user:password@192.168.1.100:554/Streaming/Channels/201")
	ptzInput             = flag.String("ptzinput", "", "PTZ camera HTTP URL (required)\n\t\tExample: http://user:password@192.168.1.100:80/")
	debugMode            = flag.Bool("debug", false, "Enable debug mode with overlay and detailed tracking logs")
	debugVerbose         = flag.Bool("debug-verbose", false, "Enable verbose debug output (includes detailed YOLO, calibration, and tracking calculations)")
	exitOnFirstTrack     = flag.Bool("exit-on-first-track", false, "Exit after first successful target lock (useful for debugging single track sessions)")
	pipZoomEnabled       = flag.Bool("pip", false, "Enable Picture-in-Picture zoom display of locked targets")
	yoloDebug            = flag.Bool("YOLOdebug", false, "Save YOLO input blob images to /tmp/YOLOdebug/ for analysis")
	yoloOverlay          = flag.Bool("yolo-overlay", false, "Show all raw YOLO detections as bounding boxes (useful for debugging P1 'all' mode)")
	targetDisplayTracked = flag.Bool("target-display-tracked", false, "Only show military target information on the tracked P1 target, not all detected P1 objects")
	p1MinConfidence      = flag.Float64("p1-min-confidence", 0.25, "Minimum confidence threshold for P1 targets (boats) (0.0-1.0, default: 0.25)\n\t\tExample: -p1-min-confidence=0.30 for less sensitive boat detection")
	p2MinConfidence      = flag.Float64("p2-min-confidence", 0.15, "Minimum confidence threshold for P2 targets (people) (0.0-1.0, default: 0.15)\n\t\tExample: -p2-min-confidence=0.20 for less sensitive person detection")

	// JPEG frame saving configuration
	jpgPath        = flag.String("jpg-path", "", "Directory path for saving JPEG frames (required when using JPEG flags)")
	preOverlayJpg  = flag.Bool("pre-overlay-jpg", false, "Save frames before overlay processing (requires -jpg-path)")
	postOverlayJpg = flag.Bool("post-overlay-jpg", false, "Save frames after overlay processing (requires -jpg-path)")

	// Overlay display configuration
	statusOverlay   = flag.Bool("status-overlay", false, "Show status information overlay (time, FPS, mode) in lower-left corner")
	targetOverlay   = flag.Bool("target-overlay", false, "Show tracking and targeting overlays (bounding boxes, paths, object info)")
	terminalOverlay = flag.Bool("terminal-overlay", false, "Show debug terminal overlay (real-time messages) in upper-left corner")

	// Tracking priority configuration
	p1Track = flag.String("p1-track", "boat", "Priority 1 tracking objects (comma-separated, or 'all') - primary targets that can achieve LOCK\n\t\tExample: -p1-track=\"boat,surfboard,kayak\" or -p1-track=\"all\"")
	p2Track = flag.String("p2-track", "person", "Priority 2 tracking objects (comma-separated, or 'all') - enhancement objects detected inside locked P1 targets\n\t\tExample: -p2-track=\"person,backpack\" or -p2-track=\"all\"")

	// Color masking for water removal
	maskColors    = flag.String("maskcolors", "", "Comma-separated hex colors to mask out (e.g., 6d9755,243314)")
	maskTolerance = flag.Int("masktolerance", 50, "Color tolerance for masking (0-255, default: 50)")

	// PTZ Movement Limits (soft limits for user safety) - camera coordinate units
	minPan  = flag.Float64("min-pan", -1, "Minimum pan position in camera units (omit flag for hardware minimum)\n\t\tExample: -min-pan=1000 prevents panning left of position 1000")
	maxPan  = flag.Float64("max-pan", -1, "Maximum pan position in camera units (omit flag for hardware maximum)\n\t\tExample: -max-pan=3000 prevents panning right of position 3000")
	minTilt = flag.Float64("min-tilt", -1, "Minimum tilt position in camera units (omit flag for hardware minimum)\n\t\tExample: -min-tilt=0 prevents tilting below horizon")
	maxTilt = flag.Float64("max-tilt", -1, "Maximum tilt position in camera units (omit flag for hardware maximum)\n\t\tExample: -max-tilt=900 prevents tilting too high")
	minZoom = flag.Float64("min-zoom", -1, "Minimum zoom level in camera units (omit flag for hardware minimum)\n\t\tExample: -min-zoom=10 prevents zooming below 1x")
	maxZoom = flag.Float64("max-zoom", -1, "Maximum zoom level in camera units (omit flag for hardware maximum)\n\t\tExample: -max-zoom=120 prevents zooming above 12x")

	// Global debug logger instance
	globalDebugLogger *DebugLogger

	// Memory tracking
	matAllocsCapture            int64
	matClosesCapture            int64
	matAllocsYOLO               int64
	matClosesYOLO               int64
	matAllocsBuffer             int64
	matClosesBuffer             int64
	matAllocsOverlay            int64
	matClosesOverlay            int64
	matMu                       sync.Mutex
	pictureSize                 string
	pictureWidth, pictureHeight int

	// Debug logging throttle
	lastModeLogTime time.Time

	// Debug throttling
	lastMainDebugTime time.Time

	// YOLO debug tracking
	yoloDebugFrameCounter int64

	// Parsed tracking priority configurations
	p1TrackList []string
	p2TrackList []string
	p1TrackAll  bool // NEW: Support for tracking all objects as P1 targets
	p2TrackAll  bool

	// Global confidence thresholds (configurable via P1/P2 confidence flags)
	globalP1MinConfidence float64
	globalP2MinConfidence float64
)

// debugMsg is the global convenience function for unified debug logging
func debugMsg(component, message string, boatID ...string) {
	if globalDebugLogger != nil {
		globalDebugLogger.debugMsg(component, message, boatID...)
	} else {
		// Fallback if logger not initialized
		fmt.Printf("[%s][%s] %s\n", time.Now().Format("15:04:05.000"), component, message)
	}
}

// debugMsgVerbose only outputs if debug-verbose flag is enabled
func debugMsgVerbose(component, message string, boatID ...string) {
	if !*debugVerbose {
		return
	}
	debugMsg(component, message, boatID...)
}

// FrameData represents a frame with its detection data
type FrameData struct {
	frame          gocv.Mat
	detectionRects []image.Rectangle
	classNames     []string
	confidences    []float64
	sequence       int64
	timestamp      time.Time
}

// FrameBuffer manages frame buffering and error recovery
type FrameBuffer struct {
	lastGoodFrame gocv.Mat
	errorCount    int
	maxErrors     int
	recoveryTime  time.Duration
	lastError     time.Time
	mu            sync.Mutex
}

// DebugSession manages detailed tracking debug logs and frame captures for a specific boat
type DebugSession struct {
	enabled        bool
	boatID         string
	sessionID      string // Unique session identifier for filenames
	baseDir        string // Shared base directory for all debug files
	logFile        *os.File
	yoloCounter    int
	overlayCounter int
	frameCounter   int // Track total frames for sampling
	startTime      time.Time
	mu             sync.Mutex

	// Spam prevention for tracking decisions
	lastDecisionLogic string
	lastDecisionTime  time.Time
}

// DebugImageSaveTask represents an async image save task
type DebugImageSaveTask struct {
	filepath string
	image    gocv.Mat
}

// DebugManager manages all debug sessions
type DebugManager struct {
	enabled        bool
	baseDir        string
	sessions       map[string]*DebugSession
	mu             sync.RWMutex
	saveQueue      chan DebugImageSaveTask
	saveWorkers    sync.WaitGroup
	stopWorkers    chan bool
	frameCounter   int            // Global frame counter for post-overlay saves
	objectCounters map[string]int // Per-object JPEG counters for unified naming
}

// DebugLogger provides unified debug message handling for console, files, and overlay
type DebugLogger struct {
	enabled        bool
	baseDir        string
	mu             sync.RWMutex
	boatFiles      map[string]*os.File // boatID -> file handle
	overlayHistory []DebugMessage      // For overlay terminal
	maxOverlayMsgs int
	writeQueue     chan DebugWriteTask
	stopWorker     chan bool
	workerStopped  sync.WaitGroup

	// Comprehensive tracking history accumulation
	trackingHistory map[string][]DebugMessage // objectID -> accumulated messages during tracking
	maxTrackingMsgs int                       // Maximum messages to store per objectID
}

type DebugMessage struct {
	Timestamp time.Time
	Component string
	Message   string
	BoatID    string
}

type DebugWriteTask struct {
	file    *os.File
	content string
}

// TrackingHistoryData contains all accumulated debug data for an objectID
type TrackingHistoryData struct {
	ObjectID        string
	Messages        []DebugMessage
	StartTime       time.Time
	EndTime         time.Time
	MessageCount    int
	ComponentCounts map[string]int // Count messages per component
}

// NewDebugLogger creates a unified debug logger
func NewDebugLogger(enabled bool) *DebugLogger {
	baseDir := "/tmp/debugMode"
	if enabled {
		if err := os.MkdirAll(baseDir, 0755); err != nil {
			fmt.Printf("[DEBUG_LOGGER] Failed to create debug directory: %v\n", err)
			enabled = false
		}
	}

	dl := &DebugLogger{
		enabled:         enabled,
		baseDir:         baseDir,
		boatFiles:       make(map[string]*os.File),
		overlayHistory:  make([]DebugMessage, 0),
		maxOverlayMsgs:  50, // Keep last 50 messages for overlay
		writeQueue:      make(chan DebugWriteTask, 100),
		stopWorker:      make(chan bool, 1),
		trackingHistory: make(map[string][]DebugMessage),
		maxTrackingMsgs: 1000, // Maximum messages to store per objectID
	}

	// Start async file writer worker if enabled
	if enabled {
		dl.workerStopped.Add(1)
		go dl.fileWriteWorker()
	}

	return dl
}

// debugMsg is the main unified debug function
func (dl *DebugLogger) debugMsg(component, message string, boatID ...string) {
	timestamp := time.Now()

	// Always output to console with timestamps (keep for console)
	consoleMsg := fmt.Sprintf("[%s][%s] %s",
		timestamp.Format("15:04:05.000"), component, message)
	fmt.Println(consoleMsg)

	// Determine boat ID
	currentBoatID := ""
	if len(boatID) > 0 && boatID[0] != "" {
		currentBoatID = boatID[0]
	}

	// Create debug message for overlay
	debugMsg := DebugMessage{
		Timestamp: timestamp,
		Component: component,
		Message:   message,
		BoatID:    currentBoatID,
	}

	dl.mu.Lock()

	// Always add to overlay history (terminal overlay should work independently of debug mode)
	dl.overlayHistory = append(dl.overlayHistory, debugMsg)
	if len(dl.overlayHistory) > dl.maxOverlayMsgs {
		dl.overlayHistory = dl.overlayHistory[1:] // Remove oldest
	}

	// Only do file/tracking processing if debug mode enabled
	if !dl.enabled {
		dl.mu.Unlock()
		return
	}

	// TRACKING HISTORY: Accumulate all messages for this objectID
	if currentBoatID != "" {
		// Initialize tracking history for this objectID if needed
		isNewTracking := dl.trackingHistory[currentBoatID] == nil
		if isNewTracking {
			dl.trackingHistory[currentBoatID] = make([]DebugMessage, 0)
			// Don't use fmt.Printf here to avoid recursion with debugMsg
			fmt.Printf("[%s][TRACKING_START] 📋 Started comprehensive tracking history for %s\n",
				timestamp.Format("15:04:05.000"), currentBoatID)
		}

		// Add message to tracking history
		dl.trackingHistory[currentBoatID] = append(dl.trackingHistory[currentBoatID], debugMsg)

		// Limit history size to prevent memory issues
		if len(dl.trackingHistory[currentBoatID]) > dl.maxTrackingMsgs {
			dl.trackingHistory[currentBoatID] = dl.trackingHistory[currentBoatID][1:] // Remove oldest
		}
	}

	// Handle boat-specific file logging
	if currentBoatID != "" {
		file := dl.getOrCreateBoatFile(currentBoatID)
		if file != nil {
			fileContent := fmt.Sprintf("[%s][%s] %s\n",
				timestamp.Format("15:04:05.000"), component, message)

			// Queue for async writing
			select {
			case dl.writeQueue <- DebugWriteTask{file: file, content: fileContent}:
			default:
				// Queue full, drop message to prevent blocking
			}
		}
	}

	dl.mu.Unlock()
}

// getOrCreateBoatFile creates or retrieves boat-specific log file
func (dl *DebugLogger) getOrCreateBoatFile(boatID string) *os.File {
	// Check if we already have a file for this boat
	if file, exists := dl.boatFiles[boatID]; exists {
		return file
	}

	// Create unified debug file
	var filepath string
	var file *os.File
	var err error

	// Use unified filename: [objectID].txt (append mode for integration with session system)
	filepath = fmt.Sprintf("%s/%s.txt", dl.baseDir, boatID)

	// Open in append mode to integrate with the new session system
	file, err = os.OpenFile(filepath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Printf("[DEBUG_LOGGER] Failed to open unified debug file %s: %v\n", filepath, err)
		return nil
	}

	// Write unified debug header (only if file is new/empty)
	fileInfo, _ := file.Stat()
	if fileInfo.Size() == 0 {
		header := fmt.Sprintf("\n=== UNIFIED DEBUG LOG: %s ===\n", boatID)
		header += fmt.Sprintf("Started: %s\n", time.Now().Format("2006-01-02 15:04:05"))
		header += fmt.Sprintf("File: %s\n", filepath)
		header += fmt.Sprintf("Contains: Session events + Debug messages\n")
		header += fmt.Sprintf("========================================\n\n")
		file.WriteString(header)
	}

	// Store file handle
	dl.boatFiles[boatID] = file

	fmt.Printf("[DEBUG_LOGGER] Created boat file: %s\n", filepath)
	return file
}

// fileWriteWorker handles async file writing
func (dl *DebugLogger) fileWriteWorker() {
	defer dl.workerStopped.Done()

	for {
		select {
		case task := <-dl.writeQueue:
			task.file.WriteString(task.content)
			task.file.Sync() // Ensure written to disk

		case <-dl.stopWorker:
			// Drain remaining tasks
			for len(dl.writeQueue) > 0 {
				task := <-dl.writeQueue
				task.file.WriteString(task.content)
				task.file.Sync()
			}
			return
		}
	}
}

// GetOverlayHistory returns recent messages for overlay terminal as clean strings
func (dl *DebugLogger) GetOverlayHistory() []interface{} {
	dl.mu.RLock()
	defer dl.mu.RUnlock()

	// Return clean message strings (no timestamps)
	history := make([]interface{}, len(dl.overlayHistory))
	for i, msg := range dl.overlayHistory {
		// Format as clean string: just the message content
		cleanMsg := msg.Message
		if msg.BoatID != "" {
			cleanMsg = fmt.Sprintf("(%s) %s", msg.BoatID, msg.Message)
		}
		history[i] = cleanMsg
	}
	return history
}

// DumpTrackingHistory saves complete tracking history for an objectID and removes it from memory
func (dl *DebugLogger) DumpTrackingHistory(objectID string) interface{} {
	if !dl.enabled || objectID == "" {
		return nil
	}

	dl.mu.Lock()
	defer dl.mu.Unlock()

	// Get accumulated messages for this objectID
	messages, exists := dl.trackingHistory[objectID]
	if !exists || len(messages) == 0 {
		return nil
	}

	// Calculate tracking statistics
	startTime := messages[0].Timestamp
	endTime := messages[len(messages)-1].Timestamp
	componentCounts := make(map[string]int)

	for _, msg := range messages {
		componentCounts[msg.Component]++
	}

	// Create comprehensive tracking data
	trackingData := &TrackingHistoryData{
		ObjectID:        objectID,
		Messages:        messages,
		StartTime:       startTime,
		EndTime:         endTime,
		MessageCount:    len(messages),
		ComponentCounts: componentCounts,
	}

	// Save to comprehensive tracking file
	dl.saveTrackingHistoryToFile(trackingData)

	// Remove from memory to free up space
	delete(dl.trackingHistory, objectID)

	// Note: JPEG counters for this objectID will be cleaned up separately by DebugManager

	return trackingData
}

// saveTrackingHistoryToFile writes comprehensive tracking history to a detailed file
func (dl *DebugLogger) saveTrackingHistoryToFile(data *TrackingHistoryData) {
	if data == nil || len(data.Messages) == 0 {
		return
	}

	// Create unified filename: objectID.txt (simple and easy to match with JPEGs)
	duration := data.EndTime.Sub(data.StartTime)
	filename := fmt.Sprintf("%s.txt", data.ObjectID)
	filepath := filepath.Join(dl.baseDir, filename)

	file, err := os.OpenFile(filepath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Printf("[TRACKING_HISTORY] Failed to open history file %s: %v\n", filepath, err)
		return
	}
	defer file.Close()

	// Write comprehensive debug message history header (appends to session data)
	fmt.Fprintf(file, "\n=== COMPREHENSIVE DEBUG MESSAGE HISTORY ===\n")
	fmt.Fprintf(file, "Object ID: %s\n", data.ObjectID)
	fmt.Fprintf(file, "Tracking Duration: %.1f seconds\n", duration.Seconds())
	fmt.Fprintf(file, "Start Time: %s\n", data.StartTime.Format("2006-01-02 15:04:05.000"))
	fmt.Fprintf(file, "End Time: %s\n", data.EndTime.Format("2006-01-02 15:04:05.000"))
	fmt.Fprintf(file, "Total Messages: %d\n", data.MessageCount)
	fmt.Fprintf(file, "\n=== MESSAGE BREAKDOWN BY COMPONENT ===\n")

	// Sort components by message count
	type ComponentCount struct {
		Component string
		Count     int
	}

	var sortedComponents []ComponentCount
	for component, count := range data.ComponentCounts {
		sortedComponents = append(sortedComponents, ComponentCount{component, count})
	}

	// Simple sort by count (descending)
	for i := 0; i < len(sortedComponents)-1; i++ {
		for j := i + 1; j < len(sortedComponents); j++ {
			if sortedComponents[i].Count < sortedComponents[j].Count {
				sortedComponents[i], sortedComponents[j] = sortedComponents[j], sortedComponents[i]
			}
		}
	}

	for _, cc := range sortedComponents {
		fmt.Fprintf(file, "%-20s: %d messages\n", cc.Component, cc.Count)
	}

	fmt.Fprintf(file, "\n=== CHRONOLOGICAL MESSAGE LOG ===\n")

	// Write all messages in chronological order
	for i, msg := range data.Messages {
		relativeTime := msg.Timestamp.Sub(data.StartTime).Seconds()
		fmt.Fprintf(file, "[%07.3fs] [%-20s] %s\n",
			relativeTime, msg.Component, msg.Message)

		// Add periodic separator for readability
		if i > 0 && (i+1)%50 == 0 {
			fmt.Fprintf(file, "\n--- %d messages processed ---\n\n", i+1)
		}
	}

	fmt.Fprintf(file, "\n=== END OF DEBUG MESSAGE HISTORY ===\n")

	fmt.Printf("[TRACKING_HISTORY] 📋 Saved comprehensive tracking history: %s (%d messages, %.1fs duration)\n",
		filename, data.MessageCount, duration.Seconds())
}

// GetActiveTrackingIDs returns all objectIDs currently being tracked
func (dl *DebugLogger) GetActiveTrackingIDs() []string {
	dl.mu.RLock()
	defer dl.mu.RUnlock()

	var activeIDs []string
	for objectID := range dl.trackingHistory {
		activeIDs = append(activeIDs, objectID)
	}
	return activeIDs
}

// GetTrackingMessageCount returns number of accumulated messages for an objectID
func (dl *DebugLogger) GetTrackingMessageCount(objectID string) int {
	dl.mu.RLock()
	defer dl.mu.RUnlock()

	if messages, exists := dl.trackingHistory[objectID]; exists {
		return len(messages)
	}
	return 0
}

// Close cleans up the debug logger
func (dl *DebugLogger) Close() {
	if !dl.enabled {
		return
	}

	// Stop worker
	dl.stopWorker <- true
	dl.workerStopped.Wait()

	// Close all boat files
	dl.mu.Lock()
	for boatID, file := range dl.boatFiles {
		file.Close()
		fmt.Printf("[DEBUG_LOGGER] Closed boat file for %s\n", boatID)
	}
	dl.mu.Unlock()
}

// NewFrameBuffer creates a new frame buffer
func NewFrameBuffer() *FrameBuffer {
	return &FrameBuffer{
		maxErrors:    5,               // Maximum consecutive errors before recovery
		recoveryTime: time.Second * 2, // Time to wait before attempting recovery
	}
}

// Close releases resources
func (fb *FrameBuffer) Close() {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if fb.lastGoodFrame.Ptr() != nil {
		fb.lastGoodFrame.Close()
		fb.lastGoodFrame = gocv.NewMat() // Reset to empty mat
	}
}

// isValidFrame checks if a frame is valid without using CGO calls
func isValidFrame(frame gocv.Mat) bool {
	// Check if the frame pointer is nil
	if frame.Ptr() == nil {
		return false
	}

	// Try to get frame size - if this fails, the frame is invalid
	rows := frame.Rows()
	cols := frame.Cols()
	channels := frame.Channels()

	return rows > 0 && cols > 0 && channels > 0
}

// NewDebugManager creates a new debug manager
func NewDebugManager(enabled bool) *DebugManager {
	baseDir := "/tmp/debugMode"
	if enabled {
		// Create base debug directory
		if err := os.MkdirAll(baseDir, 0755); err != nil {
			debugMsg("DEBUG", fmt.Sprintf("Failed to create debug directory: %v", err))
			enabled = false
		}
	}

	dm := &DebugManager{
		enabled:        enabled,
		baseDir:        baseDir,
		sessions:       make(map[string]*DebugSession),
		saveQueue:      make(chan DebugImageSaveTask, 120), // Buffer up to 120 images (4 seconds at 30fps for stability)
		stopWorkers:    make(chan bool, 1),
		frameCounter:   0,                    // Initialize frame counter
		objectCounters: make(map[string]int), // Initialize per-object JPEG counters
	}

	// Start async image save workers if debug enabled (2 workers for heavy overlay frame saving)
	if enabled {
		numWorkers := 2 // Increased from 1 to handle every-frame overlay saving
		for i := 0; i < numWorkers; i++ {
			dm.saveWorkers.Add(1)
			go func(workerID int) {
				defer dm.saveWorkers.Done()
				debugMsg("DEBUG", fmt.Sprintf("Image save worker %d started", workerID))

				for {
					select {
					case task := <-dm.saveQueue:
						// Save image asynchronously
						success := gocv.IMWrite(task.filepath, task.image)
						if !success {
							debugMsg("DEBUG", fmt.Sprintf("Worker %d failed to save image: %s", workerID, task.filepath))
						}
						// Close the image after saving
						task.image.Close()

					case <-dm.stopWorkers:
						debugMsg("DEBUG", fmt.Sprintf("Image save worker %d stopping", workerID))
						// MEMORY LEAK FIX: Aggressively drain and close ALL remaining images
						drained := 0
						for len(dm.saveQueue) > 0 {
							select {
							case task := <-dm.saveQueue:
								// Try to save but prioritize closing the Mat to prevent memory leak
								gocv.IMWrite(task.filepath, task.image)
								task.image.Close()
								drained++
							default:
								break // No more images
							}
						}
						if drained > 0 {
							debugMsg("DEBUG", fmt.Sprintf("Image save worker %d drained %d pending images to prevent memory leak", workerID, drained))
						}
						return
					}
				}
			}(i)
		}
		debugMsg("DEBUG", fmt.Sprintf("Started %d async image save workers with 4-second buffer for stability", numWorkers))
	}

	return dm
}

// Stop stops the debug manager and its workers
func (dm *DebugManager) Stop() {
	if dm.enabled {
		// MEMORY LEAK FIX: Close all active sessions before stopping workers
		dm.mu.Lock()
		sessionCount := len(dm.sessions)
		for boatID := range dm.sessions {
			session := dm.sessions[boatID]
			if session.enabled {
				session.Close()
			}
			delete(dm.sessions, boatID)
		}
		dm.mu.Unlock()

		if sessionCount > 0 {
			debugMsg("DEBUG", fmt.Sprintf("Closed %d sessions before stopping debug manager", sessionCount))
		}

		close(dm.stopWorkers)
		dm.saveWorkers.Wait()
		debugMsg("DEBUG", "Debug manager stopped")

		// Force cleanup after stopping
		runtime.GC()
	}
}

// queueImageSave queues an image for async saving
func (dm *DebugManager) queueImageSave(filepath string, image gocv.Mat) bool {
	if !dm.enabled {
		return false
	}

	// Clone the image so it's safe to use after this function returns
	imageClone := image.Clone()

	select {
	case dm.saveQueue <- DebugImageSaveTask{filepath: filepath, image: imageClone}:
		return true
	default:
		// Queue full, drop this image to prevent blocking and memory leaks
		debugMsg("DEBUG", fmt.Sprintf("Image save queue full - dropping image to prevent memory leak: %s", filepath))
		imageClone.Close()
		return false
	}
}

// SavePostOverlayFrame saves the final frame with all overlays applied using objectID naming (DEBUG MODE ONLY)
func (dm *DebugManager) SavePostOverlayFrame(overlayFrame gocv.Mat, objectID string, detectionCount int) string {
	if !dm.enabled {
		return ""
	}

	// ONLY save frames with valid tracking objectID - skip detections that aren't worth tracking
	if objectID == "" || objectID == "none" {
		debugMsg("DEBUG", fmt.Sprintf("⏭️ Skipping frame save - no valid tracking objectID (%d detections found but not tracked)", detectionCount))
		return ""
	}

	dm.mu.Lock()

	// Increment counter for this objectID
	dm.objectCounters[objectID]++
	counter := dm.objectCounters[objectID]

	dm.mu.Unlock()

	// Create unified pipeline filename: objectID_pipeline_counter.jpg
	filename := fmt.Sprintf("%s_postoverlay_%03d.jpg", objectID, counter)
	filepath := filepath.Join(dm.baseDir, filename)

	// Queue for async saving (non-blocking) - queueImageSave will clone internally
	if dm.queueImageSave(filepath, overlayFrame) {
		debugMsg("DEBUG", fmt.Sprintf("💾 Saved frame: %s (%d detections)", filename, detectionCount), objectID)
		return filename
	} else {
		debugMsg("DEBUG", "❌ Failed to save frame (queue full)", objectID)
		return ""
	}
}

// StartSession creates a new debug session for a boat
func (dm *DebugManager) StartSession(boatID string) *DebugSession {
	if !dm.enabled {
		return &DebugSession{enabled: false}
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()

	// Use unified filename format: objectID.txt (same as comprehensive tracking history)
	sessionID := boatID // Use objectID as session identifier for unified naming

	// Create log file with unified naming: objectID.txt
	logPath := filepath.Join(dm.baseDir, fmt.Sprintf("%s.txt", boatID))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		debugMsg("DEBUG", fmt.Sprintf("Failed to create log file: %v", err))
		return &DebugSession{enabled: false}
	}

	session := &DebugSession{
		enabled:        true,
		boatID:         boatID,
		sessionID:      sessionID,
		baseDir:        dm.baseDir,
		logFile:        logFile,
		yoloCounter:    0,
		overlayCounter: 0,
		frameCounter:   0,
		startTime:      time.Now(),
	}

	// Write integrated debug session header
	fmt.Fprintf(logFile, "\n=== INTEGRATED DEBUG SESSION: %s ===\n", boatID)
	fmt.Fprintf(logFile, "Session Start: %s\n", session.startTime.Format("2006-01-02 15:04:05.000"))
	fmt.Fprintf(logFile, "Object ID: %s\n", boatID)
	fmt.Fprintf(logFile, "Debug Files: %s/%s_*.jpg and %s.txt\n", dm.baseDir, boatID, boatID)
	fmt.Fprintf(logFile, "\nINTEGRATED DEBUG SYSTEM:\n")
	fmt.Fprintf(logFile, "  This file contains BOTH structured session data AND comprehensive debug messages\n")
	fmt.Fprintf(logFile, "  - Session Events: Detailed tracking analysis, YOLO data, lock progression\n")
	fmt.Fprintf(logFile, "  - Debug Messages: All debugMsg() calls accumulated during tracking\n")
	fmt.Fprintf(logFile, "  - Frame Policy: Only frames with valid tracked objectID (no transient detections)\n\n")
	fmt.Fprintf(logFile, "SESSION EVENT TYPES:\n")
	fmt.Fprintf(logFile, "  - DETAILED_TRACKING_STATE: Complete object tracking status each frame\n")
	fmt.Fprintf(logFile, "  - DETECTION_ANALYSIS: All YOLO detections with filtering details\n")
	fmt.Fprintf(logFile, "  - LOCK_PROGRESSION_ANALYSIS: Why objects are/aren't progressing toward locks\n")
	fmt.Fprintf(logFile, "  - ACTIVE_TRACKING_UPDATE: Frame-by-frame target object tracking\n")
	fmt.Fprintf(logFile, "  - TRACKING_DECISION: Camera movement decisions and logic\n")
	fmt.Fprintf(logFile, "  - MEASUREMENT_CLEANUP: Rolling average data dumps\n\n")
	fmt.Fprintf(logFile, "=== SESSION EVENTS START ===\n\n")

	dm.sessions[boatID] = session
	debugMsg("DEBUG", fmt.Sprintf("Started session for object %s with session ID %s", boatID, sessionID))
	debugMsg("DEBUG", fmt.Sprintf("All debug files will be in %s with prefix: %s", dm.baseDir, sessionID))

	return session
}

// IsEnabled returns whether debug mode is active
func (dm *DebugManager) IsEnabled() bool {
	return dm.enabled
}

// GetSession retrieves an existing debug session
func (dm *DebugManager) GetSession(boatID string) *DebugSession {
	if !dm.enabled {
		return &DebugSession{enabled: false}
	}

	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if session, exists := dm.sessions[boatID]; exists {
		return session
	}

	return &DebugSession{enabled: false}
}

// EndSession closes a debug session
func (dm *DebugManager) EndSession(boatID string) {
	if !dm.enabled {
		return
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()

	if session, exists := dm.sessions[boatID]; exists {
		session.Close()
		delete(dm.sessions, boatID)
		debugMsg("DEBUG", fmt.Sprintf("Ended session for object %s", boatID))

		// MEMORY LEAK FIX: Force garbage collection after ending sessions
		// This helps clean up any lingering Mat objects from async saves
		if len(dm.sessions) == 0 {
			debugMsg("DEBUG", "All sessions ended - forcing garbage collection to prevent memory leaks")
			runtime.GC()
		}
	}
}

// CleanupObjectCounter removes the JPEG counter for an objectID to prevent memory leaks
func (dm *DebugManager) CleanupObjectCounter(objectID string) {
	if !dm.enabled || objectID == "" {
		return
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()

	if counter, exists := dm.objectCounters[objectID]; exists {
		delete(dm.objectCounters, objectID)
		debugMsg("DEBUG", fmt.Sprintf("🧹 Cleaned up JPEG counter for %s (was at %d)", objectID, counter))
	}
}

// LogEvent logs a tracking event to the session
func (ds *DebugSession) LogEvent(eventType, message string, data map[string]interface{}) {
	if !ds.enabled {
		return
	}

	ds.mu.Lock()
	defer ds.mu.Unlock()

	timestamp := time.Now().Format("15:04:05.000")
	fmt.Fprintf(ds.logFile, "[%s] %s\n", timestamp, eventType)
	fmt.Fprintf(ds.logFile, "  %s\n", message)

	for key, value := range data {
		fmt.Fprintf(ds.logFile, "  %s: %v\n", key, value)
	}

	fmt.Fprintf(ds.logFile, "\n")
	ds.logFile.Sync()
}

// ShouldLogTrackingDecision checks if a tracking decision should be logged (spam prevention)
func (ds *DebugSession) ShouldLogTrackingDecision(decisionLogic string) bool {
	if !ds.enabled {
		return false
	}

	ds.mu.Lock()
	defer ds.mu.Unlock()

	now := time.Now()

	// Always log first decision or if logic has changed
	if ds.lastDecisionLogic == "" || ds.lastDecisionLogic != decisionLogic {
		ds.lastDecisionLogic = decisionLogic
		ds.lastDecisionTime = now
		return true
	}

	// If same logic, only log every 5 seconds to prevent spam
	if now.Sub(ds.lastDecisionTime) >= 5*time.Second {
		ds.lastDecisionTime = now
		return true
	}

	return false // Skip logging for spam prevention
}

// SaveYOLOFrame saves the YOLO input blob as an image (what YOLO actually sees)
// Saves every 2nd frame for technical debugging (overlay frames save every frame for user experience)
func (ds *DebugSession) SaveYOLOFrame(originalFrame gocv.Mat, debugManager *DebugManager) string {
	if !ds.enabled {
		return ""
	}

	ds.mu.Lock()
	defer ds.mu.Unlock()

	ds.frameCounter++

	// Only save every 2nd frame for better debugging visibility (15 fps at 30fps input)
	if ds.frameCounter%2 != 0 {
		return "" // Skip this frame
	}

	ds.yoloCounter++
	filename := fmt.Sprintf("yolo_input_%s_%03d.jpg", ds.sessionID, ds.yoloCounter)
	filepath := filepath.Join(ds.baseDir, filename)

	// Create the EXACT same letterboxed image that YOLO processes (using our fixed letterboxing)
	originalWidth := float32(originalFrame.Cols())  // 2688
	originalHeight := float32(originalFrame.Rows()) // 1520
	yoloSize := 832

	// Use same letterbox parameters as createOptimizedBlob
	aspectRatio := originalWidth / originalHeight         // 1.768
	contentHeight := int(float32(yoloSize) / aspectRatio) // 470px
	yOffset := (yoloSize - contentHeight) / 2             // 181px

	// Create properly letterboxed image (what YOLO actually sees now)
	yoloImage := gocv.NewMatWithSize(yoloSize, yoloSize, gocv.MatTypeCV8UC3)
	defer yoloImage.Close()
	yoloImage.SetTo(gocv.NewScalar(0, 0, 0, 0)) // Black letterbox bars

	// Resize original to fit in content area
	resized := gocv.NewMat()
	defer resized.Close()
	gocv.Resize(originalFrame, &resized, image.Pt(yoloSize, contentHeight), 0, 0, gocv.InterpolationLinear)

	// Copy resized content to center of letterboxed image
	contentROI := yoloImage.Region(image.Rect(0, yOffset, yoloSize, yOffset+contentHeight))
	defer contentROI.Close()
	resized.CopyTo(&contentROI)

	// Queue for async saving (non-blocking) - queueImageSave will clone internally
	if debugManager.queueImageSave(filepath, yoloImage) {
		// Image queued successfully (cloned copy will be closed by async worker)
		return filename
	} else {
		// Failed to queue - continue processing
		return ""
	}
}

// SaveYOLODetectionsFrame saves the YOLO input image with raw detection boxes overlaid
// Shows properly letterboxed input that matches what YOLO actually sees (technical debugging)
// Note: This saves every 2nd frame, while overlay frames save every frame for user experience
func (ds *DebugSession) SaveYOLODetectionsFrame(originalFrame gocv.Mat, detectionData []map[string]interface{}, debugManager *DebugManager) string {
	if !ds.enabled {
		return ""
	}

	ds.mu.Lock()
	defer ds.mu.Unlock()

	// Use same frame counter logic - only save every 2nd frame
	if ds.frameCounter%2 != 0 {
		return "" // Skip this frame
	}

	filename := fmt.Sprintf("yolo_detections_%s_%03d.jpg", ds.sessionID, ds.yoloCounter)
	filepath := filepath.Join(ds.baseDir, filename)

	// Create the EXACT same letterboxed image that YOLO sees (using fixed letterboxing)
	// This now matches perfectly with our corrected createOptimizedBlob function
	originalWidth := float32(originalFrame.Cols())  // 2688
	originalHeight := float32(originalFrame.Rows()) // 1520
	yoloSize := 832

	// Calculate letterbox parameters (same as createOptimizedBlob)
	aspectRatio := originalWidth / originalHeight         // 1.768
	contentHeight := int(float32(yoloSize) / aspectRatio) // 470px
	yOffset := (yoloSize - contentHeight) / 2             // 181px

	// Create letterboxed image (exactly what YOLO sees)
	yoloLetterboxed := gocv.NewMatWithSize(yoloSize, yoloSize, gocv.MatTypeCV8UC3)
	yoloLetterboxed.SetTo(gocv.NewScalar(0, 0, 0, 0)) // Black letterbox bars

	// Resize original to fit in content area
	resized := gocv.NewMat()
	gocv.Resize(originalFrame, &resized, image.Pt(yoloSize, contentHeight), 0, 0, gocv.InterpolationLinear)

	// Copy resized content to center of letterboxed image
	contentROI := yoloLetterboxed.Region(image.Rect(0, yOffset, yoloSize, yOffset+contentHeight))
	resized.CopyTo(&contentROI)
	contentROI.Close()
	resized.Close()

	// Draw YOLO detection boxes using raw normalized coordinates (0-1)
	for _, detection := range detectionData {
		// Get raw YOLO coordinates (normalized 0-1)
		xNorm := detection["Raw_x"].(float64)
		yNorm := detection["Raw_y"].(float64)
		wNorm := detection["Raw_w"].(float64)
		hNorm := detection["Raw_h"].(float64)
		className := detection["Class"].(string)
		confidence := detection["Confidence"].(float64)

		// Convert to 832x832 pixel coordinates (what YOLO sees)
		centerX := int(xNorm * 832)
		centerY := int(yNorm * 832)
		width := int(wNorm * 832)
		height := int(hNorm * 832)

		left := centerX - width/2
		top := centerY - height/2
		right := left + width
		bottom := top + height

		// Draw detection rectangle in bright green
		gocv.Rectangle(&yoloLetterboxed, image.Rect(left, top, right, bottom), color.RGBA{0, 255, 0, 255}, 2)

		// Draw confidence and class text
		label := fmt.Sprintf("%s %.2f", className, confidence)
		textPoint := image.Pt(left, top-5)
		if top-5 < 10 {
			textPoint.Y = top + 15 // Move text inside box if too close to top
		}
		gocv.PutText(&yoloLetterboxed, label, textPoint, gocv.FontHersheySimplex, 0.5, color.RGBA{0, 255, 0, 255}, 1)
	}

	// Queue for async saving (non-blocking)
	if debugManager.queueImageSave(filepath, yoloLetterboxed) {
		// Image queued successfully - don't close it (async worker will close it)
		return filename
	} else {
		// Failed to queue, clean up
		yoloLetterboxed.Close()
		return ""
	}
}

// SaveOverlayFrame saves the frame with tracking overlay
// Saves EVERY frame to capture complete tracking behavior and user experience
func (ds *DebugSession) SaveOverlayFrame(overlayFrame gocv.Mat, debugManager *DebugManager) string {
	if !ds.enabled {
		return ""
	}

	ds.mu.Lock()
	defer ds.mu.Unlock()

	// Save EVERY overlay frame - no skipping!
	// This captures exactly what the user sees including all predictions, decisions, overlays
	ds.overlayCounter++
	filename := fmt.Sprintf("%s_overlay_%04d.jpg", ds.boatID, ds.overlayCounter)
	filepath := filepath.Join(ds.baseDir, filename)

	// Queue for async saving (non-blocking) - queueImageSave will clone internally
	if debugManager.queueImageSave(filepath, overlayFrame) {
		// Image queued successfully (cloned copy will be closed by async worker)
		return filename
	} else {
		// Failed to queue - continue processing
		return ""
	}
}

// Close closes the debug session
func (ds *DebugSession) Close() {
	if !ds.enabled {
		return
	}

	ds.mu.Lock()
	defer ds.mu.Unlock()

	if ds.logFile != nil {
		endTime := time.Now()
		duration := endTime.Sub(ds.startTime)

		fmt.Fprintf(ds.logFile, "=== SESSION END: %s ===\n", endTime.Format("15:04:05.000"))
		fmt.Fprintf(ds.logFile, "Duration: %v\n", duration)
		fmt.Fprintf(ds.logFile, "Total Frames Processed: %d\n", ds.frameCounter)
		fmt.Fprintf(ds.logFile, "Overlay Frames Saved: %d (every frame)\n", ds.overlayCounter)
		fmt.Fprintf(ds.logFile, "Frame Save Rate: 100%% (complete user experience capture)\n")

		ds.logFile.Close()
		ds.logFile = nil
	}
}

// ProcessFrame handles frame processing with error recovery
func (fb *FrameBuffer) ProcessFrame(frame gocv.Mat) (gocv.Mat, bool) {
	fb.mu.Lock()
	defer fb.mu.Unlock()

	// Check if frame is valid
	if !isValidFrame(frame) {
		fb.errorCount++
		if fb.errorCount >= fb.maxErrors {
			// If we have a last good frame, use it
			if isValidFrame(fb.lastGoodFrame) {
				fmt.Println("[RECOVERY] Using last good frame due to invalid capture.")
				return fb.lastGoodFrame.Clone(), true
			}
		}
		return gocv.NewMat(), false
	}

	// Frame is valid, update last good frame
	if isValidFrame(fb.lastGoodFrame) {
		fb.lastGoodFrame.Close()
	}
	fb.lastGoodFrame = frame.Clone()
	fb.errorCount = 0
	fb.lastError = time.Time{}
	return frame, true
}

// PipelineStats tracks performance metrics for different parts of the pipeline
type PipelineStats struct {
	mu              sync.Mutex
	captureCount    int64
	processCount    int64
	writeCount      int64
	lastCaptureTime time.Time
	lastProcessTime time.Time
	lastWriteTime   time.Time
	lastReportTime  time.Time
	lastFPSUpdate   time.Time
	fpsCount        int64

	// Timing measurements
	readTimeTotal  time.Duration
	yoloTimeTotal  time.Duration
	trackTimeTotal time.Duration
	writeTimeTotal time.Duration
	readCount      int64
	yoloCount      int64
	trackCount     int64
}

// NewPipelineStats creates a new pipeline statistics tracker
func NewPipelineStats() *PipelineStats {
	now := time.Now()
	return &PipelineStats{
		lastCaptureTime: now,
		lastProcessTime: now,
		lastWriteTime:   now,
		lastReportTime:  now,
		lastFPSUpdate:   now,
	}
}

// GetStats returns current statistics and resets counters
func (ps *PipelineStats) GetStats() (captureFPS, processFPS, writeFPS float64, avgRead, avgYOLO, avgTrack, avgWrite time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	now := time.Now()
	timeWindow := now.Sub(ps.lastReportTime).Seconds()
	if timeWindow <= 0 {
		timeWindow = 1.0 // Prevent division by zero
	}

	// Calculate FPS for each stage
	captureFPS = float64(ps.captureCount) / timeWindow
	processFPS = float64(ps.processCount) / timeWindow
	writeFPS = float64(ps.writeCount) / timeWindow

	// Calculate average times
	if ps.readCount > 0 {
		avgRead = ps.readTimeTotal / time.Duration(ps.readCount)
	}
	if ps.yoloCount > 0 {
		avgYOLO = ps.yoloTimeTotal / time.Duration(ps.yoloCount)
	}
	if ps.trackCount > 0 {
		avgTrack = ps.trackTimeTotal / time.Duration(ps.trackCount)
	}
	if ps.writeCount > 0 {
		avgWrite = ps.writeTimeTotal / time.Duration(ps.writeCount)
	}

	// Reset counters but keep timestamps
	ps.captureCount = 0
	ps.processCount = 0
	ps.writeCount = 0
	ps.readTimeTotal = 0
	ps.yoloTimeTotal = 0
	ps.trackTimeTotal = 0
	ps.writeTimeTotal = 0
	ps.readCount = 0
	ps.yoloCount = 0
	ps.trackCount = 0
	ps.writeCount = 0
	ps.lastReportTime = now

	return
}

// UpdateCapture updates capture statistics
func (ps *PipelineStats) UpdateCapture(duration time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.captureCount++
	ps.readTimeTotal += duration
	ps.readCount++
}

// UpdateYOLO updates YOLO processing statistics
func (ps *PipelineStats) UpdateYOLO(duration time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.yoloTimeTotal += duration
	ps.yoloCount++
}

// UpdateTracking updates tracking statistics
func (ps *PipelineStats) UpdateTracking(duration time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.trackTimeTotal += duration
	ps.trackCount++
}

// UpdateWrite updates write statistics
func (ps *PipelineStats) UpdateWrite(duration time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.writeCount++
	ps.writeTimeTotal += duration
}

// UpdateProcess updates processing statistics
func (ps *PipelineStats) UpdateProcess() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.processCount++
}

// UpdateFPS updates the FPS counter
func (ps *PipelineStats) UpdateFPS() float64 {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	now := time.Now()
	ps.fpsCount++

	// Calculate FPS over a 1-second window
	if now.Sub(ps.lastFPSUpdate) >= time.Second {
		fps := float64(ps.fpsCount) / now.Sub(ps.lastFPSUpdate).Seconds()
		ps.fpsCount = 0
		ps.lastFPSUpdate = now
		return fps
	}

	// Return the last calculated FPS
	return float64(ps.fpsCount) / now.Sub(ps.lastFPSUpdate).Seconds()
}

func trackMatAlloc(segment string) {
	matMu.Lock()
	switch segment {
	case "capture":
		matAllocsCapture++
	case "yolo":
		matAllocsYOLO++
	case "buffer":
		matAllocsBuffer++
	case "overlay":
		matAllocsOverlay++
	}
	matMu.Unlock()
}

func trackMatClose(segment string) {
	matMu.Lock()
	switch segment {
	case "capture":
		matClosesCapture++
	case "yolo":
		matClosesYOLO++
	case "buffer":
		matClosesBuffer++
	case "overlay":
		matClosesOverlay++
	}
	matMu.Unlock()
}

// Periodically print stats
func startMatStatsPrinter() {
	go func() {
		for {
			time.Sleep(15 * time.Second)
			matMu.Lock()
			debugMsg("MAT", fmt.Sprintf("Capture: Allocs=%d, Closes=%d, Active=%d | YOLO: Allocs=%d, Closes=%d, Active=%d | Buffer: Allocs=%d, Closes=%d, Active=%d | Overlay: Allocs=%d, Closes=%d, Active=%d",
				matAllocsCapture, matClosesCapture, matAllocsCapture-matClosesCapture,
				matAllocsYOLO, matClosesYOLO, matAllocsYOLO-matClosesYOLO,
				matAllocsBuffer, matClosesBuffer, matAllocsBuffer-matClosesBuffer,
				matAllocsOverlay, matClosesOverlay, matAllocsOverlay-matClosesOverlay))
			matMu.Unlock()
		}
	}()
}

// FFmpegManager handles FFmpeg process management and restart
type FFmpegManager struct {
	pictureSize string
	cmd         *exec.Cmd
	stdin       *bufio.Writer
	stdinPipe   io.WriteCloser // Direct access to stdin pipe
	mu          sync.Mutex
	stopChan    chan struct{}
	rtmpURL     string
	pgid        int // Store the process group ID

	// Sequential write queue with frame ordering
	writeQueue  chan TimedFrame
	writeWorker sync.WaitGroup

	// Pending frames tracking (for bounded buffer)
	pendingFrameCount int
	pendingMu         sync.Mutex

	// Debug information
	lastFrameSize    int
	lastFrameTime    time.Time
	lastWriteTime    time.Duration
	lastWriteError   error
	lastFlushTime    time.Time
	lastFlushError   error
	lastProcessState *os.ProcessState
	debugMu          sync.Mutex
	startTime        time.Time // Track when FFmpeg started
}

// TimedFrame represents a frame with sequencing information
type TimedFrame struct {
	data     []byte
	frameNum int64
}

// NewFFmpegManager creates a new FFmpeg manager
func NewFFmpegManager(pictureSize string) *FFmpegManager {
	return &FFmpegManager{
		pictureSize: pictureSize,
		stopChan:    make(chan struct{}, 1),
		rtmpURL:     "rtmp://localhost/live/stream",
		writeQueue:  make(chan TimedFrame, 120), // Increased buffer, 4 seconds at 30fps for better stability
	}
}

// GetStopChan returns the stop channel
func (m *FFmpegManager) GetStopChan() <-chan struct{} {
	return m.stopChan
}

// GetWriteQueueStatus returns the current write queue length and capacity
func (m *FFmpegManager) GetWriteQueueStatus() (int, int) {
	return len(m.writeQueue), cap(m.writeQueue)
}

// GetPendingFramesStatus returns the current pending frames count and maximum
func (m *FFmpegManager) GetPendingFramesStatus() (int, int) {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	return m.pendingFrameCount, maxPendingFrames
}

// Start initializes and starts the FFmpeg process
func (m *FFmpegManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Create new FFmpeg command
	m.cmd = setupFFmpeg(m.pictureSize)

	// Setup stdin pipe
	stdin, err := m.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("could not get FFmpeg stdin: %v", err)
	}
	// PERFORMANCE: Increased buffer from 4MB to 48MB to handle full 12MB frames with better stability
	m.stdinPipe = stdin
	m.stdin = bufio.NewWriterSize(stdin, 48*1024*1024)

	// Set up stdout and stderr pipes for debug output
	stdout, err := m.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("could not get FFmpeg stdout: %v", err)
	}
	stderr, err := m.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("could not get FFmpeg stderr: %v", err)
	}

	// Set process group ID
	m.cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Pgid:    0,
	}

	// Log the exact command being executed
	cmdLine := fmt.Sprintf("ffmpeg %s", strings.Join(m.cmd.Args[1:], " "))
	debugMsg("FFMPEG_STARTUP", fmt.Sprintf("Executing: %s", cmdLine))

	// Start the process
	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("could not start FFmpeg: %v", err)
	}

	// Store the process group ID
	m.pgid = m.cmd.Process.Pid

	// Initialize timing for monitoring
	now := time.Now()
	m.startTime = now
	m.debugMu.Lock()
	m.lastFrameTime = now // Initialize to startup time to prevent false stall detection
	m.lastFlushTime = now
	m.debugMu.Unlock()

	debugMsg("FFMPEG_STARTUP", fmt.Sprintf("FFmpeg started successfully (PID: %d, PGID: %d)", m.cmd.Process.Pid, m.pgid))

	// Start output readers with debug formatting
	go func() {
		defer debugMsg("FFMPEG_STDOUT", "Reader exiting")
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024) // Increase buffer size
		lineCount := 0
		for scanner.Scan() {
			lineCount++
			line := scanner.Text()
			debugMsg("FFMPEG_STDOUT", line)
		}
		if err := scanner.Err(); err != nil {
			debugMsg("FFMPEG_STDOUT", fmt.Sprintf("Scanner error after %d lines: %v", lineCount, err))
		}
		debugMsg("FFMPEG_STDOUT", fmt.Sprintf("Processed %d lines before exit", lineCount))
	}()

	go func() {
		defer debugMsg("FFMPEG_STDERR", "Reader exiting")
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024) // Increase buffer size
		lineCount := 0
		for scanner.Scan() {
			lineCount++
			line := scanner.Text()
			debugMsg("FFMPEG_STDERR", line)
		}
		if err := scanner.Err(); err != nil {
			debugMsg("FFMPEG_STDERR", fmt.Sprintf("Scanner error after %d lines: %v", lineCount, err))
		}
		debugMsg("FFMPEG_STDERR", fmt.Sprintf("Processed %d lines before exit", lineCount))
	}()

	// Start monitoring goroutine
	go m.monitor()

	// Start single timed write worker for consistent frame timing
	m.writeWorker.Add(1)
	go m.timedWriteWorker()

	frameDuration := time.Second / time.Duration(frameRate) // 33.33ms for 30fps
	debugMsg("FFMPEG_STARTUP", fmt.Sprintf("Started sequential write worker with 4-second buffer for frame order preservation (%.2fms intervals)",
		frameDuration.Seconds()*1000))

	return nil
}

// Stop kills the FFmpeg process and its process group
func (m *FFmpegManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Signal async writers to stop
	close(m.stopChan)

	// Wait for async writers to finish
	m.writeWorker.Wait()

	// Close write queue
	close(m.writeQueue)

	if m.cmd != nil && m.cmd.Process != nil {
		// First try graceful shutdown
		if m.pgid != 0 {
			syscall.Kill(-m.pgid, syscall.SIGTERM)
		}

		// Give it a moment to shut down gracefully
		time.Sleep(100 * time.Millisecond)

		// If still running, force kill
		if m.pgid != 0 {
			syscall.Kill(-m.pgid, syscall.SIGKILL)
		}
		m.cmd.Process.Kill()

		// Wait for process to exit
		m.cmd.Wait()
	}
}

// GetStdin returns the FFmpeg stdin writer
func (m *FFmpegManager) GetStdin() *bufio.Writer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stdin
}

// WriteDirectToStdin writes directly to the stdin pipe without buffering
// This bypasses bufio.Writer and its automatic flushing for better performance
func (m *FFmpegManager) WriteDirectToStdin(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.stdinPipe.Write(data)
	return err
}

// WriteAsync queues frame data for sequential writing to maintain frame order
func (m *FFmpegManager) WriteAsync(data []byte, frameNum int64) error {
	// Make a copy of the data since the original may be modified
	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)

	timedFrame := TimedFrame{
		data:     dataCopy,
		frameNum: frameNum,
	}

	// CRITICAL: Never drop frames - always maintain sequence order
	select {
	case m.writeQueue <- timedFrame:
		return nil // Successfully queued
	default:
		// Queue full - DROP FRAME to prevent memory leak instead of blocking
		debugMsgVerbose("FFMPEG_SEQUENCE", fmt.Sprintf("Write queue full - DROPPING frame %d to prevent memory leak (remote FFmpeg can't keep up)", frameNum))
		return fmt.Errorf("write queue full - frame dropped")
	}
}

// timedWriteWorker handles sequential writing to maintain frame order with recovery
func (m *FFmpegManager) timedWriteWorker() {
	defer m.writeWorker.Done()

	debugMsgVerbose("FFMPEG_SEQUENCE", "Sequential write worker started for frame order preservation with 15-frame timeout")

	expectedSequence := int64(1)                // Start expecting frame 1
	pendingFrames := make(map[int64]TimedFrame) // Buffer for out-of-order frames
	maxFrameWait := 15                          // Maximum frames to wait for missing sequence

	for {
		select {
		case timedFrame := <-m.writeQueue:
			// BOUNDED BUFFER: Check if we're at capacity before adding
			m.pendingMu.Lock()
			currentPendingCount := len(pendingFrames)

			if currentPendingCount >= maxPendingFrames {
				// Drop oldest frame to make room
				oldestFrame := m.getMinFrameNumber(pendingFrames)
				delete(pendingFrames, oldestFrame)
				debugMsgVerbose("FFMPEG_SEQUENCE", fmt.Sprintf("Pending buffer full (%d/%d) - dropped frame %d to prevent memory leak",
					currentPendingCount, maxPendingFrames, oldestFrame))
			}

			// Store frame in pending buffer
			pendingFrames[timedFrame.frameNum] = timedFrame
			m.pendingFrameCount = len(pendingFrames)
			m.pendingMu.Unlock()

			// Check for missing frames and implement timeout recovery
			if len(pendingFrames) > 0 {
				minPendingFrame := m.getMinFrameNumber(pendingFrames)

				// NEW: 15-frame timeout recovery - more aggressive than 60-frame gap detection
				frameGap := minPendingFrame - expectedSequence
				if frameGap >= int64(maxFrameWait) {
					debugMsgVerbose("FFMPEG_SEQUENCE", fmt.Sprintf("15-frame timeout: skipping from %d to %d (%d frames missing due to RTSP hiccup)",
						expectedSequence, minPendingFrame, frameGap))

					// Clean up any frames before the new sequence point
					m.pendingMu.Lock()
					for frameNum := range pendingFrames {
						if frameNum < minPendingFrame {
							delete(pendingFrames, frameNum)
						}
					}
					m.pendingFrameCount = len(pendingFrames)
					m.pendingMu.Unlock()

					expectedSequence = minPendingFrame
				}
			}

			// Process frames in sequence order
			for {
				if frame, exists := pendingFrames[expectedSequence]; exists {
					// Write frame to FFmpeg in correct sequence (no rate limiting)
					writeStart := time.Now()
					_, err := m.stdinPipe.Write(frame.data)
					writeTime := time.Since(writeStart)

					// Update debug info
					m.UpdateDebugInfo(len(frame.data), writeTime, err)

					if err != nil {
						debugMsgVerbose("FFMPEG_SEQUENCE", fmt.Sprintf("Write error for frame %d: %v", frame.frameNum, err))
						return // Exit worker on error
					}

					// Debug info every 300 frames (10 seconds at 30fps)
					if expectedSequence%300 == 0 {
						debugMsgVerbose("FFMPEG_SEQUENCE", fmt.Sprintf("Frame %d processed, %d frames pending",
							expectedSequence, len(pendingFrames)))
					}

					// Clean up and advance sequence
					m.pendingMu.Lock()
					delete(pendingFrames, expectedSequence)
					m.pendingFrameCount = len(pendingFrames)
					m.pendingMu.Unlock()
					expectedSequence++
				} else {
					// Next frame in sequence not available yet
					break
				}
			}

		case <-m.stopChan:
			debugMsgVerbose("FFMPEG_SEQUENCE", fmt.Sprintf("Sequential write worker stopping (processed up to frame %d)", expectedSequence-1))

			// Flush any remaining pending frames in order
			for seq := expectedSequence; seq < expectedSequence+int64(len(pendingFrames)); seq++ {
				if frame, exists := pendingFrames[seq]; exists {
					m.stdinPipe.Write(frame.data)
					debugMsgVerbose("FFMPEG_SEQUENCE", fmt.Sprintf("Flushed frame %d during shutdown", seq))
				}
			}
			return
		}
	}
}

// getMinFrameNumber finds the lowest frame number in the pending frames map
func (m *FFmpegManager) getMinFrameNumber(pendingFrames map[int64]TimedFrame) int64 {
	if len(pendingFrames) == 0 {
		return 0
	}

	var minFrame int64 = -1
	for frameNum := range pendingFrames {
		if minFrame == -1 || frameNum < minFrame {
			minFrame = frameNum
		}
	}
	return minFrame
}

// UpdateDebugInfo updates the debug information
func (m *FFmpegManager) UpdateDebugInfo(frameSize int, writeTime time.Duration, writeErr error) {
	m.debugMu.Lock()
	defer m.debugMu.Unlock()
	m.lastFrameSize = frameSize
	m.lastFrameTime = time.Now()
	m.lastWriteTime = writeTime
	m.lastWriteError = writeErr
}

// UpdateFlushInfo updates the flush debug information
func (m *FFmpegManager) UpdateFlushInfo(err error) {
	m.debugMu.Lock()
	defer m.debugMu.Unlock()
	m.lastFlushTime = time.Now()
	m.lastFlushError = err
}

// monitor watches the FFmpeg process and handles crashes
func (m *FFmpegManager) monitor() {
	debugMsg("FFMPEG_MONITOR", fmt.Sprintf("Starting FFmpeg process monitor (PID: %d)", m.cmd.Process.Pid))

	// Enhanced monitoring with multiple checks
	go func() {
		// Check process status every 50ms (faster detection)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		lastWriteCheck := time.Now()
		writeStallThreshold := 5 * time.Second

		for {
			select {
			case <-ticker.C:
				// Check if process still exists
				if m.cmd.Process != nil {
					// Try to signal the process with signal 0 (no-op signal)
					// This will return an error if the process doesn't exist
					if err := m.cmd.Process.Signal(syscall.Signal(0)); err != nil {
						debugMsg("FFMPEG_MONITOR", fmt.Sprintf("Process health check failed: %v", err))
						debugMsg("FFMPEG_MONITOR", "FFmpeg process appears to be dead")
						// Process is dead, trigger immediate exit
						m.triggerEmergencyShutdown("process health check failed")
						return
					}

					// Additional check: see if we haven't written frames recently
					// But only after a startup grace period
					timeSinceStart := time.Since(m.startTime)
					startupGracePeriod := 10 * time.Second

					if timeSinceStart > startupGracePeriod {
						m.debugMu.Lock()
						timeSinceLastWrite := time.Since(m.lastFrameTime)
						lastWriteErr := m.lastWriteError
						m.debugMu.Unlock()

						// If we haven't written a frame in 5 seconds after startup period
						if timeSinceLastWrite > writeStallThreshold {
							debugMsg("FFMPEG_MONITOR", fmt.Sprintf("No frame writes for %v (startup was %v ago) - FFmpeg may be stalled",
								timeSinceLastWrite, time.Since(m.startTime)))
							m.triggerEmergencyShutdown("write stall detected")
							return
						}

						// If last write had an error, FFmpeg is likely dead
						if lastWriteErr != nil && time.Since(lastWriteCheck) > 1*time.Second {
							debugMsg("FFMPEG_MONITOR", fmt.Sprintf("Recent write error detected: %v", lastWriteErr))
							m.triggerEmergencyShutdown("write error detected")
							return
						}
					}

					lastWriteCheck = time.Now()
				}
			}
		}
	}()

	// Wait for the process to exit
	state, err := m.cmd.Process.Wait()

	// Log immediately when process exits
	debugMsg("FFMPEG_MONITOR", fmt.Sprintf("FFmpeg process has exited (PID: %d)", m.cmd.Process.Pid))

	if err != nil {
		debugMsg("FFMPEG_ERROR", fmt.Sprintf("Process wait error: %v", err))
	}

	// Capture process state
	m.debugMu.Lock()
	m.lastProcessState = state
	m.debugMu.Unlock()

	// Determine exit reason
	var exitReason string
	if state != nil {
		if state.Success() {
			exitReason = "normal exit"
		} else {
			exitReason = fmt.Sprintf("abnormal exit (code: %d)", state.ExitCode())
		}
	} else {
		exitReason = "unknown exit"
	}

	// Trigger shutdown
	m.triggerEmergencyShutdown(exitReason)
}

// triggerEmergencyShutdown handles the emergency shutdown process
func (m *FFmpegManager) triggerEmergencyShutdown(reason string) {
	// Dump debug information
	debugMsg("FFMPEG_CRASH", fmt.Sprintf("FFmpeg process crashed/exited. Reason: %s", reason))
	debugMsg("FFMPEG_CRASH", "Dumping debug information:")

	m.debugMu.Lock()
	state := m.lastProcessState
	if state != nil {
		debugMsg("SYSTEM_DEBUG", "Process State:")
		debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Exit Code: %d", state.ExitCode()))
		debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Success: %v", state.Success()))
		debugMsg("SYSTEM_DEBUG", fmt.Sprintf("System Time: %v", state.SystemTime()))
		debugMsg("SYSTEM_DEBUG", fmt.Sprintf("User Time: %v", state.UserTime()))
		if state.SysUsage() != nil {
			debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Sys Usage: %v", state.SysUsage()))
		}
	} else {
		debugMsg("SYSTEM_DEBUG", "Process State: NULL")
	}

	debugMsg("SYSTEM_DEBUG", "Last Frame Info:")
	debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Size: %d bytes", m.lastFrameSize))
	debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Time: %v", m.lastFrameTime))
	debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Write Duration: %v", m.lastWriteTime))
	if m.lastWriteError != nil {
		debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Write Error: %v", m.lastWriteError))
	}
	debugMsg("SYSTEM_DEBUG", "Last Flush Info:")
	debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Time: %v", m.lastFlushTime))
	if m.lastFlushError != nil {
		debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Flush Error: %v", m.lastFlushError))
	}
	m.debugMu.Unlock()

	// Dump system information
	debugMsg("SYSTEM_DEBUG", "System Info:")
	debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Goroutines: %d", runtime.NumGoroutine()))
	debugMsg("SYSTEM_DEBUG", "Memory Stats:")
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Alloc: %v", mem.Alloc))
	debugMsg("SYSTEM_DEBUG", fmt.Sprintf("TotalAlloc: %v", mem.TotalAlloc))
	debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Sys: %v", mem.Sys))
	debugMsg("SYSTEM_DEBUG", fmt.Sprintf("NumGC: %v", mem.NumGC))

	// Check commentary file status
	if info, err := os.Stat("/tmp/commentary.txt"); err != nil {
		debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Commentary File: ERROR - %v", err))
	} else {
		debugMsg("SYSTEM_DEBUG", fmt.Sprintf("Commentary File: %d bytes", info.Size()))
	}

	// Signal the stop channel to notify other parts of the application
	debugMsg("FFMPEG_CRASH", "Signaling application shutdown via stop channel")
	select {
	case m.stopChan <- struct{}{}:
		debugMsg("FFMPEG_CRASH", "Stop signal sent successfully")
	default:
		debugMsg("FFMPEG_CRASH", "Stop channel was full or closed\n")
	}

	// Give a moment for graceful shutdown
	time.Sleep(100 * time.Millisecond)

	// Force exit the application with maximum urgency
	debugMsg("FFMPEG_CRASH", "Force exiting application with code 3 (FFmpeg crash)")
	debugMsg("FFMPEG_CRASH", "Emergency shutdown initiated - no further processing")

	// Kill any remaining FFmpeg processes as a final cleanup
	exec.Command("pkill", "-9", "ffmpeg").Run()

	// Multiple exit attempts to ensure we actually exit
	go func() {
		time.Sleep(50 * time.Millisecond)
		debugMsg("FFMPEG_CRASH", "Secondary exit attempt")
		os.Exit(3)
	}()

	os.Exit(3)
}

// parsePTZURL parses a PTZ URL and returns the components needed for PTZ controller
func parsePTZURL(ptzURL string) (host, port, username, password string, err error) {
	u, err := url.Parse(ptzURL)
	if err != nil {
		return "", "", "", "", fmt.Errorf("invalid PTZ URL: %v", err)
	}

	// Extract host and port
	host = u.Hostname()
	port = u.Port()
	if port == "" {
		port = "80" // Default HTTP port
	}

	// Extract username and password
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}

	if host == "" {
		return "", "", "", "", fmt.Errorf("PTZ URL must include host")
	}
	if username == "" || password == "" {
		return "", "", "", "", fmt.Errorf("PTZ URL must include username and password")
	}

	return host, port, username, password, nil
}

// Add GPU memory monitoring and health checks after line 1630
// ... existing code ...

// GPUMemoryMonitor monitors GPU memory usage and alerts on high usage
type GPUMemoryMonitor struct {
	enabled            bool
	lastMemoryCheck    time.Time
	criticalThreshold  float64 // Memory usage percentage that triggers warnings
	emergencyThreshold float64 // Memory usage percentage that triggers emergency actions
}

// NewGPUMemoryMonitor creates a new GPU memory monitor
func NewGPUMemoryMonitor() *GPUMemoryMonitor {
	return &GPUMemoryMonitor{
		enabled:            true,
		criticalThreshold:  85.0, // Warn at 85% GPU memory usage
		emergencyThreshold: 95.0, // Emergency action at 95% usage
	}
}

// CheckGPUMemory monitors GPU memory usage and takes preventive action
func (gmm *GPUMemoryMonitor) CheckGPUMemory() error {
	if !gmm.enabled {
		return nil
	}

	// Only check every 30 seconds to avoid overhead
	if time.Since(gmm.lastMemoryCheck) < 30*time.Second {
		return nil
	}
	gmm.lastMemoryCheck = time.Now()

	// Query GPU memory usage
	cmd := exec.Command("nvidia-smi", "--query-gpu=memory.used,memory.total,temperature.gpu", "--format=csv,noheader,nounits")
	output, err := cmd.Output()
	if err != nil {
		debugMsg("GPU_MONITOR", fmt.Sprintf("Failed to query GPU memory: %v", err))
		return err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return fmt.Errorf("no GPU memory data returned")
	}

	// Parse first GPU memory usage
	fields := strings.Split(lines[0], ", ")
	if len(fields) < 3 {
		return fmt.Errorf("invalid GPU memory data format")
	}

	memoryUsed, err := strconv.ParseFloat(strings.TrimSpace(fields[0]), 64)
	if err != nil {
		return fmt.Errorf("failed to parse memory used: %v", err)
	}

	memoryTotal, err := strconv.ParseFloat(strings.TrimSpace(fields[1]), 64)
	if err != nil {
		return fmt.Errorf("failed to parse memory total: %v", err)
	}

	temperature, err := strconv.ParseFloat(strings.TrimSpace(fields[2]), 64)
	if err != nil {
		return fmt.Errorf("failed to parse temperature: %v", err)
	}

	memoryPercent := (memoryUsed / memoryTotal) * 100

	// Log current status
	debugMsg("GPU_MONITOR", fmt.Sprintf("GPU Memory: %.0f/%.0fMB (%.1f%%), Temp: %.0f°C",
		memoryUsed, memoryTotal, memoryPercent, temperature))

	// Check thresholds and take action
	if memoryPercent >= gmm.emergencyThreshold {
		debugMsg("GPU_EMERGENCY", fmt.Sprintf("🚨 CRITICAL: GPU memory at %.1f%% (≥%.1f%%) - triggering emergency GC",
			memoryPercent, gmm.emergencyThreshold))

		// Force immediate garbage collection
		runtime.GC()
		runtime.GC() // Double GC for more aggressive cleanup

		// Wait a moment then recheck
		time.Sleep(1 * time.Second)
		return gmm.CheckGPUMemory()

	} else if memoryPercent >= gmm.criticalThreshold {
		debugMsg("GPU_WARNING", fmt.Sprintf("⚠️  WARNING: GPU memory at %.1f%% (≥%.1f%%) - monitoring closely",
			memoryPercent, gmm.criticalThreshold))

		// Force garbage collection as preventive measure
		runtime.GC()
	}

	// Check temperature
	if temperature >= 85.0 {
		debugMsg("GPU_TEMP_WARNING", fmt.Sprintf("🌡️  WARNING: GPU temperature at %.0f°C (≥85°C)", temperature))
	}

	return nil
}

// RTMPHealthChecker monitors RTMP server health
type RTMPHealthChecker struct {
	enabled             bool
	lastHealthCheck     time.Time
	consecutiveFailures int
	maxFailures         int
}

// FFmpegMemoryMonitor monitors FFmpeg process memory and resource usage
type FFmpegMemoryMonitor struct {
	enabled           bool
	lastMemoryCheck   time.Time
	ffmpegManager     *FFmpegManager
	criticalMemoryMB  float64 // Memory usage in MB that triggers warnings
	emergencyMemoryMB float64 // Memory usage in MB that triggers emergency actions
}

// NewRTMPHealthChecker creates a new RTMP health checker
func NewRTMPHealthChecker() *RTMPHealthChecker {
	return &RTMPHealthChecker{
		enabled:     true,
		maxFailures: 3, // Allow 3 consecutive failures before taking action
	}
}

// CheckRTMPHealth verifies RTMP server is responsive
func (rhc *RTMPHealthChecker) CheckRTMPHealth() error {
	if !rhc.enabled {
		return nil
	}

	// Only check every 60 seconds
	if time.Since(rhc.lastHealthCheck) < 60*time.Second {
		return nil
	}
	rhc.lastHealthCheck = time.Now()

	// Check if RTMP port is listening
	conn, err := net.DialTimeout("tcp", "localhost:1935", 5*time.Second)
	if err != nil {
		rhc.consecutiveFailures++
		debugMsg("RTMP_HEALTH", fmt.Sprintf("❌ RTMP server not responding (failure %d/%d): %v",
			rhc.consecutiveFailures, rhc.maxFailures, err))

		if rhc.consecutiveFailures >= rhc.maxFailures {
			return fmt.Errorf("RTMP server failed health check %d consecutive times", rhc.consecutiveFailures)
		}
		return nil
	}
	conn.Close()

	// Reset failure count on success
	if rhc.consecutiveFailures > 0 {
		debugMsg("RTMP_HEALTH", "✅ RTMP server recovered")
		rhc.consecutiveFailures = 0
	}

	return nil
}

// NewFFmpegMemoryMonitor creates a new FFmpeg memory monitor
func NewFFmpegMemoryMonitor(ffmpegManager *FFmpegManager) *FFmpegMemoryMonitor {
	return &FFmpegMemoryMonitor{
		enabled:           true,
		ffmpegManager:     ffmpegManager,
		criticalMemoryMB:  512.0,  // Warn at 512MB FFmpeg memory usage
		emergencyMemoryMB: 1024.0, // Emergency action at 1GB usage
	}
}

// CheckFFmpegMemory monitors FFmpeg process memory usage and takes preventive action
func (fmm *FFmpegMemoryMonitor) CheckFFmpegMemory() error {
	if !fmm.enabled || fmm.ffmpegManager == nil {
		return nil
	}

	// Only check every 30 seconds to avoid overhead
	if time.Since(fmm.lastMemoryCheck) < 30*time.Second {
		return nil
	}
	fmm.lastMemoryCheck = time.Now()

	// Get FFmpeg process PID
	fmm.ffmpegManager.mu.Lock()
	cmd := fmm.ffmpegManager.cmd
	fmm.ffmpegManager.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("FFmpeg process not running")
	}

	pid := cmd.Process.Pid

	// Query process memory usage using ps command
	psCmd := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "pid,ppid,rss,vsz,pcpu,pmem,etime,comm", "--no-headers")
	output, err := psCmd.Output()
	if err != nil {
		debugMsg("FFMPEG_MONITOR", fmt.Sprintf("Failed to query FFmpeg process stats: %v", err))
		return err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return fmt.Errorf("FFmpeg process PID %d not found", pid)
	}

	// Parse ps output: PID PPID RSS VSZ %CPU %MEM ELAPSED COMMAND
	fields := strings.Fields(lines[0])
	if len(fields) < 8 {
		return fmt.Errorf("invalid ps output format: %s", lines[0])
	}

	// RSS is in KB, convert to MB
	rssKB, err := strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return fmt.Errorf("failed to parse RSS memory: %v", err)
	}
	memoryMB := rssKB / 1024.0

	// VSZ is virtual memory in KB
	vszKB, err := strconv.ParseFloat(fields[3], 64)
	if err != nil {
		return fmt.Errorf("failed to parse VSZ memory: %v", err)
	}
	virtualMB := vszKB / 1024.0

	// CPU percentage
	cpuPercent, err := strconv.ParseFloat(fields[4], 64)
	if err != nil {
		debugMsg("FFMPEG_MONITOR", fmt.Sprintf("Failed to parse CPU usage: %v", err))
		cpuPercent = 0.0
	}

	// Memory percentage
	memPercent, err := strconv.ParseFloat(fields[5], 64)
	if err != nil {
		debugMsg("FFMPEG_MONITOR", fmt.Sprintf("Failed to parse memory percentage: %v", err))
		memPercent = 0.0
	}

	// Elapsed time
	elapsedTime := fields[6]

	// Log current FFmpeg process status
	debugMsg("FFMPEG_MONITOR", fmt.Sprintf("FFmpeg Process (PID %d): Memory %.1fMB/%.1fMB (%.1f%%), CPU %.1f%%, Runtime %s",
		pid, memoryMB, virtualMB, memPercent, cpuPercent, elapsedTime))

	// Check memory thresholds and take action
	if memoryMB >= fmm.emergencyMemoryMB {
		debugMsg("FFMPEG_EMERGENCY", fmt.Sprintf("🚨 CRITICAL: FFmpeg memory at %.1fMB (≥%.1fMB) - may cause system instability",
			memoryMB, fmm.emergencyMemoryMB))

		// Force garbage collection to free up system memory
		runtime.GC()
		runtime.GC()

		debugMsg("FFMPEG_EMERGENCY", "Triggered emergency garbage collection to free system memory")

	} else if memoryMB >= fmm.criticalMemoryMB {
		debugMsg("FFMPEG_WARNING", fmt.Sprintf("⚠️  WARNING: FFmpeg memory at %.1fMB (≥%.1fMB) - monitoring closely",
			memoryMB, fmm.criticalMemoryMB))

		// Preventive garbage collection
		runtime.GC()
	}

	// Check for high CPU usage (may indicate processing bottleneck)
	if cpuPercent >= 90.0 {
		debugMsg("FFMPEG_CPU_WARNING", fmt.Sprintf("🔥 HIGH CPU: FFmpeg at %.1f%% CPU usage - may indicate bottleneck", cpuPercent))
	}

	// Get additional FFmpeg-specific stats from the manager
	fmm.ffmpegManager.debugMu.Lock()
	lastFrameSize := fmm.ffmpegManager.lastFrameSize
	lastWriteTime := fmm.ffmpegManager.lastWriteTime
	lastWriteError := fmm.ffmpegManager.lastWriteError
	fmm.ffmpegManager.debugMu.Unlock()

	// Log FFmpeg throughput statistics
	throughputMBps := 0.0
	if lastWriteTime > 0 {
		frameSizeMB := float64(lastFrameSize) / (1024.0 * 1024.0)
		throughputMBps = frameSizeMB / lastWriteTime.Seconds()
	}

	debugMsg("FFMPEG_THROUGHPUT", fmt.Sprintf("Last Frame: %.2fMB, Write Time: %v, Throughput: %.2fMB/s",
		float64(lastFrameSize)/(1024.0*1024.0), lastWriteTime, throughputMBps))

	if lastWriteError != nil {
		debugMsg("FFMPEG_ERROR", fmt.Sprintf("Last Write Error: %v", lastWriteError))
	}

	return nil
}

// parseTrackingFlags parses the comma-separated tracking priority flags
func parseTrackingFlags() {
	// Parse P1 tracking objects (primary targets)
	p1TrackAll = false
	if strings.ToLower(strings.TrimSpace(*p1Track)) == "all" {
		p1TrackAll = true
		p1TrackList = []string{} // Empty list when "all" is specified
	} else if *p1Track == "" {
		p1TrackList = []string{}
	} else {
		p1TrackList = strings.Split(strings.TrimSpace(*p1Track), ",")
		// Clean up whitespace
		for i, obj := range p1TrackList {
			p1TrackList[i] = strings.TrimSpace(obj)
		}
	}

	// Parse P2 tracking objects (enhancement objects)
	p2TrackAll = false
	if strings.ToLower(strings.TrimSpace(*p2Track)) == "all" {
		p2TrackAll = true
		p2TrackList = []string{} // Empty list when "all" is specified
	} else if *p2Track == "" {
		p2TrackList = []string{}
	} else {
		p2TrackList = strings.Split(strings.TrimSpace(*p2Track), ",")
		// Clean up whitespace
		for i, obj := range p2TrackList {
			p2TrackList[i] = strings.TrimSpace(obj)
		}
	}

	// Debug output
	if p1TrackAll {
		fmt.Printf("[TRACKING_CONFIG] P1 (Primary): ALL detected objects\n")
	} else {
		fmt.Printf("[TRACKING_CONFIG] P1 (Primary): %v\n", p1TrackList)
	}
	if p2TrackAll {
		fmt.Printf("[TRACKING_CONFIG] P2 (Enhancement): ALL non-P1 objects\n")
	} else {
		fmt.Printf("[TRACKING_CONFIG] P2 (Enhancement): %v\n", p2TrackList)
	}
}

// validateJpegFlags validates JPEG saving flag combinations and creates directories
func validateJpegFlags() error {
	jpegFlagsUsed := *preOverlayJpg || *postOverlayJpg
	jpgPathProvided := *jpgPath != ""

	// Error if JPEG flags used without jpg-path
	if jpegFlagsUsed && !jpgPathProvided {
		return fmt.Errorf("JPEG flags (-pre-overlay-jpg, -post-overlay-jpg) require -jpg-path to be specified")
	}

	// Error if jpg-path provided but no JPEG flags
	if jpgPathProvided && !jpegFlagsUsed {
		return fmt.Errorf("-jpg-path specified but no JPEG flags (-pre-overlay-jpg, -post-overlay-jpg) enabled")
	}

	// Create directory if JPEG saving is enabled
	if jpegFlagsUsed && jpgPathProvided {
		if err := os.MkdirAll(*jpgPath, 0755); err != nil {
			return fmt.Errorf("failed to create JPEG directory '%s': %v", *jpgPath, err)
		}
		fmt.Printf("[JPEG_CONFIG] Saving JPEGs to: %s\n", *jpgPath)
		if *preOverlayJpg {
			fmt.Printf("[JPEG_CONFIG] Pre-overlay frames: ENABLED\n")
		}
		if *postOverlayJpg {
			fmt.Printf("[JPEG_CONFIG] Post-overlay frames: ENABLED\n")
		}
	} else {
		fmt.Printf("[JPEG_CONFIG] JPEG saving: DISABLED\n")
	}

	return nil
}

// saveJpegFrame saves a frame as JPEG to the specified directory with timestamp naming
// Files are organized into subdirectories by date and hour (12-hour format)
func saveJpegFrame(frame gocv.Mat, directory, prefix string, detectionCount int) {
	if directory == "" {
		return
	}

	now := time.Now()

	// Create subdirectory name: 2025-01-01_03PM format
	hour := now.Hour()
	hour12 := hour % 12
	if hour12 == 0 {
		hour12 = 12
	}
	ampm := "AM"
	if hour >= 12 {
		ampm = "PM"
	}
	subdirName := fmt.Sprintf("%s_%02d%s", now.Format("2006-01-02"), hour12, ampm)

	// Create the full subdirectory path
	subdir := filepath.Join(directory, subdirName)

	// Create subdirectory if it doesn't exist
	if err := os.MkdirAll(subdir, 0755); err != nil {
		debugMsg("JPEG_ERROR", fmt.Sprintf("Failed to create subdirectory %s: %v", subdir, err))
		return
	}

	// Generate filename with timestamp
	timestamp := now.Format("20060102_150405.000")
	filename := fmt.Sprintf("%s_%s_detections_%d.jpg", timestamp, prefix, detectionCount)
	filepath := filepath.Join(subdir, filename)

	// Save the frame as JPEG (silently on success, error on failure)
	if !gocv.IMWrite(filepath, frame) {
		debugMsg("JPEG_ERROR", fmt.Sprintf("Failed to save %s frame: %s", prefix, filename))
	}
}

// isP1Object checks if an object class is a P1 (primary tracking) target
func isP1Object(className string) bool {
	// If P1 is set to "all", any object is P1
	if p1TrackAll {
		return true
	}

	// Otherwise check explicit P1 list
	for _, p1Class := range p1TrackList {
		if className == p1Class {
			return true
		}
	}
	return false
}

// isP2Object checks if an object class is a P2 (enhancement) target
func isP2Object(className string) bool {
	// If P1 is set to "all", no objects can be P2 (all objects are already P1)
	if p1TrackAll {
		return false
	}

	// If P2 is set to "all", any non-P1 object is P2
	if p2TrackAll {
		return !isP1Object(className)
	}

	// Otherwise check explicit P2 list
	for _, p2Class := range p2TrackList {
		if className == p2Class {
			return true
		}
	}
	return false
}

func main() {
	// Parse command-line flags
	flag.Parse()

	// Parse tracking priority configurations
	parseTrackingFlags()

	// Initialize global confidence thresholds
	globalP1MinConfidence = *p1MinConfidence
	globalP2MinConfidence = *p2MinConfidence
	debugMsg("CONFIDENCE_CONFIG", fmt.Sprintf("P1 confidence threshold: %.2f (%.0f%%) | P2 confidence threshold: %.2f (%.0f%%)",
		globalP1MinConfidence, globalP1MinConfidence*100, globalP2MinConfidence, globalP2MinConfidence*100))

	// Validate JPEG saving configuration
	if err := validateJpegFlags(); err != nil {
		fmt.Printf("❌ Configuration Error: %v\n", err)
		fmt.Println("\n💡 JPEG Flag Usage:")
		fmt.Println("  Save pre-overlay frames:  -jpg-path=/tmp/frames -pre-overlay-jpg")
		fmt.Println("  Save post-overlay frames: -jpg-path=/tmp/frames -post-overlay-jpg")
		fmt.Println("  Save both types:          -jpg-path=/tmp/frames -pre-overlay-jpg -post-overlay-jpg")
		os.Exit(1)
	}

	// Show usage examples for -h flag
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Println("\n🎯 NOLO - Never Only Look Once")
		fmt.Println("================================================================")
		fmt.Println("\n💡 USAGE EXAMPLES:")
		fmt.Println("\n  Basic Operation:")
		fmt.Println("    ./NOLO -input rtsp://admin:pass@192.168.0.59:554/Streaming/Channels/201 \\")
		fmt.Println("               -ptzinput http://admin:pass@192.168.0.59:80/")
		fmt.Println("\n  Debug Mode (with overlays and detailed logs):")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -debug")
		fmt.Println("\n  Verbose Debug Mode (includes detailed YOLO, calibration, and tracking calculations):")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -debug -debug-verbose")
		fmt.Println("\n  Single Track Debugging (exit after first lock):")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -debug -exit-on-first-track")
		fmt.Println("\n  With PIP Zoom Overlay:")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -debug -pip")
		fmt.Println("\n  Clean View (no PIP zoom overlay - default):")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -debug")
		fmt.Println("\n  📸 JPEG FRAME SAVING (Auto-organized by date/hour):")
		fmt.Println("  Save clean frames (before overlays):")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -jpg-path=/tmp/clean -pre-overlay-jpg")
		fmt.Println("  Save processed frames (with all overlays):")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -jpg-path=/tmp/processed -post-overlay-jpg")
		fmt.Println("  Save both types for complete analysis:")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -jpg-path=/tmp/analysis -pre-overlay-jpg -post-overlay-jpg")
		fmt.Println("  Debug mode with JPEG saving:")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -debug -jpg-path=/tmp/debug -post-overlay-jpg")
		fmt.Println("  📁 Files are automatically organized into subdirectories: /path/2025-01-01_03PM/")
		fmt.Println("\n  🎨 OVERLAY CONTROL:")
		fmt.Println("  Clean view (no overlays):")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL]")
		fmt.Println("  Status info only (lightweight monitoring):")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -status-overlay")
		fmt.Println("  Tracking visualization only:")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -target-overlay")
		fmt.Println("  Tracking visualization (tracked object only):")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -target-overlay -target-display-tracked")
		fmt.Println("  Debug terminal display:")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -debug -terminal-overlay")
		fmt.Println("  Complete overlay suite:")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -status-overlay -target-overlay -terminal-overlay")
		fmt.Println("  Debug logging without visual clutter:")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -debug")
		fmt.Println("\n  YOLO Input Analysis (save actual YOLO blob images):")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -YOLOdebug")
		fmt.Println("\n  Color Masking (remove green water areas):")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -maskcolors=6d9755,243314 -masktolerance=50")
		fmt.Println("\n  Combined YOLO Debug with Color Masking:")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -YOLOdebug -maskcolors=6d9755,243314")
		fmt.Println("\n  Confidence Thresholds (P1=boats, P2=people):")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] -p1-min-confidence=0.30 -p2-min-confidence=0.20")
		fmt.Println("\n  Safe Operation with Default River Monitoring Limits:")
		fmt.Println("    ./NOLO -input [URL] -ptzinput [URL] \\")
		fmt.Println("               -min-pan=1000 -max-pan=2392 -min-tilt=100 -max-tilt=650")
		fmt.Println("\n🔧 FLAGS:")
		flag.Usage()
		fmt.Println("\n🛡️ SAFETY NOTES:")
		fmt.Println("  • PTZ limits prevent camera from moving into unsafe positions")
		fmt.Println("  • Omit limit flags for full hardware range (default behavior)")
		fmt.Println("  • Use min/max flags to set precise camera coordinate limits")
		fmt.Println("  • Camera coordinates: Pan(0-3590), Tilt(0-900), Zoom(10-120)")
		fmt.Println("  • Your previous defaults: -min-pan=1000 -max-pan=2392 -min-tilt=100 -max-tilt=650")
		fmt.Println("  • Debug mode saves images to /tmp/debugMode/ for analysis")
		fmt.Println("\n📁 DEBUG OUTPUT LOCATIONS:")
		fmt.Println("  • Debug images: /tmp/debugMode/")
		fmt.Println("  • YOLO blob images: /tmp/YOLOdebug/ (use -YOLOdebug flag)")
		fmt.Println("  • Integrated tracking logs: [objectID].txt (contains both structured session data + all debug messages)")
		fmt.Println("  • Object frames: [objectID]_[pipeline]_[counter].jpg (postoverlay, overlay - only for actively tracked objects)")
		fmt.Println("")
		os.Exit(0)
	}

	// Validate required input flags
	if *inputStream == "" {
		fmt.Fprintf(os.Stderr, "Error: -input flag is required\n\n")
		fmt.Println("Use -h for usage examples and flag descriptions")
		os.Exit(1)
	}

	if *ptzInput == "" {
		fmt.Fprintf(os.Stderr, "Error: -ptzinput flag is required\n\n")
		fmt.Println("Use -h for usage examples and flag descriptions")
		os.Exit(1)
	}

	// Parse PTZ URL
	ptzHost, ptzPort, ptzUser, ptzPass, err := parsePTZURL(*ptzInput)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing PTZ URL: %v\n", err)
		os.Exit(1)
	}

	startMatStatsPrinter()

	// Initialize components
	ptzController := ptz.NewHikvisionController(ptzHost, ptzPort, ptzUser, ptzPass)
	renderer := overlay.NewRenderer()
	stats := NewPipelineStats()
	debugManager := NewDebugManager(*debugMode)

	// Initialize health monitoring systems
	gpuMonitor := NewGPUMemoryMonitor()
	rtmpChecker := NewRTMPHealthChecker()
	debugMsg("HEALTH_MONITOR", "Initialized GPU memory and RTMP health monitoring")

	// Initialize global unified debug logger
	globalDebugLogger = NewDebugLogger(*debugMode)
	defer globalDebugLogger.Close()

	// Test the new unified debug system
	debugMsg("SYSTEM", "🚀 Unified debug logger initialized successfully")
	debugMsg("TEST", "Testing boat-specific logging", "test_boat_123")

	// Log color masking startup info
	if *maskColors != "" {
		debugMsg("MASK", fmt.Sprintf("🎨 Color masking enabled: %s (tolerance: %d)", *maskColors, *maskTolerance))
	}

	// Start PTZ controller
	ptzController.Start()
	defer ptzController.Stop()

	// Before opening the RTSP stream, set OpenCV/FFmpeg options for LOW LATENCY capture
	os.Setenv("OPENCV_FFMPEG_CAPTURE_OPTIONS", "rtsp_transport;tcp|buffer_size;65536|stimeout;5000000")

	// RTSP stream URL from command-line flag
	streamURL := *inputStream

	debugMsg("DEBUG", fmt.Sprintf("Opening RTSP stream: %s", streamURL))
	webcam, err := gocv.VideoCaptureFile(streamURL)
	if err != nil {
		debugMsg("ERROR", fmt.Sprintf("Error opening video stream: %v", err))
		return
	}
	// Minimize OpenCV buffer size for real-time RTSP streaming
	webcam.Set(gocv.VideoCaptureBufferSize, 1)
	defer webcam.Close()
	debugMsg("DEBUG", "RTSP stream opened successfully.")

	// Read the first frame to determine size
	img := gocv.NewMat()
	if ok := webcam.Read(&img); !ok || img.Empty() {
		debugMsg("ERROR", "Could not read first frame to determine size.")
		return
	}
	pictureWidth, pictureHeight = img.Cols(), img.Rows()
	pictureSize = fmt.Sprintf("%dx%d", pictureWidth, pictureHeight)
	debugMsg("INFO", fmt.Sprintf("Detected frame size: %s", pictureSize))
	debugMsg("DEBUG", fmt.Sprintf("Frame dimensions - Width: %d, Height: %d", pictureWidth, pictureHeight))

	// Set the actual frame dimensions on the PTZ controller
	ptzController.SetFrameDimensions(pictureWidth, pictureHeight)
	debugMsg("DEBUG", fmt.Sprintf("PTZ Controller updated - Width: %d, Height: %d",
		ptzController.GetFrameWidth(), ptzController.GetFrameHeight()))
	img.Close()

	// Debug mode controlled by command-line flag
	debugMsg("DEBUG", fmt.Sprintf("Debug mode: %v (use -debug flag to enable detailed tracking logs and overlay)", *debugMode))

	// Initialize spatial tracking system with backward compatibility
	spatialIntegration := tracking.NewSpatialIntegration(ptzController, pictureWidth, pictureHeight, globalDebugLogger, p1TrackList, p2TrackList, p1TrackAll, p2TrackAll, globalP1MinConfidence, globalP2MinConfidence)

	// Set up debug references for dual logging (terminal + files)
	spatialIntegration.SetDebugReferences(debugManager, renderer)

	// Log spatial tracking initialization
	debugMsg("SPATIAL", fmt.Sprintf("Initialized spatial tracking system (Frame: %dx%d)", pictureWidth, pictureHeight))

	// Initialize Camera State Manager and CRITICAL DEBUG PIPELINE
	fmt.Printf("[MAIN_INIT] 🔗 Connecting debug functions to all packages...\n")
	ptz.SetDebugFunction(debugMsg)                           // Provide debug function to PTZ package
	overlay.SetDebugFunction(debugMsg)                       // Provide debug function to overlay package
	overlay.SetDebugVerboseFunction(debugMsgVerbose)         // Provide verbose debug function to overlay package
	overlay.SetDebugLogger(globalDebugLogger)                // Provide debug logger for tracking history
	tracking.SetSpatialDebugFunction(debugMsg)               // Provide debug function to spatial tracking package
	tracking.SetSpatialDebugVerboseFunction(debugMsgVerbose) // Provide verbose debug function to spatial tracking package
	detection.SetDebugFunction(debugMsg)                     // Provide debug function to detection package
	fmt.Printf("[MAIN_INIT] ✅ Debug pipeline setup complete\n")
	cameraStateManager := ptz.NewCameraStateManager(ptzController)

	// Set user-defined PTZ limits if provided
	if *minPan != -1 || *maxPan != -1 || *minTilt != -1 || *maxTilt != -1 || *minZoom != -1 || *maxZoom != -1 {
		limits := cameraStateManager.GetLimits()
		hasLimits := false

		// Pan limits (direct camera coordinates)
		if *minPan != -1 {
			limits.SoftMinPan = math.Max(*minPan, limits.HardMinPan)
			hasLimits = true
		}
		if *maxPan != -1 {
			limits.SoftMaxPan = math.Min(*maxPan, limits.HardMaxPan)
			hasLimits = true
		}

		// Tilt limits (direct camera coordinates)
		if *minTilt != -1 {
			limits.SoftMinTilt = math.Max(*minTilt, limits.HardMinTilt)
			hasLimits = true
		}
		if *maxTilt != -1 {
			limits.SoftMaxTilt = math.Min(*maxTilt, limits.HardMaxTilt)
			hasLimits = true
		}

		// Zoom limits (direct camera coordinates)
		if *minZoom != -1 {
			limits.SoftMinZoom = math.Max(*minZoom, limits.HardMinZoom)
			hasLimits = true
		}
		if *maxZoom != -1 {
			limits.SoftMaxZoom = math.Min(*maxZoom, limits.HardMaxZoom)
			hasLimits = true
		}

		if hasLimits {
			cameraStateManager.SetLimits(limits)

			// Build limit description strings
			panStr := fmt.Sprintf("%.0f-%.0f", limits.SoftMinPan, limits.SoftMaxPan)
			tiltStr := fmt.Sprintf("%.0f-%.0f", limits.SoftMinTilt, limits.SoftMaxTilt)
			zoomStr := fmt.Sprintf("%.0f-%.0f", limits.SoftMinZoom, limits.SoftMaxZoom)

			debugMsg("USER_LIMITS", fmt.Sprintf("Applied PTZ limits: Pan=%s Tilt=%s Zoom=%s (camera units)",
				panStr, tiltStr, zoomStr))
		}
	}

	// Ensure we start in IDLE state (reset any previous stuck state)
	cameraStateManager.ForceIdle()

	// Set up camera state manager callbacks
	cameraStateManager.SetOnStateChanged(func(oldState, newState ptz.CameraState) {
		debugMsg("CAMERA_STATE", fmt.Sprintf("State changed: %s → %s", oldState, newState))

		// DEBUG: Log camera state changes to active sessions
		if *debugMode {
			currentPos := ptzController.GetCurrentPosition()
			stateData := map[string]interface{}{
				"Old_State": oldState.String(),
				"New_State": newState.String(),
				"Pan":       currentPos.Pan,
				"Tilt":      currentPos.Tilt,
				"Zoom":      currentPos.Zoom,
			}

			// Log to all active sessions
			debugManager.mu.RLock()
			for _, session := range debugManager.sessions {
				if session.enabled {
					session.LogEvent("CAMERA_STATE_CHANGE",
						fmt.Sprintf("Camera state: %s → %s", oldState, newState),
						stateData)
				}
			}
			debugManager.mu.RUnlock()
		}

		// Clear tracking history when camera starts moving to prevent overlay mess
		if newState == ptz.MOVING {
			debugMsg("CAMERA_STATE", "🧹 Camera started moving - clearing tracking history for clean overlay")
			spatialIntegration.ClearTrackingHistory()
		}

		// FIX: Recalculate spatial positions when camera becomes IDLE
		if newState == ptz.IDLE && oldState == ptz.MOVING {
			debugMsg("CAMERA_STATE", "🔄 Camera became IDLE - recalculating spatial positions for locked boats")
			spatialIntegration.RecalculateSpatialPositions()
		}
	})

	cameraStateManager.SetOnArrived(func(target ptz.PTZPosition) {
		debugMsg("CAMERA_STATE", fmt.Sprintf("✅ Camera arrived at Pan=%.1f, Tilt=%.1f, Zoom=%.1f",
			target.Pan, target.Tilt, target.Zoom))

		// DEBUG: Log camera arrival to active sessions
		if *debugMode {
			arrivalData := map[string]interface{}{
				"Target_Pan":  target.Pan,
				"Target_Tilt": target.Tilt,
				"Target_Zoom": target.Zoom,
			}

			// Log to all active sessions
			debugManager.mu.RLock()
			for _, session := range debugManager.sessions {
				if session.enabled {
					session.LogEvent("CAMERA_ARRIVED",
						fmt.Sprintf("Camera arrived at Pan=%.1f, Tilt=%.1f, Zoom=%.1f", target.Pan, target.Tilt, target.Zoom),
						arrivalData)
				}
			}
			debugManager.mu.RUnlock()
		}
	})

	// Set tolerances for arrival detection - much tighter now that we use integer matching
	cameraStateManager.SetTolerances(1.0, 1.0, 1.0) // Pan, Tilt, Zoom tolerances (reduced from 4.0, 4.0, 2.0)

	// Start camera state monitoring
	cameraStateManager.Start()
	defer cameraStateManager.Stop()

	// Pass camera state manager to tracking system
	spatialIntegration.SetCameraStateManager(cameraStateManager)

	// Move to the first river scanning position on startup using state manager
	debugMsg("PTZ_DEBUG", "Moving to initial river scanning position")
	debugMsg("CAMERA_STATE", fmt.Sprintf("Initial state: %s", cameraStateManager.GetStateInfo()))
	initialCmd := ptz.PTZCommand{
		Command:      "absolutePosition",
		Reason:       "Initial camera position - start river scanning",
		Duration:     500 * time.Millisecond,
		AbsolutePan:  func() *float64 { p := 2570.0; return &p }(), // First river point
		AbsoluteTilt: func() *float64 { t := 130.0; return &t }(),
		AbsoluteZoom: func() *float64 { z := 50.0; return &z }(),
	}

	if !cameraStateManager.SendCommand(initialCmd) {
		debugMsg("PTZ_DEBUG", "Failed to send initial position command")
	}

	// Ensure commentary file exists before starting FFmpeg
	commentaryFile := "/tmp/commentary.txt"
	if _, err := os.Stat(commentaryFile); os.IsNotExist(err) {
		// Create initial commentary file with placeholder text
		initialText := "River camera starting up..."
		if err := os.WriteFile(commentaryFile, []byte(initialText), 0644); err != nil {
			debugMsg("WARNING", fmt.Sprintf("Could not create initial commentary file: %v", err))
			// Continue anyway - FFmpeg will handle missing file gracefully
		} else {
			debugMsg("INFO", fmt.Sprintf("Created initial commentary file: %s", commentaryFile))
		}
	}

	// Initialize FFmpeg manager
	ffmpegManager := NewFFmpegManager(pictureSize)
	if err := ffmpegManager.Start(); err != nil {
		debugMsg("ERROR", fmt.Sprintf("Failed to start FFmpeg: %v", err))
		return
	}
	defer ffmpegManager.Stop() // Ensure FFmpeg is stopped when main exits

	// Initialize FFmpeg memory monitor after FFmpeg starts
	ffmpegMonitor := NewFFmpegMemoryMonitor(ffmpegManager)
	debugMsg("HEALTH_MONITOR", "Initialized FFmpeg memory monitoring")

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGSEGV)
	go func() {
		sig := <-sigChan
		debugMsg("INFO", fmt.Sprintf("Received signal %v. Cleaning up...", sig))

		// Clean up debug sessions
		if *debugMode {
			debugMsg("DEBUG", "Emergency cleanup - closing all debug sessions...")
			debugManager.Stop() // This now includes session cleanup
		}

		ffmpegManager.Stop()
		if sig == syscall.SIGSEGV {
			// Give FFmpeg a moment to clean up
			time.Sleep(100 * time.Millisecond)
			// Force kill any remaining FFmpeg processes
			exec.Command("pkill", "-9", "ffmpeg").Run()
		}
		os.Exit(1)
	}()

	debugMsg("DEBUG", "Loading YOLOv3-tiny model...")
	net := gocv.ReadNet("yolov3-tiny.weights", "yolov3-tiny.cfg")

	// Auto-detect best available backend
	if setupGPUBackend(&net) {
		debugMsg("DEBUG", "Model loaded with GPU acceleration (CUDA + cuDNN).")
	} else {
		debugMsg("DEBUG", "Model loaded with CPU (GPU not available or failed).")
	}

	// Load class names
	namesBytes, err := ioutil.ReadFile("coco.names")
	if err != nil {
		debugMsg("ERROR", fmt.Sprintf("Could not read coco.names: %v", err))
		return
	}
	classNames := strings.Split(string(namesBytes), "\n")

	// Create channels with larger buffers
	frameChan := make(chan FrameData, 120) // Increased from 60 to 120
	errorChan := make(chan error, 1)

	// Start frame capture goroutine
	go captureFrames(webcam, frameChan, errorChan, stats)

	// Start FFmpeg writer goroutine
	go writeFrames(frameChan, ffmpegManager, renderer, spatialIntegration, &net, classNames, stats, ffmpegManager.GetStopChan(), *debugMode, debugManager, cameraStateManager, *pipZoomEnabled, gpuMonitor, rtmpChecker, ffmpegMonitor)

	// Main processing loop with enhanced error handling
	for {
		select {
		case err := <-errorChan:
			debugMsg("ERROR", fmt.Sprintf("Stream error: %v", err))
			debugMsg("ERROR", "Shutting down due to stream error")
			return
		case <-ffmpegManager.GetStopChan():
			fmt.Println("[FFMPEG] FFmpeg has stopped. Cleaning up...")
			fmt.Println("[FFMPEG] This should trigger application exit...")

			// Give brief moment for cleanup
			time.Sleep(100 * time.Millisecond)

			// Force exit if we reach this point - FFmpeg crash should have already exited
			fmt.Println("[FFMPEG] Emergency exit from main loop - FFmpeg stop channel triggered")
			exec.Command("pkill", "-9", "ffmpeg").Run() // Final cleanup
			os.Exit(3)                                  // Exit with FFmpeg crash code
		}
	}
}

// setupGPUBackend attempts to configure GPU acceleration for YOLO inference
func setupGPUBackend(net *gocv.Net) bool {
	fmt.Println("[GPU_DETECT] Testing GPU capabilities for YOLO inference...")

	// First check if CUDA devices are available
	cmd := exec.Command("nvidia-smi", "-L")
	if err := cmd.Run(); err != nil {
		debugMsg("GPU_DETECT", fmt.Sprintf("NVIDIA GPU not found or nvidia-smi failed: %v", err))
		net.SetPreferableBackend(gocv.NetBackendDefault)
		net.SetPreferableTarget(gocv.NetTargetCPU)
		return false
	}

	// CRITICAL FIX: Set CUDA environment variables for proper initialization
	fmt.Println("[GPU_DETECT] Setting CUDA environment variables...")
	os.Setenv("CUDA_VISIBLE_DEVICES", "0")
	os.Setenv("CUDA_DEVICE_ORDER", "PCI_BUS_ID")
	os.Setenv("CUDA_CACHE_DISABLE", "0")

	// CRITICAL FIX: Set cuBLAS specific environment variables
	fmt.Println("[GPU_DETECT] Setting cuBLAS environment variables...")
	os.Setenv("CUBLAS_WORKSPACE_CONFIG", ":4096:8") // Set workspace config
	os.Setenv("CUDA_LAUNCH_BLOCKING", "1")          // Force synchronous execution for debugging

	// Force library path to ensure correct cuBLAS libraries are found
	currentPath := os.Getenv("LD_LIBRARY_PATH")
	newPath := "/lib/x86_64-linux-gnu:/usr/local/lib"
	if currentPath != "" {
		newPath = newPath + ":" + currentPath
	}
	os.Setenv("LD_LIBRARY_PATH", newPath)

	// Force CUDA runtime initialization with nvidia-smi
	fmt.Println("[GPU_DETECT] Forcing CUDA runtime initialization...")
	initCmd := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader")
	if err := initCmd.Run(); err != nil {
		debugMsg("GPU_DETECT", fmt.Sprintf("CUDA initialization test failed: %v", err))
		net.SetPreferableBackend(gocv.NetBackendDefault)
		net.SetPreferableTarget(gocv.NetTargetCPU)
		return false
	}
	fmt.Println("[GPU_DETECT] CUDA runtime initialization successful")

	// Try to set CUDA backend with proper initialization order
	fmt.Println("[GPU_DETECT] NVIDIA GPU detected, testing CUDA + cuDNN support...")

	// Set backend and target carefully
	fmt.Println("[GPU_DETECT] Setting CUDA backend...")
	net.SetPreferableBackend(gocv.NetBackendCUDA)

	fmt.Println("[GPU_DETECT] Setting CUDA target...")
	net.SetPreferableTarget(gocv.NetTargetCUDA)

	// Longer delay to ensure CUDA context is fully ready
	fmt.Println("[GPU_DETECT] Waiting for CUDA context to stabilize...")
	time.Sleep(200 * time.Millisecond)

	// Test GPU inference with a small dummy input (with panic recovery)
	fmt.Println("[GPU_DETECT] Testing GPU inference with dummy input...")

	// Use a helper function with panic recovery to test GPU
	if testGPUInference(net) {
		fmt.Println("[GPU_DETECT] GPU inference test successful! Using CUDA acceleration.")
		return true
	} else {
		fmt.Println("[GPU_DETECT] GPU test failed - falling back to CPU")
		fmt.Println("[GPU_DETECT] Note: GPU encoding (FFmpeg) is still active and working")
		fmt.Println("[GPU_DETECT] CPU inference is actually quite fast for YOLOv3-tiny")
		net.SetPreferableBackend(gocv.NetBackendDefault)
		net.SetPreferableTarget(gocv.NetTargetCPU)
		return false
	}
}

// createOptimizedBlob creates a properly letterboxed YOLO input blob
func createOptimizedBlob(frame gocv.Mat) gocv.Mat {
	// CRITICAL FIX: Manual letterboxing since OpenCV crop=false doesn't work properly
	// Original frame: 2688x1520 (1.768:1) -> YOLO input: 832x832 (1:1) with letterboxing

	originalWidth := float32(frame.Cols())  // 2688
	originalHeight := float32(frame.Rows()) // 1520
	yoloSize := 832

	// Calculate letterbox parameters to preserve aspect ratio
	aspectRatio := originalWidth / originalHeight         // 1.768
	contentHeight := int(float32(yoloSize) / aspectRatio) // 470px
	yOffset := (yoloSize - contentHeight) / 2             // 181px

	// Step 1: Create 832x832 black canvas (letterbox background)
	letterboxed := gocv.NewMatWithSize(yoloSize, yoloSize, gocv.MatTypeCV8UC3)
	defer letterboxed.Close()
	letterboxed.SetTo(gocv.NewScalar(0, 0, 0, 0)) // Fill with black

	// Step 2: Resize original frame to fit content area (preserves aspect ratio)
	resized := gocv.NewMat()
	defer resized.Close()
	gocv.Resize(frame, &resized, image.Pt(yoloSize, contentHeight), 0, 0, gocv.InterpolationLinear)

	// Step 3: Copy resized content to center of letterboxed canvas
	contentROI := letterboxed.Region(image.Rect(0, yOffset, yoloSize, yOffset+contentHeight))
	defer contentROI.Close()
	resized.CopyTo(&contentROI)

	// Step 3.5: Apply color masking to remove water areas (if enabled)
	maskedLetterboxed := applyColorMasking(letterboxed)
	defer func() {
		// Only close if it's a different Mat from letterboxed
		if maskedLetterboxed.Ptr() != letterboxed.Ptr() {
			maskedLetterboxed.Close()
		}
	}()

	// Step 4: Create blob from properly letterboxed and masked image
	// Now we can use crop=true since the image is already properly formatted
	blob := gocv.BlobFromImage(maskedLetterboxed, 1.0/255.0, image.Pt(832, 832), gocv.NewScalar(0, 0, 0, 0), true, true)

	return blob
}

// parseHexColor converts hex color string to RGB values
func parseHexColor(hexColor string) (r, g, b uint8, err error) {
	// Remove # if present
	hexColor = strings.TrimPrefix(hexColor, "#")

	if len(hexColor) != 6 {
		return 0, 0, 0, fmt.Errorf("invalid hex color format: %s", hexColor)
	}

	// Parse RGB components
	rgb, err := strconv.ParseUint(hexColor, 16, 32)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to parse hex color %s: %v", hexColor, err)
	}

	r = uint8((rgb >> 16) & 0xFF)
	g = uint8((rgb >> 8) & 0xFF)
	b = uint8(rgb & 0xFF)

	return r, g, b, nil
}

// applyColorMasking applies color-based masking to remove water areas using gray replacement
func applyColorMasking(frame gocv.Mat) gocv.Mat {
	if *maskColors == "" {
		// No masking requested, return original frame
		return frame
	}

	// Parse the color list
	colorStrings := strings.Split(*maskColors, ",")
	if len(colorStrings) == 0 {
		debugMsg("MASK", "No colors specified for masking")
		return frame
	}

	// Clone the original frame (keep 3-channel BGR format for YOLO)
	maskedFrame := frame.Clone()

	tolerance := *maskTolerance
	// Only log masking info once at startup, not every frame

	// Parse each color and apply masking
	for _, colorStr := range colorStrings {
		colorStr = strings.TrimSpace(colorStr)
		r, g, b, err := parseHexColor(colorStr)
		if err != nil {
			debugMsg("MASK", fmt.Sprintf("Invalid color %s: %v", colorStr, err))
			continue
		}

		// Create mask for pixels within tolerance of this color
		// Note: OpenCV uses BGR order, so we need to swap
		lowerBound := gocv.NewMatFromScalar(gocv.NewScalar(
			math.Max(0, float64(b)-float64(tolerance)), // Blue
			math.Max(0, float64(g)-float64(tolerance)), // Green
			math.Max(0, float64(r)-float64(tolerance)), // Red
			0, // Alpha (not used for lower bound)
		), gocv.MatTypeCV8UC3)
		defer lowerBound.Close()

		upperBound := gocv.NewMatFromScalar(gocv.NewScalar(
			math.Min(255, float64(b)+float64(tolerance)), // Blue
			math.Min(255, float64(g)+float64(tolerance)), // Green
			math.Min(255, float64(r)+float64(tolerance)), // Red
			255, // Alpha (not used for upper bound)
		), gocv.MatTypeCV8UC3)
		defer upperBound.Close()

		// Create mask for this color range
		mask := gocv.NewMat()
		defer mask.Close()

		gocv.InRange(maskedFrame, lowerBound, upperBound, &mask)

		// Replace masked pixels with neutral gray (128, 128, 128)
		// This removes water texture while maintaining YOLO-compatible 3-channel format
		grayImage := gocv.NewMatWithSize(maskedFrame.Rows(), maskedFrame.Cols(), gocv.MatTypeCV8UC3)
		defer grayImage.Close()
		grayColor := gocv.NewScalar(128, 128, 128, 0) // Neutral gray in BGR
		grayImage.SetTo(grayColor)

		// Copy gray pixels to masked areas only
		grayImage.CopyToWithMask(&maskedFrame, mask)
	}

	return maskedFrame
}

// saveYOLOBlobDebug saves the actual YOLO blob data as an image (YOLO DEBUG MODE ONLY)
func saveYOLOBlobDebug(blob gocv.Mat, frameCounter int64) {
	if !*yoloDebug {
		return
	}

	// Create debug directory if it doesn't exist
	debugDir := "/tmp/YOLOdebug"
	if err := os.MkdirAll(debugDir, 0755); err != nil {
		debugMsg("YOLO_DEBUG", fmt.Sprintf("Failed to create YOLO debug directory: %v", err))
		return
	}

	// Convert blob back to image format for visualization
	// The blob is in format: [1, 3, 832, 832] (batch, channels, height, width)
	// We need to convert it back to [832, 832, 3] (height, width, channels) for saving

	// Extract the data from the blob
	blobData, err := blob.DataPtrFloat32()
	if err != nil || blobData == nil {
		debugMsg("YOLO_DEBUG", fmt.Sprintf("Failed to get blob data pointer: %v", err))
		return
	}

	// Create output image
	outputImage := gocv.NewMatWithSize(832, 832, gocv.MatTypeCV8UC3)
	defer outputImage.Close()

	// Convert blob data back to image format
	// Blob format: [batch=1, channels=3, height=832, width=832]
	// The data is normalized (0-1), so we need to scale back to 0-255
	for y := 0; y < 832; y++ {
		for x := 0; x < 832; x++ {
			// Calculate blob indices for RGB channels
			rIndex := (0*3+0)*832*832 + y*832 + x // Red channel
			gIndex := (0*3+1)*832*832 + y*832 + x // Green channel
			bIndex := (0*3+2)*832*832 + y*832 + x // Blue channel

			// Get normalized values and convert back to 0-255 range
			r := uint8(blobData[rIndex] * 255.0)
			g := uint8(blobData[gIndex] * 255.0)
			b := uint8(blobData[bIndex] * 255.0)

			// Set pixel in output image (OpenCV uses BGR order)
			outputImage.SetUCharAt(y, x*3+0, b) // Blue
			outputImage.SetUCharAt(y, x*3+1, g) // Green
			outputImage.SetUCharAt(y, x*3+2, r) // Red
		}
	}

	// Save the image
	filename := fmt.Sprintf("yolo_blob_frame_%06d.jpg", frameCounter)
	filepath := filepath.Join(debugDir, filename)

	if success := gocv.IMWrite(filepath, outputImage); success {
		debugMsg("YOLO_DEBUG", fmt.Sprintf("Saved YOLO blob: %s", filename))
	} else {
		debugMsg("YOLO_DEBUG", fmt.Sprintf("Failed to save YOLO blob: %s", filename))
	}
}

// testGPUInference safely tests if GPU inference works, catching any panics
func testGPUInference(net *gocv.Net) (success bool) {
	// Use defer/recover to catch any panics from CUDA/cuDNN failures
	defer func() {
		if r := recover(); r != nil {
			debugMsg("GPU_DETECT", fmt.Sprintf("GPU test failed with panic: %v", r))
			success = false
		}
	}()

	// CRITICAL FIX: Add small delay to ensure CUDA driver is ready
	fmt.Println("[GPU_DETECT] Allowing CUDA driver to stabilize...")
	time.Sleep(100 * time.Millisecond)
	testBlob := gocv.BlobFromImage(gocv.NewMatWithSize(832, 832, gocv.MatTypeCV8UC3), 1.0/255.0,
		image.Pt(832, 832), gocv.NewScalar(0, 0, 0, 0), true, false)
	defer testBlob.Close()

	// Try a forward pass to test if GPU actually works
	net.SetInput(testBlob, "")

	// This is where it would fail if cuDNN/CUDA setup is broken
	testOutput := net.Forward("")
	defer testOutput.Close()

	if testOutput.Empty() {
		fmt.Println("[GPU_DETECT] GPU inference returned empty output")
		return false
	}

	fmt.Println("[GPU_DETECT] CUDA context initialization and GPU inference successful!")
	return true
}

// detectGPUEncoding checks if NVIDIA GPU encoding is available
func detectGPUEncoding() bool {
	// Test if h264_nvenc is available
	cmd := exec.Command("ffmpeg", "-hide_banner", "-encoders")
	output, err := cmd.Output()
	if err != nil {
		debugMsg("GPU_DETECT", fmt.Sprintf("FFmpeg encoders check failed: %v", err))
		return false
	}

	if !strings.Contains(string(output), "h264_nvenc") {
		debugMsg("GPU_DETECT", "h264_nvenc encoder not found in FFmpeg")
		return false
	}

	// Test if NVIDIA GPU is accessible by trying a quick encode test
	testCmd := exec.Command("ffmpeg", "-hide_banner", "-f", "lavfi", "-i", "testsrc=duration=1:size=320x240:rate=1", "-c:v", "h264_nvenc", "-f", "null", "-")
	testCmd.Stdout = nil
	testCmd.Stderr = nil
	err = testCmd.Run()

	if err != nil {
		debugMsg("GPU_DETECT", fmt.Sprintf("NVIDIA GPU encoding test failed: %v", err))
		return false
	}

	debugMsg("GPU_DETECT", "NVIDIA GPU encoding is available and working")
	return true
}

// setupFFmpeg configures and returns the FFmpeg command with GPU fallback
func setupFFmpeg(pictureSize string) *exec.Cmd {
	// Detect GPU availability
	useGPU := detectGPUEncoding()

	// Base command arguments
	args := []string{
		"/home/blyon/FFmpeg-n7.1.1/ffmpeg", // TEMPORARY: Testing new FFmpeg 7.1.1 build

		// INPUT ROBUSTNESS - Add large input buffers for stability
		"-thread_queue_size", "2048", // Large thread queue (dynamic)
		"-fflags", "+flush_packets", // Allow buffer flushing (dynamic)

		// First input: Raw video from our processed frames
		"-f", "rawvideo",
		"-pix_fmt", "bgr24",
		"-s", pictureSize,
		"-r", fmt.Sprintf("%d", frameRate),
		"-i", "-",

		// TEMPORARILY REMOVED: Audio input (might be causing hang)
		// "-rtsp_transport", "tcp",
		// "-thread_queue_size", "1024", // Increased from default 8
		// "-i", "rtsp://username:password@192.168.1.100:554",

		// Video settings
		"-g", fmt.Sprintf("%d", frameRate),
		"-keyint_min", fmt.Sprintf("%d", frameRate),
		"-sc_threshold", "0",
		"-pix_fmt", "yuv420p",
	}

	// Add encoding settings based on GPU availability
	if useGPU {
		debugMsg("FFMPEG_SETUP", "Using NVIDIA GPU encoding (h264_nvenc)")
		args = append(args,
			// NVIDIA GPU encoding settings
			"-c:v", "h264_nvenc",
			"-preset", "p7", // NVENC preset: p1(fastest) to p7(slowest), highest quality
			"-tune", "ull", // Ultra Low Latency for live streaming
			"-rc", "cbr", // Constant bitrate for streaming
			"-b:v", "16000k", // Bitrate
			"-maxrate", "16000k",
			"-bufsize", "32000k", // Increased to 32MB (was 15MB) for better stability
			"-rtbufsize", "16000k", // Increased to 16MB (was 5MB) for better stability
			"-max_delay", "1000000", // Allow 1 second max delay (was 0.5s) for recovery
			"-spatial_aq", "1", // Spatial Adaptive Quantization for better quality distribution
			"-temporal_aq", "1", // Temporal Adaptive Quantization for motion quality
			"-profile:v", "high",
			"-gpu", "0", // Use first GPU
		)
	} else {
		debugMsg("FFMPEG_SETUP", "Using CPU encoding (libx264) - GPU not available")
		args = append(args,
			// CPU encoding settings (fallback)
			"-c:v", "libx264",
			"-preset", "medium", // x264 preset for CPU encoding
			"-tune", "zerolatency", // Low latency tuning
			"-b:v", "16000k", // Bitrate
			"-maxrate", "16000k",
			"-bufsize", "32000k", // Increased to 32MB (was 15MB) for better stability
			"-rtbufsize", "16000k", // Increased to 16MB (was 5MB) for better stability
			"-max_delay", "1000000", // Allow 1 second max delay (was 0.5s) for recovery
			"-profile:v", "high",
			"-level", "4.2",
			"-x264opts", "no-scenecut:nal-hrd=cbr:rc-lookahead=30",
		)
	}

	// Add common output settings
	args = append(args,
		// Output format for better client reconnection support
		"-f", "flv",
		"-flvflags", "no_duration_filesize",
		"-rtmp_live", "live",
		"rtmp://localhost/live/stream",
	)

	ffmpegCmd := exec.Command(args[0], args[1:]...)

	// Set process priority and group
	ffmpegCmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Pgid:    0,
	}

	// Add CPU affinity if available
	if runtime.NumCPU() > 1 {
		cores := runtime.NumCPU() - 1
		if cores < 1 {
			cores = 1
		}
		ffmpegCmd.Env = append(os.Environ(), fmt.Sprintf("GOMAXPROCS=%d", cores))
	}

	return ffmpegCmd
}

// captureFrames handles frame capture from the camera
func captureFrames(webcam *gocv.VideoCapture, frameChan chan<- FrameData, errorChan chan<- error, stats *PipelineStats) {
	frameSequence := int64(0)

	for {
		readStart := time.Now()
		img := gocv.NewMat()
		trackMatAlloc("capture")

		// Try to read frame - NO ARTIFICIAL DELAY, read as fast as camera provides
		if ok := webcam.Read(&img); !ok {
			img.Close()
			trackMatClose("capture")
			errorChan <- fmt.Errorf("failed to read frame from stream")
			return
		}

		// Check if frame is valid
		if img.Empty() {
			img.Close()
			trackMatClose("capture")
			continue
		}

		// Verify frame dimensions and type
		if img.Type() != gocv.MatTypeCV8UC3 || img.Channels() != 3 {
			img.Close()
			trackMatClose("capture")
			continue
		}

		stats.UpdateCapture(time.Since(readStart))

		// Create frame data with current sequence
		frameData := FrameData{
			frame:     img,
			sequence:  frameSequence,
			timestamp: time.Now(), // Real-time timestamp when frame was actually read
		}

		select {
		case frameChan <- frameData:
			// Frame sent successfully - increment sequence for next frame
			frameSequence++
		default:
			// Channel full, drop frame and continue reading (sequence not incremented - will retry)
			img.Close()
			trackMatClose("capture")
		}
	}
}

// writeFrames handles writing frames to FFmpeg
func writeFrames(frameChan <-chan FrameData, ffmpegManager *FFmpegManager, renderer *overlay.Renderer, spatialIntegration *tracking.SpatialIntegration, net *gocv.Net, classNames []string, stats *PipelineStats, stopChan <-chan struct{}, debugMode bool, debugManager *DebugManager, cameraStateManager *ptz.CameraStateManager, pipZoomEnabled bool, gpuMonitor *GPUMemoryMonitor, rtmpChecker *RTMPHealthChecker, ffmpegMonitor *FFmpegMemoryMonitor) {
	lastSequence := int64(-1)
	frameCount := 0

	// Initialize frame buffer
	frameBuffer := NewFrameBuffer()
	defer frameBuffer.Close()

	// Error recovery state
	var consecutiveErrors int
	lastErrorTime := time.Now()

	// Create a ticker for performance reporting
	perfTicker := time.NewTicker(perfReportInterval)
	defer perfTicker.Stop()

	// PERFORMANCE: Removed frameDataBuffer - we now write directly to FFmpeg

	for {
		select {
		case <-stopChan:
			// Stop writing frames when FFmpeg is restarting
			return
		case <-perfTicker.C:
			captureFPS, processFPS, writeFPS, avgRead, avgYOLO, avgTrack, avgWrite := stats.GetStats()
			debugMsg("PERF", fmt.Sprintf("Pipeline Performance (last %v):", perfReportInterval))
			debugMsg("PERF", fmt.Sprintf("Capture: %.1f fps (Read: %v)", captureFPS, avgRead))
			debugMsg("PERF", fmt.Sprintf("Process: %.1f fps (YOLO: %v, Track: %v)", processFPS, avgYOLO, avgTrack))
			debugMsg("PERF", fmt.Sprintf("Write:   %.1f fps (Write: %v)", writeFPS, avgWrite))
			debugMsg("PERF", fmt.Sprintf("Target:  %d fps", frameRate))

			// Report all three buffer levels for monitoring
			frameBufferLevel := float64(len(frameChan)) / float64(cap(frameChan)) * 100
			writeQueueLen, writeQueueCap := ffmpegManager.GetWriteQueueStatus()
			writeQueueLevel := float64(writeQueueLen) / float64(writeQueueCap) * 100
			pendingLen, pendingCap := ffmpegManager.GetPendingFramesStatus()
			pendingLevel := float64(pendingLen) / float64(pendingCap) * 100
			debugMsg("PERF", fmt.Sprintf("Buffers: FrameChan %d/%d (%.1f%%) | WriteQueue %d/%d (%.1f%%) | PendingFrames %d/%d (%.1f%%)",
				len(frameChan), cap(frameChan), frameBufferLevel,
				writeQueueLen, writeQueueCap, writeQueueLevel,
				pendingLen, pendingCap, pendingLevel))

			// CRASH PREVENTION: Check GPU memory, RTMP health, and FFmpeg memory during performance reporting
			if err := gpuMonitor.CheckGPUMemory(); err != nil {
				debugMsg("GPU_ERROR", fmt.Sprintf("GPU memory check failed: %v", err))
			}
			if err := rtmpChecker.CheckRTMPHealth(); err != nil {
				debugMsg("RTMP_ERROR", fmt.Sprintf("RTMP health check failed: %v - pipeline may be unstable", err))
			}
			if err := ffmpegMonitor.CheckFFmpegMemory(); err != nil {
				debugMsg("FFMPEG_HEALTH_ERROR", fmt.Sprintf("FFmpeg memory check failed: %v", err))
			}

			// Critical error detection: Exit if both Process and Write fps drop to 0
			if processFPS == 0.0 && writeFPS == 0.0 {
				debugMsg("CRITICAL", fmt.Sprintf("Both Process (%.1f) and Write (%.1f) fps have dropped to 0 - pipeline has failed!", processFPS, writeFPS))
				debugMsg("CRITICAL", "This indicates a critical failure in the video processing pipeline.")
				debugMsg("CRITICAL", "Exiting with error code 2.")

				// Clean up FFmpeg before exit
				ffmpegManager.Stop()

				// Give FFmpeg time to clean up
				time.Sleep(500 * time.Millisecond)

				// Force kill any remaining FFmpeg processes
				exec.Command("pkill", "-9", "ffmpeg").Run()

				// Exit with error code 2 (pipeline failure)
				os.Exit(2)
			}

		// PERFORMANCE: Disabled flush ticker case - we now write directly to stdin pipe
		// case <-flushTicker.C:
		// 	// Periodically flush FFmpeg buffer - now bypassed

		case frameData := <-frameChan:
			// Process frames as fast as possible - no ticker limitation
			// Check buffer level for monitoring and emergency dump
			bufferLevel := float64(len(frameChan)) / float64(cap(frameChan))

			// EMERGENCY BUFFER DUMP: If buffer gets too full, dump ENTIRE buffer to jump to current time
			if bufferLevel > 0.8 {
				framesToDump := len(frameChan) // Dump ALL frames instead of 50%
				debugMsg("BUFFER_DUMP", fmt.Sprintf("Buffer dangerously full %.1f%% (%d/%d) - dumping ALL %d frames to jump to current time",
					bufferLevel*100, len(frameChan), cap(frameChan), framesToDump))

				drainStart := time.Now()
				dumpedFrames := 0

				// Drain ALL buffered frames for complete reset to current time
				for len(frameChan) > 0 {
					select {
					case dumpFrame := <-frameChan:
						// Properly close the dumped frame to prevent memory leaks
						dumpFrame.frame.Close()
						trackMatClose("capture")
						dumpedFrames++
					default:
						// No more frames to dump
						break
					}
				}

				drainTime := time.Since(drainStart)
				newBufferLevel := float64(len(frameChan)) / float64(cap(frameChan))
				debugMsg("BUFFER_DUMP", fmt.Sprintf("Successfully dumped ALL %d frames in %v - buffer now %.1f%% (%d/%d)",
					dumpedFrames, drainTime, newBufferLevel*100, len(frameChan), cap(frameChan)))
				debugMsg("BUFFER_DUMP", "Stream jumped to current time - complete latency reset")
			} else if bufferLevel > 0.5 {
				// Also check writeQueue level for comprehensive monitoring
				writeQueueLen, writeQueueCap := ffmpegManager.GetWriteQueueStatus()
				writeQueueLevel := float64(writeQueueLen) / float64(writeQueueCap)
				debugMsg("BUFFER_MONITOR", fmt.Sprintf("Buffer levels: FrameChan %.1f%% (%d/%d) | WriteQueue %.1f%% (%d/%d)",
					bufferLevel*100, len(frameChan), cap(frameChan),
					writeQueueLevel*100, writeQueueLen, writeQueueCap))
			}
			// Check frame validity before processing
			if !isValidFrame(frameData.frame) {
				frameData.frame.Close()
				trackMatClose("capture")
				continue
			}

			if frameData.frame.Type() == gocv.MatTypeCV8UC3 && frameData.frame.Channels() == 3 {
				frameCount++

				// Check frame sequence
				if frameData.sequence <= lastSequence {
					frameData.frame.Close()
					trackMatClose("capture")
					continue
				}
				lastSequence = frameData.sequence

				// Process frame with error recovery
				frame, valid := frameBuffer.ProcessFrame(frameData.frame)
				if !valid {
					frameData.frame.Close()
					trackMatClose("capture")
					consecutiveErrors++

					// If we've had too many errors, wait before trying again
					if consecutiveErrors > 5 && time.Since(lastErrorTime) < time.Second*2 {
						continue
					}
					continue
				}
				consecutiveErrors = 0
				lastErrorTime = time.Now()

				stats.UpdateProcess()

				// Create a copy of the frame for drawing
				frameToWrite := frame.Clone()
				trackMatAlloc("buffer")

				// SAVE PRE-OVERLAY FRAME: Only save during LOCK/SUPER LOCK
				if *preOverlayJpg && spatialIntegration.GetCurrentMode() == tracking.ModeTracking {
					// Only save if we have a locked target
					if lockedTarget := spatialIntegration.GetLockedTargetForPIP(); lockedTarget != nil {
						saveJpegFrame(frameToWrite, *jpgPath, "pre-overlay", 0) // Detection count not available yet
					}
				}

				// CONDITIONAL STATUS OVERLAY: Draw status overlay only when enabled
				if *statusOverlay {
					// Draw status overlay at 1200px down
					statusRect := image.Rect(10, 1200, 500, 1370)
					gocv.Rectangle(&frameToWrite, statusRect, color.RGBA{0, 0, 0, 200}, -1) // Semi-transparent black background

					// Draw status text
					statusLines := []string{
						fmt.Sprintf("Time: %s", time.Now().Format("Mon Jan 2 15:04:05 MST 2006")),
						fmt.Sprintf("Frame: %d", frameCount),
						fmt.Sprintf("FPS: %.1f", stats.UpdateFPS()),
						fmt.Sprintf("Objects: %d", len(spatialIntegration.GetTrackedObjects())),
						fmt.Sprintf("Current Object: %s", spatialIntegration.GetCurrentTrackedObjectDisplay()),
						fmt.Sprintf("Mode: %s", spatialIntegration.GetDetailedTrackingMode()),
					}

					// Get current PTZ position directly from the controller
					currentPos := spatialIntegration.GetPTZController().GetCurrentPosition()
					// Use raw Hikvision values
					pValue := int(currentPos.Pan)  // 0-3590
					tValue := int(currentPos.Tilt) // 0-900
					zValue := int(currentPos.Zoom) // 10-120
					statusLines = append(statusLines, fmt.Sprintf("PTZ: P%d T%d Z%d", pValue, tValue, zValue))

					// Add camera state info if available
					if stateManager := spatialIntegration.GetCameraStateManager(); stateManager != nil {
						statusLines = append(statusLines, fmt.Sprintf("Camera: %s", stateManager.GetState()))
					}

					// Add tracked objects counter
					statusLines = append(statusLines, fmt.Sprintf("Tracked: %d", spatialIntegration.GetTotalDetectedObjects()))

					// Draw each status line aligned with the black box at 1200px
					for i, line := range statusLines {
						textPoint := image.Pt(20, 1220+i*18) // Start at 1220 to align with black box at 1200
						gocv.PutText(&frameToWrite, line, textPoint, gocv.FontHersheySimplex, 0.5, color.RGBA{255, 255, 255, 255}, 1)
					}
				}

				// Process frame with YOLO if enabled
				var detectionRects []image.Rectangle
				var detectionClassNames []string
				var detectionConfidences []float64

				if !disableYOLO {
					yoloStart := time.Now()
					blob := createOptimizedBlob(frame)
					trackMatAlloc("yolo")

					// YOLO DEBUG: Save actual blob data that YOLO receives (every 120th frame = every 4 seconds)
					if *yoloDebug {
						yoloDebugFrameCounter++
						if yoloDebugFrameCounter%120 == 0 {
							saveYOLOBlobDebug(blob, yoloDebugFrameCounter)
						}
					}

					net.SetInput(blob, "")
					output := net.Forward("")
					trackMatAlloc("yolo")
					stats.UpdateYOLO(time.Since(yoloStart))

					// Collect all raw YOLO detections for overlay (before filtering)
					var allRawDetections []image.Rectangle
					var allRawClassNames []string
					var allRawConfidences []float64

					// Process YOLO output and draw all detections
					for i := 0; i < output.Rows(); i++ {
						row := output.RowRange(i, i+1)
						trackMatAlloc("yolo")
						data := row.Clone()
						trackMatAlloc("yolo")
						scores := data.ColRange(5, data.Cols())
						trackMatAlloc("yolo")
						_, maxVal, _, maxLoc := gocv.MinMaxLoc(scores)
						classID := maxLoc.X
						confidence := maxVal
						className := ""
						if classID < len(classNames) {
							className = classNames[classID]
						}

						// Calculate detection rectangle FIRST (for raw YOLO overlay)
						// PROPER LETTERBOX COORDINATE TRANSFORMATION
						// Original frame: 2688x1520 (1.768:1), YOLO input: 832x832 (1:1)
						// Letterboxing adds black bars on top/bottom to preserve aspect ratio

						originalWidth := float32(frame.Cols())  // 2688
						originalHeight := float32(frame.Rows()) // 1520
						yoloSize := float32(832)                // 832x832 YOLO input

						// Calculate letterbox parameters
						aspectRatio := originalWidth / originalHeight // 1.768
						contentHeight := yoloSize / aspectRatio       // 470px (actual content height)
						yOffset := (yoloSize - contentHeight) / 2     // 181px (black bar offset)

						// STEP 1: Get normalized YOLO coordinates (0.0-1.0)
						xNorm := data.GetFloatAt(0, 0)
						yNorm := data.GetFloatAt(0, 1)
						wNorm := data.GetFloatAt(0, 2)
						hNorm := data.GetFloatAt(0, 3)

						// STEP 2: Convert to 832x832 pixel coordinates
						xPixel832 := xNorm * yoloSize // 0-832 pixel coordinate in letterboxed space
						yPixel832 := yNorm * yoloSize // 0-832 pixel coordinate in letterboxed space
						wPixel832 := wNorm * yoloSize // width in letterboxed space
						hPixel832 := hNorm * yoloSize // height in letterboxed space

						// STEP 3: Remove letterbox offset from Y coordinate to get content-area coordinate
						yContentPixel := yPixel832 - yOffset // Y coordinate within 470px content area

						// STEP 4: Scale to original frame dimensions
						centerX := int(xPixel832 * (originalWidth / yoloSize))           // Scale X directly
						centerY := int(yContentPixel * (originalHeight / contentHeight)) // Scale Y from content area
						width := int(wPixel832 * (originalWidth / yoloSize))
						height := int(hPixel832 * (originalHeight / contentHeight))
						left := centerX - width/2
						top := centerY - height/2
						rect := image.Rect(left, top, left+width, top+height)

						// Store raw detection for YOLO overlay (before any filtering)
						if confidence > 0.1 { // Only store detections with minimal confidence to avoid noise
							allRawDetections = append(allRawDetections, rect)
							allRawClassNames = append(allRawClassNames, className)
							allRawConfidences = append(allRawConfidences, float64(confidence))
						}

						// DYNAMIC FILTERING: Use configurable P1/P2 tracking priorities with separate confidence thresholds
						validClass := false
						var minConfidenceThreshold float64

						if isP1Object(className) {
							// P1 objects (primary tracking targets) use P1 confidence threshold
							validClass = true
							minConfidenceThreshold = globalP1MinConfidence
						} else if isP2Object(className) {
							// P2 objects (enhancement objects) use P2 confidence threshold and require tracking mode
							if spatialIntegration.GetCurrentMode() == tracking.ModeTracking {
								validClass = true
								minConfidenceThreshold = globalP2MinConfidence
							}
						} else {
							// Ignore all other classes not in P1 or P2 lists
							validClass = false
							minConfidenceThreshold = 1.0 // Set impossible threshold to ensure rejection
						}

						// Always close Mats before any continue
						earlyExit := false
						if !validClass || confidence < float32(minConfidenceThreshold) {
							earlyExit = true
						}
						if earlyExit {
							scores.Close()
							trackMatClose("yolo")
							data.Close()
							trackMatClose("yolo")
							row.Close()
							trackMatClose("yolo")
							continue
						}

						// Additional size filtering to eliminate tiny false positives
						objectArea := width * height
						minArea := 2000 // Minimum 2000 pixels for valid detection
						if objectArea < minArea {
							// Removed spam log message - this filters many detections per frame
							scores.Close()
							trackMatClose("yolo")
							data.Close()
							trackMatClose("yolo")
							row.Close()
							trackMatClose("yolo")
							continue
						}

						// DYNAMIC SIZE FILTER: Reject P1 objects that are too small (configurable by object type)
						if isP1Object(className) && (width <= 50 || height <= 50) {
							debugMsg("YOLO_FILTER", fmt.Sprintf("Rejecting small %s: dimensions %dx%d (≤50x50 pixels)", className, width, height))
							scores.Close()
							trackMatClose("yolo")
							data.Close()
							trackMatClose("yolo")
							row.Close()
							trackMatClose("yolo")
							continue
						}

						// Debug: Show accepted detections
						debugMsgVerbose("YOLO_ACCEPT", fmt.Sprintf("%s: conf=%.2f, area=%d, pos=(%d,%d)",
							className, confidence, objectArea, centerX, centerY))

						// Debug: Show YOLO coordinate calculation for boats
						if className == "boat" {
							debugMsgVerbose("DEBUG", fmt.Sprintf("YOLO: Raw values: x=%.3f, y=%.3f, w=%.3f, h=%.3f",
								data.GetFloatAt(0, 0), data.GetFloatAt(0, 1), data.GetFloatAt(0, 2), data.GetFloatAt(0, 3)))
							debugMsgVerbose("DEBUG", fmt.Sprintf("YOLO: Frame size: %dx%d", frame.Cols(), frame.Rows()))
							debugMsgVerbose("DEBUG", fmt.Sprintf("YOLO: Calculated center: (%d,%d), size: %dx%d",
								centerX, centerY, width, height))
						}

						detectionRects = append(detectionRects, rect)
						detectionClassNames = append(detectionClassNames, className)
						detectionConfidences = append(detectionConfidences, float64(confidence))

						// NOTE: Debug session creation moved to after tracking update to use consistent object IDs

						// ONLY draw raw YOLO detection boxes if yolo-overlay is specifically enabled
						// This prevents green YOLO boxes from cluttering the military targeting overlay
						if *yoloOverlay {
							renderer.DrawDetection(frameToWrite, rect, className, float64(confidence))
						}

						scores.Close()
						trackMatClose("yolo")
						data.Close()
						trackMatClose("yolo")
						row.Close()
						trackMatClose("yolo")
					}

					// Draw raw YOLO detections overlay if enabled
					if *yoloOverlay {
						renderer.DrawYOLODetections(frameToWrite, allRawDetections, allRawClassNames, allRawConfidences)
					}

					// Update tracking
					trackStart := time.Now()
					frameBytes, err := frame.DataPtrUint8()
					if err != nil {
						debugMsg("ERROR", fmt.Sprintf("Could not get frame data: %v", err))
						continue
					}

					// Store previous tracked objects for session cleanup
					prevTrackedObjects := make(map[int]bool)
					if debugMode {
						for objID := range spatialIntegration.GetTrackedObjects() {
							prevTrackedObjects[objID] = true
						}
					}

					spatialIntegration.UpdateTracking(detectionRects, detectionClassNames, detectionConfidences, frameBytes)
					stats.UpdateTracking(time.Since(trackStart))

					// INTEGRATED DEBUG SYSTEM: Combine structured session data + comprehensive message history
					if debugMode {
						currentMode := spatialIntegration.GetCurrentMode()

						// Only create debug sessions when in TRACKING mode (not scanning)
						if currentMode == tracking.ModeTracking {
							// Log mode change to decision terminal (independent of debug mode)
							renderer.LogDecision("Mode: TRACKING", "MODE", 1)

							// Get the actively tracked target (the one with target lock)
							lockedTarget := spatialIntegration.GetLockedTargetForPIP()

							if lockedTarget != nil {
								objectID := lockedTarget.ObjectID

								// Log target lock to decision terminal (independent of debug mode)
								renderer.LogDecision(fmt.Sprintf("Target locked: %s", objectID), "STATUS", 2)

								// Only create debug sessions when in debug mode
								if debugMode {

									// We have an active target lock - create/update debug session
									session := debugManager.GetSession(objectID)

									// Create new session if needed
									if !session.enabled {
										session = debugManager.StartSession(objectID)
										debugMsg("DEBUG", fmt.Sprintf("Started ACTIVE TRACKING session for %s (target locked)", objectID))

										// Exit on first track if flag is enabled
										if *exitOnFirstTrack {
											debugMsg("EXIT_ON_FIRST_TRACK", fmt.Sprintf("First target lock achieved for %s - exiting as requested", objectID))
											debugMsg("EXIT_ON_FIRST_TRACK", fmt.Sprintf("Debug files saved in: %s", debugManager.baseDir))
											debugMsg("EXIT_ON_FIRST_TRACK", fmt.Sprintf("Use session ID: %s to identify this tracking session", session.sessionID))

											// Graceful shutdown
											debugManager.Stop()
											spatialIntegration.GetPTZController().Stop()
											if cameraStateManager != nil {
												cameraStateManager.Stop()
											}
											os.Exit(0)
										}
									}

									// LOG DETAILED TRACKING STATE TO DEBUG SESSION
									// This captures the same debug info we print to console for later analysis
									allTrackedObjects := spatialIntegration.GetTrackedObjects()

									// Count lock candidates and locked boats
									lockCandidates := 0
									lockedBoats := 0
									boatSummaries := make([]map[string]interface{}, 0)

									for objID, obj := range allTrackedObjects {
										// Note: TrackedObject doesn't have IsLocked field, so we use detection count as proxy
										lockEligible := obj.DetectionCount >= 3 && obj.Confidence > 0.30
										if lockEligible {
											lockCandidates++
										}

										// For target object, consider it locked since it's being tracked
										isCurrentTarget := obj.ObjectID == objectID
										if isCurrentTarget {
											lockedBoats++
										}

										boatSummary := map[string]interface{}{
											"Object_ID":         objID,
											"Center_X":          obj.CenterX,
											"Center_Y":          obj.CenterY,
											"Detections":        obj.DetectionCount,
											"Confidence":        obj.Confidence,
											"Lost_Frames":       obj.LostFrames,
											"Lock_Eligible":     lockEligible,
											"Is_Current_Target": isCurrentTarget,
											"Area":              obj.Area,
										}
										boatSummaries = append(boatSummaries, boatSummary)
									}

									// Log comprehensive tracking state
									session.LogEvent("DETAILED_TRACKING_STATE",
										fmt.Sprintf("Frame %d tracking state: %d boats, %d lock candidates, %d locked",
											frameCount, len(allTrackedObjects), lockCandidates, lockedBoats),
										map[string]interface{}{
											"Frame_Count":           frameCount,
											"Total_Boats":           len(allTrackedObjects),
											"Lock_Candidates":       lockCandidates,
											"Locked_Boats":          lockedBoats,
											"Detections_This_Frame": len(detectionRects),
											"Boat_Details":          boatSummaries,
											"Tracking_Mode":         getModeName(currentMode),
										})

									// LOG DETECTION DETAILS TO DEBUG SESSION
									if len(detectionRects) > 0 {
										detectionDetails := make([]map[string]interface{}, 0)
										for i, detection := range detectionRects {
											className := ""
											confidence := 0.0
											if i < len(detectionClassNames) {
												className = detectionClassNames[i]
											}
											if i < len(detectionConfidences) {
												confidence = detectionConfidences[i]
											}

											detectionDetail := map[string]interface{}{
												"Index":      i,
												"X":          detection.Min.X,
												"Y":          detection.Min.Y,
												"Width":      detection.Dx(),
												"Height":     detection.Dy(),
												"ClassName":  className,
												"Confidence": confidence,
											}
											detectionDetails = append(detectionDetails, detectionDetail)
										}

										session.LogEvent("DETECTION_ANALYSIS",
											fmt.Sprintf("Frame %d: %d detections analyzed for tracking relevance", frameCount, len(detectionRects)),
											map[string]interface{}{
												"Frame_Count":       frameCount,
												"Detection_Count":   len(detectionRects),
												"Detection_Details": detectionDetails,
											})
									}

									// LOG LOCK PROGRESSION ANALYSIS TO DEBUG SESSION
									lockIssueAnalysis := make([]map[string]interface{}, 0)
									for objID, obj := range allTrackedObjects {
										meetsDetectionCriteria := obj.DetectionCount >= 3
										meetsConfidenceCriteria := obj.Confidence > 0.30
										isLockReady := meetsDetectionCriteria && meetsConfidenceCriteria

										issueAnalysis := map[string]interface{}{
											"Object_ID":                 objID,
											"Detection_Count":           obj.DetectionCount,
											"Meets_Detection_Criteria":  meetsDetectionCriteria,
											"Confidence":                obj.Confidence,
											"Meets_Confidence_Criteria": meetsConfidenceCriteria,
											"Lock_Ready":                isLockReady,
											"Lost_Frames":               obj.LostFrames,
										}

										if !isLockReady {
											blockers := make([]string, 0)
											if !meetsDetectionCriteria {
												blockers = append(blockers, fmt.Sprintf("need %d more detections", 3-obj.DetectionCount))
											}
											if !meetsConfidenceCriteria {
												blockers = append(blockers, fmt.Sprintf("confidence %.3f too low", obj.Confidence))
											}
											issueAnalysis["Lock_Blockers"] = blockers
										}

										lockIssueAnalysis = append(lockIssueAnalysis, issueAnalysis)
									}

									session.LogEvent("LOCK_PROGRESSION_ANALYSIS",
										fmt.Sprintf("Frame %d: Lock progression analysis for %d boats", frameCount, len(allTrackedObjects)),
										map[string]interface{}{
											"Boat_Lock_Analysis":      lockIssueAnalysis,
											"Min_Detections_Required": 3,
											"Min_Confidence_Required": 0.3,
										})

									// Get the tracked object data
									currentTrackedObjects := spatialIntegration.GetTrackedObjects()
									var targetObj *tracking.TrackedObject
									var targetObjExists bool
									// Find the target object by ObjectID
									for _, obj := range currentTrackedObjects {
										if obj.ObjectID == objectID {
											targetObj = obj
											targetObjExists = true
											break
										}
									}
									if targetObjExists {
										// Only save frames when there are actual detections
										hasDetections := len(detectionRects) > 0

										if hasDetections {
											// Save current overlay frame with tracking overlay showing COMPLETE user experience
											// This includes all predictions, tracking decisions, overlays, etc.
											overlayFrameFile := session.SaveOverlayFrame(frameToWrite, debugManager)

											// Log overlay image saving (every frame now)
											if overlayFrameFile != "" {
												debugMsg("DEBUG", fmt.Sprintf("ACTIVE TARGET %s - saved overlay image %s", objectID, overlayFrameFile))
											} else {
												debugMsg("DEBUG", fmt.Sprintf("ACTIVE TARGET %s - failed to save overlay image (queue full)", objectID))
											}

											// Log active tracking event with frame data
											trackingData := map[string]interface{}{
												"Object_ID":            objectID,
												"Center_X":             targetObj.CenterX,
												"Center_Y":             targetObj.CenterY,
												"Width":                targetObj.Width,
												"Height":               targetObj.Height,
												"Area":                 targetObj.Area,
												"Confidence":           targetObj.Confidence,
												"ClassName":            targetObj.ClassName,
												"TrackedFrames":        targetObj.TrackedFrames,
												"LostFrames":           targetObj.LostFrames,
												"DetectionCount":       targetObj.DetectionCount,
												"TrackingMode":         "ACTIVE_TARGET_LOCK",
												"HasDetections":        true,
												"DetectionCount_Frame": len(detectionRects),
												"Frame_Number":         session.frameCounter,
												"Images_Saved":         overlayFrameFile != "",
												"Save_Policy":          "Every frame (complete user experience)",
											}

											if overlayFrameFile != "" {
												trackingData["Overlay_Frame"] = overlayFrameFile
											}

											session.LogEvent("ACTIVE_TRACKING_UPDATE",
												fmt.Sprintf("ACTIVE TARGET %s at (%d,%d) [%dx%d] - conf:%.3f saved:%s",
													objectID, targetObj.CenterX, targetObj.CenterY, targetObj.Width, targetObj.Height,
													targetObj.Confidence, overlayFrameFile != ""),
												trackingData)

										} else {
											// Log tracking without detections - still useful for tracking state
											trackingData := map[string]interface{}{
												"Object_ID":     objectID,
												"Center_X":      targetObj.CenterX,
												"Center_Y":      targetObj.CenterY,
												"Confidence":    targetObj.Confidence,
												"ClassName":     targetObj.ClassName,
												"TrackedFrames": targetObj.TrackedFrames,
												"LostFrames":    targetObj.LostFrames,
												"TrackingMode":  "ACTIVE_TARGET_LOCK",
												"HasDetections": false,
												"Note":          "Active target but no detections this frame - no images saved",
											}

											session.LogEvent("ACTIVE_TRACKING_NO_DETECTION",
												fmt.Sprintf("ACTIVE TARGET %s tracked at (%d,%d) - no detections", objectID, targetObj.CenterX, targetObj.CenterY),
												trackingData)
										}
									} else {
										// Active target ID but object no longer exists - target lost
										session := debugManager.GetSession(objectID)
										if session.enabled {
											session.LogEvent("TARGET_LOST", fmt.Sprintf("ACTIVE TARGET %s lost - no longer in tracked objects", objectID), map[string]interface{}{
												"Object_ID":    objectID,
												"TrackingMode": "TARGET_LOST",
												"Reason":       "Active target no longer in tracked objects",
											})
											debugMsg("DEBUG", fmt.Sprintf("Ended session for lost active target %s", objectID))
										}
										debugManager.EndSession(objectID)
									}
								} // End debugMode block
							} else {
								debugMsgVerbose("DEBUG", "Tracking mode but no active target lock - no debug session")
							}
						} else {
							// Log scanning mode to decision terminal
							if debugMode {
								// Remove spam: renderer.LogDecision("Mode: SCANNING", "MODE", 0)
							}

							// Only print debug message every 5 seconds to avoid spam
							if time.Since(lastModeLogTime) > 5*time.Second {
								debugMsg("DEBUG", fmt.Sprintf("Not in tracking mode (%s) - no debug sessions", getModeName(currentMode)))
								lastModeLogTime = time.Now()
							}

							// MEMORY LEAK FIX: Clean up sessions more carefully to prevent crashes
							// Only clean up when we're truly in scanning mode (no target boat at all)
							debugManager.mu.RLock()
							sessionCount := len(debugManager.sessions)
							debugManager.mu.RUnlock()

							if currentMode == tracking.ModeScanning && sessionCount > 0 {
								// Double-check we're really not tracking anything
								trackedObjects := spatialIntegration.GetTrackedObjects()
								if len(trackedObjects) == 0 {
									debugManager.mu.RLock()
									sessionCountForLog := len(debugManager.sessions)
									debugManager.mu.RUnlock()
									debugMsg("DEBUG", fmt.Sprintf("Confirmed no tracked objects - safely cleaning up %d debug sessions", sessionCountForLog))

									debugManager.mu.Lock()
									sessionList := make([]string, 0, len(debugManager.sessions))
									for objIDStr := range debugManager.sessions {
										sessionList = append(sessionList, objIDStr)
									}
									debugManager.mu.Unlock()

									// Clean up sessions one by one to prevent race conditions
									for _, objIDStr := range sessionList {
										session := debugManager.GetSession(objIDStr)
										if session.enabled {
											session.LogEvent("MODE_CHANGE", "Confirmed scanning mode - ending debug session", map[string]interface{}{
												"TrackingMode":   getModeName(currentMode),
												"TrackedObjects": len(trackedObjects),
												"Reason":         "No tracked objects - confirmed scanning mode",
											})
											debugMsg("DEBUG", fmt.Sprintf("Safely ended session %s (no tracked objects)", objIDStr))
										}
										debugManager.EndSession(objIDStr)
									}
								} else {
									debugMsg("DEBUG", fmt.Sprintf("Mode says scanning but still have %d tracked objects - keeping sessions", len(trackedObjects)))
								}
							}
						}
					}

					// CONDITIONAL TARGET OVERLAY: Show tracking overlay only when enabled
					if *targetOverlay {
						// Check what GetTrackedObjects actually returns
						trackedObjects := spatialIntegration.GetTrackedObjects()

						// Throttle debug output - only show every 3 seconds when empty, always show when objects found
						shouldLogDebug := len(trackedObjects) > 0 ||
							(lastMainDebugTime.IsZero() || time.Since(lastMainDebugTime) > 3*time.Second)

						if shouldLogDebug {
							debugMsgVerbose("TARGET_OVERLAY", fmt.Sprintf("🔍 spatialIntegration.GetTrackedObjects() returned %d objects", len(trackedObjects)))
							if len(trackedObjects) == 0 {
								lastMainDebugTime = time.Now()
							}
						}

						if err := renderer.CreateTrackingOverlay(frameToWrite, trackedObjects, *targetOverlay, spatialIntegration, debugManager, *targetDisplayTracked, !cameraStateManager.IsIdle()); err != nil {
							debugMsg("ERROR", fmt.Sprintf("Failed to create tracking overlay: %v", err))
							continue
						}

						// Get tracking history and future track
						history, futureTrack, velX, velY := spatialIntegration.GetTrackingInfo()

						// Draw tracking visualization
						renderer.DrawTrackingPath(&frameToWrite, history, futureTrack, velX, velY)
					}

					// CONDITIONAL TERMINAL OVERLAY: Show debug terminal only when enabled
					if *terminalOverlay {
						// Draw decision terminal
						renderer.DrawDecisionTerminal(&frameToWrite, spatialIntegration, *terminalOverlay, globalDebugLogger)
					}

					// DISABLED: DrawTrackingDecision - causes confusing yellow TARGET box in wrong positions
					// This conflicts with the clean military targeting system and uses stale tracking data
					/*
						// Draw tracking decision overlay
						if trackingDecision := spatialIntegration.GetTrackingDecision(); trackingDecision != nil {
							// Log tracking decision to terminal (DEBUG MODE)
							if debugMode {
								// Log camera command
								if trackingDecision.Command != "" && trackingDecision.Command != "IDLE" {
									renderer.LogDecision(fmt.Sprintf("CMD: %s", trackingDecision.Command), "COMMAND", 1)
								}

								// Log key logic points
								if len(trackingDecision.Logic) > 0 {
									// Show most important logic point
									renderer.LogDecision(trackingDecision.Logic[0], "LOGIC", 0)
								}
							}

							// Convert tracking.TrackingDecision to overlay.TrackingDecision
							overlayDecision := &overlay.TrackingDecision{
								CurrentPosition:    trackingDecision.CurrentPosition,
								TargetPosition:     trackingDecision.TargetPosition,
								Command:            trackingDecision.Command,
								PanAdjustment:      trackingDecision.PanAdjustment,
								TiltAdjustment:     trackingDecision.TiltAdjustment,
								ZoomLevel:          trackingDecision.ZoomLevel,
								Logic:              trackingDecision.Logic,
								DistanceFromCenter: trackingDecision.DistanceFromCenter,
								TrackingEffort:     trackingDecision.TrackingEffort,
								Confidence:         trackingDecision.Confidence,
							}
							renderer.DrawTrackingDecision(&frameToWrite, overlayDecision)

							// DEBUG: Log tracking decisions to active sessions (frames already saved in tracking update)
							if debugMode {
								// Get current PTZ position for context
								currentPos := spatialIntegration.GetPTZController().GetCurrentPosition()

								// Create unique decision signature for spam prevention
								decisionSignature := fmt.Sprintf("%s|%.1f,%.1f,%.1f|%d,%d",
									trackingDecision.Command,
									currentPos.Pan, currentPos.Tilt, currentPos.Zoom,
									trackingDecision.TargetPosition.X, trackingDecision.TargetPosition.Y)

								// Log tracking decision to ALL active debug sessions (with spam prevention)
								debugManager.mu.RLock()
								for _, session := range debugManager.sessions {
									if session.enabled && session.ShouldLogTrackingDecision(decisionSignature) {
										// Log tracking decision event (no duplicate frame saving)
										trackingData := map[string]interface{}{
											"Current_Pan":          currentPos.Pan,
											"Current_Tilt":         currentPos.Tilt,
											"Current_Zoom":         currentPos.Zoom,
											"Target_Position_X":    trackingDecision.TargetPosition.X,
											"Target_Position_Y":    trackingDecision.TargetPosition.Y,
											"Pan_Adjustment":       trackingDecision.PanAdjustment,
											"Tilt_Adjustment":      trackingDecision.TiltAdjustment,
											"Distance_From_Center": trackingDecision.DistanceFromCenter,
											"Tracking_Effort":      trackingDecision.TrackingEffort,
											"Decision_Logic":       trackingDecision.Logic,
											"Command":              trackingDecision.Command,
											"Confidence":           trackingDecision.Confidence,
										}

										session.LogEvent("TRACKING_DECISION",
											fmt.Sprintf("Command: %s, Logic: %s", trackingDecision.Command, trackingDecision.Logic),
											trackingData)
									}
								}
								debugManager.mu.RUnlock()
							}
						}
					*/

					output.Close()
					trackMatClose("yolo")
					blob.Close()
					trackMatClose("yolo")
				}

				// Draw PIP zoom when target is locked (if enabled by flag)
				if pipZoomEnabled {
					isTracking := spatialIntegration.GetCurrentMode() == tracking.ModeTracking
					cameraMoving := spatialIntegration.GetCameraStateManager() != nil && !spatialIntegration.GetCameraStateManager().IsIdle()
					renderer.DrawPIPZoom(&frameToWrite, frame, spatialIntegration.GetTrackedObjects(), isTracking, cameraMoving, spatialIntegration)
				}

				// SAVE POST-OVERLAY FRAME: Only save during LOCK/SUPER LOCK with detections
				if *postOverlayJpg && spatialIntegration.GetCurrentMode() == tracking.ModeTracking && len(detectionRects) > 0 {
					// Only save if we have a locked target
					if lockedTarget := spatialIntegration.GetLockedTargetForPIP(); lockedTarget != nil {
						saveJpegFrame(frameToWrite, *jpgPath, "post-overlay", len(detectionRects))
					}
				}

				// Write frame to FFmpeg using optimized direct write
				writeStart := time.Now()

				// Get frame data and ensure it's valid
				frameBytes, err := frameToWrite.DataPtrUint8()
				if err != nil || frameBytes == nil {
					debugMsg("FFMPEG_ERROR", fmt.Sprintf("Could not get data pointer from Mat: %v", err))
					frameToWrite.Close()
					trackMatClose("buffer")
					continue
				}

				// PERFORMANCE OPTIMIZATION: Write async to reduce network blocking
				// This queues the frame for async writing to the remote FFmpeg server
				writeStart = time.Now()
				requiredSize := len(frameBytes)
				err = ffmpegManager.WriteAsync(frameBytes, frameData.sequence)
				writeTime := time.Since(writeStart)

				// Update debug info
				ffmpegManager.UpdateDebugInfo(requiredSize, writeTime, err)

				if err != nil {
					debugMsg("FFMPEG_ERROR", fmt.Sprintf("Could not write frame to FFmpeg stdin: %v", err))
					debugMsg("FFMPEG_ERROR", fmt.Sprintf("Error details: %T - %v", err, err))
					debugMsg("FFMPEG_ERROR", "This indicates FFmpeg has crashed or stdin pipe is broken")
					debugMsg("FFMPEG_ERROR", fmt.Sprintf("Frame size: %d bytes", requiredSize))
					debugMsg("FFMPEG_ERROR", "Triggering emergency shutdown from stdin write failure")

					frameToWrite.Close()
					trackMatClose("buffer")

					// Signal FFmpeg failure immediately
					ffmpegManager.Stop()

					// Kill any remaining FFmpeg processes
					exec.Command("pkill", "-9", "ffmpeg").Run()

					// Give a moment for cleanup
					time.Sleep(100 * time.Millisecond)

					// Force exit with FFmpeg error code - multiple attempts for reliability
					debugMsg("FFMPEG_STDIN_ERROR", "Force exiting application with code 3 (stdin write failure)")
					go func() {
						time.Sleep(50 * time.Millisecond)
						debugMsg("FFMPEG_STDIN_ERROR", "Secondary exit attempt from stdin error")
						os.Exit(3)
					}()
					os.Exit(3)
				}

				stats.UpdateWrite(writeTime)
				stats.UpdateProcess()
				lastSequence = frameData.sequence

				// Clean up
				frameToWrite.Close()
				trackMatClose("buffer")

				// MEMORY LEAK FIX: Periodic garbage collection during heavy processing
				if frameCount%300 == 0 { // Every 10 seconds at 30fps
					var m runtime.MemStats
					runtime.ReadMemStats(&m)
					if m.Alloc > 500*1024*1024 { // More than 500MB allocated
						debugMsg("MEMORY", fmt.Sprintf("High memory usage detected: %d MB - forcing GC", m.Alloc/(1024*1024)))
						runtime.GC()
						runtime.ReadMemStats(&m)
						debugMsg("MEMORY", fmt.Sprintf("After GC: %d MB allocated", m.Alloc/(1024*1024)))
					}
				}
			}
			frameData.frame.Close()
			trackMatClose("capture")
		}
	}
}

// getModeName returns a string representation of the tracking mode
func getModeName(mode tracking.TrackingMode) string {
	switch mode {
	case tracking.ModeScanning:
		return "Scanning"
	case tracking.ModeTracking:
		return "Tracking"
	case tracking.ModeRecovery:
		return "Recovery"
	default:
		return "Unknown"
	}
}
