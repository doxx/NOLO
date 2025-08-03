package overlay

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"io/ioutil"
	"math"
	"os"
	"strings"
	"time"

	"rivercam/ptz"
	"rivercam/tracking"

	"gocv.io/x/gocv"
)

// debugMsgFunc is a function that will be set by main package to use unified logging
var debugMsgFunc func(component, message string, boatID ...string)

// debugMsgVerboseFunc is a function that will be set by main package for verbose logging only
var debugMsgVerboseFunc func(component, message string, boatID ...string)

// globalDebugLogger reference for accessing tracking history functionality
var globalDebugLogger interface {
	DumpTrackingHistory(objectID string) interface{}
}

// SetDebugFunction allows main package to provide the debug logger
func SetDebugFunction(fn func(component, message string, boatID ...string)) {
	debugMsgFunc = fn
	fmt.Printf("[OVERLAY_INIT] ✅ Debug function connected from main.go\n")
}

// SetDebugVerboseFunction allows main package to provide the verbose debug logger
func SetDebugVerboseFunction(fn func(component, message string, boatID ...string)) {
	debugMsgVerboseFunc = fn
	fmt.Printf("[OVERLAY_INIT] ✅ Verbose debug function connected from main.go\n")
}

// SetDebugLogger allows main package to provide the debug logger for tracking history
func SetDebugLogger(logger interface {
	DumpTrackingHistory(objectID string) interface{}
}) {
	globalDebugLogger = logger
}

// debugMsg is a wrapper that handles nil checks
func debugMsg(component, message string, boatID ...string) {
	if debugMsgFunc != nil {
		debugMsgFunc(component, message, boatID...)
	} else {
		// PIPELINE DEBUG: If this appears, the connection is broken
		fmt.Printf("[OVERLAY_DEBUG_BROKEN] %s: %s (boatID: %v)\n", component, message, boatID)
	}
}

// CalibrationData represents the structure of pixels-inches-cal.json
type CalibrationData struct {
	CalibrationType   string                 `json:"calibration_type"`
	FrameDimensions   map[string]int         `json:"frame_dimensions"`
	ReferenceDistance int                    `json:"reference_distance"`
	ReferenceUnit     string                 `json:"reference_unit"`
	CalibrationData   []ZoomCalibrationPoint `json:"calibration_data"`
}

// ZoomCalibrationPoint represents a single zoom level calibration point
type ZoomCalibrationPoint struct {
	ZoomLevel          float64 `json:"zoom_level"`
	PixelsFor369Inches int     `json:"pixels_for_369_inches"`
	PixelsPerInch      float64 `json:"pixels_per_inch"`
	MeasurementType    string  `json:"measurement_type"`
}

// Renderer handles visualization and overlay rendering
type Renderer struct {
	detectionMessages []string
	maxMessages       int
	historyColor      color.RGBA
	predictionColor   color.RGBA
	velocityColor     color.RGBA
	decisionColor     color.RGBA
	targetColor       color.RGBA
	animationTime     float64 // For time-based animations
	militaryGreen     color.RGBA
	targetRed         color.RGBA
	systemBlue        color.RGBA
	// PIP fade state
	pipVisible       bool
	pipFadeStartTime time.Time
	pipLingerEndTime time.Time
	pipMinDisplayEnd time.Time // Minimum 5-second display time once activated
	// PIP coordinate buffering
	lastKnownCoords *PIPCoordinates
	lastUpdateTime  time.Time
	// Decision terminal state
	lastDecisionUpdate time.Time
	decisionHistory    []DecisionLogEntry
	maxDecisionHistory int
	// Speed tracking for any boat (not just locked targets)
	boatSpeedHistory map[int]*BoatSpeedTracker // Track speed history by object ID
	// Tracking overlay fade-out state
	trackingOverlayVisible bool
	lastTrackingTime       time.Time
	// Speed calculation spam prevention
	lastLoggedZoom           float64
	lastLoggedPixelsPerInch  float64
	trackingOverlayFadeStart time.Time
	lastTrackedTarget        *tracking.TrackedObject // Cache last target for fade-out period
	// Pixel-to-inches calibration data
	calibrationData   *CalibrationData
	calibrationLoaded bool
	// Debugging
	lastDebugTime time.Time

	// Per-object speed tracking for military overlay
	boatSpeedData map[string]*SimpleSpeedTracker // Speed tracking per object ID (unified format)

	// Comprehensive measurement tracking with 60-frame rolling averages
	objectMeasurements map[string]*ObjectMeasurements // Rolling measurements per object ID
	lastCleanupTime    time.Time                      // Last time stale measurements were cleaned

	// Military targeting display improvements
	targetDisplayBuffer map[int]*TargetDisplayData // Buffer for stable target rendering
	maxDisplayHistory   int                        // Number of YOLO frames to average
	lingerFrames        int                        // Frames to keep target after YOLO miss
	currentFrameNumber  int                        // Current frame number for linger tracking

	// YOLO detection history for enhanced right-side panel
	yoloDetectionHistory []YOLODetectionEntry // Rolling history of recent detections
	maxYOLOHistory       int                  // Maximum detections to keep in history (default: 5)
	yoloPanelFrameCount  int                  // Frame counter for YOLO panel display
}

// YOLODetectionEntry represents a single YOLO detection for history tracking
type YOLODetectionEntry struct {
	ClassName  string    // Name of detected object (boat, person, chair, etc.)
	Confidence float64   // Detection confidence (0.0-1.0)
	Width      int       // Bounding box width in pixels
	Height     int       // Bounding box height in pixels
	CenterX    int       // Center X coordinate
	CenterY    int       // Center Y coordinate
	Timestamp  time.Time // When this detection occurred
}

// SimpleSpeedTracker tracks speed for individual boats
type SimpleSpeedTracker struct {
	lastPosition   image.Point
	lastUpdateTime time.Time
	speedHistory   []float64 // Recent speed measurements
	maxHistory     int       // Maximum number of speed measurements to keep
	averageSpeed   float64   // Current averaged speed
}

// ObjectMeasurements tracks comprehensive rolling averages for an object over 60 frames
type ObjectMeasurements struct {
	ObjectID         string
	SpeedHistory     []float64   // ALL speed measurements from IDLE frames
	HeightHistory    []float64   // ALL height measurements from IDLE frames
	WidthHistory     []float64   // ALL width measurements from IDLE frames
	LengthHistory    []float64   // ALL length measurements from IDLE frames
	DirectionHistory []float64   // ALL direction measurements from IDLE frames (radians)
	SampleTimes      []time.Time // Timestamp for each measurement (for cleanup)
	LastUpdated      time.Time   // When last measurement was added
	LastPosition     image.Point // For speed calculation
	LastUpdateTime   time.Time   // For speed calculation timing
}

// NewSimpleSpeedTracker creates a new speed tracker for a boat
func NewSimpleSpeedTracker() *SimpleSpeedTracker {
	return &SimpleSpeedTracker{
		speedHistory: make([]float64, 0),
		maxHistory:   10, // Keep last 10 speed measurements
		averageSpeed: 0.0,
	}
}

// UpdateSpeed adds a new speed measurement and updates the average with outlier protection
func (st *SimpleSpeedTracker) UpdateSpeed(currentPos image.Point) {
	now := time.Now()

	// Calculate speed if we have previous position
	if !st.lastUpdateTime.IsZero() && (st.lastPosition.X != 0 || st.lastPosition.Y != 0) {
		deltaTime := now.Sub(st.lastUpdateTime).Seconds()

		// Only update if enough time has passed
		if deltaTime > 0.1 { // 10Hz max update rate
			deltaX := float64(currentPos.X - st.lastPosition.X)
			deltaY := float64(currentPos.Y - st.lastPosition.Y)
			pixelDistance := math.Sqrt(deltaX*deltaX + deltaY*deltaY)

			// TELEPORTATION PROTECTION: Check for unreasonable position jumps
			maxReasonableJump := 150.0 // pixels per frame (same as display averaging)

			if pixelDistance > maxReasonableJump && deltaTime < 1.0 {
				// BOAT ID MIXUP DETECTED: This looks like a different boat with same ID
				// Clear speed history and reset to prevent 250 MPH outliers
				st.speedHistory = []float64{}
				st.averageSpeed = 0.0
				st.lastPosition = currentPos
				st.lastUpdateTime = now
				if debugMsg != nil {
					debugMsg("SPEED_JUMP_PROTECTION", fmt.Sprintf("🚫🏃 Position jump %.1fpx > %.1fpx in %.2fs - resetting speed tracking to prevent outliers",
						pixelDistance, maxReasonableJump, deltaTime))
				}
				return
			}

			currentSpeed := pixelDistance / deltaTime

			// OUTLIER FILTERING: Sanity check for reasonable boat speeds
			maxReasonableSpeed := 1500.0 // pixels/second (roughly 30 MPH at typical zoom)

			if currentSpeed > maxReasonableSpeed {
				// Speed outlier detected - don't add to history
				if debugMsg != nil {
					debugMsg("SPEED_OUTLIER", fmt.Sprintf("🚫⚡ Speed outlier %.1f px/s > %.1f px/s - ignoring measurement",
						currentSpeed, maxReasonableSpeed))
				}
				// Update position but don't record the bogus speed
				st.lastPosition = currentPos
				st.lastUpdateTime = now
				return
			}

			// MOVEMENT FILTERING: Only add meaningful movement to history
			// This prevents stationary boats from pulling the average down to 0
			minMovementSpeed := 5.0 // pixels/second - roughly 0.1 mph at typical zoom levels

			if currentSpeed >= minMovementSpeed {
				// Valid movement detected - add to history
				st.speedHistory = append(st.speedHistory, currentSpeed)

				// Keep only recent measurements
				if len(st.speedHistory) > st.maxHistory {
					st.speedHistory = st.speedHistory[1:]
				}

				// Calculate average from movement-only data (no zeros)
				if len(st.speedHistory) > 0 {
					total := 0.0
					for _, speed := range st.speedHistory {
						total += speed
					}
					st.averageSpeed = total / float64(len(st.speedHistory))
				}
			} else {
				// Movement too slow - don't add to history but still update position
				if debugMsg != nil && len(st.speedHistory) > 0 {
					debugMsg("SPEED_STATIONARY", fmt.Sprintf("📍 Speed %.1f px/s < %.1f px/s (stationary) - not adding to average, keeping historical %.1f px/s",
						currentSpeed, minMovementSpeed, st.averageSpeed))
				}
			}

			// Update tracking state
			st.lastPosition = currentPos
			st.lastUpdateTime = now
		}
	} else {
		// First time - just update position
		st.lastPosition = currentPos
		st.lastUpdateTime = now
	}
}

// GetAverageSpeed returns the current averaged speed
func (st *SimpleSpeedTracker) GetAverageSpeed() float64 {
	return st.averageSpeed
}

// Reset clears all speed history and tracking state (for boat ID mixups)
func (st *SimpleSpeedTracker) Reset() {
	st.speedHistory = []float64{}
	st.averageSpeed = 0.0
	st.lastPosition = image.Point{}
	st.lastUpdateTime = time.Time{}
}

// GetMovementSampleCount returns the number of movement samples recorded
func (st *SimpleSpeedTracker) GetMovementSampleCount() int {
	return len(st.speedHistory)
}

// TargetDisplayData stores averaged and lingered display information for stable rendering
type TargetDisplayData struct {
	// Averaged position and size from recent YOLO detections
	CenterX int
	CenterY int
	Width   int
	Height  int

	// Position/size history for averaging
	PositionHistory []image.Point // Recent center positions
	SizeHistory     []image.Point // Recent width/height pairs

	// Linger support (frame-based legacy)
	LastSeenFrame int     // Frame number when last detected by YOLO
	CurrentFrame  int     // Current frame number
	Confidence    float64 // Last known confidence
	ClassName     string  // Object class

	// NEW: Time-based persistence (like PIP)
	FirstSeenTime      time.Time     // When target first appeared
	LastUpdateTime     time.Time     // Last YOLO detection time
	MinDisplayDuration time.Duration // Minimum time to show target
	LingerDuration     time.Duration // How long to linger after loss

	// Stability metrics
	AvgStability  float64 // How stable the averaged position is
	SizeStability float64 // How stable the size is

	// NEW: Exponential smoothing for jitter reduction (like PIP)
	SmoothedCenterX float64 // Exponentially smoothed X coordinate
	SmoothedCenterY float64 // Exponentially smoothed Y coordinate
	SmoothedWidth   float64 // Exponentially smoothed width
	SmoothedHeight  float64 // Exponentially smoothed height

	// NEW: Spatial coordinate tracking for camera-movement compensation
	SpatialPan     float64 // Real-world pan coordinate when first detected
	SpatialTilt    float64 // Real-world tilt coordinate when first detected
	SpatialZoom    float64 // Zoom level when spatial coordinates were calculated
	HasSpatialData bool    // Whether spatial coordinates are valid
}

// spatialToPixelCoordinate converts real-world spatial coordinates back to pixel coordinates
// This is the INVERSE of calculateSpatialCoordinatesForPixel - enables camera-movement compensation
func (r *Renderer) spatialToPixelCoordinate(spatialPan, spatialTilt float64, spatialInterface interface{}) (int, int, bool) {
	// Use reflection-like approach to access the spatial integration methods
	// We know spatialInterface is *tracking.SpatialIntegration but we can't import it here

	// Use interface{} and method calls via reflection/type assertion
	// This is a simplified approach that will work with the existing codebase

	// Try to call calculateSpatialCoordinatesForPixel in reverse
	// For now, we'll use a fallback calculation with hardcoded calibration
	// TODO: This needs access to the actual spatial integration object

	// FALLBACK IMPLEMENTATION: Use basic calibration values for now
	// This is a simplified version that will be improved once we have proper interface access

	// Assume current camera position is approximately center (fallback)
	frameCenterX := 1300 // 2600/2
	frameCenterY := 713  // 1426/2

	// For now, just return the input coordinates unchanged as a placeholder
	// This will be enhanced once we have proper access to spatial integration
	debugMsgVerboseFunc("SPATIAL_CONVERSION", fmt.Sprintf("🌍→📺 Spatial conversion placeholder: (%.1f°,%.1f°)", spatialPan, spatialTilt))

	return frameCenterX, frameCenterY, false // Return false to indicate this is not yet implemented
}

// spatialCoordinateResult holds the result of spatial coordinate calculation
type spatialCoordinateResult struct {
	pan     float64
	tilt    float64
	zoom    float64
	isValid bool
}

// calculateSpatialCoordinateForPixel calculates spatial coordinates for a pixel position
func (r *Renderer) calculateSpatialCoordinateForPixel(pixelX, pixelY int, spatialIntegration interface{}) spatialCoordinateResult {
	// This is a simplified implementation that tries to use reflection to call the spatial integration
	// For now, we'll use a placeholder that marks coordinates as invalid
	// TODO: Implement proper spatial integration access

	debugMsgVerboseFunc("SPATIAL_CALC", fmt.Sprintf("🧮 Calculating spatial coordinates for pixel (%d,%d)", pixelX, pixelY))

	// Placeholder implementation - return invalid coordinates for now
	return spatialCoordinateResult{
		pan:     0.0,
		tilt:    0.0,
		zoom:    10.0,
		isValid: false, // Mark as invalid until we implement proper calculation
	}
}

// DecisionLogEntry represents a single decision log entry for the terminal
type DecisionLogEntry struct {
	Timestamp time.Time
	Message   string
	Type      string // "MODE", "COMMAND", "LOGIC", "STATUS"
	Priority  int    // 0=normal, 1=important, 2=critical
}

// BoatSpeedTracker tracks speed history for outlier filtering and averaging
type BoatSpeedTracker struct {
	LastPosition  image.Point
	LastTime      time.Time
	SpeedHistory  []float64 // Recent speed measurements in px/s
	MaxHistory    int       // Maximum history to keep
	ValidMeasures int       // Count of valid speed measurements
}

// PIPCoordinates stores the last known good coordinates for PIP display
type PIPCoordinates struct {
	CenterX    int
	CenterY    int
	Width      int
	Height     int
	ClassName  string
	Confidence float64

	// Exponential smoothing for jitter reduction
	SmoothedX float64
	SmoothedY float64
	SmoothedW float64
	SmoothedH float64
}

// applyUnsharpMasking function removed - was causing CPU bottleneck in PIP processing
// Original function eliminated double-resize operations and 9-16ms processing overhead

// NewRenderer creates a new overlay renderer
func NewRenderer() *Renderer {
	return &Renderer{
		detectionMessages:      make([]string, 0),
		maxMessages:            5,
		historyColor:           color.RGBA{0, 255, 0, 180},   // Semi-transparent green for history
		predictionColor:        color.RGBA{0, 255, 0, 180},   // Semi-transparent military green for prediction
		velocityColor:          color.RGBA{0, 191, 255, 180}, // Semi-transparent deep sky blue for velocity
		decisionColor:          color.RGBA{255, 255, 0, 255}, // Bright yellow for decision info
		targetColor:            color.RGBA{255, 0, 255, 255}, // Bright magenta for target position
		animationTime:          0.0,                          // Start animation time at 0
		militaryGreen:          color.RGBA{0, 255, 0, 255},   // Bright military green
		targetRed:              color.RGBA{255, 0, 0, 255},   // Target red
		systemBlue:             color.RGBA{0, 150, 255, 255}, // System blue
		decisionHistory:        make([]DecisionLogEntry, 0),
		maxDecisionHistory:     20, // Increased from 8 to 20 for more terminal history
		boatSpeedHistory:       make(map[int]*BoatSpeedTracker),
		targetDisplayBuffer:    make(map[int]*TargetDisplayData),
		maxDisplayHistory:      3,                                    // RESTORED: 3 frames for smooth targeting with jump protection
		lingerFrames:           5,                                    // Frames to keep target after YOLO miss
		trackingOverlayVisible: true,                                 // Start visible
		lastTrackingTime:       time.Now(),                           // Initialize to current time
		boatSpeedData:          make(map[string]*SimpleSpeedTracker), // Initialize speed tracking map
		objectMeasurements:     make(map[string]*ObjectMeasurements), // Initialize comprehensive measurement tracking
		lastCleanupTime:        time.Now(),                           // Initialize cleanup timer
	}
}

// UpdateAnimation advances the animation time for smooth effects
func (r *Renderer) UpdateAnimation(deltaTime float64) {
	r.animationTime += deltaTime * 1000.0 // Convert to milliseconds

	// Keep animation time bounded to prevent float overflow
	if r.animationTime > 1000.0 {
		r.animationTime -= 1000.0
	}
}

// CreateTrackingOverlay draws tracking information on the frame
func (r *Renderer) CreateTrackingOverlay(img gocv.Mat, trackedObjects map[int]*tracking.TrackedObject, debugMode bool, spatialIntegration interface{}, debugManager interface{}, targetDisplayTracked bool, cameraMoving bool) error {
	// PIPELINE TEST: This should appear in debug files if pipeline works
	if debugMode && r.currentFrameNumber%30 == 0 { // Every 30 frames
		debugMsgVerboseFunc("PIPELINE_TEST", fmt.Sprintf("🔧 Overlay pipeline test - frame %d", r.currentFrameNumber))
	}

	// Update animation time (assuming ~25fps)
	r.UpdateAnimation(0.04)

	// Increment frame counter for linger tracking
	r.currentFrameNumber++

	// Update display buffer with current YOLO data for averaging and linger
	r.updateTargetDisplayBuffer(trackedObjects, r.currentFrameNumber, spatialIntegration, cameraMoving)

	// Clean up stale speed trackers for boats that no longer exist
	r.cleanupStaleSpeedTrackers(trackedObjects)

	// Clean up stale measurements (60+ seconds old)
	r.cleanupStaleMeasurements(debugMode, debugManager) // Pass debugManager for JPEG counter cleanup

	// Get stable, averaged target display data
	stableTargets := r.getStableTargetDisplay(spatialIntegration)

	now := time.Now()

	// THROTTLE DEBUG MESSAGES - only log every 2 seconds to reduce spam
	shouldDebug := debugMode && (r.lastDebugTime.IsZero() || now.Sub(r.lastDebugTime) > 2*time.Second)
	if shouldDebug {
		r.lastDebugTime = now
		if debugMsgVerboseFunc != nil {
			debugMsgVerboseFunc("VISUAL_TARGET", fmt.Sprintf("📊 CreateTrackingOverlay: %d raw objects → %d stable targets (averaging over %d frames, linger %d frames) [LIGHTNING-FAST: 1/2+ with 3-frame averaging]",
				len(trackedObjects), len(stableTargets), r.maxDisplayHistory, r.lingerFrames))
		}
	}

	// Draw all stable targets with military-style targeting using averaged data
	for id, stableTarget := range stableTargets {
		// Get corresponding tracked object for frame count data (if still exists)
		var trackedFrames int
		var isLingering bool
		var objectID string // Add this to track the ObjectID
		framesSinceLastSeen := stableTarget.CurrentFrame - stableTarget.LastSeenFrame

		// FIND MATCHING OBJECT BY POSITION instead of ID (IDs don't align between systems)
		var originalObj *tracking.TrackedObject
		var found bool

		// Look for tracked object at similar position (within 50 pixels)
		for _, obj := range trackedObjects {
			deltaX := math.Abs(float64(obj.CenterX - stableTarget.CenterX))
			deltaY := math.Abs(float64(obj.CenterY - stableTarget.CenterY))
			distance := math.Sqrt(deltaX*deltaX + deltaY*deltaY)

			if distance <= 50.0 { // 50px matching tolerance
				originalObj = obj
				found = true
				break
			}
		}

		if found {
			// Fresh YOLO detection - use real tracking data
			trackedFrames = originalObj.TrackedFrames
			objectID = originalObj.ObjectID // Real ObjectID like "20250731-2-52.002"
			isLingering = false
		} else {
			// Lingering target - estimate tracking frames based on stability
			trackedFrames = int(15.0 * stableTarget.AvgStability) // Higher stability = more "mature"
			objectID = fmt.Sprintf("lingering_%d", id)            // Generate temporary ObjectID for lingering targets
			isLingering = true
		}

		// Debug output to match with stats overlay debugging (throttled)
		if shouldDebug {
			lingerStatus := ""
			if isLingering {
				lingerStatus = fmt.Sprintf(" [LINGER:%d/%d]", framesSinceLastSeen, r.lingerFrames)
			}
			stabilityInfo := fmt.Sprintf(" [PosStab:%.2f SizeStab:%.2f]", stableTarget.AvgStability, stableTarget.SizeStability)

			if trackedFrames >= 2 {
				if debugMsgVerboseFunc != nil {
					debugMsgVerboseFunc("VISUAL_TARGET", fmt.Sprintf("🟢 Drawing green box for ID:%d Frames:%d Class:%s%s%s",
						id, trackedFrames, stableTarget.ClassName, lingerStatus, stabilityInfo))
				}
			} else if trackedFrames >= 1 {
				if debugMsgVerboseFunc != nil {
					debugMsgVerboseFunc("VISUAL_TARGET", fmt.Sprintf("🔴 Drawing red box for ID:%d Frames:%d Class:%s%s%s",
						id, trackedFrames, stableTarget.ClassName, lingerStatus, stabilityInfo))
				}
			}
		}

		// Create rectangle from stable averaged data with 20% expansion for display
		baseRect := image.Rect(
			stableTarget.CenterX-stableTarget.Width/2,
			stableTarget.CenterY-stableTarget.Height/2,
			stableTarget.CenterX+stableTarget.Width/2,
			stableTarget.CenterY+stableTarget.Height/2,
		)

		// IMPROVEMENT: 20% larger display to prevent blocking boat view
		expandW := int(float64(stableTarget.Width) * 0.20)  // 20% wider
		expandH := int(float64(stableTarget.Height) * 0.20) // 20% taller

		displayRect := image.Rect(
			baseRect.Min.X-expandW,
			baseRect.Min.Y-expandH,
			baseRect.Max.X+expandW,
			baseRect.Max.Y+expandH,
		)

		// Calculate tracking maturity with lightning-fast progression (2 frames = full maturity)
		trackingMaturity := math.Min(float64(trackedFrames)/2.0, 1.0) // Lightning-fast: 2 frames = 100% maturity

		// Create mock tracked object for drawing functions (they expect *tracking.TrackedObject)
		mockObj := &tracking.TrackedObject{
			ID:            id,
			ObjectID:      objectID, // FIX: Set the ObjectID field for direction tracking
			CenterX:       stableTarget.CenterX,
			CenterY:       stableTarget.CenterY,
			Width:         stableTarget.Width,
			Height:        stableTarget.Height,
			TrackedFrames: trackedFrames,
			Confidence:    stableTarget.Confidence,
			ClassName:     stableTarget.ClassName,
		}

		// DISABLED: Military-style center crosshair (user preference)
		// centerPoint := image.Pt(stableTarget.CenterX, stableTarget.CenterY)
		// r.drawMilitaryCrosshair(img, centerPoint, stableTarget.Confidence, trackingMaturity)

		// Determine if we should show military targeting for this object
		showMilitaryTargeting := true
		if targetDisplayTracked {
			// Only show military targeting for the tracked object when flag is enabled
			var currentTrackedID string
			if si, ok := spatialIntegration.(*tracking.SpatialIntegration); ok {
				currentTrackedID = si.GetCurrentTrackedObject()
			}

			// Only show targeting if this object is the tracked target
			showMilitaryTargeting = (objectID == currentTrackedID)
		}

		// Show military targeting system with lightning-fast frame thresholds (conditionally)
		if showMilitaryTargeting {
			if trackedFrames >= 2 {
				// Mode 2: Locked target (2+ frames) - full military targeting system, camera movement enabled
				r.drawMilitaryLockOn(img, displayRect, mockObj, trackingMaturity, spatialIntegration)
			} else if trackedFrames >= 1 {
				// Mode 1: Initial detection (1 frame) - simple pulsing box
				r.drawMilitaryDetection(img, displayRect, mockObj, trackingMaturity)
			}
		}

		// REMOVED: Old debug text labels that caused flickering at YOLO detection rate
		// and triggered stats overlay with wrong/inconsistent data
		// Military targeting system now provides all necessary visual feedback
	}

	// REMOVED: Upper right stats window - info now integrated into military targeting overlay
	// All target information is now displayed directly on the target itself for cleaner look

	return nil
}

// DrawDetection draws a single detection on the frame using military-style targeting
// DrawYOLODetections draws all raw YOLO detections as bounding boxes for debugging with enhanced right-side panel
func (r *Renderer) DrawYOLODetections(img gocv.Mat, detections []image.Rectangle, classNames []string, confidences []float64) {
	// Use a distinct color for raw YOLO detections (light blue)
	yoloBlue := color.RGBA{R: 0x00, G: 0x7f, B: 0xff, A: 180} // Light blue, semi-transparent

	// Always update detection history for the enhanced panel (even if no detections)
	r.updateYOLODetectionHistory(detections, classNames, confidences)

	// Draw individual detection boxes on the main frame (only if detections exist)
	for i, rect := range detections {
		className := ""
		confidence := 0.0

		if i < len(classNames) {
			className = classNames[i]
		}
		if i < len(confidences) {
			confidence = confidences[i]
		}

		// Draw simple rectangle outline
		gocv.Rectangle(&img, rect, yoloBlue, 2)

		// Add small center dot
		centerX := rect.Min.X + rect.Dx()/2
		centerY := rect.Min.Y + rect.Dy()/2
		gocv.Circle(&img, image.Point{centerX, centerY}, 3, yoloBlue, -1)

		// Add label with class and confidence
		label := fmt.Sprintf("%s %.0f%%", className, confidence*100)
		labelPos := image.Point{rect.Min.X, rect.Min.Y - 8}

		// Ensure label stays within frame
		if labelPos.Y < 15 {
			labelPos.Y = rect.Max.Y + 20
		}

		gocv.PutText(&img, label, labelPos, gocv.FontHersheySimplex, 0.4, yoloBlue, 1)
	}

	// Always draw enhanced detection panel on the right side (even with 0 detections)
	r.drawEnhancedYOLOPanel(img, detections, yoloBlue)
}

// This provides stable visual feedback when the tracking system is deadlocked by camera movement
func (r *Renderer) DrawDetection(img gocv.Mat, rect image.Rectangle, className string, confidence float64) {
	// Use the same stable military targeting as LOCK mode but with different green color
	// Color #118a28 as requested
	yoloGreen := color.RGBA{
		R: 0x11,                       // 17
		G: 0x8a,                       // 138
		B: 0x28,                       // 40
		A: uint8(200 + 55*confidence), // Alpha varies with confidence (200-255)
	}

	// Use the same military components as LOCK mode
	r.drawCornerBrackets(img, rect, yoloGreen, 2, 15, math.Sin(r.animationTime*4.0)*0.3+0.7)

	// Draw center crosshair using the same style
	centerX := rect.Min.X + rect.Dx()/2
	centerY := rect.Min.Y + rect.Dy()/2
	center := image.Point{centerX, centerY}

	// Military-style crosshair (same as LOCK mode)
	size := 12
	thickness := 2
	gap := 3

	// Horizontal crosshair arms
	gocv.Line(&img,
		image.Point{center.X - size, center.Y},
		image.Point{center.X - gap, center.Y},
		yoloGreen, thickness)
	gocv.Line(&img,
		image.Point{center.X + gap, center.Y},
		image.Point{center.X + size, center.Y},
		yoloGreen, thickness)

	// Vertical crosshair arms
	gocv.Line(&img,
		image.Point{center.X, center.Y - size},
		image.Point{center.X, center.Y - gap},
		yoloGreen, thickness)
	gocv.Line(&img,
		image.Point{center.X, center.Y + gap},
		image.Point{center.X, center.Y + size},
		yoloGreen, thickness)

	// Center dot
	gocv.Circle(&img, center, 2, yoloGreen, -1)

	// Military-style status text (same font and positioning as LOCK mode)
	statusText := "RAW YOLO"
	textPos := image.Point{rect.Min.X, rect.Min.Y - 25}
	gocv.PutText(&img, statusText, textPos, gocv.FontHersheySimplex, 0.6, yoloGreen, 2)

	// Create detailed label with detection info
	label := fmt.Sprintf("%s (%.0f%%)", className, confidence*100)
	detailTextPos := image.Point{rect.Min.X, rect.Min.Y - 5}
	gocv.PutText(&img, label, detailTextPos, gocv.FontHersheySimplex, 0.3, yoloGreen, 1)
}

// updateNarrationFile updates the narration file with detection messages
func (r *Renderer) updateNarrationFile(message string) {
	// Escape special characters for FFmpeg drawtext
	escapedMessage := message
	// Escape percentage signs
	escapedMessage = strings.ReplaceAll(escapedMessage, "%", "%%")
	// Escape backslashes
	escapedMessage = strings.ReplaceAll(escapedMessage, "\\", "\\\\")
	// Escape quotes
	escapedMessage = strings.ReplaceAll(escapedMessage, "\"", "\\\"")
	// Escape newlines
	escapedMessage = strings.ReplaceAll(escapedMessage, "\n", " ")

	// Add new message to the buffer
	r.detectionMessages = append(r.detectionMessages, escapedMessage)

	// Keep only the last maxMessages
	if len(r.detectionMessages) > r.maxMessages {
		r.detectionMessages = r.detectionMessages[len(r.detectionMessages)-r.maxMessages:]
	}

	// Write messages to file with proper line endings
	content := strings.Join(r.detectionMessages, "\n") + "\n"
	err := ioutil.WriteFile("/tmp/ai_narration.txt", []byte(content), 0644)
	if err != nil {
		if debugMsg != nil {
			debugMsg("ERROR", fmt.Sprintf("Failed to write narration file: %v", err))
		}
	}
}

// DrawTrackingPath draws the historical path and simplified future prediction
func (r *Renderer) DrawTrackingPath(img *gocv.Mat, history []tracking.DetectionPoint, futureTrack []tracking.DetectionPoint, velX, velY float64) {
	// REMOVED: Complex prediction system - it's disabled in actual tracking logic
	// Just draw simple historical path without the CPU-intensive prediction rendering

	if len(history) <= 1 {
		return // Need at least 2 points for a path
	}

	// Draw simple historical path with military green
	for i := 1; i < len(history); i++ {
		prev := history[i-1]
		curr := history[i]

		// Draw line between consecutive points
		gocv.Line(img, prev.Position, curr.Position, color.RGBA{0, 255, 0, 180}, 2)

		// Draw small circle at each history point
		gocv.Circle(img, curr.Position, 2, color.RGBA{0, 255, 0, 200}, -1)
	}

	// Show simple velocity info at current position if moving
	if len(history) > 0 && (math.Abs(velX) > 2 || math.Abs(velY) > 2) {
		lastPoint := history[len(history)-1]
		speed := math.Sqrt(velX*velX + velY*velY)
		speedText := fmt.Sprintf("%.0f px/s", speed)
		speedPos := image.Point{lastPoint.Position.X + 10, lastPoint.Position.Y - 10}
		gocv.PutText(img, speedText, speedPos, gocv.FontHersheySimplex, 0.4, color.RGBA{0, 255, 0, 255}, 1)
	}
}

// drawDashedLine draws a dashed line between two points
func (r *Renderer) drawDashedLine(img *gocv.Mat, start, end image.Point, color color.RGBA, thickness int) {
	// Calculate line length and angle
	dx := float64(end.X - start.X)
	dy := float64(end.Y - start.Y)
	length := math.Sqrt(dx*dx + dy*dy)
	angle := math.Atan2(dy, dx)

	// Draw dashed line
	dashLength := 10.0
	gapLength := 5.0
	currentLength := 0.0

	for currentLength < length {
		// Calculate start and end of this dash
		dashStart := image.Point{
			X: start.X + int(currentLength*math.Cos(angle)),
			Y: start.Y + int(currentLength*math.Sin(angle)),
		}
		dashEnd := image.Point{
			X: start.X + int(math.Min(currentLength+dashLength, length)*math.Cos(angle)),
			Y: start.Y + int(math.Min(currentLength+dashLength, length)*math.Sin(angle)),
		}

		// Draw the dash
		gocv.Line(img, dashStart, dashEnd, color, thickness)

		// Move to next dash
		currentLength += dashLength + gapLength
	}
}

// TrackingDecision represents the tracking decision information
type TrackingDecision struct {
	CurrentPosition    image.Point // Where the object currently is
	TargetPosition     image.Point // Where we want the camera to point
	Command            string      // The PTZ command we're sending
	PanAdjustment      float64     // Pan adjustment amount
	TiltAdjustment     float64     // Tilt adjustment amount
	ZoomLevel          float64     // Target zoom level
	Logic              []string    // Key logic points explaining the decision
	DistanceFromCenter float64     // How far object is from center (0-1)
	TrackingEffort     float64     // How hard we're working to track (0-2+)
	Confidence         float64     // Object detection confidence
}

// DrawTrackingDecision draws simplified tracking decision overlay (PREDICTION DISABLED)
func (r *Renderer) DrawTrackingDecision(img *gocv.Mat, decision *TrackingDecision) {
	if decision == nil {
		return
	}

	// Get frame dimensions
	frameWidth := img.Cols()
	frameHeight := img.Rows()
	frameCenter := image.Point{frameWidth / 2, frameHeight / 2}

	// 1. Draw frame center crosshair
	crosshairSize := 20
	gocv.Line(img,
		image.Point{frameCenter.X - crosshairSize, frameCenter.Y},
		image.Point{frameCenter.X + crosshairSize, frameCenter.Y},
		color.RGBA{255, 255, 255, 255}, 2)
	gocv.Line(img,
		image.Point{frameCenter.X, frameCenter.Y - crosshairSize},
		image.Point{frameCenter.X, frameCenter.Y + crosshairSize},
		color.RGBA{255, 255, 255, 255}, 2)

	// 2. Draw current object position (simple, since prediction is disabled)
	gocv.Circle(img, decision.CurrentPosition, 4, color.RGBA{0, 255, 0, 150}, 1)

	// 3. REMOVED: PREDICT TARGET text - prediction system is disabled in tracking logic
	// Just show current target position without misleading prediction labels
	gocv.Circle(img, decision.TargetPosition, 8, color.RGBA{255, 255, 0, 200}, 2) // Yellow for target
	gocv.PutText(img, "TARGET",
		image.Point{decision.TargetPosition.X + 15, decision.TargetPosition.Y - 10},
		gocv.FontHersheySimplex, 0.5, color.RGBA{255, 255, 0, 255}, 1) // Yellow instead of pink

	// 4. Draw frame center for reference
	gocv.Circle(img, frameCenter, 4, color.RGBA{255, 255, 255, 150}, 1)
	gocv.PutText(img, "CENTER",
		image.Point{frameCenter.X + 15, frameCenter.Y + 15},
		gocv.FontHersheySimplex, 0.4, color.RGBA{255, 255, 255, 150}, 1)

	// REMOVED: Old camera direction arrow (was causing duplicate arrows with boat direction system)

	// Add distance information
	dx := float64(decision.TargetPosition.X - decision.CurrentPosition.X)
	dy := float64(decision.TargetPosition.Y - decision.CurrentPosition.Y)
	distance := math.Sqrt(dx*dx + dy*dy)

	if distance > 20 { // Only show if meaningful distance
		distText := fmt.Sprintf("%.0fpx", distance)
		midPoint := image.Point{
			(decision.CurrentPosition.X + decision.TargetPosition.X) / 2,
			(decision.CurrentPosition.Y+decision.TargetPosition.Y)/2 - 15,
		}
		gocv.PutText(img, distText, midPoint, gocv.FontHersheySimplex, 0.4, color.RGBA{255, 255, 0, 255}, 1)
	}

	// REMOVED: Misleading "PREDICTIVE TRACKING" panel since prediction is disabled
	// The actual tracking system uses simple position-based tracking, not prediction
}

// DrawPIPZoom draws a Picture-in-Picture zoom view of the tracked object
func (r *Renderer) DrawPIPZoom(img *gocv.Mat, originalFrame gocv.Mat, trackedObjects map[int]*tracking.TrackedObject, isTracking bool, cameraMoving bool, spatialIntegration *tracking.SpatialIntegration) {
	// Primary PIP object selection using spatial integration (preferred method)
	var primaryObject *tracking.TrackedObject
	var pipMode string

	if spatialIntegration != nil {
		// PIP FOR P2 OBJECTS: Only SUPER LOCK targets (24+ detections) with people can trigger PIP
		primaryObject = spatialIntegration.GetLockedTargetForPIP()
		if primaryObject != nil {
			if primaryObject.TrackedFrames >= 24 { // SUPER LOCK: 24+ frames for MEGA ZOOM
				pipMode = "SUPER_LOCK_PEOPLE"
				if debugMsg != nil {
					debugMsg("PIP_PEOPLE", fmt.Sprintf("👤💎 Using SUPER LOCK target with people (det:%d) for MEGA ZOOM PIP - Mode: %s", primaryObject.TrackedFrames, pipMode))
				}
			} else {
				pipMode = "LOCK_PEOPLE"
				if debugMsg != nil {
					debugMsg("PIP_PEOPLE", fmt.Sprintf("👤🔒 Using LOCK target with people (det:%d) for PIP - Mode: %s", primaryObject.TrackedFrames, pipMode))
				}
			}
		}
		// NO BOATS: PIP is PEOPLE ONLY in SUPER LOCK mode
	}

	now := time.Now()

	// Get the LOCKED target directly from spatial integration (no more guessing!)
	// NO FALLBACK: PIP is SUPER LOCK ONLY via spatial integration

	// Update time for PIP linger logic
	now = time.Now()

	// THROTTLE PIP DEBUG MESSAGES - only log every 2 seconds to reduce spam
	shouldDebugPIP := r.lastDebugTime.IsZero() || now.Sub(r.lastDebugTime) > 2*time.Second
	if shouldDebugPIP {
		r.lastDebugTime = now
	}

	// STABLE PIP LOGIC: 5-second minimum display + 5-second linger after loss
	if isTracking && primaryObject != nil {
		// We have an active target
		if !r.pipVisible {
			// PIP not visible - activate it with 5-second minimum display
			r.pipVisible = true
			r.pipFadeStartTime = now
			r.pipMinDisplayEnd = now.Add(5 * time.Second) // Minimum 5 seconds once activated
			r.pipLingerEndTime = now.Add(5 * time.Second) // Also set linger time
			if shouldDebugPIP {
				if debugMsg != nil {
					debugMsg("PIP_STATE", fmt.Sprintf("📺👤 PIP activated with 5s minimum display for %s", primaryObject.ClassName))
				}
			}
		} else {
			// PIP already visible - refresh linger time but keep minimum display time
			r.pipLingerEndTime = now.Add(5 * time.Second)
		}

		// Update coordinates with fresh data using exponential smoothing
		alpha := 0.3 // Smoothing factor: 0.3 = balanced responsiveness vs stability

		if r.lastKnownCoords != nil {
			// Existing coordinates - apply exponential smoothing
			r.lastKnownCoords.SmoothedX = alpha*float64(primaryObject.CenterX) + (1-alpha)*r.lastKnownCoords.SmoothedX
			r.lastKnownCoords.SmoothedY = alpha*float64(primaryObject.CenterY) + (1-alpha)*r.lastKnownCoords.SmoothedY
			r.lastKnownCoords.SmoothedW = alpha*float64(primaryObject.Width) + (1-alpha)*r.lastKnownCoords.SmoothedW
			r.lastKnownCoords.SmoothedH = alpha*float64(primaryObject.Height) + (1-alpha)*r.lastKnownCoords.SmoothedH

			// Update raw coordinates for fallback
			r.lastKnownCoords.CenterX = int(r.lastKnownCoords.SmoothedX)
			r.lastKnownCoords.CenterY = int(r.lastKnownCoords.SmoothedY)
			r.lastKnownCoords.Width = int(r.lastKnownCoords.SmoothedW)
			r.lastKnownCoords.Height = int(r.lastKnownCoords.SmoothedH)
			r.lastKnownCoords.ClassName = primaryObject.ClassName
			r.lastKnownCoords.Confidence = primaryObject.Confidence
		} else {
			// First detection - initialize with fresh coordinates
			r.lastKnownCoords = &PIPCoordinates{
				CenterX:    primaryObject.CenterX,
				CenterY:    primaryObject.CenterY,
				Width:      primaryObject.Width,
				Height:     primaryObject.Height,
				ClassName:  primaryObject.ClassName,
				Confidence: primaryObject.Confidence,
				SmoothedX:  float64(primaryObject.CenterX),
				SmoothedY:  float64(primaryObject.CenterY),
				SmoothedW:  float64(primaryObject.Width),
				SmoothedH:  float64(primaryObject.Height),
			}
		}
		r.lastUpdateTime = now
	} else if r.pipVisible {
		// No active target - check if we should continue showing PIP

		// Check minimum display time first
		inMinDisplayPeriod := now.Before(r.pipMinDisplayEnd)
		inLingerPeriod := now.Before(r.pipLingerEndTime)

		// SMART DISABLE: Camera moving + no YOLO + past minimum display = immediate disable
		if cameraMoving && !inMinDisplayPeriod {
			r.pipVisible = false
			if shouldDebugPIP {
				if debugMsg != nil {
					debugMsg("PIP_STATE", "📺🚫 PIP disabled - camera moving, past minimum display time")
				}
			}
		} else if !inMinDisplayPeriod && !inLingerPeriod {
			// Past both minimum display and linger time - disable PIP
			r.pipVisible = false
			if shouldDebugPIP {
				if debugMsg != nil {
					debugMsg("PIP_STATE", "📺⏰ PIP disabled - past minimum display (5s) and linger time (5s)")
				}
			}
		} else {
			// Still within minimum display or linger period - keep showing
			if shouldDebugPIP {
				var reason string
				if inMinDisplayPeriod {
					timeRemaining := r.pipMinDisplayEnd.Sub(now).Seconds()
					reason = fmt.Sprintf("in minimum display period (%.1fs remaining)", timeRemaining)
				} else {
					timeRemaining := r.pipLingerEndTime.Sub(now).Seconds()
					reason = fmt.Sprintf("in linger period (%.1fs remaining)", timeRemaining)
				}
				if debugMsg != nil {
					debugMsg("PIP_CONTINUE", fmt.Sprintf("📺⏱️ PIP continuing - %s", reason))
				}
			}
		}
	}

	// Exit if PIP should not be shown
	if !r.pipVisible {
		return
	}

	// Determine what coordinates to use (active target or lingering coordinates)
	var coordsToUse *PIPCoordinates
	var dataFreshness time.Duration
	var isStale bool

	if primaryObject != nil {
		// Use smoothed coordinates from recently updated lastKnownCoords (already smoothed above)
		coordsToUse = r.lastKnownCoords
		dataFreshness = 0
		isStale = false
		if shouldDebugPIP {
			if debugMsg != nil {
				debugMsg("PIP_COORDS", fmt.Sprintf("📺 Using smoothed coordinates (%.1f,%.1f) from active target (camera: %s)",
					coordsToUse.SmoothedX, coordsToUse.SmoothedY, map[bool]string{true: "MOVING", false: "STATIONARY"}[cameraMoving]))
			}
		}
	} else if r.lastKnownCoords != nil {
		// Use lingering coordinates from last known location
		coordsToUse = r.lastKnownCoords
		dataFreshness = now.Sub(r.lastUpdateTime)
		isStale = true
		if shouldDebugPIP {
			if debugMsg != nil {
				debugMsg("PIP_LINGER", fmt.Sprintf("📺 Using lingering coordinates (%d,%d) - age: %.1fs (camera: STATIONARY)",
					coordsToUse.CenterX, coordsToUse.CenterY, dataFreshness.Seconds()))
			}
		}
	} else {
		// No coordinates available at all
		if shouldDebugPIP {
			if debugMsg != nil {
				debugMsg("PIP_ERROR", "📺 No coordinates available - disabling PIP")
			}
		}
		r.pipVisible = false
		return
	}

	// Calculate fade alpha based on timing
	var fadeAlpha float64 = 1.0
	fadeInDuration := 300 * time.Millisecond
	fadeOutDuration := 500 * time.Millisecond

	if isTracking && len(trackedObjects) > 0 {
		// Fade in when tracking starts
		timeSinceStart := now.Sub(r.pipFadeStartTime)
		if timeSinceStart < fadeInDuration {
			fadeAlpha = float64(timeSinceStart) / float64(fadeInDuration)
		}
	} else {
		// Fade out during linger period
		timeUntilEnd := r.pipLingerEndTime.Sub(now)
		if timeUntilEnd < fadeOutDuration {
			fadeAlpha = float64(timeUntilEnd) / float64(fadeOutDuration)
		}
	}

	// Clamp alpha between 0 and 1
	if fadeAlpha < 0 {
		fadeAlpha = 0
	} else if fadeAlpha > 1 {
		fadeAlpha = 1
	}

	// Calculate PIP dimensions and position (larger size)
	outputFrameWidth := img.Cols()
	outputFrameHeight := img.Rows()
	pipWidth := 1200 // PIP window width (4x original: 300 -> 1200)
	pipHeight := 800 // PIP window height (4x original: 200 -> 800)
	pipMargin := 20  // Distance from edges

	// Position in lower right corner
	pipX := outputFrameWidth - pipWidth - pipMargin
	pipY := outputFrameHeight - pipHeight - pipMargin

	// Create PIP window background with fade
	pipRect := image.Rect(pipX, pipY, pipX+pipWidth, pipY+pipHeight)

	// Apply staleness fade based on data freshness
	var staleFadeAlpha float64 = 1.0
	var borderColor color.RGBA
	var statusText string

	if isStale && dataFreshness > 0 {
		// Apply fade based on data staleness
		if dataFreshness < 500*time.Millisecond {
			// Recent data (< 0.5s) - slight fade, yellow border
			staleFadeAlpha = 0.9
			borderColor = color.RGBA{255, 255, 0, 255} // Yellow
			statusText = fmt.Sprintf("LAST SEEN: %.1fs ago", dataFreshness.Seconds())
		} else if dataFreshness < 1500*time.Millisecond {
			// Stale data (0.5-1.5s) - more fade, orange border
			staleFadeAlpha = 0.7
			borderColor = color.RGBA{255, 165, 0, 255} // Orange
			statusText = fmt.Sprintf("STALE: %.1fs ago", dataFreshness.Seconds())
		} else {
			// Very stale data (> 1.5s) - heavy fade, red border
			staleFadeAlpha = 0.5
			borderColor = color.RGBA{255, 0, 0, 255} // Red
			statusText = fmt.Sprintf("OLD: %.1fs ago", dataFreshness.Seconds())
		}
	} else {
		// Fresh data - full brightness, green border
		staleFadeAlpha = 1.0
		borderColor = color.RGBA{0, 255, 0, 255} // Green
		statusText = "LIVE"
	}

	// Combine fade effects
	finalAlpha := fadeAlpha * staleFadeAlpha
	if finalAlpha < 0 {
		finalAlpha = 0
	} else if finalAlpha > 1 {
		finalAlpha = 1
	}

	// Apply fade alpha to colors
	bgAlpha := uint8(255 * finalAlpha)
	borderAlpha := uint8(float64(borderColor.A) * finalAlpha)

	// Draw background with border (with fade and staleness indication)
	gocv.Rectangle(img, pipRect, color.RGBA{0, 0, 0, bgAlpha}, -1)                                        // Black background
	gocv.Rectangle(img, pipRect, color.RGBA{borderColor.R, borderColor.G, borderColor.B, borderAlpha}, 3) // Status-colored border

	// If we have coordinates to use, show the zoom content
	if coordsToUse != nil {
		// Extract smoothed coordinates for use throughout PIP rendering
		smoothedCenterX := int(coordsToUse.SmoothedX)
		smoothedCenterY := int(coordsToUse.SmoothedY)
		smoothedWidth := int(coordsToUse.SmoothedW)
		smoothedHeight := int(coordsToUse.SmoothedH)

		// Create bounding box for the tracked object with some padding using smoothed coordinates
		padding := 50 // Extra pixels around the object

		objectRect := image.Rect(
			smoothedCenterX-smoothedWidth/2-padding,
			smoothedCenterY-smoothedHeight/2-padding,
			smoothedCenterX+smoothedWidth/2+padding,
			smoothedCenterY+smoothedHeight/2+padding,
		)

		// Ensure bounding box is within frame bounds
		frameRect := image.Rect(0, 0, originalFrame.Cols(), originalFrame.Rows())
		objectRect = objectRect.Intersect(frameRect)

		// Check if we have a valid region to extract
		if !objectRect.Empty() && objectRect.Dx() >= 20 && objectRect.Dy() >= 20 {
			// Extract ROI from original frame
			roi := originalFrame.Region(objectRect)
			if !roi.Empty() {
				defer roi.Close()

				// Apply 2x digital zoom by cropping the center of the ROI
				zoomFactor := 2.0
				roiWidth := roi.Cols()
				roiHeight := roi.Rows()

				// Calculate the center crop area (1/zoomFactor of the original size)
				cropWidth := int(float64(roiWidth) / zoomFactor)
				cropHeight := int(float64(roiHeight) / zoomFactor)

				// Center the crop
				cropX := (roiWidth - cropWidth) / 2
				cropY := (roiHeight - cropHeight) / 2

				// Ensure crop bounds are valid
				if cropX < 0 {
					cropX = 0
				}
				if cropY < 0 {
					cropY = 0
				}
				if cropX+cropWidth > roiWidth {
					cropWidth = roiWidth - cropX
				}
				if cropY+cropHeight > roiHeight {
					cropHeight = roiHeight - cropY
				}

				// Create the cropped (zoomed) region
				var zoomedROI gocv.Mat
				if cropWidth > 10 && cropHeight > 10 {
					cropRect := image.Rect(cropX, cropY, cropX+cropWidth, cropY+cropHeight)
					zoomedROI = roi.Region(cropRect)
				} else {
					// Fallback to original ROI if crop is too small
					zoomedROI = roi.Clone()
				}
				defer zoomedROI.Close()

				// Create a region in the main image for the PIP content (inside the border)
				contentRect := image.Rect(pipX+3, pipY+3, pipX+pipWidth-3, pipY+pipHeight-3)
				pipRegion := img.Region(contentRect)
				defer pipRegion.Close()

				// HIGH QUALITY: Use Lanczos4 interpolation for maximum digital zoom quality
				finalROI := gocv.NewMat()
				defer finalROI.Close()

				// Use Lanczos4 interpolation for superior upscaling quality (now that debug overhead is eliminated)
				gocv.Resize(zoomedROI, &finalROI, image.Pt(contentRect.Dx(), contentRect.Dy()), 0, 0, gocv.InterpolationLanczos4)

				// Apply fade to the video content
				if finalAlpha < 1.0 {
					// MEMORY LEAK FIX: Create black Mat and properly close it
					blackMat := gocv.NewMatWithSize(contentRect.Dy(), contentRect.Dx(), gocv.MatTypeCV8UC3)
					defer blackMat.Close()

					// Apply fade by blending with black
					gocv.AddWeighted(finalROI, finalAlpha, blackMat, 1.0-finalAlpha, 0, &pipRegion)
				} else {
					// No fade needed - direct copy
					finalROI.CopyTo(&pipRegion)
				}
			}
		}
	}

	// Draw PIP title and info with fade
	titleAlpha := uint8(255 * finalAlpha)
	titleY := pipY - 10
	titleText := "TARGET ZOOM"
	gocv.PutText(img, titleText,
		image.Point{pipX, titleY},
		gocv.FontHersheySimplex, 0.6, color.RGBA{0, 255, 0, titleAlpha}, 2)

	// Draw object info (if we have coordinates)
	if coordsToUse != nil {
		// Extract smoothed coordinates for info display
		smoothedCenterX := int(coordsToUse.SmoothedX)
		smoothedCenterY := int(coordsToUse.SmoothedY)
		smoothedWidth := int(coordsToUse.SmoothedW)
		smoothedHeight := int(coordsToUse.SmoothedH)

		infoAlpha := uint8(255 * finalAlpha)
		infoY := pipY + pipHeight + 20
		infoText := fmt.Sprintf("%s | Conf: %.0f%% | %dx%d px (smoothed)",
			coordsToUse.ClassName,
			coordsToUse.Confidence*100,
			smoothedWidth,
			smoothedHeight)
		gocv.PutText(img, infoText,
			image.Point{pipX, infoY},
			gocv.FontHersheySimplex, 0.4, color.RGBA{255, 255, 255, infoAlpha}, 1)

		// Draw zoom level indicator (showing actual digital zoom) using smoothed coordinates
		padding := 50
		objectRect := image.Rect(
			smoothedCenterX-smoothedWidth/2-padding,
			smoothedCenterY-smoothedHeight/2-padding,
			smoothedCenterX+smoothedWidth/2+padding,
			smoothedCenterY+smoothedHeight/2+padding,
		)
		// Calculate total zoom: (PIP size / original object size) * digital zoom factor
		baseZoom := float64(pipWidth) / float64(objectRect.Dx())
		digitalZoom := 2.0 // Our digital zoom factor
		totalZoom := baseZoom * digitalZoom

		zoomText := fmt.Sprintf("%.1fX DIGITAL ZOOM", totalZoom)
		gocv.PutText(img, zoomText,
			image.Point{pipX + 10, pipY + 20},
			gocv.FontHersheySimplex, 0.5, color.RGBA{255, 255, 0, infoAlpha}, 1)
	} else {
		// No coordinates available, show "NO DATA" message
		lingerAlpha := uint8(255 * finalAlpha)
		lingerText := "NO DATA"
		gocv.PutText(img, lingerText,
			image.Point{pipX + 10, pipY + 20},
			gocv.FontHersheySimplex, 0.5, color.RGBA{255, 255, 0, lingerAlpha}, 1)
	}

	// Draw data freshness status
	statusAlpha := uint8(255 * finalAlpha)
	statusY := pipY + pipHeight + 40
	gocv.PutText(img, statusText,
		image.Point{pipX, statusY},
		gocv.FontHersheySimplex, 0.4, color.RGBA{borderColor.R, borderColor.G, borderColor.B, statusAlpha}, 1)

	// Draw crosshair on PIP to show center
	crosshairAlpha := uint8(255 * finalAlpha)
	pipCenterX := pipX + pipWidth/2
	pipCenterY := pipY + pipHeight/2
	crosshairSize := 10

	// Draw crosshair with fade
	gocv.Line(img,
		image.Point{pipCenterX - crosshairSize, pipCenterY},
		image.Point{pipCenterX + crosshairSize, pipCenterY},
		color.RGBA{255, 0, 0, crosshairAlpha}, 2)
	gocv.Line(img,
		image.Point{pipCenterX, pipCenterY - crosshairSize},
		image.Point{pipCenterX, pipCenterY + crosshairSize},
		color.RGBA{255, 0, 0, crosshairAlpha}, 2)
}

// drawMilitaryLockOn draws full military targeting system for locked targets
func (r *Renderer) drawMilitaryLockOn(img gocv.Mat, rect image.Rectangle, obj *tracking.TrackedObject, maturity float64, spatialIntegration interface{}) {
	// Animate the targeting system
	pulse := math.Sin(r.animationTime*4.0)*0.3 + 0.7 // Gentle pulse between 0.4 and 1.0

	// Main targeting box with corner brackets
	r.drawCornerBrackets(img, rect, r.militaryGreen, 3, 20, pulse)

	// Inner tracking box (slightly smaller) with custom muted gray-purple color
	innerRect := image.Rect(rect.Min.X+5, rect.Min.Y+5, rect.Max.X-5, rect.Max.Y-5)
	innerBracketColor := color.RGBA{R: 118, G: 144, B: 116, A: 255} // #769074 (muted gray-purple)
	r.drawCornerBrackets(img, innerRect, innerBracketColor, 2, 15, pulse*0.8)

	// Add targeting reticle around the target
	centerX, centerY := obj.CenterX, obj.CenterY
	reticleSize := 25

	// Rotating reticle elements
	rotation := r.animationTime * 0.5
	r.drawRotatingReticle(img, image.Point{centerX, centerY}, reticleSize, rotation, r.militaryGreen)

	// Calculate current measurements for target info display
	currentZoom := r.getCurrentZoomFromSpatialIntegration(spatialIntegration)
	pixelsPerInch := r.getPixelsPerInchForZoom(currentZoom)
	currentPos := image.Point{X: obj.CenterX, Y: obj.CenterY}

	// Get camera state - EXACT same logic as main.go line 3882
	var cameraIsIdle bool = false // Default to MOVING for safety

	// Try to get camera state manager - use correct return type from method signature
	if si, ok := spatialIntegration.(interface {
		GetCameraStateManager() *ptz.CameraStateManager
	}); ok {
		csm := si.GetCameraStateManager()
		if csm != nil {
			cameraIsIdle = csm.IsIdle() // Direct call - we know it has IsIdle()
		} else {
			// GetCameraStateManager returned nil - assume MOVING for safety
			cameraIsIdle = false
		}
	} else {
		// Interface assertion failed - this shouldn't happen if spatialIntegration is correct type
		if debugMsg != nil {
			debugMsg("CAMERA_STATE_ERROR", fmt.Sprintf("⚠️ spatialIntegration doesn't implement GetCameraStateManager() - assuming MOVING"), obj.ObjectID)
		}
	}

	// Initialize comprehensive measurement tracking if needed
	if r.objectMeasurements == nil {
		r.objectMeasurements = make(map[string]*ObjectMeasurements)
	}

	// Get or create measurement tracker for this object
	if r.objectMeasurements[obj.ObjectID] == nil {
		r.objectMeasurements[obj.ObjectID] = NewObjectMeasurements(obj.ObjectID)
	}

	measurements := r.objectMeasurements[obj.ObjectID]

	// Calculate current real-world measurements
	heightPixels := float64(obj.Height)
	widthPixels := float64(obj.Width)
	lengthPixels := math.Max(widthPixels, heightPixels)

	currentHeightFeet := (heightPixels / pixelsPerInch) / 12.0
	currentWidthFeet := (widthPixels / pixelsPerInch) / 12.0
	currentLengthFeet := (lengthPixels / pixelsPerInch) / 12.0

	// Calculate current speed (always needed for display logic)
	var currentSpeedPixelsPerSecond float64 = 0
	if !measurements.LastUpdateTime.IsZero() && (measurements.LastPosition.X != 0 || measurements.LastPosition.Y != 0) {
		deltaTime := time.Since(measurements.LastUpdateTime).Seconds()
		deltaX := float64(currentPos.X - measurements.LastPosition.X)
		deltaY := float64(currentPos.Y - measurements.LastPosition.Y)
		pixelDistance := math.Sqrt(deltaX*deltaX + deltaY*deltaY)

		// Teleportation protection
		maxReasonableJump := 150.0
		if pixelDistance <= maxReasonableJump || deltaTime >= 1.0 {
			currentSpeedPixelsPerSecond = pixelDistance / deltaTime

			// SPEED DEBUG: Log all speed calculations
			if debugMsgVerboseFunc != nil && currentSpeedPixelsPerSecond > 0 {
				speedMPH := ((currentSpeedPixelsPerSecond / pixelsPerInch) / 12.0) * 3600.0 / 5280.0
				debugMsgVerboseFunc("SPEED_CALC", fmt.Sprintf("🚤 %s: %.1fpx/s = %.2f mph (zoom:%.1f, ppi:%.2f, Δt:%.2fs, Δd:%.1fpx)",
					obj.ObjectID, currentSpeedPixelsPerSecond, speedMPH, currentZoom, pixelsPerInch, deltaTime, pixelDistance), obj.ObjectID)
			}
		}
	}

	// DIRECTION CALCULATION: Calculate direction every frame (like speed calculation)
	var currentDirection float64 = 0 // Default to 0 radians (right direction)

	// DIRECTION TEST: Always fire to see if this code path runs
	if debugMsgVerboseFunc != nil {
		debugMsgVerboseFunc("DIRECTION_TEST", fmt.Sprintf("🧭 %s: Direction calculation attempt - LastTime:%v LastPos:(%d,%d) CurrentPos:(%d,%d)",
			obj.ObjectID, measurements.LastUpdateTime.IsZero(), measurements.LastPosition.X, measurements.LastPosition.Y, currentPos.X, currentPos.Y), obj.ObjectID)
	}

	if !measurements.LastUpdateTime.IsZero() && (measurements.LastPosition.X != 0 || measurements.LastPosition.Y != 0) {
		deltaTime := time.Since(measurements.LastUpdateTime).Seconds()
		deltaX := float64(currentPos.X - measurements.LastPosition.X)
		deltaY := float64(currentPos.Y - measurements.LastPosition.Y)
		pixelDistance := math.Sqrt(deltaX*deltaX + deltaY*deltaY)

		// Teleportation protection (same as speed)
		maxReasonableJump := 150.0
		if pixelDistance <= maxReasonableJump || deltaTime >= 1.0 {
			// Calculate direction only for meaningful movement (5+ pixels minimum for stability)
			if pixelDistance >= 5.0 {
				currentDirection = math.Atan2(deltaY, deltaX) // Radians: -π to π

				// DIRECTION DEBUG: Log all direction calculations (like speed debug)
				if debugMsgVerboseFunc != nil {
					directionDegrees := currentDirection * 180 / math.Pi
					debugMsgVerboseFunc("DIRECTION_CALC", fmt.Sprintf("🧭 %s: REALTIME Δx=%.1f Δy=%.1f → %.1f° (%.3f rad) distance=%.1fpx",
						obj.ObjectID, deltaX, deltaY, directionDegrees, currentDirection, pixelDistance), obj.ObjectID)
				}
			} else if debugMsgVerboseFunc != nil {
				debugMsgVerboseFunc("DIRECTION_SKIP", fmt.Sprintf("⏭️ %s: Movement too small: %.1fpx < 5.0px threshold", obj.ObjectID, pixelDistance), obj.ObjectID)
			}
		} else if debugMsg != nil {
			debugMsg("DIRECTION_TELEPORT", fmt.Sprintf("🚫 %s: Direction teleport protection: %.1fpx > %.1fpx in %.2fs",
				obj.ObjectID, pixelDistance, maxReasonableJump, deltaTime), obj.ObjectID)
		}
	}

	// TEMPORARILY REMOVED: IDLE restriction for troubleshooting
	// shouldCollect := cameraIsIdle && obj.ObjectID != "" && !strings.HasPrefix(obj.ObjectID, "lingering_")
	shouldCollect := obj.ObjectID != "" && !strings.HasPrefix(obj.ObjectID, "lingering_")

	// DIAGNOSTIC: Always log shouldCollect decision to debug the measurement collection issue
	if debugMsgVerboseFunc != nil {
		debugMsgVerboseFunc("COLLECT_DECISION", fmt.Sprintf("🔍 %s: shouldCollect=%t (IDLE_CHECK_REMOVED, ObjectID='%s', isLingering=%t)",
			obj.ObjectID, shouldCollect, obj.ObjectID, strings.HasPrefix(obj.ObjectID, "lingering_")), obj.ObjectID)
	}

	if shouldCollect {
		// Add current measurements to rolling averages (TESTING: collecting regardless of camera state)
		measurements.AddMeasurement(currentSpeedPixelsPerSecond, currentHeightFeet, currentWidthFeet, currentLengthFeet, currentDirection, currentPos)

		stateInfo := "TESTING_ANY_STATE"
		if cameraIsIdle {
			stateInfo = "IDLE"
		} else {
			stateInfo = "MOVING"
		}

		// DEBUG: Always log direction collection to see what's being stored
		if debugMsgVerboseFunc != nil && currentDirection != 0 {
			debugMsgVerboseFunc("DIRECTION_COLLECT", fmt.Sprintf("📍 %s: Stored direction %.1f° (raw=%.3f rad) - total samples now: %d",
				stateInfo, currentDirection*180/math.Pi, currentDirection, len(measurements.DirectionHistory)), obj.ObjectID)
		}

		if debugMsg != nil && len(measurements.HeightHistory)%15 == 0 { // Log every 15 samples when collecting
			debugMsg("MEASUREMENT_COLLECT", fmt.Sprintf("📊 %s: Collected sample %d for %s - Speed:%.1fpx/s H:%.1fft W:%.1fft L:%.1fft Dir:%.1f°",
				stateInfo, len(measurements.HeightHistory), obj.ObjectID, currentSpeedPixelsPerSecond, currentHeightFeet, currentWidthFeet, currentLengthFeet, currentDirection*180/math.Pi), obj.ObjectID)
		}
	} else {
		// Not collecting - show reason (ALWAYS log this for debugging, not just after 5 seconds)
		if debugMsgVerboseFunc != nil {
			reason := ""
			if obj.ObjectID == "" {
				reason = "no ObjectID assigned"
			} else if strings.HasPrefix(obj.ObjectID, "lingering_") {
				reason = "temporary/lingering object"
			} else {
				reason = "unknown condition (IDLE check removed for testing)"
			}
			debugMsgVerboseFunc("MEASUREMENT_SKIP", fmt.Sprintf("⏸️ %s: Skipping collection - %s, last update %.1fs ago",
				obj.ObjectID, reason, time.Since(measurements.LastUpdated).Seconds()), obj.ObjectID)
		}
	}

	// DISPLAY DATA: Always show stored averages (regardless of camera state)
	avgSpeed, avgHeight, _, avgLength := measurements.GetAverages()

	// Use stored averages if available, otherwise fall back to current measurements
	var displaySpeed, displayHeight, displayLength float64
	var speedText string

	if len(measurements.HeightHistory) > 0 {
		// Display stored averages from ALL collected samples
		displaySpeed = avgSpeed
		displayHeight = avgHeight
		displayLength = avgLength

		// Convert averaged speed to display units
		speedInchesPerSecond := displaySpeed / pixelsPerInch
		speedFeetPerSecond := speedInchesPerSecond / 12.0
		speedMPH := speedFeetPerSecond * 3600.0 / 5280.0
		speedKPH := speedMPH * 1.60934
		speedKnots := speedMPH * 0.868976

		// Format speed display with all three units (clean double-space formatting)
		if displaySpeed < 1.0 {
			speedText = "0.0 mph  0.0 kph  0.0 kts" // Show stationary instead of empty
		} else if speedMPH > 50.0 {
			speedText = "INVALID"
		} else if speedMPH >= 0.5 {
			speedText = fmt.Sprintf("%.1f mph  %.1f kph  %.1f kts", speedMPH, speedKPH, speedKnots)
		} else if speedKnots >= 0.2 {
			speedText = fmt.Sprintf("%.1f mph  %.1f kph  %.1f kts", speedMPH, speedKPH, speedKnots)
		} else if speedFeetPerSecond >= 0.05 {
			speedText = fmt.Sprintf("%.1f ft/s", speedFeetPerSecond)
		} else {
			speedText = fmt.Sprintf("%.1f px/s", displaySpeed)
		}
	} else {
		// No stored data - use current measurements and show initial speed
		displayHeight = currentHeightFeet
		displayLength = currentLengthFeet

		// Show current instantaneous speed instead of empty
		if currentSpeedPixelsPerSecond > 0.5 { // Only show if meaningful movement
			speedInchesPerSecond := currentSpeedPixelsPerSecond / pixelsPerInch
			speedMPH := (speedInchesPerSecond / 12.0) * 3600.0 / 5280.0
			speedKPH := speedMPH * 1.60934
			speedKnots := speedMPH * 0.868976
			if speedMPH >= 0.05 { // Lower threshold for initial display
				speedText = fmt.Sprintf("%.1f mph  %.1f kph  %.1f kts*", speedMPH, speedKPH, speedKnots) // * indicates instantaneous
			} else {
				speedText = fmt.Sprintf("%.2f mph  %.2f kph  %.2f kts*", speedMPH, speedKPH, speedKnots) // Show more precision for very low speeds
			}
		} else {
			speedText = "0.0 mph  0.0 kph  0.0 kts*" // Show something instead of empty
		}
	}

	// Enhanced debug logging for speed troubleshooting
	if debugMsg != nil && len(measurements.HeightHistory) > 0 {
		stateIndicator := "TESTING_ANY_STATE (collecting + displaying)"
		if cameraIsIdle {
			stateIndicator += " - camera IDLE"
		} else {
			stateIndicator += " - camera MOVING"
		}

		// Get tracking message count for comprehensive debug info
		var trackingMsgCount int
		if globalDebugLogger != nil {
			if msgCounter, ok := globalDebugLogger.(interface {
				GetTrackingMessageCount(string) int
			}); ok {
				trackingMsgCount = msgCounter.GetTrackingMessageCount(obj.ObjectID)
			}
		}

		// Enhanced speed debugging - show raw values
		debugMsgVerboseFunc("MEASUREMENT_DISPLAY", fmt.Sprintf("📈 %s: %s - %d samples avg, %d msgs tracked",
			stateIndicator, obj.ObjectID, len(measurements.HeightHistory), trackingMsgCount), obj.ObjectID)
		debugMsgVerboseFunc("SPEED_DISPLAY", fmt.Sprintf("🔢 %s: Raw avgSpeed=%.1fpx/s → Display='%s' (zoom:%.1f ppi:%.2f)",
			obj.ObjectID, avgSpeed, speedText, currentZoom, pixelsPerInch), obj.ObjectID)
		debugMsgVerboseFunc("MEASUREMENT_SUMMARY", fmt.Sprintf("📏 %s: H:%.1fft L:%.1fft",
			obj.ObjectID, displayHeight, displayLength), obj.ObjectID)
	}

	// Display info around target with improved layout
	statusTextColor := color.RGBA{R: 184, G: 224, B: 191, A: 255} // #b8e0bf (bright mint green for status/measurements)
	objectIDColor := color.RGBA{R: 116, G: 144, B: 140, A: 255}   // #74908c (slightly different for objectID)
	textThickness := 2                                            // Increased thickness for better visibility

	// Get tracking mode and CLEAN object info from spatial integration
	var modeText, objectText string
	if si, ok := spatialIntegration.(interface {
		GetDetailedTrackingMode() string
		GetCurrentTrackedObject() string
	}); ok {
		modeText = si.GetDetailedTrackingMode()
		objectText = si.GetCurrentTrackedObject() // Now returns CLEAN objectID (no status text)
		if objectText == "" {
			objectText = "none"
		}
	} else {
		modeText = "UNKNOWN"
		// Fallback to object ID if available, otherwise use class name
		if obj.ObjectID != "" {
			objectText = obj.ObjectID
		} else {
			objectText = fmt.Sprintf("%s #%d", obj.ClassName, obj.ID)
		}
	}

	// TOP LEFT: Mode information (slightly smaller font for longer text)
	modePos := image.Point{rect.Min.X - 80, rect.Min.Y - 30}
	gocv.PutText(&img, modeText, modePos, gocv.FontHersheySimplex, 0.8, statusTextColor, textThickness)

	// LEFT: Object name (below mode) - 15% smaller and different color
	objectPos := image.Point{rect.Min.X - 80, rect.Min.Y - 5}
	gocv.PutText(&img, objectText, objectPos, gocv.FontHersheySimplex, 0.68, objectIDColor, textThickness)

	// TOP RIGHT: Speed information
	if speedText != "" {
		speedPos := image.Point{rect.Max.X - 80, rect.Min.Y - 30}
		gocv.PutText(&img, speedText, speedPos, gocv.FontHersheySimplex, 0.8, statusTextColor, textThickness)
	}

	// RIGHT: Height with professional vertical arrows
	heightText := fmt.Sprintf("%.1fft", displayHeight)
	heightPos := image.Point{rect.Max.X + 10, rect.Min.Y + rect.Dy()/2}
	gocv.PutText(&img, heightText, heightPos, gocv.FontHersheySimplex, 0.8, statusTextColor, textThickness)

	// Draw vertical arrows for height
	arrowX := rect.Max.X + 5
	arrowTopY := rect.Min.Y + rect.Dy()/2 - 25
	arrowBottomY := rect.Min.Y + rect.Dy()/2 + 25
	// Vertical line
	gocv.Line(&img, image.Point{arrowX, arrowTopY}, image.Point{arrowX, arrowBottomY}, statusTextColor, 2)
	// Top arrow head
	gocv.Line(&img, image.Point{arrowX, arrowTopY}, image.Point{arrowX - 3, arrowTopY + 5}, statusTextColor, 2)
	gocv.Line(&img, image.Point{arrowX, arrowTopY}, image.Point{arrowX + 3, arrowTopY + 5}, statusTextColor, 2)
	// Bottom arrow head
	gocv.Line(&img, image.Point{arrowX, arrowBottomY}, image.Point{arrowX - 3, arrowBottomY - 5}, statusTextColor, 2)
	gocv.Line(&img, image.Point{arrowX, arrowBottomY}, image.Point{arrowX + 3, arrowBottomY - 5}, statusTextColor, 2)

	// BELOW: Length with professional horizontal arrows (text below arrow line)
	arrowY := rect.Max.Y + 35
	arrowLeftX := rect.Min.X + rect.Dx()/2 - 50
	arrowRightX := rect.Min.X + rect.Dx()/2 + 50

	// Draw horizontal arrow first
	// Horizontal line
	gocv.Line(&img, image.Point{arrowLeftX, arrowY}, image.Point{arrowRightX, arrowY}, statusTextColor, 2)
	// Left arrow head
	gocv.Line(&img, image.Point{arrowLeftX, arrowY}, image.Point{arrowLeftX + 5, arrowY - 3}, statusTextColor, 2)
	gocv.Line(&img, image.Point{arrowLeftX, arrowY}, image.Point{arrowLeftX + 5, arrowY + 3}, statusTextColor, 2)
	// Right arrow head
	gocv.Line(&img, image.Point{arrowRightX, arrowY}, image.Point{arrowRightX - 5, arrowY - 3}, statusTextColor, 2)
	gocv.Line(&img, image.Point{arrowRightX, arrowY}, image.Point{arrowRightX - 5, arrowY + 3}, statusTextColor, 2)

	// Then draw text below the arrow line (centered and with more spacing)
	lengthText := fmt.Sprintf("%.1fft", displayLength)
	// Center text on the arrow (arrow spans from -50 to +50, center at rect.Min.X + rect.Dx()/2)
	textWidth := len(lengthText) * 8 // Approximate text width for centering
	lengthPos := image.Point{rect.Min.X + rect.Dx()/2 - textWidth/2, arrowY + 25}
	gocv.PutText(&img, lengthText, lengthPos, gocv.FontHersheySimplex, 0.8, statusTextColor, textThickness)

	// DIRECTION ARROW: Use averaged direction for smooth, meaningful display
	avgDirection := measurements.GetAverageDirection()

	if debugMsg != nil {
		// Show detailed direction data
		recentDirections := ""
		if len(measurements.DirectionHistory) > 0 {
			// Show last 5 direction values to see the raw data
			start := len(measurements.DirectionHistory) - 5
			if start < 0 {
				start = 0
			}
			for i := start; i < len(measurements.DirectionHistory); i++ {
				deg := measurements.DirectionHistory[i] * 180 / math.Pi
				if i > start {
					recentDirections += ", "
				}
				recentDirections += fmt.Sprintf("%.0f°", deg)
			}
		}
		debugMsgVerboseFunc("ARROW_DEBUG", fmt.Sprintf("🎯 %s: Recent directions=[%s] → Avg=%.1f° Samples=%d",
			obj.ObjectID, recentDirections, avgDirection*180/math.Pi, len(measurements.DirectionHistory)), obj.ObjectID)
	}

	// Only show arrow for the PRIMARY tracked object (the one the camera is following)
	var isPrimaryTarget bool
	if si, ok := spatialIntegration.(interface {
		GetCurrentTrackedObject() string
	}); ok {
		primaryObjectID := si.GetCurrentTrackedObject()
		isPrimaryTarget = (obj.ObjectID == primaryObjectID && obj.ObjectID != "")
	}

	// Show arrow if we have at least 2 direction samples AND this is the primary target
	if isPrimaryTarget && len(measurements.DirectionHistory) >= 2 {
		r.drawDirectionArrow(img, rect, avgDirection)
		if debugMsgVerboseFunc != nil {
			directionDegrees := avgDirection * 180 / math.Pi
			debugMsgVerboseFunc("DIRECTION_ARROW_DRAWN", fmt.Sprintf("🎯 %s: Drew PRIMARY target arrow at %.1f° (n=%d)",
				obj.ObjectID, directionDegrees, len(measurements.DirectionHistory)), obj.ObjectID)
		}
	} else if debugMsgVerboseFunc != nil {
		reason := ""
		if !isPrimaryTarget {
			reason = "not primary target"
		} else if len(measurements.DirectionHistory) < 2 {
			reason = fmt.Sprintf("samples=%d (need >=2)", len(measurements.DirectionHistory))
		}
		debugMsgVerboseFunc("DIRECTION_NO_ARROW", fmt.Sprintf("❌ %s: No arrow - %s",
			obj.ObjectID, reason), obj.ObjectID)
	}
}

// drawMilitaryBrackets draws corner brackets for acquiring targets
func (r *Renderer) drawMilitaryBrackets(img gocv.Mat, rect image.Rectangle, obj *tracking.TrackedObject, maturity float64) {
	// Animate brackets closing in as target becomes more stable
	bracketExtension := int(20.0 * (1.0 - maturity)) // Brackets get shorter as maturity increases

	// Color transitions from yellow to green as tracking improves
	acquisitionColor := color.RGBA{
		R: uint8(255 * (1 - maturity)),
		G: uint8(255),
		B: 0,
		A: uint8(200 + 55*maturity),
	}

	r.drawCornerBrackets(img, rect, acquisitionColor, 2, 15+bracketExtension, 1.0)

	// Acquisition status
	acqText := "ACQUIRING"
	textPos := image.Point{rect.Min.X, rect.Min.Y - 20}
	gocv.PutText(&img, acqText, textPos, gocv.FontHersheySimplex, 0.5, acquisitionColor, 1)
}

// drawMilitaryDetection draws initial detection box with pulsing effect
func (r *Renderer) drawMilitaryDetection(img gocv.Mat, rect image.Rectangle, obj *tracking.TrackedObject, maturity float64) {
	// Fast pulsing for new detections
	pulse := math.Sin(r.animationTime*8.0)*0.5 + 0.5

	// Color starts red and transitions to yellow
	detectionColor := color.RGBA{
		R: uint8(255),
		G: uint8(100 + 155*maturity),
		B: 0,
		A: uint8(150 + 100*pulse),
	}

	// Simple pulsing rectangle
	thickness := int(1 + pulse*2)
	gocv.Rectangle(&img, rect, detectionColor, thickness)

	// Detection status
	detText := "CONTACT"
	textPos := image.Point{rect.Min.X, rect.Min.Y - 15}
	gocv.PutText(&img, detText, textPos, gocv.FontHersheySimplex, 0.4, detectionColor, 1)
}

// drawMilitaryCrosshair draws military-style crosshair at center point
func (r *Renderer) drawMilitaryCrosshair(img gocv.Mat, center image.Point, confidence float64, maturity float64) {
	// Color based on tracking maturity with lightning-fast frame-based thresholds
	var crosshairColor color.RGBA
	if maturity >= 1.0 { // 2+ frames = GREEN (locked, camera movement enabled)
		crosshairColor = r.militaryGreen // Green for locked targets
	} else { // 1 frame = RED (initial detection)
		crosshairColor = r.targetRed // Red for initial detection
	}

	// Draw crosshair
	size := 8
	thickness := 2

	// Horizontal line
	gocv.Line(&img,
		image.Point{center.X - size, center.Y},
		image.Point{center.X + size, center.Y},
		crosshairColor, thickness)

	// Vertical line
	gocv.Line(&img,
		image.Point{center.X, center.Y - size},
		image.Point{center.X, center.Y + size},
		crosshairColor, thickness)

	// Center dot
	gocv.Circle(&img, center, 2, crosshairColor, -1)
}

// drawCornerBrackets draws military-style corner brackets
func (r *Renderer) drawCornerBrackets(img gocv.Mat, rect image.Rectangle, color color.RGBA, thickness, length int, intensity float64) {
	// Apply intensity to color
	adjustedColor := color
	adjustedColor.A = uint8(float64(color.A) * intensity)

	// Top-left corner
	gocv.Line(&img, rect.Min, image.Point{rect.Min.X + length, rect.Min.Y}, adjustedColor, thickness)
	gocv.Line(&img, rect.Min, image.Point{rect.Min.X, rect.Min.Y + length}, adjustedColor, thickness)

	// Top-right corner
	gocv.Line(&img, image.Point{rect.Max.X, rect.Min.Y}, image.Point{rect.Max.X - length, rect.Min.Y}, adjustedColor, thickness)
	gocv.Line(&img, image.Point{rect.Max.X, rect.Min.Y}, image.Point{rect.Max.X, rect.Min.Y + length}, adjustedColor, thickness)

	// Bottom-left corner
	gocv.Line(&img, image.Point{rect.Min.X, rect.Max.Y}, image.Point{rect.Min.X + length, rect.Max.Y}, adjustedColor, thickness)
	gocv.Line(&img, image.Point{rect.Min.X, rect.Max.Y}, image.Point{rect.Min.X, rect.Max.Y - length}, adjustedColor, thickness)

	// Bottom-right corner
	gocv.Line(&img, rect.Max, image.Point{rect.Max.X - length, rect.Max.Y}, adjustedColor, thickness)
	gocv.Line(&img, rect.Max, image.Point{rect.Max.X, rect.Max.Y - length}, adjustedColor, thickness)
}

// drawRotatingReticle draws rotating targeting reticle
func (r *Renderer) drawRotatingReticle(img gocv.Mat, center image.Point, size int, rotation float64, color color.RGBA) {
	// Draw 4 short lines rotated around center
	for i := 0; i < 4; i++ {
		angle := rotation + float64(i)*math.Pi/2

		// Calculate line positions
		x1 := center.X + int(float64(size)*math.Cos(angle))
		y1 := center.Y + int(float64(size)*math.Sin(angle))
		x2 := center.X + int(float64(size-5)*math.Cos(angle))
		y2 := center.Y + int(float64(size-5)*math.Sin(angle))

		gocv.Line(&img, image.Point{x1, y1}, image.Point{x2, y2}, color, 2)
	}
}

// LogDecision adds a decision to the terminal history
func (r *Renderer) LogDecision(message, decisionType string, priority int) {
	now := time.Now()

	// Add to history
	entry := DecisionLogEntry{
		Timestamp: now,
		Message:   message,
		Type:      decisionType,
		Priority:  priority,
	}

	r.decisionHistory = append(r.decisionHistory, entry)

	// Keep only recent entries
	if len(r.decisionHistory) > r.maxDecisionHistory {
		r.decisionHistory = r.decisionHistory[len(r.decisionHistory)-r.maxDecisionHistory:]
	}

	r.lastDecisionUpdate = now
}

// DrawDecisionTerminal draws a terminal-like overlay showing real-time tracking decisions (TERMINAL OVERLAY MODE)
func (r *Renderer) DrawDecisionTerminal(img *gocv.Mat, spatialIntegration interface{}, terminalOverlay bool, debugLogger interface{}) {
	// ONLY show when terminal overlay is enabled
	if !terminalOverlay {
		return
	}

	now := time.Now()

	// Check if we currently have a LOCK or SUPER LOCK target
	hasLockedTarget := false
	if spatialIntegration != nil {
		// Check for any locked target using the same logic as PIP
		if si, ok := spatialIntegration.(interface {
			GetLockedTargetForPIP() *tracking.TrackedObject
		}); ok {
			lockedTarget := si.GetLockedTargetForPIP()
			if lockedTarget != nil {
				hasLockedTarget = true
			}
		}
	}

	// Update tracking state and fade timing
	if hasLockedTarget {
		// We have a locked target - reset fade timer
		r.lastTrackingTime = now
		r.trackingOverlayVisible = true
	} else {
		// No locked target - check if we should start fading after 10 seconds
		timeSinceLastLock := now.Sub(r.lastTrackingTime)
		if timeSinceLastLock > 10*time.Second {
			// Start fading if not already started
			if r.trackingOverlayFadeStart.IsZero() {
				r.trackingOverlayFadeStart = now
			}
		}
	}

	// Calculate fade alpha based on 10-second timeout + 3-second fade
	var fadeAlpha float64 = 1.0
	if !hasLockedTarget && !r.trackingOverlayFadeStart.IsZero() {
		fadeOutDuration := 3 * time.Second // 3-second fade out
		timeSinceFadeStart := now.Sub(r.trackingOverlayFadeStart)

		if timeSinceFadeStart >= fadeOutDuration {
			// Completely faded out - don't draw terminal
			return
		} else {
			// Fading out - calculate alpha
			fadeAlpha = 1.0 - (float64(timeSinceFadeStart) / float64(fadeOutDuration))
		}
	} else {
		// Reset fade start if we have a locked target again
		r.trackingOverlayFadeStart = time.Time{}
	}

	// Terminal dimensions and position (top-left area, above existing status box)
	terminalWidth := 650  // Increased from 500 for more content
	terminalHeight := 450 // Increased to 450 to properly cover 30 messages (30 × 14px + 10px padding)
	terminalX := 20       // Align with lower left status display (same x=20)
	terminalY := 20

	// Semi-transparent background (like a terminal) with fade
	terminalRect := image.Rect(terminalX, terminalY, terminalX+terminalWidth, terminalY+terminalHeight)

	// Draw semi-transparent black background with fade
	bgAlpha := uint8(180 * fadeAlpha) // Apply fade to background transparency
	gocv.Rectangle(img, terminalRect, color.RGBA{0, 0, 0, bgAlpha}, -1)

	// Content area (start higher since no header)
	contentY := terminalY + 10
	lineHeight := 14 // Further reduced for maximum content

	// Get debug messages from the unified debug logger
	var debugMessages []interface{}
	if debugLogger != nil {
		// Use interface method to get debug messages
		if dl, ok := debugLogger.(interface {
			GetOverlayHistory() []interface{}
		}); ok {
			debugMessages = dl.GetOverlayHistory()
		}
	}

	// Show recent debug messages (limit for terminal size)
	maxMessages := 30 // Show even more messages now that we have extra space
	if len(debugMessages) > 0 {
		startIdx := 0
		if len(debugMessages) > maxMessages {
			startIdx = len(debugMessages) - maxMessages
		}

		// No section header - just start with messages

		for i := startIdx; i < len(debugMessages); i++ {
			msg := debugMessages[i]

			// Extract clean message (now just a simple string)
			var message string
			if str, ok := msg.(string); ok {
				message = str
			} else {
				message = fmt.Sprintf("%v", msg)
			}

			// Use white text with fade applied
			textAlpha := uint8(255 * fadeAlpha) // Apply fade to text
			textColor := color.RGBA{255, 255, 255, textAlpha}

			// Use the clean message directly
			displayText := message

			// Truncate if too long for terminal width
			maxLineLen := 110 // More characters since no timestamp prefix
			if len(displayText) > maxLineLen {
				displayText = displayText[:maxLineLen-3] + "..."
			}

			gocv.PutText(img, displayText,
				image.Point{terminalX + 10, contentY},
				gocv.FontHersheySimplex, 0.35, textColor, 1)
			contentY += lineHeight
		}
	} else {
		// No debug messages available
		noMsgAlpha := uint8(128 * fadeAlpha) // Apply fade to "no messages" text
		gocv.PutText(img, "No debug messages available...",
			image.Point{terminalX + 10, contentY},
			gocv.FontHersheySimplex, 0.4, color.RGBA{128, 128, 128, noMsgAlpha}, 1)
	}

	// No blinking cursor needed
}

// REMOVED: drawTrackingStatsOverlay - replaced with integrated target overlay info
// All target information now appears directly on the military targeting system
// loadCalibrationData loads the pixel-to-inches calibration data from JSON file
func (r *Renderer) loadCalibrationData() error {
	if r.calibrationLoaded {
		return nil // Already loaded
	}

	// Try to load from the root directory
	calibrationPath := "pixels-inches-cal.json"

	// Check if file exists
	if _, err := os.Stat(calibrationPath); os.IsNotExist(err) {
		return fmt.Errorf("calibration file not found: %s", calibrationPath)
	}

	// Read the file
	jsonData, err := ioutil.ReadFile(calibrationPath)
	if err != nil {
		return fmt.Errorf("failed to read calibration file: %v", err)
	}

	// Parse JSON
	var calibData CalibrationData
	if err := json.Unmarshal(jsonData, &calibData); err != nil {
		return fmt.Errorf("failed to parse calibration JSON: %v", err)
	}

	r.calibrationData = &calibData
	r.calibrationLoaded = true

	fmt.Printf("✅ Loaded pixel-to-inches calibration data with %d zoom levels\n", len(calibData.CalibrationData))
	return nil
}

// getPixelsPerInchForZoom returns the pixels-per-inch conversion for a given zoom level
func (r *Renderer) getPixelsPerInchForZoom(currentZoom float64) float64 {
	// Try to load calibration data if not already loaded
	if err := r.loadCalibrationData(); err != nil {
		// Fallback to old hardcoded value if calibration can't be loaded
		fmt.Printf("⚠️  Using fallback calibration (Zoom 10): %v\n", err)
		return 0.954 // Our measured Zoom 10 value from calibration data
	}

	// If we have calibration data, interpolate for the current zoom level
	return r.interpolatePixelsPerInch(currentZoom)
}

// interpolatePixelsPerInch interpolates pixels-per-inch for any zoom level
func (r *Renderer) interpolatePixelsPerInch(targetZoom float64) float64 {
	if r.calibrationData == nil || len(r.calibrationData.CalibrationData) == 0 {
		return 0.954 // Fallback to Zoom 10 value
	}

	calibPoints := r.calibrationData.CalibrationData

	// Find the two closest calibration points
	var lowerPoint, upperPoint *ZoomCalibrationPoint

	for i := range calibPoints {
		point := &calibPoints[i]

		if point.ZoomLevel <= targetZoom {
			if lowerPoint == nil || point.ZoomLevel > lowerPoint.ZoomLevel {
				lowerPoint = point
			}
		}

		if point.ZoomLevel >= targetZoom {
			if upperPoint == nil || point.ZoomLevel < upperPoint.ZoomLevel {
				upperPoint = point
			}
		}
	}

	// If we have an exact match, use it
	if lowerPoint != nil && lowerPoint.ZoomLevel == targetZoom {
		return lowerPoint.PixelsPerInch
	}
	if upperPoint != nil && upperPoint.ZoomLevel == targetZoom {
		return upperPoint.PixelsPerInch
	}

	// Interpolate between the two points
	if lowerPoint != nil && upperPoint != nil {
		// Linear interpolation
		zoomRange := upperPoint.ZoomLevel - lowerPoint.ZoomLevel
		zoomOffset := targetZoom - lowerPoint.ZoomLevel
		pixelRange := upperPoint.PixelsPerInch - lowerPoint.PixelsPerInch

		interpolatedValue := lowerPoint.PixelsPerInch + (zoomOffset/zoomRange)*pixelRange
		return interpolatedValue
	}

	// Extrapolation cases
	if lowerPoint != nil && upperPoint == nil {
		// Beyond maximum zoom - use the highest calibration point
		return lowerPoint.PixelsPerInch
	}

	if upperPoint != nil && lowerPoint == nil {
		// Below minimum zoom - use the lowest calibration point
		return upperPoint.PixelsPerInch
	}

	// Should never reach here, but fallback to Zoom 10
	return 0.954
}

// getCurrentZoomFromSpatialIntegration extracts current zoom level from spatial integration
func (r *Renderer) getCurrentZoomFromSpatialIntegration(spatialIntegration interface{}) float64 {
	if spatialIntegration == nil {
		return 10.0 // Default fallback zoom
	}

	// Try to call GetSpatialTrackingInfo method
	if siPtr, ok := spatialIntegration.(interface {
		GetSpatialTrackingInfo() *tracking.SpatialTrackingInfo
	}); ok {
		spatialInfo := siPtr.GetSpatialTrackingInfo()
		if spatialInfo != nil {
			return spatialInfo.CurrentPosition.Zoom
		}
	}

	// Final fallback - return default zoom
	return 10.0
}

// writeSpeedTrackingDebug writes detailed speed tracking data to debug files
// REMOVED: writeSpeedTrackingDebug - part of old stats overlay system
func (r *Renderer) writeSpeedTrackingDebug(targetID int, speedTracker *BoatSpeedTracker, targetToShow *tracking.TrackedObject, currentZoom float64, pixelsPerInch float64, debugMode bool) {
	// Function removed - was part of upper right stats window that's been eliminated
	return
}

// updateTargetDisplayBuffer updates the averaged display buffer for stable target rendering
func (r *Renderer) updateTargetDisplayBuffer(trackedObjects map[int]*tracking.TrackedObject, currentFrame int, spatialIntegration interface{}, cameraMoving bool) {
	// Update buffer for each current tracked object
	for id, obj := range trackedObjects {
		displayData, exists := r.targetDisplayBuffer[id]
		if !exists {
			// Create new display data entry with spatial coordinate calculation and time-based persistence
			now := time.Now()
			displayData = &TargetDisplayData{
				PositionHistory:    make([]image.Point, 0, r.maxDisplayHistory),
				SizeHistory:        make([]image.Point, 0, r.maxDisplayHistory),
				CurrentFrame:       currentFrame,
				LastSeenFrame:      currentFrame,
				FirstSeenTime:      now,
				LastUpdateTime:     now,
				MinDisplayDuration: 1 * time.Second, // Minimum 1 second display
				LingerDuration:     3 * time.Second, // 3 second linger when camera stationary
				HasSpatialData:     false,           // Will be set below if calculation succeeds
			}

			// SPATIAL-AWARE LINGERING: Calculate real-world coordinates for new targets
			if spatialIntegration != nil {
				spatialCoord := r.calculateSpatialCoordinateForPixel(obj.CenterX, obj.CenterY, spatialIntegration)
				if spatialCoord.isValid {
					displayData.SpatialPan = spatialCoord.pan
					displayData.SpatialTilt = spatialCoord.tilt
					displayData.SpatialZoom = spatialCoord.zoom
					displayData.HasSpatialData = true

					debugMsgVerboseFunc("SPATIAL_STORE", fmt.Sprintf("📍🌍 Stored spatial coordinates for new target ID:%d at pixel (%d,%d) → spatial (%.1f°,%.1f°,%.1fx)",
						id, obj.CenterX, obj.CenterY, spatialCoord.pan, spatialCoord.tilt, spatialCoord.zoom))
				}
			}

			r.targetDisplayBuffer[id] = displayData
		}

		// Update with fresh YOLO data (frame-based and time-based)
		displayData.LastSeenFrame = currentFrame
		displayData.CurrentFrame = currentFrame
		displayData.LastUpdateTime = time.Now() // Refresh time-based tracking
		displayData.Confidence = obj.Confidence
		displayData.ClassName = obj.ClassName

		// Add to position history with jump detection
		newPosition := image.Point{X: obj.CenterX, Y: obj.CenterY}

		// PHANTOM TARGET PROTECTION: Check for unreasonable position jumps
		if len(displayData.PositionHistory) > 0 {
			lastPos := displayData.PositionHistory[len(displayData.PositionHistory)-1]
			deltaX := float64(newPosition.X - lastPos.X)
			deltaY := float64(newPosition.Y - lastPos.Y)
			jumpDistance := math.Sqrt(deltaX*deltaX + deltaY*deltaY)

			// Maximum reasonable movement per frame (roughly boat speed + camera movement)
			maxReasonableJump := 150.0 // pixels per frame

			if jumpDistance > maxReasonableJump {
				// TELEPORTATION DETECTED: This looks like a different boat with same ID
				// Clear history and start fresh to prevent phantom averaging
				displayData.PositionHistory = []image.Point{newPosition}
				if debugMsgVerboseFunc != nil {
					debugMsgVerboseFunc("JUMP_PROTECTION", fmt.Sprintf("🚫📍 ID:%d position jump %.1fpx > %.1fpx - clearing history to prevent phantom target",
						id, jumpDistance, maxReasonableJump))
				}
			} else {
				// Normal movement - safe to add to averaging history
				displayData.PositionHistory = append(displayData.PositionHistory, newPosition)
				if len(displayData.PositionHistory) > r.maxDisplayHistory {
					displayData.PositionHistory = displayData.PositionHistory[1:]
				}
			}
		} else {
			// First position - just add it
			displayData.PositionHistory = append(displayData.PositionHistory, newPosition)
		}

		// Add to size history with jump protection
		newSize := image.Point{X: obj.Width, Y: obj.Height}

		// PHANTOM TARGET PROTECTION: Also protect size averaging from boat ID mixups
		if len(displayData.SizeHistory) > 0 {
			lastSize := displayData.SizeHistory[len(displayData.SizeHistory)-1]
			deltaW := float64(newSize.X - lastSize.X)
			deltaH := float64(newSize.Y - lastSize.Y)
			sizeJump := math.Sqrt(deltaW*deltaW + deltaH*deltaH)

			// Maximum reasonable size change per frame
			maxReasonableSizeJump := 100.0 // pixels per frame

			if sizeJump > maxReasonableSizeJump {
				// Size teleportation - different boat, clear size history
				displayData.SizeHistory = []image.Point{newSize}
			} else {
				// Normal size change - safe to average
				displayData.SizeHistory = append(displayData.SizeHistory, newSize)
				if len(displayData.SizeHistory) > r.maxDisplayHistory {
					displayData.SizeHistory = displayData.SizeHistory[1:]
				}
			}
		} else {
			// First size - just add it
			displayData.SizeHistory = append(displayData.SizeHistory, newSize)
		}

		// Calculate averaged position
		if len(displayData.PositionHistory) > 0 {
			var sumX, sumY int
			for _, pos := range displayData.PositionHistory {
				sumX += pos.X
				sumY += pos.Y
			}
			avgX := sumX / len(displayData.PositionHistory)
			avgY := sumY / len(displayData.PositionHistory)

			// Apply exponential smoothing to the averaged position (like PIP)
			alpha := 0.3 // Same smoothing factor as PIP: 30% new data + 70% historical
			if displayData.SmoothedCenterX == 0 && displayData.SmoothedCenterY == 0 {
				// First time - initialize with averaged position
				displayData.SmoothedCenterX = float64(avgX)
				displayData.SmoothedCenterY = float64(avgY)
			} else {
				// Apply exponential smoothing to reduce jitter
				displayData.SmoothedCenterX = alpha*float64(avgX) + (1-alpha)*displayData.SmoothedCenterX
				displayData.SmoothedCenterY = alpha*float64(avgY) + (1-alpha)*displayData.SmoothedCenterY
			}

			// Use smoothed coordinates for display
			displayData.CenterX = int(displayData.SmoothedCenterX)
			displayData.CenterY = int(displayData.SmoothedCenterY)

			// Calculate position stability (lower variance = more stable)
			var varianceX, varianceY float64
			for _, pos := range displayData.PositionHistory {
				varianceX += float64((pos.X - displayData.CenterX) * (pos.X - displayData.CenterX))
				varianceY += float64((pos.Y - displayData.CenterY) * (pos.Y - displayData.CenterY))
			}
			displayData.AvgStability = 1.0 / (1.0 + math.Sqrt(varianceX+varianceY)/float64(len(displayData.PositionHistory)))
		}

		// Calculate averaged size
		if len(displayData.SizeHistory) > 0 {
			var sumW, sumH int
			for _, size := range displayData.SizeHistory {
				sumW += size.X
				sumH += size.Y
			}
			avgW := sumW / len(displayData.SizeHistory)
			avgH := sumH / len(displayData.SizeHistory)

			// Apply exponential smoothing to the averaged size (like PIP)
			alpha := 0.3 // Same smoothing factor as position
			if displayData.SmoothedWidth == 0 && displayData.SmoothedHeight == 0 {
				// First time - initialize with averaged size
				displayData.SmoothedWidth = float64(avgW)
				displayData.SmoothedHeight = float64(avgH)
			} else {
				// Apply exponential smoothing to reduce size jitter
				displayData.SmoothedWidth = alpha*float64(avgW) + (1-alpha)*displayData.SmoothedWidth
				displayData.SmoothedHeight = alpha*float64(avgH) + (1-alpha)*displayData.SmoothedHeight
			}

			// Use smoothed dimensions for display
			displayData.Width = int(displayData.SmoothedWidth)
			displayData.Height = int(displayData.SmoothedHeight)

			// Calculate size stability
			var varianceW, varianceH float64
			for _, size := range displayData.SizeHistory {
				varianceW += float64((size.X - displayData.Width) * (size.X - displayData.Width))
				varianceH += float64((size.Y - displayData.Height) * (size.Y - displayData.Height))
			}
			displayData.SizeStability = 1.0 / (1.0 + math.Sqrt(varianceW+varianceH)/float64(len(displayData.SizeHistory)))
		}
	}

	// Update frame counter and clean up stale entries with camera-aware persistence (like PIP)
	now := time.Now()
	for id, displayData := range r.targetDisplayBuffer {
		displayData.CurrentFrame = currentFrame
		framesSinceLastSeen := currentFrame - displayData.LastSeenFrame
		timeSinceLastSeen := now.Sub(displayData.LastUpdateTime)
		timeSinceFirstSeen := now.Sub(displayData.FirstSeenTime)

		// Camera-aware persistence logic (similar to PIP)
		shouldRemove := false

		if cameraMoving {
			// Camera moving: Quick removal to prevent stale overlays
			// Honor minimum display time, then short linger (0.5 seconds)
			if timeSinceFirstSeen > displayData.MinDisplayDuration {
				shouldRemove = timeSinceLastSeen > 500*time.Millisecond
			}
		} else {
			// Camera stationary: Longer persistence for stable tracking
			// Honor minimum display time, then full linger duration
			if timeSinceFirstSeen > displayData.MinDisplayDuration {
				shouldRemove = timeSinceLastSeen > displayData.LingerDuration
			}
		}

		// Fallback: Also remove if frame-based limit exceeded (safety net)
		if framesSinceLastSeen > 180 { // 6 seconds at 30fps maximum
			shouldRemove = true
		}

		if shouldRemove {
			delete(r.targetDisplayBuffer, id)
		}
	}
}

// getStableTargetDisplay returns stable, averaged display data for targets with camera movement compensation
func (r *Renderer) getStableTargetDisplay(spatialIntegration interface{}) map[int]*TargetDisplayData {
	result := make(map[int]*TargetDisplayData)

	// Return all targets that are still within persistence time (already filtered by updateTargetDisplayBuffer)
	for id, displayData := range r.targetDisplayBuffer {
		// All targets in the buffer are already validated for persistence in updateTargetDisplayBuffer
		// Create copy with camera movement compensation for lingering targets
		copyData := &TargetDisplayData{
			CenterX:         displayData.CenterX,
			CenterY:         displayData.CenterY,
			Width:           displayData.Width,
			Height:          displayData.Height,
			SmoothedCenterX: displayData.SmoothedCenterX,
			SmoothedCenterY: displayData.SmoothedCenterY,
			SmoothedWidth:   displayData.SmoothedWidth,
			SmoothedHeight:  displayData.SmoothedHeight,
			LastSeenFrame:   displayData.LastSeenFrame,
			CurrentFrame:    displayData.CurrentFrame,
			Confidence:      displayData.Confidence,
			ClassName:       displayData.ClassName,
			AvgStability:    displayData.AvgStability,
			SizeStability:   displayData.SizeStability,
			// Copy time-based persistence data
			FirstSeenTime:      displayData.FirstSeenTime,
			LastUpdateTime:     displayData.LastUpdateTime,
			MinDisplayDuration: displayData.MinDisplayDuration,
			LingerDuration:     displayData.LingerDuration,
			// Copy spatial data
			SpatialPan:     displayData.SpatialPan,
			SpatialTilt:    displayData.SpatialTilt,
			SpatialZoom:    displayData.SpatialZoom,
			HasSpatialData: displayData.HasSpatialData,
		}

		// SPATIAL-AWARE LINGERING: Compensate for camera movement during linger period
		timeSinceLastSeen := time.Since(displayData.LastUpdateTime)
		if timeSinceLastSeen > 0 && displayData.HasSpatialData && spatialIntegration != nil {
			// This is a lingering target - recalculate pixel position based on current camera position
			newPixelX, newPixelY, isVisible := r.spatialToPixelCoordinate(displayData.SpatialPan, displayData.SpatialTilt, spatialIntegration)

			if isVisible {
				copyData.CenterX = newPixelX
				copyData.CenterY = newPixelY
				debugMsgVerboseFunc("SPATIAL_LINGER", fmt.Sprintf("🌍→📺 Compensated lingering target ID:%d from pixel (%d,%d) to (%d,%d) based on spatial (%.1f°,%.1f°)",
					id, displayData.CenterX, displayData.CenterY, newPixelX, newPixelY, displayData.SpatialPan, displayData.SpatialTilt))
			} else {
				// Target moved outside camera view - don't include it
				debugMsgVerboseFunc("SPATIAL_LINGER", fmt.Sprintf("🌍❌ Lingering target ID:%d moved outside camera view at spatial (%.1f°,%.1f°) - excluding",
					id, displayData.SpatialPan, displayData.SpatialTilt))
				continue
			}
		}

		result[id] = copyData
	}

	return result
}

// cleanupStaleSpeedTrackers removes speed trackers for boats that no longer exist
func (r *Renderer) cleanupStaleSpeedTrackers(trackedObjects map[int]*tracking.TrackedObject) {
	if r.boatSpeedData == nil {
		return
	}

	// Create set of active object IDs from tracked objects
	activeObjectIDs := make(map[string]bool)
	for _, obj := range trackedObjects {
		activeObjectIDs[obj.ObjectID] = true
	}

	// Remove speed trackers for objects that are no longer being tracked
	for objectID := range r.boatSpeedData {
		if !activeObjectIDs[objectID] {
			delete(r.boatSpeedData, objectID)
			if debugMsg != nil {
				debugMsg("SPEED_CLEANUP", fmt.Sprintf("🧹 Removed stale speed tracker for object ID:%s", objectID), objectID)
			}
		}
	}
}

// cleanMessageFromDebugStruct extracts just the message content from a verbose debug struct string
func cleanMessageFromDebugStruct(str string) string {
	// Remove the verbose timestamp format like "2025-07-26 11:44:58.716220977 -0400 EDT m=+32.536102569"
	// Look for patterns like "Message: actual message content"
	if strings.Contains(str, "Message:") {
		parts := strings.Split(str, "Message:")
		if len(parts) > 1 {
			return strings.TrimSpace(parts[1])
		}
	}

	// Try to extract from struct format like "{Timestamp:... Component:... Message:... BoatID:...}"
	if strings.Contains(str, "{") && strings.Contains(str, "}") {
		// Find Message field in struct
		msgStart := strings.Index(str, "Message:")
		if msgStart != -1 {
			remaining := str[msgStart+8:] // Skip "Message:"
			// Find the end of the message (before next field or closing brace)
			msgEnd := strings.Index(remaining, " BoatID:")
			if msgEnd == -1 {
				msgEnd = strings.Index(remaining, "}")
			}
			if msgEnd != -1 {
				return strings.TrimSpace(remaining[:msgEnd])
			} else {
				return strings.TrimSpace(remaining)
			}
		}
	}

	// Fallback - return as is
	return str
}

// NewObjectMeasurements creates a new measurement tracker for an object
func NewObjectMeasurements(objectID string) *ObjectMeasurements {
	return &ObjectMeasurements{
		ObjectID:         objectID,
		SpeedHistory:     make([]float64, 0, 100),   // Dynamic array, pre-allocate for efficiency
		HeightHistory:    make([]float64, 0, 100),   // Dynamic array, pre-allocate for efficiency
		WidthHistory:     make([]float64, 0, 100),   // Dynamic array, pre-allocate for efficiency
		LengthHistory:    make([]float64, 0, 100),   // Dynamic array, pre-allocate for efficiency
		DirectionHistory: make([]float64, 0, 100),   // Dynamic array for direction tracking
		SampleTimes:      make([]time.Time, 0, 100), // Track timestamps for cleanup
		LastUpdated:      time.Now(),
	}
}

// AddMeasurement adds ALL measurements from IDLE frames (no 60-frame limit)
func (om *ObjectMeasurements) AddMeasurement(speed, height, width, length, direction float64, position image.Point) {
	now := time.Now()

	// Append ALL measurements - no more 60-frame limit!
	om.SpeedHistory = append(om.SpeedHistory, speed)
	om.HeightHistory = append(om.HeightHistory, height)
	om.WidthHistory = append(om.WidthHistory, width)
	om.LengthHistory = append(om.LengthHistory, length)
	om.DirectionHistory = append(om.DirectionHistory, direction)
	om.SampleTimes = append(om.SampleTimes, now)

	// Update tracking state
	om.LastPosition = position
	om.LastUpdateTime = now
	om.LastUpdated = now

	// Cleanup old samples (older than 5 minutes to prevent unlimited growth)
	om.cleanupOldSamples(5 * time.Minute)
}

// cleanupOldSamples removes measurements older than maxAge to prevent unlimited memory growth
func (om *ObjectMeasurements) cleanupOldSamples(maxAge time.Duration) {
	if len(om.SampleTimes) == 0 {
		return
	}

	cutoff := time.Now().Add(-maxAge)

	// Find first sample to keep
	keepStart := 0
	for i, t := range om.SampleTimes {
		if t.After(cutoff) {
			keepStart = i
			break
		}
	}

	// Remove old samples if any found
	if keepStart > 0 {
		om.SpeedHistory = om.SpeedHistory[keepStart:]
		om.HeightHistory = om.HeightHistory[keepStart:]
		om.WidthHistory = om.WidthHistory[keepStart:]
		om.LengthHistory = om.LengthHistory[keepStart:]
		om.DirectionHistory = om.DirectionHistory[keepStart:]
		om.SampleTimes = om.SampleTimes[keepStart:]
	}
}

// GetAverages returns averages from ALL collected IDLE measurements
func (om *ObjectMeasurements) GetAverages() (speed, height, width, length float64) {
	if len(om.HeightHistory) == 0 {
		return 0, 0, 0, 0
	}

	// Calculate averages from ALL available data
	speedSum, heightSum, widthSum, lengthSum := 0.0, 0.0, 0.0, 0.0
	speedSamples := 0 // Count non-zero speed samples for better averaging

	// Sum all speed measurements (only meaningful ones)
	for _, speed := range om.SpeedHistory {
		if speed > 0.5 { // Only count speeds > 0.5 px/s as meaningful
			speedSum += speed
			speedSamples++
		}
	}

	// Sum all size measurements
	for _, height := range om.HeightHistory {
		heightSum += height
	}
	for _, width := range om.WidthHistory {
		widthSum += width
	}
	for _, length := range om.LengthHistory {
		lengthSum += length
	}

	// Calculate speed average from meaningful samples only
	averageSpeed := 0.0
	if speedSamples > 0 {
		averageSpeed = speedSum / float64(speedSamples)
	}

	count := float64(len(om.HeightHistory))
	return averageSpeed, heightSum / count, widthSum / count, lengthSum / count
}

// GetAverageDirection returns the average direction with proper angle wraparound handling
func (om *ObjectMeasurements) GetAverageDirection() float64 {
	if len(om.DirectionHistory) == 0 {
		return 0 // No direction data
	}

	// Filter meaningful direction values (like speed filtering)
	meaningfulDirections := make([]float64, 0)
	for _, angle := range om.DirectionHistory {
		// Skip 0° values (no meaningful movement detected)
		if angle != 0 {
			meaningfulDirections = append(meaningfulDirections, angle)
		}
	}

	if len(meaningfulDirections) == 0 {
		return 0 // No meaningful direction data
	}

	// Simple outlier detection: remove values that are >90° different from median
	if len(meaningfulDirections) >= 3 {
		// Sort to find median for outlier detection
		sorted := make([]float64, len(meaningfulDirections))
		copy(sorted, meaningfulDirections)
		for i := 0; i < len(sorted)-1; i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[i] > sorted[j] {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
		median := sorted[len(sorted)/2]

		// Filter out outliers (>90° from median)
		filtered := make([]float64, 0)
		for _, angle := range meaningfulDirections {
			angleDiff := math.Abs(angle - median)
			// Handle wraparound: -179° and +179° are only 2° apart
			if angleDiff > math.Pi {
				angleDiff = 2*math.Pi - angleDiff
			}
			if angleDiff <= math.Pi/2 { // 90° threshold
				filtered = append(filtered, angle)
			}
		}
		meaningfulDirections = filtered
	}

	if len(meaningfulDirections) == 0 {
		return 0 // All values were outliers
	}

	// Convert angles to unit vectors, average them, then back to angle
	// This properly handles -179°/+179° wraparound issues
	sumX, sumY := 0.0, 0.0
	for _, angle := range meaningfulDirections {
		sumX += math.Cos(angle)
		sumY += math.Sin(angle)
	}

	return math.Atan2(sumY, sumX)
}

// formatDirection converts radians to human-readable direction with symbol
func formatDirection(radians float64) string {
	// Convert to degrees and normalize to 0-360
	degrees := radians * 180 / math.Pi
	if degrees < 0 {
		degrees += 360
	}

	// 8-direction compass with ASCII symbols (avoid Unicode rendering issues)
	switch {
	case degrees >= 337.5 || degrees < 22.5:
		return "RIGHT 0deg ->"
	case degrees >= 22.5 && degrees < 67.5:
		return fmt.Sprintf("DOWN-RIGHT %.0fdeg", degrees)
	case degrees >= 67.5 && degrees < 112.5:
		return fmt.Sprintf("DOWN %.0fdeg", degrees)
	case degrees >= 112.5 && degrees < 157.5:
		return fmt.Sprintf("DOWN-LEFT %.0fdeg", degrees)
	case degrees >= 157.5 && degrees < 202.5:
		return fmt.Sprintf("LEFT %.0fdeg", degrees)
	case degrees >= 202.5 && degrees < 247.5:
		return fmt.Sprintf("UP-LEFT %.0fdeg", degrees)
	case degrees >= 247.5 && degrees < 292.5:
		return fmt.Sprintf("UP %.0fdeg", degrees)
	case degrees >= 292.5 && degrees < 337.5:
		return fmt.Sprintf("UP-RIGHT %.0fdeg", degrees)
	default:
		return "UNKNOWN"
	}
}

// drawDirectionArrow draws a direction arrow positioned outside the target overlay area
func (r *Renderer) drawDirectionArrow(img gocv.Mat, rect image.Rectangle, direction float64) {
	if math.IsNaN(direction) {
		return // Skip if direction is invalid
	}

	// Calculate target center for reference
	centerX := float64(rect.Min.X + rect.Dx()/2)
	centerY := float64(rect.Min.Y + rect.Dy()/2)

	// Calculate direction vector components
	cos := math.Cos(direction)
	sin := math.Sin(direction)

	// Find intersection point with rectangle perimeter based on direction
	var edgeX, edgeY float64

	// Determine which edge the direction vector intersects
	if math.Abs(cos) > math.Abs(sin) {
		// Direction is more horizontal - use left or right edge
		if cos > 0 {
			// Pointing right
			edgeX = float64(rect.Max.X)
			edgeY = centerY + (edgeX-centerX)*sin/cos
		} else {
			// Pointing left
			edgeX = float64(rect.Min.X)
			edgeY = centerY + (edgeX-centerX)*sin/cos
		}
	} else {
		// Direction is more vertical - use top or bottom edge
		if sin > 0 {
			// Pointing down
			edgeY = float64(rect.Max.Y)
			edgeX = centerX + (edgeY-centerY)*cos/sin
		} else {
			// Pointing up
			edgeY = float64(rect.Min.Y)
			edgeX = centerX + (edgeY-centerY)*cos/sin
		}
	}

	// Position arrow start point outside the target area with some buffer
	bufferDistance := 30.0 // Distance outside the target rectangle
	startX := edgeX + bufferDistance*cos
	startY := edgeY + bufferDistance*sin

	// Shorter arrow length for cleaner appearance
	arrowLength := 50.0

	// Calculate arrow end point
	endX := startX + arrowLength*cos
	endY := startY + arrowLength*sin

	startPoint := image.Point{int(startX), int(startY)}
	endPoint := image.Point{int(endX), int(endY)}

	// Use specified color #008753 (RGB: 0, 135, 83)
	arrowColor := color.RGBA{R: 0, G: 135, B: 83, A: 255}

	// Draw clean, shorter arrow positioned outside target area
	gocv.ArrowedLine(&img, startPoint, endPoint, arrowColor, 2)

	// Add direction text near the arrow end
	directionText := formatDirection(direction)
	textOffsetX := 15 // Reduced offset for shorter arrow
	textOffsetY := -8 // Slightly above arrow end
	textPos := image.Point{int(endX) + textOffsetX, int(endY) + textOffsetY}
	gocv.PutText(&img, directionText, textPos, gocv.FontHersheySimplex, 0.5, arrowColor, 1)
}

// IsStale returns true if measurements haven't been updated in 60 seconds
func (om *ObjectMeasurements) IsStale() bool {
	return time.Since(om.LastUpdated) > 60*time.Second
}

// GetDebugData returns measurement data for debug output
func (om *ObjectMeasurements) GetDebugData() map[string]interface{} {
	avgSpeed, avgHeight, avgWidth, avgLength := om.GetAverages()
	avgDirection := om.GetAverageDirection()

	return map[string]interface{}{
		"object_id":         om.ObjectID,
		"sample_count":      len(om.HeightHistory),
		"avg_speed":         avgSpeed,
		"avg_height":        avgHeight,
		"avg_width":         avgWidth,
		"avg_length":        avgLength,
		"avg_direction":     avgDirection,
		"last_updated":      om.LastUpdated,
		"speed_history":     om.SpeedHistory,     // ALL speed samples
		"height_history":    om.HeightHistory,    // ALL height samples
		"width_history":     om.WidthHistory,     // ALL width samples
		"length_history":    om.LengthHistory,    // ALL length samples
		"direction_history": om.DirectionHistory, // ALL direction samples
	}
}

// cleanupStaleMeasurements removes measurements that haven't been updated in 60 seconds
func (r *Renderer) cleanupStaleMeasurements(debugMode bool, debugManager interface{}) {
	now := time.Now()

	// Only cleanup every 30 seconds to avoid excessive processing
	if now.Sub(r.lastCleanupTime) < 30*time.Second {
		return
	}

	r.lastCleanupTime = now
	staleMeasurements := make([]*ObjectMeasurements, 0)

	// Find stale measurements
	for objectID, measurement := range r.objectMeasurements {
		if measurement.IsStale() {
			staleMeasurements = append(staleMeasurements, measurement)

			// Save debug data if in debug mode
			if debugMode {
				r.saveMeasurementDebugData(measurement, debugManager)
			}

			delete(r.objectMeasurements, objectID)
		}
	}

	if len(staleMeasurements) > 0 && debugMsg != nil {
		debugMsg("MEASUREMENT_CLEANUP", fmt.Sprintf("🧹 Cleaned up %d stale measurement trackers", len(staleMeasurements)))
	}
}

// saveMeasurementDebugData saves measurement data to debug files and dumps tracking history
func (r *Renderer) saveMeasurementDebugData(measurement *ObjectMeasurements, debugManager interface{}) {
	if dm, ok := debugManager.(interface {
		GetSession(string) interface {
			LogEvent(string, string, map[string]interface{})
		}
	}); ok {
		if session := dm.GetSession(measurement.ObjectID); session != nil {
			debugData := measurement.GetDebugData()
			session.LogEvent("MEASUREMENT_CLEANUP", "Final measurement data before cleanup", debugData)
		}
	}

	// COMPREHENSIVE TRACKING HISTORY: Dump all accumulated debug messages for this objectID
	if globalDebugLogger != nil {
		trackingData := globalDebugLogger.DumpTrackingHistory(measurement.ObjectID)
		if trackingData != nil && debugMsg != nil {
			debugMsg("TRACKING_HISTORY_DUMP", fmt.Sprintf("🗂️ Dumped comprehensive tracking history for %s (stale cleanup)",
				measurement.ObjectID), measurement.ObjectID)
		}
	}

	// CLEANUP JPEG COUNTER: Also cleanup the JPEG counter for this objectID
	if dm, ok := debugManager.(interface {
		CleanupObjectCounter(string)
	}); ok {
		dm.CleanupObjectCounter(measurement.ObjectID)
	}
}

// GetObjectMeasurements returns measurement data for a specific object ID (for recovery system)
func (r *Renderer) GetObjectMeasurements(objectID string) interface{} {
	if r.objectMeasurements == nil {
		return nil
	}

	measurement, exists := r.objectMeasurements[objectID]
	if !exists {
		return nil
	}

	return measurement
}

// updateYOLODetectionHistory updates the rolling history of YOLO detections for the enhanced panel
func (r *Renderer) updateYOLODetectionHistory(detections []image.Rectangle, classNames []string, confidences []float64) {
	// Initialize maxYOLOHistory if not set
	if r.maxYOLOHistory == 0 {
		r.maxYOLOHistory = 5 // Keep last 5 detections
	}

	now := time.Now()

	// Add current detections to history
	for i, rect := range detections {
		if i >= len(classNames) || i >= len(confidences) {
			continue
		}

		entry := YOLODetectionEntry{
			ClassName:  classNames[i],
			Confidence: confidences[i],
			Width:      rect.Dx(),
			Height:     rect.Dy(),
			CenterX:    rect.Min.X + rect.Dx()/2,
			CenterY:    rect.Min.Y + rect.Dy()/2,
			Timestamp:  now,
		}

		r.yoloDetectionHistory = append(r.yoloDetectionHistory, entry)
	}

	// Keep only the most recent detections
	if len(r.yoloDetectionHistory) > r.maxYOLOHistory {
		r.yoloDetectionHistory = r.yoloDetectionHistory[len(r.yoloDetectionHistory)-r.maxYOLOHistory:]
	}

	// Increment frame counter for stats
	r.yoloPanelFrameCount++
}

// drawEnhancedYOLOPanel draws the enhanced YOLO detection panel on the right side of the screen
func (r *Renderer) drawEnhancedYOLOPanel(img gocv.Mat, detections []image.Rectangle, yoloBlue color.RGBA) {
	frameWidth := img.Cols()

	// Panel dimensions and positioning (right side)
	panelWidth := 400
	panelHeight := 300
	panelX := frameWidth - panelWidth - 20 // Right edge with margin
	panelY := 20                           // Top edge (clear of terminal)

	// Colors: Blue border, white text for readability
	whiteText := color.RGBA{R: 255, G: 255, B: 255, A: 255} // White text for readability

	// Panel background
	panelRect := image.Rect(panelX, panelY, panelX+panelWidth, panelY+panelHeight)
	gocv.Rectangle(&img, panelRect, color.RGBA{0, 0, 0, 120}, -1) // Semi-transparent black background
	gocv.Rectangle(&img, panelRect, yoloBlue, 2)                  // Blue border (keep blue for identification)

	// Header with total count
	headerText := fmt.Sprintf("YOLO DETECTIONS: %d objects", len(detections))
	headerPos := image.Point{panelX + 10, panelY + 25}
	gocv.PutText(&img, headerText, headerPos, gocv.FontHersheySimplex, 0.6, whiteText, 2)

	// Line separator
	lineY := panelY + 35
	gocv.Line(&img, image.Point{panelX + 10, lineY}, image.Point{panelX + panelWidth - 10, lineY}, yoloBlue, 1)

	// Recent detections list (last 5 detections with full details)
	listStartY := lineY + 15
	lineHeight := 20

	recentText := "Recent detections:"
	gocv.PutText(&img, recentText, image.Point{panelX + 10, listStartY}, gocv.FontHersheySimplex, 0.4, whiteText, 1)

	// Show last detections with enhanced info including object names
	for i, entry := range r.yoloDetectionHistory {
		if i >= 5 { // Limit to 5 recent detections
			break
		}

		// Format: "[1] boat (85%) 180x120px @(1200,400)"
		detailText := fmt.Sprintf("[%d] %s (%.0f%%) %dx%dpx @(%d,%d)",
			i+1, entry.ClassName, entry.Confidence*100, entry.Width, entry.Height, entry.CenterX, entry.CenterY)

		textY := listStartY + 20 + (i * lineHeight)
		gocv.PutText(&img, detailText, image.Point{panelX + 10, textY}, gocv.FontHersheySimplex, 0.35, whiteText, 1)
	}

	// Real-time stats at bottom
	statsY := panelY + panelHeight - 40
	gocv.Line(&img, image.Point{panelX + 10, statsY - 10}, image.Point{panelX + panelWidth - 10, statsY - 10}, yoloBlue, 1)

	// Frame count and detection stats
	statsText := fmt.Sprintf("Frame: %d | Active: %d detections", r.yoloPanelFrameCount, len(detections))
	gocv.PutText(&img, statsText, image.Point{panelX + 10, statsY + 5}, gocv.FontHersheySimplex, 0.4, whiteText, 1)

	// Additional info line - always show, even with 0 detections
	avgConf := 0.0
	for _, entry := range r.yoloDetectionHistory {
		if len(r.yoloDetectionHistory) <= 5 { // Only count recent ones
			avgConf += entry.Confidence
		}
	}
	if len(r.yoloDetectionHistory) > 0 {
		avgConf /= float64(len(r.yoloDetectionHistory))
	}

	infoText := fmt.Sprintf("Avg confidence: %.0f%% | History: %d", avgConf*100, len(r.yoloDetectionHistory))
	gocv.PutText(&img, infoText, image.Point{panelX + 10, statsY + 25}, gocv.FontHersheySimplex, 0.35, whiteText, 1)
}
