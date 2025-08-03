package tracking

import (
	"fmt"
	"image"
	"math"
	"sync"
	"time"

	"rivercam/ptz"
)

// SpatialIntegration is now the PRIMARY tracking system (no more legacy confusion)
type SpatialIntegration struct {
	spatialTracker *SpatialTracker
	ptzCtrl        ptz.Controller // Direct access to PTZ controller

	// Camera state management
	cameraStateManager *ptz.CameraStateManager

	// Frame properties for pixel tracking
	frameWidth   int
	frameHeight  int
	frameCenterX int
	frameCenterY int

	// Camera movement tracking for overlay cleanup
	lastCameraPosition SpatialCoordinate
	lastPositionUpdate time.Time
	lastHistoryClear   time.Time // Track when we last cleared history for adaptive matching

	// High-frequency camera position tracking for velocity compensation
	lastVelocityCalcPosition SpatialCoordinate
	lastVelocityCalcTime     time.Time

	// Multi-object tracking
	allBoats             map[string]*TrackedBoat // All detected boats by ID
	targetBoat           *TrackedBoat            // Currently targeted boat for camera control
	pixelTrackingHistory []image.Point           // Clean pixel history for overlay

	// Target locking settings
	minDetectionsForLock int // Minimum detections before boat can be locked
	maxLostFrames        int // Max frames a boat can be lost before removal
	targetSwitchCooldown int // Cooldown frames before switching targets
	lastTargetSwitch     int // Frame count when target was last switched
	frameCount           int // Current frame count

	// Post-lock holdover (linger after losing locked boat before resuming scanning)
	lastLockLoss        time.Time         // When we lost the last locked boat
	postLockHoldover    time.Duration     // How long to linger before resuming scanning
	lastLockedPosition  SpatialCoordinate // Where the locked boat was last seen
	holdoverPositionSet bool              // Whether we've set the holdover position

	// Command deduplication to prevent API spam
	lastSentPan  float64 // Last pan command sent
	lastSentTilt float64 // Last tilt command sent
	lastSentZoom float64 // Last zoom command sent

	// Predictive search tracking
	lastSearchBoatID string    // ID of boat we last searched for
	lastSearchTime   time.Time // When we last sent a search command

	// Race condition protection
	mu sync.RWMutex // Protects all tracking state during concurrent access

	// Smart PTZ tracking configuration
	smartPTZEnabled   bool    // Enable/disable smart PTZ tracking
	ptzPredictionTime float64 // Look ahead time for predictions (seconds)
	ptzMinVelocity    float64 // Minimum PTZ velocity to trigger prediction
	ptzBufferFactor   float64 // Buffer factor for camera positioning

	// Latency compensation and fallback triggers
	pipelineLatency        float64 // YOLO pipeline latency compensation (seconds)
	centerTriggerThreshold float64 // Percentage off-center to trigger immediate movement (fallback)

	// Debug logging
	debugLogger interface{} // Debug logger for boat-specific logging

	// Debug logging integration
	debugManager interface{} // Reference to debug manager for file logging (avoid import cycle)
	renderer     interface{} // Reference to renderer for terminal logging (avoid import cycle)

	// Unified boat ID generation system
	lastMinuteTimestamp         string // Track current minute for counter reset (20240125-12-30)
	currentMinuteCounter        int    // Counter that resets each minute (1, 2, 3...)
	totalDetectedObjectsCounter int64  // Session-wide counter, never resets

	// RECOVERY mode state
	recoveryData *RecoveryData // Recovery data for lost boat prediction
	isInRecovery bool          // Whether we're currently in recovery mode

	// Dynamic tracking priority configuration
	p1TrackList     []string      // P1 objects (primary tracking targets)
	p1TrackAll      bool          // P1 tracks all detected objects
	p2TrackList     []string      // P2 objects (enhancement objects)
	p2TrackAll      bool          // P2 tracks all non-P1 objects
	p1MinConfidence float64       // Minimum confidence threshold for P1 targets (boats)
	p2MinConfidence float64       // Minimum confidence threshold for P2 targets (people)
	recoveryTimeout time.Duration // Maximum time to spend in recovery (30 seconds)
}

// TrackedBoat represents a boat we're actively tracking

// SpatialTrackingInfo provides current zoom data for calibration calculations (spatial overlay system removed)
type SpatialTrackingInfo struct {
	IsScanning      bool
	CurrentPosition SpatialCoordinate
	ScanPattern     []SpatialCoordinate
	TrackedObjects  map[string]*SpatialObject
	LockedObject    *SpatialObject
	TotalObjects    int
}

// PTZ-Native Zone System for Smart Predictive Tracking
type PTZFieldOfView struct {
	Center   SpatialCoordinate // Current PTZ center position
	Coverage struct {
		PanRange  float64 // How many pan units this frame covers
		TiltRange float64 // How many tilt units this frame covers
	}
	Boundaries struct {
		LeftEdge   float64 // Pan coordinate of left frame edge
		RightEdge  float64 // Pan coordinate of right frame edge
		TopEdge    float64 // Tilt coordinate of top frame edge
		BottomEdge float64 // Tilt coordinate of bottom frame edge
	}
}

type PTZPrediction struct {
	CurrentFOV     PTZFieldOfView
	FuturePTZ      SpatialCoordinate
	WillExitFrame  bool
	ExitDirection  string
	NewPTZTarget   SpatialCoordinate
	MovementType   string
	PredictionTime float64
	TimeToExit     float64
	PTZVelocity    struct {
		Pan  float64
		Tilt float64
	}
}

type MovementType string

const (
	MovementNone       MovementType = "NONE"
	MovementLeftTrack  MovementType = "LEFT_TRACK"
	MovementRightTrack MovementType = "RIGHT_TRACK"
	MovementUpTrack    MovementType = "UP_TRACK"
	MovementDownTrack  MovementType = "DOWN_TRACK"
	MovementDiagonal   MovementType = "DIAGONAL_TRACK"
)

// RECOVERY mode phases
type RecoveryPhase int

const (
	RECOVERY_MOVE_TO_PREDICTED_1 RecoveryPhase = iota
	RECOVERY_ZOOM_OUT
	RECOVERY_MOVE_TO_PREDICTED_2
	RECOVERY_COMPLETE
)

func (r RecoveryPhase) String() string {
	switch r {
	case RECOVERY_MOVE_TO_PREDICTED_1:
		return "RECOVERY PHASE 1"
	case RECOVERY_ZOOM_OUT:
		return "RECOVERY PHASE 2"
	case RECOVERY_MOVE_TO_PREDICTED_2:
		return "RECOVERY PHASE 3"
	case RECOVERY_COMPLETE:
		return "RECOVERY COMPLETE"
	default:
		return "RECOVERY UNKNOWN"
	}
}

// Recovery data for lost boat prediction
type RecoveryData struct {
	ObjectID             string            // Lost boat's ObjectID
	LastKnownPixelPos    image.Point       // Last pixel position
	LastKnownSpatialPos  SpatialCoordinate // Last spatial position
	AverageDirection     float64           // From DirectionHistory (radians)
	AverageSpeedPixelSec float64           // From SpeedHistory
	LossTime             time.Time         // When boat was lost
	OriginalZoom         float64           // Zoom level when lost
	CurrentPhase         RecoveryPhase     // Current recovery phase
	RecoveryStartTime    time.Time         // When recovery started
	LingerStartTime      time.Time         // When directional search linger started
	LingerDuration       time.Duration     // How long to linger (5 seconds)
	PhaseStartTime       time.Time         // When current phase started
	PhaseTarget          SpatialCoordinate // Target position for current phase
	WaitingForArrival    bool              // Whether we're waiting for camera to reach target
}

type TrackedBoat struct {
	ID             string
	Classification string
	Confidence     float64
	FirstDetected  time.Time
	LastSeen       time.Time
	DetectionCount int
	LostFrames     int // Track how many frames since last detection

	// Pixel tracking (for overlay)
	CurrentPixel   image.Point
	PixelHistory   []image.Point
	PredictedPixel image.Point
	PixelArea      float64         // Store actual detection area in pixels
	BoundingBox    image.Rectangle // Current bounding box for person detection

	// P2 object detection (enhancement objects inside P1 targets)
	HasP2Objects bool      // TRUE if P2 objects detected inside P1 target
	P2Confidence float64   // Confidence of P2 object detection
	P2Count      int       // Number of P2 objects detected in P1 target
	LastP2Seen   time.Time // When P2 objects were last seen in P1 target

	// Enhanced P2 tracking for LOCK/SUPER LOCK modes
	P2Positions []image.Point   // Individual P2 object pixel positions
	P2Centroid  image.Point     // Calculated centroid of all P2 objects
	P2Bounds    image.Rectangle // Bounding box containing all P2 objects
	P2Spread    float64         // Distance between furthest P2 objects (for zoom calc)
	P2Quality   float64         // Quality score for P2 tracking (0-1)
	UseP2Target bool            // TRUE when using P2 centroid for LOCK targeting

	// Spatial tracking (for camera control)
	CurrentSpatial   SpatialCoordinate
	SpatialHistory   []SpatialCoordinate
	PredictedSpatial SpatialCoordinate

	// Movement analysis
	PixelVelocity   struct{ X, Y float64 }
	SpatialVelocity struct{ Pan, Tilt float64 }
	IsLocked        bool
	LockStrength    float64

	// Target selection priority
	TrackingPriority float64 // Higher = more likely to be selected as target

	// Debug session logging (spatial calculation details)
	HasSpatialDebugData bool                   // Flag indicating debug data is ready
	SpatialDebugData    map[string]interface{} // Detailed spatial calculation data for debug sessions
}

// NewSpatialIntegration creates a clean, multi-object tracking system
func NewSpatialIntegration(ptzCtrl ptz.Controller, frameWidth, frameHeight int, debugLogger interface{}, p1TrackList, p2TrackList []string, p1TrackAll, p2TrackAll bool, p1MinConfidence, p2MinConfidence float64) *SpatialIntegration {
	integration := &SpatialIntegration{
		spatialTracker: NewSpatialTracker(ptzCtrl, frameWidth, frameHeight, p1TrackList, p2TrackList, p1TrackAll, p2TrackAll, p1MinConfidence, p2MinConfidence),
		ptzCtrl:        ptzCtrl, // Store PTZ controller for direct access
		frameWidth:     frameWidth,
		frameHeight:    frameHeight,
		frameCenterX:   frameWidth / 2,
		frameCenterY:   frameHeight / 2,
		debugLogger:    debugLogger, // Store debug logger for boat-specific logging

		// Initialize multi-object tracking
		allBoats:             make(map[string]*TrackedBoat),
		minDetectionsForLock: 2,   // LIGHTNING-FAST: Camera movement at 2 detections (~0.07s) for instant tracking responsiveness
		maxLostFrames:        150, // Remove boats after 150 lost frames (5.0s at 30fps)
		targetSwitchCooldown: 120, // INCREASED from 30 to 120 frames for more stable switching
		lastTargetSwitch:     0,
		frameCount:           0,

		// Post-lock holdover settings (REMOVED: will be replaced by RECOVERY mode)
		postLockHoldover: 10 * time.Second, // Linger for 10 seconds after losing locked boat

		// RECOVERY mode settings
		recoveryTimeout: 30 * time.Second, // Maximum 30 seconds in recovery mode

		// Dynamic tracking priority configuration
		p1TrackList:     p1TrackList,
		p2TrackList:     p2TrackList,
		p1TrackAll:      p1TrackAll,
		p2TrackAll:      p2TrackAll,
		p1MinConfidence: p1MinConfidence,
		p2MinConfidence: p2MinConfidence,
	}

	// Initialize smart PTZ tracking configuration
	integration.smartPTZEnabled = true  // Enable smart tracking by default
	integration.ptzPredictionTime = 3.0 // Look ahead 3 seconds by default
	integration.ptzMinVelocity = 0.5    // Minimum 0.5 PTZ units/second to trigger prediction
	integration.ptzBufferFactor = 0.3   // Keep boat 30% from center for better tracking

	// Latency compensation and fallback triggers
	integration.pipelineLatency = 2.0         // Compensate for 2-second YOLO pipeline latency
	integration.centerTriggerThreshold = 0.01 // 1% off-center trigger (reduced from 5% for faster response)

	integration.debugMsg("SPATIAL_INIT", fmt.Sprintf("🔧 Lock criteria initialized: minDetections=%d, minConfidence=0.30 (LIGHTNING-FAST tracking)",
		integration.minDetectionsForLock))
	integration.debugMsg("SPATIAL_INIT", fmt.Sprintf("🔧 Cleanup settings: maxLostFrames=%d, targetSwitchCooldown=%d",
		integration.maxLostFrames, integration.targetSwitchCooldown))

	// DUAL LOGGING: Initialization complete
	integration.logDebugMessage("🔧 Spatial tracking initialized", "SPATIAL_INIT", 1, map[string]interface{}{
		"min_detections":         integration.minDetectionsForLock,
		"min_confidence":         0.30,
		"max_lost_frames":        integration.maxLostFrames,
		"target_switch_cooldown": integration.targetSwitchCooldown,
		"tracking_mode":          "LIGHTNING_FAST",
	})

	integration.debugMsg("MULTI_TRACKING", fmt.Sprintf("Initialized multi-object tracking system (%dx%d)", frameWidth, frameHeight))
	integration.debugMsg("MULTI_TRACKING", fmt.Sprintf("LIGHTNING-FAST Lock: %d detections (~%.2fs), max %d lost frames (%.1fs at 30fps)",
		integration.minDetectionsForLock, float64(integration.minDetectionsForLock)/30.0, integration.maxLostFrames, float64(integration.maxLostFrames)/30.0))
	integration.debugMsg("MULTI_TRACKING", "Matching: YOLO bounding box overlap (priority) + 200px distance fallback")
	integration.debugMsg("MULTI_TRACKING", "📊 DETAILED DEBUG LOGS: Console output reduced, full tracking analysis in /tmp/debugMode/<session>/tracking_session.txt")
	integration.debugMsg("MULTI_TRACKING", fmt.Sprintf("Post-lock holdover: %.1fs (linger after losing locked boat before scanning)",
		integration.postLockHoldover.Seconds()))
	integration.debugMsg("SMART_PTZ", fmt.Sprintf("Smart PTZ tracking: %v (prediction: %.1fs, min velocity: %.1f units/s, buffer: %.1f%%)",
		integration.smartPTZEnabled, integration.ptzPredictionTime, integration.ptzMinVelocity, integration.ptzBufferFactor*100))
	integration.debugMsg("SMART_PTZ", fmt.Sprintf("Latency compensation: %.1fs, center trigger: %.1f%% (responsive tracking)",
		integration.pipelineLatency, integration.centerTriggerThreshold*100))
	return integration
}

// isP2Object checks if an object class is a P2 (enhancement) target for this SpatialIntegration
func (si *SpatialIntegration) isP2Object(className string) bool {
	// If P1 is set to "all", no objects can be P2 (all objects are already P1)
	if si.p1TrackAll {
		return false
	}

	// If P2 is set to "all", any non-P1 object is P2
	if si.p2TrackAll {
		return !si.isP1Object(className)
	}

	// Otherwise check explicit P2 list
	for _, p2Class := range si.p2TrackList {
		if className == p2Class {
			return true
		}
	}
	return false
}

// isP1Object checks if an object class is a P1 (primary tracking) target for this SpatialIntegration
func (si *SpatialIntegration) isP1Object(className string) bool {
	// If P1 is set to "all", any object is P1
	if si.p1TrackAll {
		return true
	}

	// Otherwise check explicit P1 list
	for _, p1Class := range si.p1TrackList {
		if className == p1Class {
			return true
		}
	}
	return false
}

// debugMsg is a convenience method for unified debug logging with boat IDs
func (si *SpatialIntegration) debugMsg(component, message string, boatID ...string) {
	if si.debugLogger != nil {
		// Try to cast to the debug logger interface and call debugMsg
		if dl, ok := si.debugLogger.(interface {
			debugMsg(component, message string, boatID ...string)
		}); ok {
			dl.debugMsg(component, message, boatID...)
		}
	}
}

func (si *SpatialIntegration) debugMsgVerbose(component, message string, boatID ...string) {
	if si.debugLogger != nil {
		// Try to cast to the debug logger interface and call debugMsgVerbose
		if dl, ok := si.debugLogger.(interface {
			debugMsgVerbose(component, message string, boatID ...string)
		}); ok {
			dl.debugMsgVerbose(component, message, boatID...)
		}
	}
}

// UpdateTracking - THE MAIN MULTI-OBJECT TRACKING FUNCTION
func (si *SpatialIntegration) UpdateTracking(detections []image.Rectangle, classNames []string, confidences []float64, frameData []byte) {
	// RACE CONDITION FIX: Protect the main tracking loop from concurrent access
	si.mu.Lock()
	defer si.mu.Unlock()

	si.frameCount++

	// Clean up stale data when camera moves
	si.detectAndCleanupCameraMovement()

	// RATE LIMITED TRACKING: Always process all YOLO detections
	// Rate limiting in CameraStateManager prevents command flooding

	// Process all YOLO detections and update/create boats
	boatsBeforeUpdate := len(si.allBoats)
	si.updateAllBoats(detections, classNames, confidences)
	boatsAfterUpdate := len(si.allBoats)
	newBoatsCreated := boatsAfterUpdate - boatsBeforeUpdate

	if newBoatsCreated > 0 {
		si.debugMsg("BOAT_CREATION", fmt.Sprintf("📈 Frame %d: Created %d new boats (%d→%d total). Many new boats might indicate matching issues.",
			si.frameCount, newBoatsCreated, boatsBeforeUpdate, boatsAfterUpdate))
	}

	// Detect P2 objects inside P1 targets for enhanced targeting
	si.detectP2ObjectsInP1Targets(detections, classNames, confidences)

	// NEW: Feed locked boats with ALL detections in their area to maintain tracking
	si.feedLockedBoatsWithClusterDetections(detections, classNames, confidences)

	// Clean up lost boats
	si.cleanupLostBoats()

	// Select target boat for camera tracking
	si.selectTargetBoat()

	if si.targetBoat != nil {
		// Store safe reference to target boat to prevent nil pointer issues if it gets modified during processing
		targetBoat := si.targetBoat

		// Update camera tracking for the target boat
		si.updateCameraTracking()

		// Make sure we're not in scanning mode when tracking a boat
		if si.spatialTracker.IsScanning() {
			si.debugMsg("TARGET_LOCK", fmt.Sprintf("Disabling river scanning - now tracking %s",
				targetBoat.Classification), targetBoat.ID)
			si.spatialTracker.SetScanningMode(false)
		}

		trackingType := "Tracking"
		if targetBoat.LostFrames > 0 {
			trackingType = "Predictive"
		}

		si.debugMsg("TARGET_TRACK", fmt.Sprintf("%s %s at pixel (%d,%d) → spatial (%.1f,%.1f) | Total boats: %d",
			trackingType, targetBoat.Classification,
			targetBoat.CurrentPixel.X, targetBoat.CurrentPixel.Y,
			targetBoat.CurrentSpatial.Pan, targetBoat.CurrentSpatial.Tilt,
			len(si.allBoats)), targetBoat.ID)
	} else {
		// No target boat - check if we're in RECOVERY mode
		if si.isInRecovery {
			// Execute recovery mode - try to find the lost boat
			si.executeRecovery(detections)
		} else if si.isInPostLockHoldover() {
			// We're in holdover period - stay in the area where the locked boat was last seen
			si.handlePostLockHoldover()
		} else {
			// No holdover period - handle loss and ensure river scanning is active
			si.handleTargetLoss()

			// Make sure we're in river scanning mode when no target is selected
			if !si.spatialTracker.IsScanning() && len(si.allBoats) == 0 {
				si.debugMsg("RIVER_SCAN", "No boats detected - activating river scanning")
				si.spatialTracker.SetScanningMode(true)
			}

			// Execute river scanning pattern
			if si.spatialTracker.IsScanning() {
				si.spatialTracker.ExecuteRiverScan()
			}
		}
	}

	// COMPREHENSIVE FRAME SUMMARY DEBUG (show every 30 frames to avoid spam)
	if si.frameCount%30 == 0 || len(si.allBoats) > 0 {
		si.logFrameSummary(detections, classNames, confidences)
	}

	// Send additional scanning status updates for better terminal activity
	if len(si.allBoats) == 0 && si.frameCount%60 == 0 { // Every 2 seconds during scanning
		detectionCount := len(detections)
		if detectionCount > 0 {
			si.logDebugMessage(fmt.Sprintf("Scanning: %d objects detected, none qualified", detectionCount),
				"SCAN_STATUS", 0, map[string]interface{}{
					"detections": detectionCount,
					"frame":      si.frameCount,
				})
		} else {
			si.logDebugMessage("Scanning: Water surface monitoring active",
				"SCAN_STATUS", 0, map[string]interface{}{
					"frame": si.frameCount,
				})
		}
	}
}

// logFrameSummary provides comprehensive debug information about the current frame state
func (si *SpatialIntegration) logFrameSummary(detections []image.Rectangle, classNames []string, confidences []float64) {
	// Count different types of objects detected
	boatDetections := 0
	personDetections := 0
	otherDetections := 0
	totalConfidence := 0.0

	for i, className := range classNames {
		switch className {
		case "boat":
			boatDetections++
		case "person":
			personDetections++
		default:
			otherDetections++
		}
		totalConfidence += confidences[i]
	}

	avgConfidence := 0.0
	if len(detections) > 0 {
		avgConfidence = totalConfidence / float64(len(detections))
	}

	// Analyze current boat states
	totalBoats := len(si.allBoats)
	lockedBoats := 0
	readyToLockBoats := 0
	buildingBoats := 0
	lostBoats := 0

	var boatStatusDetails []string
	for _, boat := range si.allBoats {
		meetsDetectionCriteria := boat.DetectionCount >= si.minDetectionsForLock
		meetsConfidenceCriteria := boat.Confidence > 0.30
		meetsEarlyLockCriteria := boat.Confidence >= 0.80 && boat.DetectionCount >= 1

		if boat.IsLocked {
			lockedBoats++
			boatStatusDetails = append(boatStatusDetails, fmt.Sprintf("%s:🔒LOCKED", boat.ID))
		} else if meetsEarlyLockCriteria || (meetsDetectionCriteria && meetsConfidenceCriteria) {
			readyToLockBoats++
			boatStatusDetails = append(boatStatusDetails, fmt.Sprintf("%s:🎯READY", boat.ID))
		} else if boat.LostFrames > 5 {
			lostBoats++
			boatStatusDetails = append(boatStatusDetails, fmt.Sprintf("%s:❌LOST(%d)", boat.ID, boat.LostFrames))
		} else {
			buildingBoats++
			needed := si.minDetectionsForLock - boat.DetectionCount
			boatStatusDetails = append(boatStatusDetails, fmt.Sprintf("%s:🔨BUILDING(need %d)", boat.ID, needed))
		}
	}

	// Target status
	targetStatus := "NONE"
	if si.targetBoat != nil {
		if si.targetBoat.IsLocked {
			targetStatus = fmt.Sprintf("🔒%s", si.targetBoat.ID)
		} else {
			targetStatus = fmt.Sprintf("🎯%s", si.targetBoat.ID)
		}
	}

	// Camera status
	cameraStatus := "IDLE"
	if si.cameraStateManager != nil && !si.cameraStateManager.IsIdle() {
		cameraStatus = "MOVING"
	}

	si.debugMsg("FRAME_SUMMARY", fmt.Sprintf("📊 Frame %d: YOLO detected %d boats, %d people, %d others (avg conf: %.2f)",
		si.frameCount, boatDetections, personDetections, otherDetections, avgConfidence))

	si.debugMsg("FRAME_SUMMARY", fmt.Sprintf("📊 Tracked boats: %d total (%d locked, %d ready-to-lock, %d building, %d lost)",
		totalBoats, lockedBoats, readyToLockBoats, buildingBoats, lostBoats))

	if len(boatStatusDetails) > 0 {
		si.debugMsg("FRAME_SUMMARY", fmt.Sprintf("📊 Boat states: %v", boatStatusDetails))
	}

	si.debugMsg("FRAME_SUMMARY", fmt.Sprintf("📊 Target: %s | Camera: %s | Scanning: %v",
		targetStatus, cameraStatus, si.spatialTracker.IsScanning()))

	// DUAL LOGGING: Send frame summary to both terminal and debug files
	frameSummaryData := map[string]interface{}{
		"frame":               si.frameCount,
		"yolo_boats":          boatDetections,
		"yolo_people":         personDetections,
		"yolo_others":         otherDetections,
		"avg_confidence":      avgConfidence,
		"total_boats":         totalBoats,
		"locked_boats":        lockedBoats,
		"ready_to_lock_boats": readyToLockBoats,
		"building_boats":      buildingBoats,
		"lost_boats":          lostBoats,
		"target_status":       targetStatus,
		"camera_status":       cameraStatus,
		"scanning_mode":       si.spatialTracker.IsScanning(),
		"boat_details":        boatStatusDetails,
	}

	// Make terminal message more informative and unique to avoid filtering
	timeStr := time.Now().Format("15:04:05")
	if totalBoats > 0 {
		terminalMessage := fmt.Sprintf("%s: %d boats (%d locked, %d ready)",
			timeStr, totalBoats, lockedBoats, readyToLockBoats)
		si.logDebugMessage(terminalMessage, "FRAME_SUMMARY", 0, frameSummaryData)
	} else {
		// More varied messages during scanning to avoid filtering
		scanMessages := []string{
			fmt.Sprintf("%s: Scanning water surface (%d detections processed)", timeStr, boatDetections),
			fmt.Sprintf("%s: No qualified boats found (frame %d)", timeStr, si.frameCount),
			fmt.Sprintf("%s: Active monitoring (%.1f avg confidence)", timeStr, avgConfidence),
		}
		messageIndex := (si.frameCount / 30) % len(scanMessages)
		si.logDebugMessage(scanMessages[messageIndex], "FRAME_SUMMARY", 0, frameSummaryData)
	}

	// Highlight potential issues
	if boatDetections > 0 && totalBoats == 0 {
		si.debugMsg("FRAME_SUMMARY", "⚠️  ISSUE: YOLO detected boats but none are being tracked - check boat creation logic")
		si.logDebugMessage("ISSUE: YOLO detected boats but none tracked", "ISSUE", 2,
			map[string]interface{}{"yolo_boats": boatDetections, "tracked_boats": totalBoats})
	}
	if readyToLockBoats > 0 && lockedBoats == 0 {
		si.debugMsg("FRAME_SUMMARY", "⚠️  ISSUE: Boats ready to lock but none are locked - check lock assignment logic")
		si.logDebugMessage("ISSUE: Boats ready to lock but none locked", "ISSUE", 2,
			map[string]interface{}{"ready_boats": readyToLockBoats, "locked_boats": lockedBoats})
	}
	if totalBoats > 3 {
		si.debugMsg("FRAME_SUMMARY", fmt.Sprintf("⚠️  ISSUE: Too many boats (%d) - possible boat matching/cleanup issues", totalBoats))
		si.logDebugMessage(fmt.Sprintf("ISSUE: Too many boats (%d)", totalBoats), "ISSUE", 1,
			map[string]interface{}{"total_boats": totalBoats})
	}
	if si.targetBoat != nil && !si.targetBoat.IsLocked && si.targetBoat.DetectionCount >= si.minDetectionsForLock {
		si.debugMsg("FRAME_SUMMARY", "⚠️  ISSUE: Target boat meets criteria but not locked", si.targetBoat.ID)
		si.logDebugMessage(fmt.Sprintf("ISSUE: Target %s meets criteria but not locked", si.targetBoat.ID), "ISSUE", 2,
			map[string]interface{}{"target_boat": si.targetBoat.ID, "detections": si.targetBoat.DetectionCount, "confidence": si.targetBoat.Confidence})
	}
}

// updateAllBoats processes all YOLO detections and updates existing boats or creates new ones
func (si *SpatialIntegration) updateAllBoats(detections []image.Rectangle, classNames []string, confidences []float64) {
	// First, increment lost frames for all existing boats
	for _, boat := range si.allBoats {
		boat.LostFrames++
	}

	// LOCK DEBUG: Show current lock status before processing
	lockCandidates := 0
	lockedBoats := 0
	for _, boat := range si.allBoats {
		if boat.DetectionCount >= si.minDetectionsForLock && boat.Confidence > si.p1MinConfidence {
			lockCandidates++
		}
		if boat.IsLocked {
			lockedBoats++
		}
	}

	// REDUCED CONSOLE SPAM: Only show every 10th frame AND only when boats are detected
	if si.frameCount%10 == 0 && len(si.allBoats) > 0 {
		si.debugMsg("LOCK_STATUS", fmt.Sprintf("Frame %d: %d total boats, %d lock candidates (≥%d det + >0.3 conf), %d locked",
			si.frameCount, len(si.allBoats), lockCandidates, si.minDetectionsForLock, lockedBoats))
	}

	// Process each detection
	for i, detection := range detections {
		className := classNames[i]
		confidence := confidences[i]

		// Filter by P1 tracking configuration
		if !si.isP1Object(className) {
			continue
		}

		// Calculate detection center and area
		centerX := detection.Min.X + detection.Dx()/2
		centerY := detection.Min.Y + detection.Dy()/2
		area := float64(detection.Dx() * detection.Dy())

		// REDUCED CONSOLE SPAM: Only show detailed detection info for first few detections per frame
		if i < 2 { // Only first 2 detections per frame
			si.debugMsg("DETECTION_DEBUG", fmt.Sprintf("🔍 Processing detection #%d: pos=(%d,%d), size=%dx%d, area=%.0f, conf=%.3f",
				i+1, centerX, centerY, detection.Dx(), detection.Dy(), area, confidence))
		}

		// Filter by minimum size
		if area < 2000 {
			si.debugMsg("MULTI_FILTER", fmt.Sprintf("❌ Rejecting detection #%d: area %.0f < 2000 pixels", i+1, area))
			continue
		}

		// Filter by pixel dimensions
		if detection.Dx() <= 50 || detection.Dy() <= 50 {
			si.debugMsg("MULTI_FILTER", fmt.Sprintf("❌ Rejecting detection #%d: dimensions %dx%d (≤50x50 pixels)",
				i+1, detection.Dx(), detection.Dy()))
			continue
		}

		si.debugMsg("DETECTION_DEBUG", fmt.Sprintf("✅ Detection #%d passed filters, looking for nearest boat...", i+1))

		// Find nearest existing boat within matching distance
		matchedBoat := si.findNearestBoat(detection, centerX, centerY)

		if matchedBoat != nil {
			// Update existing boat
			oldDetectionCount := matchedBoat.DetectionCount
			oldLocked := matchedBoat.IsLocked

			si.updateExistingBoat(matchedBoat, centerX, centerY, area, confidence, className)

			// LOCK PROGRESSION DEBUG
			newLocked := matchedBoat.IsLocked || (matchedBoat.DetectionCount >= si.minDetectionsForLock && matchedBoat.Confidence > 0.30)
			lockProgress := fmt.Sprintf("(%d/%d detections needed)", matchedBoat.DetectionCount, si.minDetectionsForLock)
			if newLocked && !oldLocked {
				lockProgress = "🔒 JUST LOCKED!"
			} else if matchedBoat.IsLocked {
				lockProgress = "🔒 LOCKED"
			}

			si.debugMsg("MULTI_UPDATE", fmt.Sprintf("✅ Updated boat at (%d,%d), detections: %d→%d %s, lost: %d",
				centerX, centerY, oldDetectionCount, matchedBoat.DetectionCount, lockProgress, matchedBoat.LostFrames), matchedBoat.ID)
		} else {
			// Create new boat
			newBoat := si.createNewTrackedObject(centerX, centerY, area, confidence, className)
			si.allBoats[newBoat.ID] = newBoat
			si.debugMsg("MULTI_NEW", fmt.Sprintf("🆕 Created new boat at (%d,%d), total boats: %d, detections: 1/%d needed for lock",
				centerX, centerY, len(si.allBoats), si.minDetectionsForLock), newBoat.ID)
		}
	}
}

// detectP2ObjectsInP1Targets scans for P2 (enhancement) objects inside P1 (primary) target bounding boxes for enhanced tracking
func (si *SpatialIntegration) detectP2ObjectsInP1Targets(detections []image.Rectangle, classNames []string, confidences []float64) {
	// First, reset P2 object detection for all P1 targets
	for _, boat := range si.allBoats {
		boat.HasP2Objects = false
		boat.P2Count = 0
		boat.P2Confidence = 0.0
		boat.P2Positions = nil // Clear previous P2 object positions
		boat.P2Centroid = image.Point{}
		boat.P2Bounds = image.Rectangle{}
		boat.P2Spread = 0.0
		boat.P2Quality = 0.0
		boat.UseP2Target = false
	}

	// Process all person detections
	for i, detection := range detections {
		className := classNames[i]
		confidence := confidences[i]

		// Only process P2 (enhancement) object detections
		if !si.isP2Object(className) {
			continue
		}

		// Accept ALL P2 detections for ghost box tracking (no confidence threshold)
		// This ensures ghost boxes can maintain tracking even with low-confidence P2 detections

		// Calculate person center
		personCenterX := detection.Min.X + detection.Dx()/2
		personCenterY := detection.Min.Y + detection.Dy()/2
		personCenter := image.Point{X: personCenterX, Y: personCenterY}

		// Find all boats that contain this person, then assign to closest one (prevents oscillation)
		var candidateBoats []*TrackedBoat
		var candidateDistances []float64

		// Search active boats
		for _, boat := range si.allBoats {
			if boat.BoundingBox.Min.X <= personCenter.X && personCenter.X <= boat.BoundingBox.Max.X &&
				boat.BoundingBox.Min.Y <= personCenter.Y && personCenter.Y <= boat.BoundingBox.Max.Y {

				// Calculate distance from person to boat center
				deltaX := personCenter.X - boat.CurrentPixel.X
				deltaY := personCenter.Y - boat.CurrentPixel.Y
				distance := math.Sqrt(float64(deltaX*deltaX + deltaY*deltaY))

				candidateBoats = append(candidateBoats, boat)
				candidateDistances = append(candidateDistances, distance)
			}
		}

		// Assign person to closest boat (prevents PIP oscillation between nearby boats)
		if len(candidateBoats) > 0 {
			closestIndex := 0
			closestDistance := candidateDistances[0]

			for i, distance := range candidateDistances {
				if distance < closestDistance {
					closestDistance = distance
					closestIndex = i
				}
			}

			closestBoat := candidateBoats[closestIndex]

			// Debug message for multiple candidates (oscillation prevention)
			if len(candidateBoats) > 1 {
				var otherBoats []string
				for i, boat := range candidateBoats {
					if i != closestIndex {
						otherBoats = append(otherBoats, fmt.Sprintf("%s(%.0fpx)", boat.ID, candidateDistances[i]))
					}
				}
				si.debugMsg("PIP_OSCILLATION_PREVENTION", fmt.Sprintf("👥🎯 Person contested by %d boats - assigned to closest %s(%.0fpx) over %v",
					len(candidateBoats), closestBoat.ID, closestDistance, otherBoats), closestBoat.ID)
			}

			// Update ACTIVE BOAT with P2 detection
			closestBoat.HasP2Objects = true
			closestBoat.P2Count++
			closestBoat.P2Confidence = math.Max(closestBoat.P2Confidence, confidence)
			closestBoat.LastP2Seen = time.Now()

			// Store individual person position for enhanced tracking
			closestBoat.P2Positions = append(closestBoat.P2Positions, personCenter)

			// Calculate where the person was found relative to boat center
			deltaX := personCenter.X - closestBoat.CurrentPixel.X
			deltaY := personCenter.Y - closestBoat.CurrentPixel.Y

			// Determine location description
			location := "center"
			if math.Abs(float64(deltaX)) > math.Abs(float64(deltaY)) {
				if deltaX > 0 {
					location = "starboard/right side"
				} else {
					location = "port/left side"
				}
			} else {
				if deltaY < 0 {
					location = "bow/front"
				} else {
					location = "stern/back"
				}
			}

			si.debugMsg("PERSON_DETECTED", fmt.Sprintf("🚤👤 Person (conf: %.2f) found in %s (%.0fpx from center)! PersonCount: %d",
				confidence, location, closestDistance, closestBoat.P2Count), closestBoat.ID)
		}
	}

	// Calculate enhanced people tracking data for each boat
	for _, boat := range si.allBoats {
		if boat.HasP2Objects && len(boat.P2Positions) > 0 {
			si.calculateP2TrackingData(boat)

			si.debugMsg("BOAT_WITH_PERSON", fmt.Sprintf("🎯 Boat has %d people (best conf: %.2f, quality: %.2f) - PRIORITY TARGET!",
				boat.P2Count, boat.P2Confidence, boat.P2Quality), boat.ID)

			// Determine if this boat should use P2-centric targeting
			boat.UseP2Target = si.shouldUseP2Targeting(boat)

			if boat.UseP2Target {
				si.debugMsg("PEOPLE_TARGET", fmt.Sprintf("🎯👤 LOCK boat %s switching to P2-centric targeting! Centroid: (%d,%d), Spread: %.1fpx",
					boat.ID, boat.P2Centroid.X, boat.P2Centroid.Y, boat.P2Spread), boat.ID)
			}
		}
	}
}

// calculateP2TrackingData computes enhanced tracking data for P2 objects in P1 targets
func (si *SpatialIntegration) calculateP2TrackingData(boat *TrackedBoat) {
	if len(boat.P2Positions) == 0 {
		return
	}

	// Calculate P2 centroid
	totalX, totalY := 0, 0
	for _, pos := range boat.P2Positions {
		totalX += pos.X
		totalY += pos.Y
	}
	boat.P2Centroid = image.Point{
		X: totalX / len(boat.P2Positions),
		Y: totalY / len(boat.P2Positions),
	}

	// Calculate people bounding box
	minX, minY := boat.P2Positions[0].X, boat.P2Positions[0].Y
	maxX, maxY := minX, minY

	for _, pos := range boat.P2Positions {
		if pos.X < minX {
			minX = pos.X
		}
		if pos.X > maxX {
			maxX = pos.X
		}
		if pos.Y < minY {
			minY = pos.Y
		}
		if pos.Y > maxY {
			maxY = pos.Y
		}
	}
	boat.P2Bounds = image.Rect(minX, minY, maxX, maxY)

	// Calculate people spread (max distance between people)
	boat.P2Spread = 0.0
	for i := 0; i < len(boat.P2Positions); i++ {
		for j := i + 1; j < len(boat.P2Positions); j++ {
			deltaX := boat.P2Positions[i].X - boat.P2Positions[j].X
			deltaY := boat.P2Positions[i].Y - boat.P2Positions[j].Y
			distance := math.Sqrt(float64(deltaX*deltaX + deltaY*deltaY))
			if distance > boat.P2Spread {
				boat.P2Spread = distance
			}
		}
	}

	// Calculate people tracking quality (0-1 score)
	// Factors: confidence, count, spread, centering relative to boat
	confidenceScore := math.Min(boat.P2Confidence, 1.0)
	countScore := math.Min(float64(boat.P2Count)/3.0, 1.0) // Normalize to 3 people max

	// Spread score: moderate spread is good, too tight or too spread is bad
	spreadScore := 1.0
	if boat.P2Spread > 200 {
		spreadScore = 0.5 // Too spread out
	} else if boat.P2Spread < 20 && boat.P2Count > 1 {
		spreadScore = 0.7 // Too clustered for multiple people
	}

	// Centering score: how well people are positioned relative to boat center
	centroidDeltaX := boat.P2Centroid.X - boat.CurrentPixel.X
	centroidDeltaY := boat.P2Centroid.Y - boat.CurrentPixel.Y
	centroidDistance := math.Sqrt(float64(centroidDeltaX*centroidDeltaX + centroidDeltaY*centroidDeltaY))
	maxBoatDistance := math.Max(float64(boat.BoundingBox.Dx()), float64(boat.BoundingBox.Dy())) / 2
	centeringScore := 1.0 - math.Min(centroidDistance/maxBoatDistance, 1.0)

	// Combined quality score
	boat.P2Quality = (confidenceScore*0.4 + countScore*0.3 + spreadScore*0.15 + centeringScore*0.15)
}

// shouldUseP2Targeting determines if a boat should use P2-centric targeting with enhanced fallback logic
func (si *SpatialIntegration) shouldUseP2Targeting(boat *TrackedBoat) bool {
	// Only use P2 targeting for LOCK or SUPER LOCK boats
	if !boat.IsLocked {
		if boat.UseP2Target {
			si.debugMsg("PEOPLE_FALLBACK", fmt.Sprintf("🔓 Boat %s lost LOCK status - falling back to boat tracking", boat.ID), boat.ID)
		}
		return false
	}

	// Must have people detected recently
	if !boat.HasP2Objects || len(boat.P2Positions) == 0 {
		if boat.UseP2Target {
			si.debugMsg("PEOPLE_FALLBACK", fmt.Sprintf("👤❌ Boat %s lost people detection - falling back to boat tracking", boat.ID), boat.ID)
		}
		return false
	}

	// Check if people detection is too stale (haven't seen people in 2 seconds)
	if !boat.LastP2Seen.IsZero() && time.Since(boat.LastP2Seen) > 2*time.Second {
		if boat.UseP2Target {
			timeSince := time.Since(boat.LastP2Seen)
			si.debugMsg("PEOPLE_FALLBACK", fmt.Sprintf("👤⏰ Boat %s people detection stale (%.1fs ago) - falling back to boat tracking",
				boat.ID, timeSince.Seconds()), boat.ID)
		}
		return false
	}

	// Quality threshold - only use if people tracking is reliable
	if boat.P2Quality < 0.6 {
		if boat.UseP2Target {
			si.debugMsg("PEOPLE_FALLBACK", fmt.Sprintf("👤📉 Boat %s people quality too low (%.2f < 0.6) - falling back to boat tracking",
				boat.ID, boat.P2Quality), boat.ID)
		}
		return false
	}

	// Don't use P2 targeting if too many people (becomes chaotic)
	if boat.P2Count > 5 {
		if boat.UseP2Target {
			si.debugMsg("PEOPLE_FALLBACK", fmt.Sprintf("👥 Boat %s has too many people (%d > 5) - falling back to boat tracking",
				boat.ID, boat.P2Count), boat.ID)
		}
		return false
	}

	// Don't use if people are too spread out (unreliable centroid)
	if boat.P2Spread > 300 {
		if boat.UseP2Target {
			si.debugMsg("PEOPLE_FALLBACK", fmt.Sprintf("👥📏 Boat %s people too spread (%.1fpx > 300px) - falling back to boat tracking",
				boat.ID, boat.P2Spread), boat.ID)
		}
		return false
	}

	// Additional stability check: P2 centroid shouldn't be too far from boat center
	if boat.P2Centroid.X != 0 && boat.P2Centroid.Y != 0 {
		deltaX := boat.P2Centroid.X - boat.CurrentPixel.X
		deltaY := boat.P2Centroid.Y - boat.CurrentPixel.Y
		centroidDistance := math.Sqrt(float64(deltaX*deltaX + deltaY*deltaY))
		maxReasonableDistance := math.Max(float64(boat.BoundingBox.Dx()), float64(boat.BoundingBox.Dy()))

		if centroidDistance > maxReasonableDistance {
			if boat.UseP2Target {
				si.debugMsg("PEOPLE_FALLBACK", fmt.Sprintf("👤🎯 Boat %s P2 centroid too far from boat (%.1fpx > %.1fpx) - falling back to boat tracking",
					boat.ID, centroidDistance, maxReasonableDistance), boat.ID)
			}
			return false
		}
	}

	// All checks passed - safe to use P2 targeting
	if !boat.UseP2Target {
		si.debugMsg("PEOPLE_ACTIVATE", fmt.Sprintf("👤✅ Boat %s meets criteria for P2 targeting - activating (quality: %.2f, count: %d, spread: %.1fpx)",
			boat.ID, boat.P2Quality, boat.P2Count, boat.P2Spread), boat.ID)
	}

	return true
}

// calculateSpatialCoordinatesForPixel converts pixel coordinates to spatial PTZ coordinates
func (si *SpatialIntegration) calculateSpatialCoordinatesForPixel(pixelX, pixelY int) SpatialCoordinate {
	// Get current camera position
	actualPos := si.ptzCtrl.GetCurrentPosition()
	currentSpatial := SpatialCoordinate{
		Pan:  actualPos.Pan,
		Tilt: actualPos.Tilt,
		Zoom: actualPos.Zoom,
	}

	// Calculate pixel offset from frame center
	offsetX := pixelX - si.frameCenterX // Positive = target is right of center
	offsetY := pixelY - si.frameCenterY // Positive = target is below center

	// Get calibrated conversion rates for current zoom level
	panPixelsPerUnit := si.spatialTracker.InterpolatePanCalibration(currentSpatial.Zoom)
	tiltPixelsPerUnit := si.spatialTracker.InterpolateTiltCalibration(currentSpatial.Zoom)

	// Calculate PTZ adjustments needed to center the target
	panAdjustment := float64(offsetX) / panPixelsPerUnit
	tiltAdjustment := float64(offsetY) / tiltPixelsPerUnit

	// Calculate target spatial coordinate
	target := SpatialCoordinate{
		Pan:  currentSpatial.Pan + panAdjustment,
		Tilt: currentSpatial.Tilt + tiltAdjustment,
		Zoom: currentSpatial.Zoom, // Keep current zoom for now
	}

	return target
}

// feedLockedBoatsWithClusterDetections - NEW: Use ALL detections within locked boat areas to maintain tracking
func (si *SpatialIntegration) feedLockedBoatsWithClusterDetections(detections []image.Rectangle, classNames []string, confidences []float64) {
	// Only process locked boats for cluster feeding
	for _, boat := range si.allBoats {
		if !boat.IsLocked {
			continue // Only feed locked boats
		}

		// Create expanded detection area (current bounding box + 20%)
		expandedBox := boat.BoundingBox
		expandWidth := int(float64(expandedBox.Dx()) * 0.20)  // 20% expansion
		expandHeight := int(float64(expandedBox.Dy()) * 0.20) // 20% expansion

		expandedBox.Min.X = int(math.Max(0, float64(expandedBox.Min.X-expandWidth)))
		expandedBox.Min.Y = int(math.Max(0, float64(expandedBox.Min.Y-expandHeight)))
		expandedBox.Max.X = int(math.Min(float64(si.frameWidth), float64(expandedBox.Max.X+expandWidth)))
		expandedBox.Max.Y = int(math.Min(float64(si.frameHeight), float64(expandedBox.Max.Y+expandHeight)))

		// Count detections within expanded area
		clusterDetections := 0
		clusterScore := 0.0
		var clusterTypes []string

		for i, detection := range detections {
			className := classNames[i]
			confidence := confidences[i]

			// Accept ALL detections for robust cluster tracking (no confidence threshold)
			// This helps maintain locks even with low-confidence detections

			// Calculate detection center
			centerX := detection.Min.X + detection.Dx()/2
			centerY := detection.Min.Y + detection.Dy()/2

			// Check if detection falls within expanded boat area
			if centerX >= expandedBox.Min.X && centerX <= expandedBox.Max.X &&
				centerY >= expandedBox.Min.Y && centerY <= expandedBox.Max.Y {

				clusterDetections++
				clusterTypes = append(clusterTypes, className)

				// Weight different object types for lock maintenance
				var detectionWeight float64
				switch className {
				case "person":
					detectionWeight = 1.0 // People are strongest anchor
				case "boat":
					detectionWeight = 1.2 // Boat detections are strongest (but already handled)
				case "backpack", "handbag", "suitcase":
					detectionWeight = 0.6 // Personal items are good anchors
				case "bottle", "cup":
					detectionWeight = 0.4 // Small objects help
				case "chair", "bench":
					detectionWeight = 0.5 // Furniture helps
				default:
					detectionWeight = 0.3 // Other objects provide some support
				}

				clusterScore += confidence * detectionWeight
			}
		}

		// Use cluster detections to maintain lock
		if clusterDetections > 0 {
			// Reset lost frames - cluster is keeping the boat "alive"
			boat.LostFrames = 0
			boat.LastSeen = time.Now()

			// Add to detection count (but limit growth to prevent inflation)
			if boat.DetectionCount < 100 { // Cap at 100 to prevent endless growth
				boat.DetectionCount++
			}

			// Update confidence based on cluster strength
			clusterConfidence := math.Min(clusterScore/float64(clusterDetections), 1.0)
			boat.Confidence = math.Max(boat.Confidence*0.9, clusterConfidence) // Gentle confidence update

			si.debugMsg("CLUSTER_FEED", fmt.Sprintf("🎯📦 Locked boat %s maintained by cluster: %d detections [%v] (score: %.2f, conf: %.2f)",
				boat.ID, clusterDetections, clusterTypes, clusterScore, clusterConfidence), boat.ID)
		}
	}
}

// findNearestBoat finds the closest existing boat to a detection using actual YOLO bounding box
func (si *SpatialIntegration) findNearestBoat(detectionRect image.Rectangle, centerX, centerY int) *TrackedBoat {
	// SIMPLIFIED MATCHING DISTANCE - no more zoom scaling complexity
	baseDistance := 200.0 // Base distance for fallback distance matching

	// ADAPTIVE DISTANCE: Increase matching distance after recent history clearing to maintain boat continuity
	if !si.lastHistoryClear.IsZero() && time.Since(si.lastHistoryClear) < 10*time.Second {
		baseDistance = 400.0 // Double distance for 10 seconds after history clearing
		si.debugMsg("ADAPTIVE_MATCH", fmt.Sprintf("🔄 Using increased matching distance %.1f after recent history clear (%.1fs ago)",
			baseDistance, time.Since(si.lastHistoryClear).Seconds()))
	}

	// Additional allowance if camera was recently moving (tracking artifacts)
	cameraMovingBonus := 1.0
	if si.cameraStateManager != nil {
		if !si.cameraStateManager.IsIdle() {
			cameraMovingBonus = 1.5 // 50% more tolerance when camera is moving
			baseDistance *= cameraMovingBonus
		}
	}

	// Cap at reasonable limits for distance fallback
	finalDistance := math.Max(100.0, math.Min(800.0, baseDistance)) // 100px min, 800px max

	// REDUCED CONSOLE SPAM: Only show matching details occasionally (full details in debug session files)
	showMatchingDebug := (centerX+centerY)%500 < 50 // Show ~10% of matching attempts
	if showMatchingDebug {
		si.debugMsg("BOAT_MATCH", fmt.Sprintf("🔍 Looking for boat near (%d,%d) using YOLO rect %dx%d", centerX, centerY, detectionRect.Dx(), detectionRect.Dy()))
		si.debugMsg("BOAT_MATCH", fmt.Sprintf("📏 Distance fallback: base=%.1f × camera=%.1f = %.1f → clamped=%.1f",
			200.0, cameraMovingBonus, baseDistance, finalDistance))
	}

	var nearestBoat *TrackedBoat
	minDistance := finalDistance
	allDistances := make(map[string]float64) // Track all distances for debugging

	// NEW: Enhanced matching with multiple strategies
	var bestMatchBoat *TrackedBoat
	var bestMatchReason string

	for _, boat := range si.allBoats {
		// STRATEGY 1: Bounding Box Overlap Check (highest priority) - USING ACTUAL YOLO RECTANGLES
		boatBoundingBox := boat.BoundingBox

		// Use the ACTUAL YOLO detection rectangle instead of fake 150x150 box
		detectionBoundingBox := detectionRect

		// Check if bounding boxes overlap
		if boatBoundingBox.Overlaps(detectionBoundingBox) {
			bestMatchBoat = boat
			bestMatchReason = "BOUNDING_BOX_OVERLAP"
			if showMatchingDebug {
				si.debugMsg("BOAT_MATCH", fmt.Sprintf("🎯 BOUNDING BOX OVERLAP: YOLO detection %dx%d at (%d,%d) overlaps with boat %s bbox",
					detectionRect.Dx(), detectionRect.Dy(), centerX, centerY, boat.ID))
			}
			break // Bounding box overlap is definitive - use this boat
		}

		// STRATEGY 2: Predicted Position Matching (for moving boats)
		if boat.PixelVelocity.X != 0 || boat.PixelVelocity.Y != 0 {
			// Calculate 30-60 frame prediction (1-2 seconds at 30fps)
			predictionTime1s := 1.0 // 30 frames ahead
			predictionTime2s := 2.0 // 60 frames ahead

			predicted30 := image.Point{
				X: boat.CurrentPixel.X + int(boat.PixelVelocity.X*predictionTime1s),
				Y: boat.CurrentPixel.Y + int(boat.PixelVelocity.Y*predictionTime1s),
			}

			predicted60 := image.Point{
				X: boat.CurrentPixel.X + int(boat.PixelVelocity.X*predictionTime2s),
				Y: boat.CurrentPixel.Y + int(boat.PixelVelocity.Y*predictionTime2s),
			}

			// Check distance to both predictions
			dist30 := math.Sqrt(float64((centerX-predicted30.X)*(centerX-predicted30.X) + (centerY-predicted30.Y)*(centerY-predicted30.Y)))
			dist60 := math.Sqrt(float64((centerX-predicted60.X)*(centerX-predicted60.X) + (centerY-predicted60.Y)*(centerY-predicted60.Y)))

			minPredictedDist := math.Min(dist30, dist60)

			// Use enhanced distance for predicted matching
			predictedDistance := finalDistance * 1.5 // 50% more tolerance for predictions

			if minPredictedDist < predictedDistance && (bestMatchBoat == nil || minPredictedDist < minDistance) {
				bestMatchBoat = boat
				bestMatchReason = fmt.Sprintf("PREDICTION_MATCH_%.0fs", math.Min(predictionTime1s, predictionTime2s))
				minDistance = minPredictedDist
				if showMatchingDebug {
					si.debugMsg("BOAT_MATCH", fmt.Sprintf("🔮 PREDICTION MATCH: Detection (%d,%d) matches boat %s predicted at (%d,%d) dist=%.1f",
						centerX, centerY, boat.ID, predicted30.X, predicted30.Y, minPredictedDist))
				}
			}
		}

		// STRATEGY 3: Enhanced Distance Matching (with locked boat bonuses)
		deltaX := float64(centerX - boat.CurrentPixel.X)
		deltaY := float64(centerY - boat.CurrentPixel.Y)
		distance := math.Sqrt(deltaX*deltaX + deltaY*deltaY)

		allDistances[boat.ID] = distance

		// LOCKED BOAT BONUS: 3x distance tolerance for locked boats
		adjustedDistance := finalDistance
		if boat.IsLocked {
			adjustedDistance = finalDistance * 3.0 // 3x tolerance for locked boats
			if showMatchingDebug {
				si.debugMsg("BOAT_MATCH", fmt.Sprintf("🔒 LOCKED BOAT BONUS: %s gets 3x distance tolerance (%.1f → %.1f)",
					boat.ID, finalDistance, adjustedDistance))
			}
		}

		if distance < adjustedDistance && (bestMatchBoat == nil || distance < minDistance) {
			bestMatchBoat = boat
			if boat.IsLocked {
				bestMatchReason = "DISTANCE_LOCKED"
			} else {
				bestMatchReason = "DISTANCE_STANDARD"
			}
			minDistance = distance
		}
	}

	// Use the best match found
	nearestBoat = bestMatchBoat

	// DETAILED MATCHING DEBUG - show ALL boats and their distances (reduced spam)
	if showMatchingDebug && len(si.allBoats) > 0 {
		si.debugMsg("BOAT_MATCH", fmt.Sprintf("📊 Checking %d existing boats:", len(si.allBoats)))
		for _, boat := range si.allBoats {
			distance := allDistances[boat.ID]
			status := "❌ too far"
			lockStatus := fmt.Sprintf("(%d/%d det)", boat.DetectionCount, si.minDetectionsForLock)
			if boat.IsLocked {
				lockStatus = "🔒 LOCKED"
			}

			adjustedDist := finalDistance
			if boat.IsLocked {
				adjustedDist = finalDistance * 3.0
			}

			if distance < adjustedDist {
				status = "✅ MATCH!"
				if nearestBoat != nil && boat.ID == nearestBoat.ID {
					status = "🎯 BEST MATCH"
				}
			}

			si.debugMsg("BOAT_MATCH", fmt.Sprintf("  %s: pos=(%d,%d), dist=%.1f %s %s",
				boat.ID, boat.CurrentPixel.X, boat.CurrentPixel.Y, distance, status, lockStatus))
		}
	} else if showMatchingDebug && len(si.allBoats) == 0 {
		si.debugMsg("BOAT_MATCH", "📊 No existing boats to match against")
	}

	// Enhanced result logging
	if nearestBoat != nil {
		lockStatus := fmt.Sprintf("(%d/%d detections)", nearestBoat.DetectionCount, si.minDetectionsForLock)
		if nearestBoat.IsLocked {
			lockStatus = "🔒 LOCKED"
		}
		si.debugMsg("BOAT_MATCH", fmt.Sprintf("✅ MATCHED detection at (%d,%d) to boat %s via %s at distance %.1fpx %s",
			centerX, centerY, nearestBoat.ID, bestMatchReason, minDistance, lockStatus))
	} else {
		if len(si.allBoats) > 0 {
			si.debugMsg("BOAT_MATCH", fmt.Sprintf("❌ NO MATCH for detection at (%d,%d) - closest was %.1fpx > threshold %.1fpx",
				centerX, centerY, minDistance, finalDistance))
		} else {
			si.debugMsg("BOAT_MATCH", fmt.Sprintf("🆕 NO EXISTING BOATS - will create new boat at (%d,%d)",
				centerX, centerY))
		}
	}

	return nearestBoat
}

// updateExistingBoat updates an existing boat with new detection data
func (si *SpatialIntegration) updateExistingBoat(boat *TrackedBoat, centerX, centerY int, area, confidence float64, className string) {
	// Store old values for debug comparison
	oldDetectionCount := boat.DetectionCount
	oldConfidence := boat.Confidence

	// Reset lost frames (boat was found)
	boat.LostFrames = 0
	boat.LastSeen = time.Now()
	boat.DetectionCount++
	boat.Confidence = math.Max(boat.Confidence, confidence)

	// CLEAN SLATE TRANSITION: Reset contaminated early detection data when reaching lock threshold
	justReachedLock := boat.DetectionCount == si.minDetectionsForLock && oldDetectionCount == si.minDetectionsForLock-1
	if justReachedLock {
		si.debugMsg("CLEAN_SLATE", fmt.Sprintf("🧹 Boat %s reached lock threshold (%d detections) - resetting to fresh coordinates (%d,%d)",
			boat.ID, boat.DetectionCount, centerX, centerY), boat.ID)

		// Reset pixel tracking history (clear contaminated early positions)
		boat.PixelHistory = []image.Point{}
		boat.PredictedPixel = image.Point{}

		// Reset spatial tracking history (clear contaminated early spatial data)
		boat.SpatialHistory = []SpatialCoordinate{}
		boat.PredictedSpatial = SpatialCoordinate{}

		// Use fresh YOLO coordinates as new baseline
		boat.CurrentPixel = image.Point{X: centerX, Y: centerY}
		boat.PixelArea = area

		// Update spatial position with clean baseline
		si.updateBoatSpatialPosition(boat)

		si.debugMsg("CLEAN_SLATE", "✅ Boat baseline reset - starting precision tracking with clean data", boat.ID)

		// DUAL LOGGING: Critical clean slate transition
		si.logDebugMessage("🧹 Clean slate transition - fresh baseline set", "CLEAN_SLATE", 2, map[string]interface{}{
			"boat_id":           boat.ID,
			"detection_count":   boat.DetectionCount,
			"fresh_coordinates": fmt.Sprintf("(%d,%d)", centerX, centerY),
			"history_cleared":   true,
			"baseline_reset":    true,
		})
	} else {
		// Normal update for non-transition frames - update position with conditional smoothing

		// Update pixel position with conditional smoothing
		oldX := float64(boat.CurrentPixel.X)
		oldY := float64(boat.CurrentPixel.Y)

		// Calculate how much the detection moved
		deltaX := float64(centerX) - oldX
		deltaY := float64(centerY) - oldY
		movementDistance := math.Sqrt(deltaX*deltaX + deltaY*deltaY)

		var finalX, finalY int

		// CRITICAL FIX: No smoothing for locked targets - use fresh coordinates for accurate tracking
		if boat.IsLocked || boat == si.targetBoat {
			// For locked targets: Use fresh coordinates immediately (no lag)
			finalX = centerX
			finalY = centerY
			si.debugMsg("NO_SMOOTH", fmt.Sprintf("🎯 Locked target %s: using fresh coords (%d,%d) - no smoothing lag",
				boat.ID, finalX, finalY), boat.ID)
		} else {
			// For non-locked targets: Apply smoothing to reduce jitter
			finalX = int(oldX*0.7 + float64(centerX)*0.3)
			finalY = int(oldY*0.7 + float64(centerY)*0.3)
			if movementDistance > 20 { // Only log significant movements for non-locked targets
				si.debugMsg("SMOOTH", fmt.Sprintf("Unlocked target %s: (%d,%d)→(%d,%d) smoothed to (%d,%d)",
					boat.ID, int(oldX), int(oldY), centerX, centerY, finalX, finalY), boat.ID)
			}
		}

		boat.CurrentPixel = image.Point{X: finalX, Y: finalY}

		// Update area with conditional smoothing
		if boat.IsLocked || boat == si.targetBoat {
			// For locked targets: Use fresh area immediately (no lag in bounding box)
			boat.PixelArea = area
		} else {
			// For non-locked targets: Apply area smoothing to reduce jitter
			boat.PixelArea = boat.PixelArea*0.7 + area*0.3
		}

		// Update spatial position
		si.updateBoatSpatialPosition(boat)
	}

	// ENHANCED LOCK PROGRESSION DEBUG
	meetsDetectionCriteria := boat.DetectionCount >= si.minDetectionsForLock
	meetsConfidenceCriteria := boat.Confidence > 0.30
	meetsEarlyLockCriteria := boat.Confidence >= 0.80 && boat.DetectionCount >= 1

	lockStatus := "🔓 NOT LOCKED"
	if boat.IsLocked {
		lockStatus = fmt.Sprintf("🔒 LOCKED (strength: %.2f)", boat.LockStrength)
	} else if meetsEarlyLockCriteria {
		lockStatus = "⚡ READY FOR EARLY LOCK"
	} else if meetsDetectionCriteria && meetsConfidenceCriteria {
		lockStatus = "🎯 READY FOR STANDARD LOCK"
	} else {
		// Show what's blocking the lock
		blockers := []string{}
		if !meetsDetectionCriteria {
			needed := si.minDetectionsForLock - boat.DetectionCount
			blockers = append(blockers, fmt.Sprintf("need %d more detections", needed))
		}
		if !meetsConfidenceCriteria {
			blockers = append(blockers, fmt.Sprintf("confidence %.3f too low", boat.Confidence))
		}
		lockStatus = fmt.Sprintf("🔓 BLOCKED: %v", blockers)
	}

	// Show detailed progression for boats close to locking
	if boat.DetectionCount >= si.minDetectionsForLock-2 || boat.Confidence > 0.25 {
		confidenceChange := ""
		if boat.Confidence > oldConfidence {
			confidenceChange = fmt.Sprintf(" (↑%.3f)", boat.Confidence-oldConfidence)
		}

		si.debugMsg("LOCK_PROGRESS", fmt.Sprintf("🔍 Boat detections %d→%d, confidence %.3f%s, %s",
			oldDetectionCount, boat.DetectionCount, boat.Confidence, confidenceChange, lockStatus), boat.ID)

		// DUAL LOGGING: Send to both terminal and debug files
		debugData := map[string]interface{}{
			"boat_id":                   boat.ID,
			"old_detections":            oldDetectionCount,
			"new_detections":            boat.DetectionCount,
			"confidence":                boat.Confidence,
			"confidence_change":         boat.Confidence - oldConfidence,
			"lock_status":               lockStatus,
			"meets_detection_criteria":  meetsDetectionCriteria,
			"meets_confidence_criteria": meetsConfidenceCriteria,
			"meets_early_lock_criteria": meetsEarlyLockCriteria,
		}

		priority := 1 // Normal priority
		if meetsEarlyLockCriteria || (meetsDetectionCriteria && meetsConfidenceCriteria) {
			priority = 2 // High priority for boats ready to lock
		}

		si.logDebugMessage(fmt.Sprintf("Boat %s: %d→%d det, conf %.3f, %s",
			boat.ID, oldDetectionCount, boat.DetectionCount, boat.Confidence, lockStatus),
			"LOCK_PROGRESS", priority, debugData)
	}

	// Update bounding box for P2 detection using MILITARY TARGET DIMENSIONS
	// Match the military target overlay area for visual consistency and precision
	baseWidth := int(math.Sqrt(boat.PixelArea) * 0.8)  // Wider base estimation
	baseHeight := int(math.Sqrt(boat.PixelArea) * 0.6) // Taller base estimation

	// Use military target expansion (20%) to match overlay dimensions exactly
	bufferX := int(float64(baseWidth) * 0.2)  // 20% buffer horizontally (matches military target)
	bufferY := int(float64(baseHeight) * 0.2) // 20% buffer vertically (matches military target)

	expandedWidth := baseWidth + bufferX
	expandedHeight := baseHeight + bufferY

	// Apply bounds checking to keep bounding box within frame
	minX := int(math.Max(0, float64(boat.CurrentPixel.X-expandedWidth)))
	minY := int(math.Max(0, float64(boat.CurrentPixel.Y-expandedHeight)))
	maxX := int(math.Min(float64(si.frameWidth), float64(boat.CurrentPixel.X+expandedWidth)))
	maxY := int(math.Min(float64(si.frameHeight), float64(boat.CurrentPixel.Y+expandedHeight)))

	boat.BoundingBox = image.Rectangle{
		Min: image.Point{X: minX, Y: minY},
		Max: image.Point{X: maxX, Y: maxY},
	}

	si.debugMsg("BBOX_MILITARY", fmt.Sprintf("Boat %s: base %dx%d + military target buffer %dx%d = P2 area %dx%d (matches overlay)",
		boat.ID, baseWidth, baseHeight, bufferX, bufferY, expandedWidth, expandedHeight), boat.ID)

	// Update pixel history for visualization
	boat.PixelHistory = append(boat.PixelHistory, boat.CurrentPixel)
	if len(boat.PixelHistory) > 20 {
		boat.PixelHistory = boat.PixelHistory[1:]
	}

	// Calculate velocity (always needed for targeting decisions)
	si.calculateBoatVelocity(boat)

	// ONLY update spatial position for locked boats - no predictive logic for unlocked boats
	if boat.IsLocked {
		si.debugMsg("SPATIAL_UPDATE", "🔒 Updating spatial position for LOCKED boat", boat.ID)
		si.updateBoatSpatialPosition(boat)
	} else {
		si.debugMsg("SPATIAL_SKIP", fmt.Sprintf("🔓 Skipping spatial position update for unlocked boat (%d/5 detections)",
			boat.DetectionCount), boat.ID)
	}

	// Update tracking priority (boats with more detections get higher priority)
	boat.TrackingPriority = float64(boat.DetectionCount) * boat.Confidence
}

// calculateBoatVelocity calculates pixel velocity for a boat using simple direct approach (like July 6th version)
func (si *SpatialIntegration) calculateBoatVelocity(boat *TrackedBoat) {
	if len(boat.PixelHistory) < 2 {
		return
	}

	// SIMPLE APPROACH: Only calculate velocity when camera is IDLE
	if si.cameraStateManager != nil && !si.cameraStateManager.IsIdle() {
		si.debugMsg("VELOCITY_CALC", "⏸️ Skipping velocity calculation - camera is MOVING")
		return
	}

	// Use last few points for velocity calculation
	historyCount := int(math.Min(5, float64(len(boat.PixelHistory))))
	if historyCount < 2 {
		return
	}

	points := boat.PixelHistory[len(boat.PixelHistory)-historyCount:]

	// Calculate time difference between actual history points
	// Assume roughly 30fps for timing (33ms per frame)
	frameTimeDiff := float64(historyCount-1) / 30.0 // Time span of history points in seconds

	if frameTimeDiff <= 0 {
		return
	}

	// SIMPLE DIRECT CALCULATION: Just pixel movement / time (like July 6th version)
	// Calculate pixel movement (difference between latest and earliest point in history)
	startPoint := points[0]
	endPoint := points[len(points)-1]

	movementX := float64(endPoint.X - startPoint.X)
	movementY := float64(endPoint.Y - startPoint.Y)

	// Calculate velocity directly - NO COMPENSATION
	boat.PixelVelocity.X = movementX / frameTimeDiff
	boat.PixelVelocity.Y = movementY / frameTimeDiff

	si.debugMsg("VELOCITY_CALC", fmt.Sprintf("✅ Boat %s SIMPLE velocity: (%.1f, %.1f) px/s over %.1fs (%d points)",
		boat.ID, boat.PixelVelocity.X, boat.PixelVelocity.Y, frameTimeDiff, historyCount), boat.ID)
}

// updateBoatSpatialPosition calculates where camera should move to center the boat
// CLEAN STATE MANAGEMENT: Only calculates when camera is IDLE to prevent corruption
func (si *SpatialIntegration) updateBoatSpatialPosition(boat *TrackedBoat) {
	// Continue spatial calculations even during movement - rate limiting handles command flow

	// CRITICAL FIX: Always get ACTUAL camera position directly from PTZ controller
	// This prevents using stale cached data from spatial tracker
	actualPos := si.ptzCtrl.GetCurrentPosition()
	currentSpatial := SpatialCoordinate{
		Pan:  actualPos.Pan,
		Tilt: actualPos.Tilt,
		Zoom: actualPos.Zoom,
	}

	si.debugMsg("SPATIAL_CALC", fmt.Sprintf("🎯 Using ACTUAL camera position: Pan=%.1f Tilt=%.1f Zoom=%.1f (not cached)",
		currentSpatial.Pan, currentSpatial.Tilt, currentSpatial.Zoom))

	// SIMPLE APPROACH: Always use current boat position (like July 6th version)
	targetPixelX := boat.CurrentPixel.X
	targetPixelY := boat.CurrentPixel.Y
	targetZoom := si.calculateOptimalZoom(boat, currentSpatial.Zoom) // Re-enable progressive zoom calculations

	// Calculate pixel offset from frame center (using predicted or current position)
	offsetX := targetPixelX - si.frameCenterX // Positive = boat is right of center
	offsetY := targetPixelY - si.frameCenterY // Positive = boat is below center

	// Log detailed spatial calculation to debug session if active
	si.debugMsg("SPATIAL_CALC", fmt.Sprintf("Boat pixel (%d,%d) → calculating spatial position...", targetPixelX, targetPixelY), boat.ID)

	// FIXED: Use proper zoom-aware calibration instead of hardcoded values
	// Get calibrated conversion rates for current zoom level
	panPixelsPerUnit := si.spatialTracker.InterpolatePanCalibration(currentSpatial.Zoom)
	tiltPixelsPerUnit := si.spatialTracker.InterpolateTiltCalibration(currentSpatial.Zoom)

	// CRITICAL DEBUG: Check for invalid calibration values
	if panPixelsPerUnit <= 0 || tiltPixelsPerUnit <= 0 {
		si.debugMsg("SPATIAL_BUG", fmt.Sprintf("🚨 INVALID CALIBRATION: pan=%.6f, tilt=%.6f (should be positive!)",
			panPixelsPerUnit, tiltPixelsPerUnit))
		// Use safe fallback values
		panPixelsPerUnit = 15.0
		tiltPixelsPerUnit = 12.0
		si.debugMsg("SPATIAL_BUG", fmt.Sprintf("🛡️ Using fallback calibration: pan=%.1f, tilt=%.1f",
			panPixelsPerUnit, tiltPixelsPerUnit))
	}

	// Calculate PTZ adjustments needed to center the boat (at predicted position)
	panAdjustment := float64(offsetX) / panPixelsPerUnit
	tiltAdjustment := float64(offsetY) / tiltPixelsPerUnit

	// Log detailed spatial calculation steps (calculation will be completed below)
	si.logSpatialCalculationInProgress(boat, targetPixelX, targetPixelY, offsetX, offsetY,
		currentSpatial, panPixelsPerUnit, tiltPixelsPerUnit, panAdjustment, tiltAdjustment)

	si.debugMsgVerbose("CALIBRATION", fmt.Sprintf("Zoom=%.1f: Using %.2f px/pan-unit, %.2f px/tilt-unit (was hardcoded 15.0, 12.0)",
		currentSpatial.Zoom, panPixelsPerUnit, tiltPixelsPerUnit))

	// SAFETY CHECK: Prevent massive camera jumps due to calculation bugs
	maxReasonableAdjustment := 200.0 // Maximum reasonable pan/tilt adjustment per tracking update

	if math.Abs(panAdjustment) > maxReasonableAdjustment {
		si.debugMsg("SPATIAL_SAFETY", fmt.Sprintf("🚨 DANGEROUS PAN ADJUSTMENT: %.1f (max: %.1f) - CLAMPING",
			panAdjustment, maxReasonableAdjustment))
		if panAdjustment > 0 {
			panAdjustment = maxReasonableAdjustment
		} else {
			panAdjustment = -maxReasonableAdjustment
		}
		si.debugMsg("SPATIAL_SAFETY", fmt.Sprintf("🛡️ Clamped pan adjustment to: %.1f", panAdjustment))
	}

	if math.Abs(tiltAdjustment) > maxReasonableAdjustment {
		si.debugMsg("SPATIAL_SAFETY", fmt.Sprintf("🚨 DANGEROUS TILT ADJUSTMENT: %.1f (max: %.1f) - CLAMPING",
			tiltAdjustment, maxReasonableAdjustment))
		if tiltAdjustment > 0 {
			tiltAdjustment = maxReasonableAdjustment
		} else {
			tiltAdjustment = -maxReasonableAdjustment
		}
		si.debugMsg("SPATIAL_SAFETY", fmt.Sprintf("🛡️ Clamped tilt adjustment to: %.1f", tiltAdjustment))
	}

	// Calculate target position (where camera should move to center the boat when zoom completes)
	targetPan := currentSpatial.Pan + panAdjustment
	targetTilt := currentSpatial.Tilt + tiltAdjustment

	// CRITICAL DEBUG: Show final calculation
	// Update debug data with final calculation results if available
	if boat.HasSpatialDebugData && boat.SpatialDebugData != nil {
		boat.SpatialDebugData["Target_Pan"] = targetPan
		boat.SpatialDebugData["Target_Tilt"] = targetTilt
		boat.SpatialDebugData["Final_Calculation"] = fmt.Sprintf("Final → Pan:%.1f, Tilt:%.1f", targetPan, targetTilt)

		// Sanity check logging
		expectedTiltDirection := "UP"
		if targetPixelY > si.frameCenterY {
			expectedTiltDirection = "DOWN"
		}

		boat.SpatialDebugData["Sanity_Check"] = fmt.Sprintf("Boat at pixel (%d,%d) should move camera %s, calculated tilt=%.1f",
			targetPixelX, targetPixelY, expectedTiltDirection, targetTilt)
	}

	// Simple console logging for final result
	si.debugMsg("SPATIAL_CALC", fmt.Sprintf("Final → Pan:%.1f, Tilt:%.1f", targetPan, targetTilt), boat.ID)

	// FINAL SAFETY CHECK: Ensure target coordinates are within reasonable PTZ limits
	// Prevent movements to completely invalid positions that could damage hardware
	if targetPan < 0 || targetPan > 3600 {
		si.debugMsg("SPATIAL_SAFETY", fmt.Sprintf("🚨 TARGET PAN OUT OF BOUNDS: %.1f (should be 0-3600) - CLAMPING", targetPan))
		targetPan = math.Max(0, math.Min(3600, targetPan))
		si.debugMsg("SPATIAL_SAFETY", fmt.Sprintf("🛡️ Clamped target pan to: %.1f", targetPan))
	}

	if targetTilt < 0 || targetTilt > 900 {
		si.debugMsg("SPATIAL_SAFETY", fmt.Sprintf("🚨 TARGET TILT OUT OF BOUNDS: %.1f (should be 0-900) - CLAMPING", targetTilt))
		targetTilt = math.Max(0, math.Min(900, targetTilt))
		si.debugMsg("SPATIAL_SAFETY", fmt.Sprintf("🛡️ Clamped target tilt to: %.1f", targetTilt))
	}

	if targetZoom < 10 || targetZoom > 120 {
		si.debugMsg("SPATIAL_SAFETY", fmt.Sprintf("🚨 TARGET ZOOM OUT OF BOUNDS: %.1f (should be 10-120) - CLAMPING", targetZoom))
		targetZoom = math.Max(10, math.Min(120, targetZoom))
		si.debugMsg("SPATIAL_SAFETY", fmt.Sprintf("🛡️ Clamped target zoom to: %.1f", targetZoom))
	}

	boat.CurrentSpatial = SpatialCoordinate{
		Pan:  targetPan,
		Tilt: targetTilt,
		Zoom: targetZoom,
	}

	// Simple debug showing calculation
	si.debugMsg("SPATIAL_CALC", fmt.Sprintf("Boat at pixel (%d,%d), center (%d,%d), offset (%d,%d)",
		targetPixelX, targetPixelY, si.frameCenterX, si.frameCenterY, offsetX, offsetY), boat.ID)
	si.debugMsg("SPATIAL_CALC", fmt.Sprintf("Current PTZ: Pan=%.1f Tilt=%.1f Zoom=%.1f → Target PTZ: Pan=%.1f Tilt=%.1f Zoom=%.1f",
		currentSpatial.Pan, currentSpatial.Tilt, currentSpatial.Zoom, targetPan, targetTilt, targetZoom), boat.ID)
}

// calculateOptimalZoom determines the best zoom level for tracking a boat using PROGRESSIVE ZOOM
func (si *SpatialIntegration) calculateOptimalZoom(boat *TrackedBoat, currentZoom float64) float64 {
	// Zoom constraints
	const minZoom = 10.0  // Minimum zoom level
	const maxZoom = 120.0 // Maximum zoom level

	// === PROGRESSIVE ZOOM SYSTEM ===
	// Start conservative, increase gradually as tracking becomes more stable
	var targetZoom float64

	// Stage 1: INITIAL DETECTION (1-3 detections) - Keep zoom LOW for fast movement
	if boat.DetectionCount <= 3 {
		targetZoom = 15.0 // Very conservative - fast camera movement
		si.debugMsg("ZOOM_PROGRESSIVE", fmt.Sprintf("🚀 INITIAL STAGE: Boat %s (det:%d) → Conservative zoom %.1f for fast movement",
			boat.ID, boat.DetectionCount, targetZoom), boat.ID)
		return math.Max(minZoom, math.Min(maxZoom, targetZoom))
	}

	// Stage 2: BUILDING CONFIDENCE (4-6 detections) - Gradual increase
	if boat.DetectionCount <= 6 {
		baseZoom := 20.0
		confidenceBonus := (boat.Confidence - 0.3) * 15.0 // Up to +15 for high confidence
		targetZoom = baseZoom + confidenceBonus

		si.debugMsg("ZOOM_PROGRESSIVE", fmt.Sprintf("📈 BUILDING STAGE: Boat %s (det:%d, conf:%.2f) → Base %.1f + Conf bonus %.1f = %.1f",
			boat.ID, boat.DetectionCount, boat.Confidence, baseZoom, confidenceBonus, targetZoom), boat.ID)
		return math.Max(minZoom, math.Min(maxZoom, targetZoom))
	}

	// Stage 3: LOCKED TRACKING (7-23 detections) - More aggressive zoom based on stability
	if boat.DetectionCount <= 23 { // Changed from 11 to 23 to accommodate 24+ for SUPER LOCK
		baseZoom := 25.0

		if boat.IsLocked {
			// Locked boat - calculate stability-based zoom
			stabilityFactor := math.Min(float64(boat.DetectionCount)/15.0, 1.0) // 0-1 based on detection count
			confidenceFactor := math.Min((boat.Confidence-0.3)/0.2, 1.0)        // 0-1 based on confidence (50% for full bonus)

			// Calculate distance from center for centering bonus
			centerX := float64(si.frameCenterX)
			centerY := float64(si.frameCenterY)
			deltaX := math.Abs(float64(boat.CurrentPixel.X) - centerX)
			deltaY := math.Abs(float64(boat.CurrentPixel.Y) - centerY)
			distanceFromCenter := math.Sqrt(deltaX*deltaX + deltaY*deltaY)
			maxDistance := math.Sqrt(centerX*centerX + centerY*centerY)
			centeringFactor := 1.0 - (distanceFromCenter / maxDistance) // 1.0 = centered, 0.0 = edge

			// Calculate velocity factor (stationary boats get more zoom)
			velocity := math.Sqrt(boat.PixelVelocity.X*boat.PixelVelocity.X + boat.PixelVelocity.Y*boat.PixelVelocity.Y)
			velocityFactor := 1.0
			if velocity > 20.0 {
				velocityFactor = 0.7 // Reduce zoom for fast moving boats
			} else if velocity < 5.0 {
				velocityFactor = 1.3 // Increase zoom for stationary boats
			}

			// Progressive zoom formula: Start at 25, build up to 60 based on stability
			stabilityZoom := 25.0 + (stabilityFactor * 25.0) // 25-50 based on detection count
			confidenceBonus := confidenceFactor * 15.0       // +0-15 based on confidence
			centeringBonus := centeringFactor * 10.0         // +0-10 based on centering

			targetZoom = stabilityZoom + confidenceBonus + centeringBonus
			targetZoom *= velocityFactor

			// Person detection bonus (people deserve more zoom!)
			if boat.HasP2Objects {
				personBonus := 10.0 + (float64(boat.P2Count) * 3.0) // +10-16 for people
				targetZoom += personBonus
				si.debugMsg("ZOOM_PERSON", fmt.Sprintf("👤 Boat %s has %d people - adding +%.1f zoom bonus",
					boat.ID, boat.P2Count, personBonus), boat.ID)
			}

			si.debugMsg("ZOOM_PROGRESSIVE", fmt.Sprintf("🔒 LOCKED STAGE: Boat %s (det:%d, conf:%.2f, centered:%.1f%%, vel:%.1f)",
				boat.ID, boat.DetectionCount, boat.Confidence, centeringFactor*100, velocity), boat.ID)
			si.debugMsg("ZOOM_PROGRESSIVE", fmt.Sprintf("🔒 Stability:%.1f + Confidence:%.1f + Centering:%.1f × Velocity:%.1f = %.1f",
				stabilityZoom, confidenceBonus, centeringBonus, velocityFactor, targetZoom), boat.ID)
		} else {
			// Unlocked boat - conservative zoom
			targetZoom = baseZoom + (boat.Confidence * 10.0) // 25-35 range
			si.debugMsg("ZOOM_PROGRESSIVE", fmt.Sprintf("🔓 UNLOCKED STAGE: Boat %s (det:%d, conf:%.2f) → Base %.1f + Conf %.1f = %.1f",
				boat.ID, boat.DetectionCount, boat.Confidence, baseZoom, boat.Confidence*10.0, targetZoom), boat.ID)
		}
	} else {
		// Stage 4: SUPER LOCK MEGA ZOOM (24+ detections) - Maximum optical zoom for sustained tracking
		baseZoom := 50.0

		if boat.IsLocked {
			// SUPER LOCK - MEGA ZOOM calculations (much more aggressive for 24+ frames)
			stabilityFactor := math.Min(float64(boat.DetectionCount)/20.0, 1.0) // 0-1 based on detection count (peak at 20)
			confidenceFactor := math.Min((boat.Confidence-0.3)/0.2, 1.0)        // 0-1 based on confidence

			// Calculate distance from center for centering bonus
			centerX := float64(si.frameCenterX)
			centerY := float64(si.frameCenterY)
			deltaX := math.Abs(float64(boat.CurrentPixel.X) - centerX)
			deltaY := math.Abs(float64(boat.CurrentPixel.Y) - centerY)
			distanceFromCenter := math.Sqrt(deltaX*deltaX + deltaY*deltaY)
			maxDistance := math.Sqrt(centerX*centerX + centerY*centerY)
			centeringFactor := 1.0 - (distanceFromCenter / maxDistance) // 1.0 = centered, 0.0 = edge

			// Calculate velocity factor (stationary boats get more zoom)
			velocity := math.Sqrt(boat.PixelVelocity.X*boat.PixelVelocity.X + boat.PixelVelocity.Y*boat.PixelVelocity.Y)
			velocityFactor := 1.0
			if velocity > 15.0 {
				velocityFactor = 0.8 // Slightly reduce zoom for fast moving boats
			} else if velocity < 3.0 {
				velocityFactor = 1.2 // Increase for stationary boats (reduced from 1.4 to prevent over-zoom)
			}

			// AGGRESSIVE MEGA ZOOM formula: Start at 70, build up to 120 based on stability
			megaZoom := 70.0 + (stabilityFactor * 40.0) // 70-110 based on detection count (more aggressive base)
			confidenceBonus := confidenceFactor * 15.0  // +0-15 based on confidence
			centeringBonus := centeringFactor * 10.0    // +0-10 based on centering

			targetZoom = megaZoom + confidenceBonus + centeringBonus
			targetZoom *= velocityFactor

			// ENHANCED PEOPLE-CENTRIC ZOOM: Optimize for people framing when using P2 targeting
			if boat.UseP2Target {
				// Calculate optimal zoom based on people spread for precise framing
				peopleOptimalZoom := si.calculateP2OptimalZoom(boat, targetZoom)
				targetZoom = peopleOptimalZoom

				si.debugMsg("PEOPLE_ZOOM", fmt.Sprintf("🎯👤 Using P2-centric zoom optimization: spread=%.1fpx → zoom=%.1f",
					boat.P2Spread, targetZoom), boat.ID)
			} else if boat.HasP2Objects {
				// Standard person bonus when not using P2-centric targeting
				megaPersonBonus := 15.0 + (float64(boat.P2Count) * 5.0) // +15-25 for people (reduced to prevent over-zoom)
				targetZoom += megaPersonBonus
				si.debugMsg("MEGA_ZOOM_PERSON", fmt.Sprintf("👤🔍 SUPER LOCK boat %s has %d people - adding +%.1f MEGA zoom bonus",
					boat.ID, boat.P2Count, megaPersonBonus), boat.ID)
			}

			si.debugMsg("MEGA_ZOOM", fmt.Sprintf("🚀💎 SUPER LOCK STAGE: Boat %s (det:%d, conf:%.2f, centered:%.1f%%, vel:%.1f)",
				boat.ID, boat.DetectionCount, boat.Confidence, centeringFactor*100, velocity), boat.ID)
			si.debugMsg("MEGA_ZOOM", fmt.Sprintf("🚀💎 MegaBase:%.1f + Confidence:%.1f + Centering:%.1f × Velocity:%.1f = %.1f",
				megaZoom, confidenceBonus, centeringBonus, velocityFactor, targetZoom), boat.ID)
		} else {
			// Even unlocked 24+ detection boats get enhanced zoom
			targetZoom = baseZoom + (boat.Confidence * 15.0) // 50-65 range
			si.debugMsg("ZOOM_PROGRESSIVE", fmt.Sprintf("🔓💎 SUPER LOCK UNLOCKED: Boat %s (det:%d, conf:%.2f) → Base %.1f + Conf %.1f = %.1f",
				boat.ID, boat.DetectionCount, boat.Confidence, baseZoom, boat.Confidence*15.0, targetZoom), boat.ID)
		}
	}

	// Apply zoom change limits to prevent huge jumps - REDUCED for better lag handling
	var maxZoomChange float64
	if boat.DetectionCount <= 3 {
		maxZoomChange = 5.0 // Very conservative changes for new boats
	} else if boat.DetectionCount <= 6 {
		maxZoomChange = 5.0 // Reduced from 15.0 - more conservative for building boats
	} else if boat.DetectionCount >= 24 && boat.IsLocked { // SUPER LOCK: 24+ frames for MEGA ZOOM changes
		maxZoomChange = 25.0 // MEGA ZOOM changes for SUPER LOCK boats (unchanged)
	} else if boat.IsLocked {
		maxZoomChange = 15.0 // Reduced from 25.0 - less aggressive for locked boats
	} else {
		maxZoomChange = 15.0 // Standard changes for unlocked boats
	}

	// Apply zoom change limit
	zoomChange := targetZoom - currentZoom
	if math.Abs(zoomChange) > maxZoomChange {
		if zoomChange > 0 {
			targetZoom = currentZoom + maxZoomChange
		} else {
			targetZoom = currentZoom - maxZoomChange
		}
		si.debugMsg("ZOOM_LIMIT", fmt.Sprintf("Limited zoom change from %.1f to %.1f (max change: %.1f)",
			zoomChange, targetZoom-currentZoom, maxZoomChange), boat.ID)
	}

	// Apply hard constraints
	targetZoom = math.Max(minZoom, math.Min(maxZoom, targetZoom))

	// Final debug output
	si.debugMsg("ZOOM_FINAL", fmt.Sprintf("Boat %s: Current=%.1f → Target=%.1f (change: %.1f, stage: %s)",
		boat.ID, currentZoom, targetZoom, targetZoom-currentZoom,
		si.getZoomStage(boat.DetectionCount, boat.IsLocked)), boat.ID)

	return targetZoom
}

// calculateP2OptimalZoom calculates optimal zoom to frame P2 cluster effectively
func (si *SpatialIntegration) calculateP2OptimalZoom(boat *TrackedBoat, baseZoom float64) float64 {
	if !boat.HasP2Objects || len(boat.P2Positions) == 0 {
		return baseZoom
	}

	// Target pixel sizes for optimal people framing
	const idealPeopleAreaMin = 80.0  // Minimum pixels across for P2 cluster (too close)
	const idealPeopleAreaMax = 400.0 // Maximum pixels across for P2 cluster (too far)
	const targetPeopleArea = 200.0   // Ideal pixels across for P2 cluster

	// Current people spread in pixels
	currentSpread := boat.P2Spread
	if currentSpread < 20 {
		currentSpread = 20 // Minimum spread to avoid division issues
	}

	// Calculate zoom adjustment factor based on people spread
	var zoomFactor float64

	if currentSpread < idealPeopleAreaMin {
		// People too close - need to zoom out to see the full group
		zoomFactor = currentSpread / targetPeopleArea
		si.debugMsg("PEOPLE_ZOOM", fmt.Sprintf("👥 People too close (%.1fpx < %.1fpx) - zooming out with factor %.2f",
			currentSpread, idealPeopleAreaMin, zoomFactor), boat.ID)
	} else if currentSpread > idealPeopleAreaMax {
		// People too spread out - zoom in to focus on the cluster center
		zoomFactor = targetPeopleArea / currentSpread
		si.debugMsg("PEOPLE_ZOOM", fmt.Sprintf("👥 People too spread (%.1fpx > %.1fpx) - zooming in with factor %.2f",
			currentSpread, idealPeopleAreaMax, zoomFactor), boat.ID)
	} else {
		// People spread is in good range - fine-tune based on proximity to ideal
		spreadRatio := currentSpread / targetPeopleArea
		zoomFactor = 1.0 + (1.0-spreadRatio)*0.2 // Minor adjustments
		si.debugMsg("PEOPLE_ZOOM", fmt.Sprintf("👥 People spread optimal (%.1fpx in range) - fine tuning with factor %.2f",
			currentSpread, zoomFactor), boat.ID)
	}

	// Apply people quality factor - higher quality allows more aggressive zoom
	qualityFactor := 0.5 + (boat.P2Quality * 0.5) // 0.5-1.0 range
	zoomFactor *= qualityFactor

	// Calculate target zoom with P2-centric optimization
	peopleZoom := baseZoom * zoomFactor

	// Add people count bonus (more people = slight zoom adjustment for better view)
	if boat.P2Count > 2 {
		groupBonus := math.Min(float64(boat.P2Count-2)*3.0, 10.0) // Max +10 for large groups
		peopleZoom += groupBonus
		si.debugMsg("PEOPLE_ZOOM", fmt.Sprintf("👥 Group bonus for %d people: +%.1f zoom",
			boat.P2Count, groupBonus), boat.ID)
	}

	// Constraints - don't go too extreme with people-based zooming
	minPeopleZoom := baseZoom * 0.7 // Don't zoom out too much
	maxPeopleZoom := baseZoom * 1.4 // Don't zoom in too much

	peopleZoom = math.Max(minPeopleZoom, math.Min(maxPeopleZoom, peopleZoom))

	si.debugMsg("PEOPLE_ZOOM", fmt.Sprintf("🎯👤 People zoom calculation: base=%.1f × factor=%.2f × quality=%.2f = %.1f (clamped to %.1f)",
		baseZoom, zoomFactor/qualityFactor, qualityFactor, baseZoom*zoomFactor*qualityFactor, peopleZoom), boat.ID)

	return peopleZoom
}

// getZoomStage returns a string describing the current zoom stage
func (si *SpatialIntegration) getZoomStage(detectionCount int, isLocked bool) string {
	if detectionCount <= 3 {
		return "INITIAL"
	} else if detectionCount <= 6 {
		return "BUILDING"
	} else if detectionCount >= 24 && isLocked {
		return "SUPER_LOCK_MEGA_ZOOM"
	} else if isLocked {
		return "LOCKED"
	} else if detectionCount >= 24 {
		return "SUPER_LOCK_UNLOCKED"
	} else {
		return "UNLOCKED"
	}
}

// generateNewObjectID creates a unified object ID in format: 20240125-12-30.001
func (si *SpatialIntegration) generateNewObjectID() string {
	now := time.Now()

	// Format: 20240125-12-30 (12-hour format)
	currentMinute := now.Format("20060102-3-04") // 3 = 12-hour, 04 = minute

	// Reset counter if new minute
	if currentMinute != si.lastMinuteTimestamp {
		si.lastMinuteTimestamp = currentMinute
		si.currentMinuteCounter = 0
	}

	// Increment counters
	si.currentMinuteCounter++
	si.totalDetectedObjectsCounter++

	// Return padded format: 20240125-12-30.001
	return fmt.Sprintf("%s.%03d", currentMinute, si.currentMinuteCounter)
}

// createNewTrackedObject creates a new tracked object (renamed from createNewBoat)
func (si *SpatialIntegration) createNewTrackedObject(centerX, centerY int, area, confidence float64, className string) *TrackedBoat {
	now := time.Now()
	objectID := si.generateNewObjectID() // Use unified ID format: 20240125-12-30.001

	// Calculate P2 detection bounding box using MILITARY TARGET DIMENSIONS
	// Match the military target overlay area for visual consistency and precision
	baseWidth := int(math.Sqrt(area) * 0.8)  // Wider base estimation
	baseHeight := int(math.Sqrt(area) * 0.6) // Taller base estimation

	// Use military target expansion (20%) to match overlay dimensions exactly
	bufferX := int(float64(baseWidth) * 0.2)  // 20% buffer horizontally (matches military target)
	bufferY := int(float64(baseHeight) * 0.2) // 20% buffer vertically (matches military target)

	expandedWidth := baseWidth + bufferX
	expandedHeight := baseHeight + bufferY

	// Apply bounds checking to keep bounding box within frame
	minX := int(math.Max(0, float64(centerX-expandedWidth)))
	minY := int(math.Max(0, float64(centerY-expandedHeight)))
	maxX := int(math.Min(float64(si.frameWidth), float64(centerX+expandedWidth)))
	maxY := int(math.Min(float64(si.frameHeight), float64(centerY+expandedHeight)))

	boundingBox := image.Rectangle{
		Min: image.Point{X: minX, Y: minY},
		Max: image.Point{X: maxX, Y: maxY},
	}

	si.debugMsg("NEW_OBJECT_MILITARY", fmt.Sprintf("Object %s: base %dx%d + military target buffer %dx%d = P2 area %dx%d (matches overlay)",
		objectID, baseWidth, baseHeight, bufferX, bufferY, expandedWidth, expandedHeight), objectID)

	boat := &TrackedBoat{
		ID:               objectID,
		Classification:   className,
		Confidence:       confidence,
		FirstDetected:    now,
		LastSeen:         now,
		DetectionCount:   1,
		LostFrames:       0,
		CurrentPixel:     image.Point{X: centerX, Y: centerY},
		PixelArea:        area,
		BoundingBox:      boundingBox,
		PixelHistory:     []image.Point{{X: centerX, Y: centerY}},
		TrackingPriority: confidence, // Initial priority based on confidence
		HasP2Objects:     false,      // Initialize person detection
		P2Confidence:     0.0,
		P2Count:          0,
	}

	// Calculate initial spatial position if camera is IDLE
	if si.cameraStateManager == nil || si.cameraStateManager.IsIdle() {
		si.updateBoatSpatialPosition(boat)
		si.debugMsg("NEW_OBJECT", fmt.Sprintf("✅ Created object %s with initial spatial position (%.1f,%.1f,%.1f)",
			objectID, boat.CurrentSpatial.Pan, boat.CurrentSpatial.Tilt, boat.CurrentSpatial.Zoom), objectID)
	} else {
		si.debugMsg("NEW_OBJECT", fmt.Sprintf("⏸️ Created object %s - spatial position will be calculated when camera becomes IDLE", objectID), objectID)
	}

	return boat
}

// cleanupLostBoats removes boats that have been lost for too long
func (si *SpatialIntegration) cleanupLostBoats() {
	var removedBoats []string

	for id, boat := range si.allBoats {
		// 🔥 P2-BASED LOCK MAINTENANCE - Use people detection to maintain locks even when P1 is lost!
		if boat.LostFrames > 0 && (boat.IsLocked || boat.LockStrength > 0.8) && boat.HasP2Objects {
			// P1 lost but P2 active - MAINTAIN LOCK using P2 data
			boat.LostFrames = 0 // Reset - we're not really "lost"
			boat.LastSeen = time.Now()
			boat.UseP2Target = true // Target the people, not the boat center

			si.debugMsg("P2_LOCK_MAINTENANCE", fmt.Sprintf("🔒👤 LOCK maintained via P2! P1 lost but %d people detected - targeting P2 centroid (%.2f quality)",
				boat.P2Count, boat.P2Quality), boat.ID)
			continue // Skip removal check since we're maintaining lock via P2
		}

		if boat.LostFrames > si.maxLostFrames {
			// Remove boat that's been lost too long
			removedBoats = append(removedBoats, id)
			delete(si.allBoats, id)

			// If this was our target boat, clear the target
			if si.targetBoat != nil && si.targetBoat.ID == id {
				si.debugMsg("MULTI_CLEANUP", fmt.Sprintf("Target boat %s lost for %d frames - clearing target",
					si.targetBoat.ID, si.targetBoat.LostFrames), si.targetBoat.ID)
				si.targetBoat = nil
			}
		}
	}

	if len(removedBoats) > 0 {
		si.debugMsg("MULTI_CLEANUP", fmt.Sprintf("Removed %d lost boats: %v", len(removedBoats), removedBoats))
	}

}

// DISABLED: Ghost box functions removed in favor of elegant P2-based lock maintenance
/*
func (si *SpatialIntegration) createGhostP1Box(boat *TrackedBoat) {
	now := time.Now()

	// Get current camera position for movement detection
	currentSpatial := SpatialCoordinate{}
	if si.ptzCtrl != nil {
		currentPos := si.ptzCtrl.GetCurrentPosition()
		currentSpatial = SpatialCoordinate{
			Pan:  currentPos.Pan,
			Tilt: currentPos.Tilt,
			Zoom: currentPos.Zoom,
		}
	}

	// Create ghost box
	ghostBox := &GhostP1BoundingBox{
		OriginalBoatID:   boat.ID,
		BoundingBox:      boat.BoundingBox,
		LastP1Seen:       boat.LastSeen,
		LastP2Seen:       boat.LastP2Seen, // Preserve last P2 detection time
		SpatialCoords:    currentSpatial,
		P2DetectionCount: 0, // Reset counter for ghost period
		CreatedAt:        now,
	}

	// Store ghost box
	si.ghostP1Boxes[boat.ID] = ghostBox

	si.debugMsg("GHOST_P1_CREATED", fmt.Sprintf("👻 Created ghost P1 box for lost boat %s - P2 detection will continue in this area", boat.ID), boat.ID)
}

// cleanupGhostP1Boxes removes expired ghost boxes
func (si *SpatialIntegration) cleanupGhostP1Boxes() {
	now := time.Now()
	var expiredGhosts []string

	for id, ghostBox := range si.ghostP1Boxes {
		shouldRemove := false
		removeReason := ""

		// Check for camera movement (invalidates spatial coordinates)
		if si.ptzCtrl != nil {
			currentPos := si.ptzCtrl.GetCurrentPosition()
			panDiff := math.Abs(currentPos.Pan - ghostBox.SpatialCoords.Pan)
			tiltDiff := math.Abs(currentPos.Tilt - ghostBox.SpatialCoords.Tilt)
			zoomDiff := math.Abs(currentPos.Zoom - ghostBox.SpatialCoords.Zoom)

			// Significant camera movement invalidates ghost box
			if panDiff > 50 || tiltDiff > 50 || zoomDiff > 5 {
				shouldRemove = true
				removeReason = fmt.Sprintf("camera moved (ΔPan:%.1f ΔTilt:%.1f ΔZoom:%.1f)", panDiff, tiltDiff, zoomDiff)
			}
		}

		// Check for P2 inactivity timeout (no P2 objects found for 10 seconds)
		if !shouldRemove && now.Sub(ghostBox.LastP2Seen) > 10*time.Second {
			shouldRemove = true
			removeReason = fmt.Sprintf("P2 inactive for %.1fs", now.Sub(ghostBox.LastP2Seen).Seconds())
		}

		// Check for absolute timeout (ghost box exists for 30 seconds)
		if !shouldRemove && now.Sub(ghostBox.CreatedAt) > 30*time.Second {
			shouldRemove = true
			removeReason = fmt.Sprintf("max ghost time (%.1fs)", now.Sub(ghostBox.CreatedAt).Seconds())
		}

		if shouldRemove {
			expiredGhosts = append(expiredGhosts, id)
			delete(si.ghostP1Boxes, id)
			si.debugMsg("GHOST_P1_EXPIRED", fmt.Sprintf("👻💀 Removed ghost P1 box %s: %s", id, removeReason), id)
		}
	}

	if len(expiredGhosts) > 0 {
		si.debugMsg("GHOST_P1_CLEANUP", fmt.Sprintf("Cleaned up %d expired ghost P1 boxes: %v", len(expiredGhosts), expiredGhosts))
	}
}

// createOrUpdateVirtualBoat creates or updates a virtual boat from a ghost box for targeting system compatibility
func (si *SpatialIntegration) createOrUpdateVirtualBoat(ghostBox *GhostP1BoundingBox, virtualBoat *TrackedBoat, personCenter image.Point, confidence float64) {
	// Check if we already have a virtual boat for this ghost box
	existingBoat, exists := si.allBoats[ghostBox.OriginalBoatID+"_ghost"]

	if exists {
		// Update existing virtual boat
		existingBoat.HasP2Objects = true
		existingBoat.P2Count++
		existingBoat.P2Confidence = math.Max(existingBoat.P2Confidence, confidence)
		existingBoat.LastP2Seen = time.Now()
		existingBoat.P2Positions = append(existingBoat.P2Positions, personCenter)
		existingBoat.LostFrames = 0 // Reset lost frames when P2 object is detected
		existingBoat.LastSeen = time.Now()

		si.debugMsg("GHOST_VIRTUAL_UPDATE", fmt.Sprintf("👻🔄 Updated virtual boat %s from ghost box - P2Count: %d", existingBoat.ID, existingBoat.P2Count), existingBoat.ID)
	} else {
		// Create new virtual boat for targeting system
		now := time.Now()
		virtualBoatID := ghostBox.OriginalBoatID + "_ghost"

		newVirtualBoat := &TrackedBoat{
			ID:               virtualBoatID,
			Classification:   "ghost_boat", // Special classification for ghost boats
			Confidence:       0.5,          // Moderate confidence for ghost boats
			FirstDetected:    now,
			LastSeen:         now,
			DetectionCount:   10, // High detection count to make it eligible for targeting
			LostFrames:       0,
			IsLocked:         false,
			BoundingBox:      ghostBox.BoundingBox,
			CurrentPixel:     virtualBoat.CurrentPixel,
			PixelArea:        float64(ghostBox.BoundingBox.Dx() * ghostBox.BoundingBox.Dy()),
			PixelHistory:     []image.Point{virtualBoat.CurrentPixel},
			TrackingPriority: 1.0, // High priority for ghost boats with people
			HasP2Objects:     true,
			P2Count:          1,
			P2Confidence:     confidence,
			LastP2Seen:       now,
			P2Positions:      []image.Point{personCenter},
		}

		// Add to tracking system
		si.allBoats[virtualBoatID] = newVirtualBoat

		si.debugMsg("GHOST_VIRTUAL_CREATED", fmt.Sprintf("👻✨ Created virtual boat %s from ghost box for targeting - enables PIP on lost P1 with people", virtualBoatID), virtualBoatID)
	}
}
*/

// selectTargetBoat chooses which boat to actively track with the camera
func (si *SpatialIntegration) selectTargetBoat() {
	// If we have a current target that's still valid, check if we should keep it
	if si.targetBoat != nil {
		// CRITICAL FIX: Only consider a boat truly "lost" if it's not being detected at all
		// Check if the current target boat is still being detected (LostFrames should be 0 if detected)
		if si.targetBoat.LostFrames == 0 {
			// Boat is actively being detected - keep it regardless of other factors
			return
		}

		// REDUCED tolerance for locked boats - but allow some frames for detection gaps
		// Keep current target if it's locked and recently seen (allow up to 10 frames gap)
		if si.targetBoat.IsLocked && si.targetBoat.LostFrames <= 10 {
			return // Keep current target (400ms tolerance at 25fps)
		}

		// REDUCED tolerance for locked boats during predictive tracking to prevent stale position tracking
		// Allow up to 60 frames (2 seconds) for predictive tracking to help YOLO re-acquire
		if si.targetBoat.IsLocked && si.targetBoat.LostFrames <= 60 {
			// CRITICAL FIX: Check if predicted position is reasonable before continuing tracking
			offsetX := si.targetBoat.CurrentPixel.X - si.frameCenterX
			offsetY := si.targetBoat.CurrentPixel.Y - si.frameCenterY
			distanceFromCenter := math.Sqrt(float64(offsetX*offsetX + offsetY*offsetY))
			maxReasonableDistance := float64(si.frameWidth) * 0.3 // Max 30% of frame width from center

			if distanceFromCenter > maxReasonableDistance {
				si.debugMsg("STALE_POSITION", fmt.Sprintf("🚨 Boat %s position (%d,%d) is %.0f pixels from center (max: %.0f) - ABANDONING predictive tracking",
					si.targetBoat.ID, si.targetBoat.CurrentPixel.X, si.targetBoat.CurrentPixel.Y, distanceFromCenter, maxReasonableDistance), si.targetBoat.ID)

				// Clear the target to prevent camera from tracking to stale off-center position
				si.targetBoat = nil
				return
			}

			si.debugMsg("PREDICTIVE_KEEP", fmt.Sprintf("🔮 Keeping locked boat %s for predictive tracking (%d/60 frames lost) - position reasonable",
				si.targetBoat.ID, si.targetBoat.LostFrames), si.targetBoat.ID)
			return // Keep target during predictive tracking period
		}

		// AGGRESSIVE switching when locked boat is clearly lost (60+ frames = 2+ seconds)
		if si.targetBoat.IsLocked && si.targetBoat.LostFrames > 60 {
			si.debugMsg("TARGET_SWITCH", fmt.Sprintf("🔄 Locked boat %s lost for %d frames - ENTERING RECOVERY MODE",
				si.targetBoat.ID, si.targetBoat.LostFrames), si.targetBoat.ID)

			// NEW: Prepare recovery data instead of holdover
			si.prepareRecoveryData(si.targetBoat)

			// CRITICAL FIX: Remove the ghost boat from allBoats to prevent re-selection
			delete(si.allBoats, si.targetBoat.ID)
			si.debugMsg("GHOST_REMOVAL", "👻 Removed ghost boat from tracking pool", si.targetBoat.ID)

			// DON'T clear targetBoat yet - keep it during RECOVERY to preserve ObjectID
			// si.targetBoat will be cleared only when RECOVERY actually fails in endRecovery()
		}

		// Check if enough time has passed since last target switch (prevents rapid switching)
		if si.frameCount-si.lastTargetSwitch < si.targetSwitchCooldown {
			if si.targetBoat != nil && si.targetBoat.LostFrames <= 5 { // Allow some lost frames before switching
				return // Keep current target during cooldown
			}
		}
	}

	// GHOST BOAT CLEANUP: Remove any boats that have been lost for too long BEFORE selection
	var ghostBoats []string
	var ghostBoatDetails []string
	for id, boat := range si.allBoats {
		if boat.LostFrames > 25 { // 1 second at 25fps - boat is clearly gone
			// DETAILED GHOST ANALYSIS - what was lost?
			lockStatus := "unlocked"
			if boat.IsLocked {
				lockStatus = "🔒 LOCKED"
			} else if boat.DetectionCount >= si.minDetectionsForLock && boat.Confidence > 0.30 {
				lockStatus = "🔓 ready-to-lock"
			} else {
				lockStatus = fmt.Sprintf("🔓 (%d/%d det)", boat.DetectionCount, si.minDetectionsForLock)
			}

			ghostDetail := fmt.Sprintf("%s[%s,conf=%.2f,lost=%d]", id, lockStatus, boat.Confidence, boat.LostFrames)
			ghostBoatDetails = append(ghostBoatDetails, ghostDetail)
			ghostBoats = append(ghostBoats, id)
			delete(si.allBoats, id)

			// CRITICAL: Clear target boat if it references the removed boat
			if si.targetBoat != nil && si.targetBoat.ID == id {
				si.debugMsg("GHOST_CLEANUP", fmt.Sprintf("🎯 Clearing target boat %s (was removed as ghost)", id), id)
				si.targetBoat = nil
			}
		}
	}
	if len(ghostBoats) > 0 {
		si.debugMsg("GHOST_CLEANUP", fmt.Sprintf("👻 Removed %d ghost boats: %v", len(ghostBoats), ghostBoatDetails))
	}

	// Find the best boat to target
	var bestBoat *TrackedBoat
	bestScore := 0.0
	candidateAnalysis := make(map[string]map[string]interface{}) // Track all candidates for debugging

	// Only print analysis message when there are boats to analyze
	if len(si.allBoats) > 0 {
		si.debugMsg("TARGET_SELECTION", fmt.Sprintf("🎯 Analyzing %d boats for target selection:", len(si.allBoats)))

		// DUAL LOGGING: Target selection start
		si.logDebugMessage(fmt.Sprintf("🎯 Analyzing %d boats for target", len(si.allBoats)),
			"TARGET_SELECTION", 1, map[string]interface{}{
				"candidate_count": len(si.allBoats),
				"frame":           si.frameCount,
			})
	}

	for _, boat := range si.allBoats {
		// FIXED: Allow boats with more lost frames to be selected if they're the best available
		// Skip boats that are lost for too long (more than 25 frames = 1 second)
		if boat.LostFrames > 25 {
			si.debugMsg("TARGET_SELECTION", fmt.Sprintf("  %s: ❌ SKIPPED (lost %d frames > 25)", boat.ID, boat.LostFrames), boat.ID)
			continue
		}

		// Calculate targeting score
		score := si.calculateTargetingScore(boat)

		// ENHANCED DEBUG: Track all candidate analysis
		meetsDetectionCriteria := boat.DetectionCount >= si.minDetectionsForLock
		meetsConfidenceCriteria := boat.Confidence > 0.30
		meetsEarlyLockCriteria := boat.Confidence >= 0.80 && boat.DetectionCount >= 1
		canBeLocked := meetsEarlyLockCriteria || (meetsDetectionCriteria && meetsConfidenceCriteria)

		lockability := "🔓 NOT LOCKABLE"
		if boat.IsLocked {
			lockability = fmt.Sprintf("🔒 ALREADY LOCKED (strength: %.2f)", boat.LockStrength)
		} else if meetsEarlyLockCriteria {
			lockability = "⚡ READY FOR EARLY LOCK (high confidence)"
		} else if meetsDetectionCriteria && meetsConfidenceCriteria {
			lockability = "🎯 READY FOR STANDARD LOCK"
		} else {
			blockers := []string{}
			if !meetsDetectionCriteria {
				needed := si.minDetectionsForLock - boat.DetectionCount
				blockers = append(blockers, fmt.Sprintf("need %d more detections", needed))
			}
			if !meetsConfidenceCriteria {
				blockers = append(blockers, fmt.Sprintf("conf %.3f < 0.30", boat.Confidence))
			}
			lockability = fmt.Sprintf("🔓 BLOCKED: %v", blockers)
		}

		// Store analysis for debugging
		candidateAnalysis[boat.ID] = map[string]interface{}{
			"score":         score,
			"detections":    boat.DetectionCount,
			"confidence":    boat.Confidence,
			"lost_frames":   boat.LostFrames,
			"lockability":   lockability,
			"can_be_locked": canBeLocked,
		}

		si.debugMsg("TARGET_SELECTION", fmt.Sprintf("  %s: score=%.2f, detections=%d, conf=%.3f, lost=%d, %s",
			boat.ID, score, boat.DetectionCount, boat.Confidence, boat.LostFrames, lockability), boat.ID)

		// DUAL LOGGING: Individual boat analysis
		status := "building"
		if canBeLocked {
			status = "ready"
		}
		if boat.IsLocked {
			status = "locked"
		}

		si.logDebugMessage(fmt.Sprintf("• %s: score %.2f, %s", boat.ID, score, status),
			"BOAT_ANALYSIS", 0, map[string]interface{}{
				"boat_id":     boat.ID,
				"score":       score,
				"detections":  boat.DetectionCount,
				"confidence":  boat.Confidence,
				"lost_frames": boat.LostFrames,
				"lockable":    canBeLocked,
				"status":      status,
				"lockability": lockability,
			})

		if score > bestScore {
			bestScore = score
			bestBoat = boat
			si.debugMsg("TARGET_SELECTION", fmt.Sprintf("    ↑ NEW BEST CANDIDATE (score %.2f > %.2f)", score, bestScore-score), boat.ID)

			// DUAL LOGGING: New best candidate
			si.logDebugMessage(fmt.Sprintf("↑ NEW BEST: %s (%.2f)", boat.ID, score),
				"BEST_CANDIDATE", 1, map[string]interface{}{
					"boat_id":        boat.ID,
					"new_score":      score,
					"old_best_score": bestScore - score,
				})
		}
	}

	// Switch target if we found a better boat
	if bestBoat != nil && (si.targetBoat == nil || bestBoat.ID != si.targetBoat.ID) {
		oldTarget := "none"
		if si.targetBoat != nil {
			oldTarget = si.targetBoat.ID
		}

		si.targetBoat = bestBoat
		si.lastTargetSwitch = si.frameCount

		// DETAILED LOCK CRITERIA CHECK
		meetsDetectionCriteria := bestBoat.DetectionCount >= si.minDetectionsForLock
		meetsConfidenceCriteria := bestBoat.Confidence > 0.30

		si.debugMsg("LOCK_CHECK", fmt.Sprintf("🔍 Boat %s lock criteria: detections=%d≥%d?%v, confidence=%.3f>0.30?%v",
			bestBoat.ID, bestBoat.DetectionCount, si.minDetectionsForLock, meetsDetectionCriteria,
			bestBoat.Confidence, meetsConfidenceCriteria), bestBoat.ID)

		// FIXED: Only lock with mature targets (24+ detections + confidence), no early lock
		if meetsDetectionCriteria && meetsConfidenceCriteria {
			wasAlreadyLocked := si.targetBoat.IsLocked
			si.targetBoat.IsLocked = true

			if !wasAlreadyLocked {
				si.targetBoat.LockStrength = math.Min(1.0, si.targetBoat.LockStrength+0.1)
				si.debugMsg("LOCK_CAMERA", fmt.Sprintf("🔒 Target boat %s LOCKED for camera tracking (mature target for SUPER LOCK)!", si.targetBoat.ID), si.targetBoat.ID)
			} else {
				si.targetBoat.LockStrength = math.Min(1.0, si.targetBoat.LockStrength+0.05)
				si.debugMsg("LOCK_CAMERA", fmt.Sprintf("🔒 Target boat %s just became locked in camera tracking!", si.targetBoat.ID), si.targetBoat.ID)
			}
		} else {
			// LOCK BLOCKED - Explain why
			lockBlockers := []string{}
			if bestBoat.Confidence < 0.30 && bestBoat.DetectionCount < si.minDetectionsForLock {
				lockBlockers = append(lockBlockers, fmt.Sprintf("need %.2f confidence AND %d detections",
					0.30, si.minDetectionsForLock-bestBoat.DetectionCount))
			} else if !meetsDetectionCriteria {
				lockBlockers = append(lockBlockers, fmt.Sprintf("need %d more detections", si.minDetectionsForLock-bestBoat.DetectionCount))
			}
			if !meetsConfidenceCriteria {
				lockBlockers = append(lockBlockers, fmt.Sprintf("confidence %.3f too low", bestBoat.Confidence))
			}
			si.debugMsg("LOCK_BLOCKED", fmt.Sprintf("🔓 Boat NOT locked: %v", lockBlockers), bestBoat.ID)

			// DUAL LOGGING: Lock blocked explanation
			si.logDebugMessage(fmt.Sprintf("🔓 BLOCKED: %s - %v", bestBoat.ID, lockBlockers),
				"LOCK_BLOCKED", 1, map[string]interface{}{
					"boat_id":         bestBoat.ID,
					"detections":      bestBoat.DetectionCount,
					"min_detections":  si.minDetectionsForLock,
					"confidence":      bestBoat.Confidence,
					"min_confidence":  0.30,
					"early_threshold": "DISABLED",
					"blockers":        lockBlockers,
				})
		}

		si.debugMsg("TARGET_SWITCH", fmt.Sprintf("Changed target: %s → %s (score: %.2f, detections: %d, locked: %v, lost: %d)",
			oldTarget, bestBoat.ID, bestScore, bestBoat.DetectionCount, bestBoat.IsLocked, bestBoat.LostFrames))
	} else if si.targetBoat == nil && bestBoat == nil && len(si.allBoats) > 0 {
		// ANTI-SPAM FIX: Only print NO_TARGETS when we have boats but none are suitable
		// This avoids spam when in normal scanning mode with no boats
		si.debugMsg("NO_TARGETS", fmt.Sprintf("🔍 No valid boats available for targeting (%d boats detected but all unsuitable)", len(si.allBoats)))

		// Check if we need to record a lock loss for any remaining locked boats
		for _, boat := range si.allBoats {
			if boat.IsLocked && boat.LostFrames > 150 {
				// We have a locked boat that just exceeded the threshold
				si.lastLockLoss = time.Now()
				si.lastLockedPosition = boat.CurrentSpatial
				si.debugMsg("LOCK_LOSS", fmt.Sprintf("Lost locked boat %s (exceeded threshold) - starting holdover period (%.1fs)",
					boat.ID, si.postLockHoldover.Seconds()), boat.ID)
				break
			}
		}
	}
	// NOTE: When si.targetBoat == nil && bestBoat == nil && len(si.allBoats) == 0
	// We don't print anything - this is normal scanning mode with no boats detected
}

// calculateTargetingScore calculates how good a boat is as a tracking target
func (si *SpatialIntegration) calculateTargetingScore(boat *TrackedBoat) float64 {
	if boat == nil {
		return 0.0
	}

	// Base score from detections and confidence
	detectionScore := math.Min(float64(boat.DetectionCount)/20.0, 1.5) // INCREASED: Normalize to 0-1.5, cap at 20 detections
	confidenceScore := boat.Confidence

	// Bonus for being near center of frame
	centerX := float64(si.frameCenterX)
	centerY := float64(si.frameCenterY)
	deltaX := math.Abs(float64(boat.CurrentPixel.X) - centerX)
	deltaY := math.Abs(float64(boat.CurrentPixel.Y) - centerY)
	distanceFromCenter := math.Sqrt(deltaX*deltaX + deltaY*deltaY)
	maxDistance := math.Sqrt(centerX*centerX + centerY*centerY)
	centerScore := 1.0 - (distanceFromCenter / maxDistance)

	// Bonus for larger objects
	sizeScore := math.Min(boat.PixelArea/10000.0, 1.0)

	// Penalty for lost frames
	lostFramesPenalty := 1.0 - (float64(boat.LostFrames) / float64(si.maxLostFrames))

	// Bonus for being the current target (stability)
	stabilityBonus := 0.0
	if si.targetBoat != nil && si.targetBoat.ID == boat.ID {
		stabilityBonus = 0.2
	}

	// MASSIVE BONUS for P1 objects with P2 enhancements! 🚤👤
	enhancementBonus := 0.0
	if boat.HasP2Objects {
		enhancementBonus = 0.5 + (float64(boat.P2Count) * 0.2) // +0.5 base + 0.2 per enhancement object

		// Dynamic debug message based on P2 configuration
		enhancementType := "people"
		if si.p2TrackAll {
			enhancementType = "P2 objects"
		} else if len(si.p2TrackList) > 1 {
			enhancementType = fmt.Sprintf("P2 objects (%v)", si.p2TrackList)
		}

		si.debugMsg("P2_PRIORITY", fmt.Sprintf("🎯👤 %s %s gets +%.2f priority for having %d %s!",
			boat.Classification, boat.ID, enhancementBonus, boat.P2Count, enhancementType), boat.ID)
	}

	// Combine scores - REBALANCED: Favor large P1 objects (mega yachts) over small P1 objects with P2 enhancements
	totalScore := (detectionScore*0.15 + confidenceScore*0.15 + centerScore*0.10 + sizeScore*0.25 + stabilityBonus*0.15 + enhancementBonus*0.20) * lostFramesPenalty

	// Debug output to understand scoring decisions
	si.debugMsg("SCORE_DEBUG", fmt.Sprintf("%s %s: det=%.2f(%.0f), conf=%.2f, center=%.2f, size=%.2f, stable=%.2f, p2bonus=%.2f, penalty=%.2f → TOTAL=%.3f",
		boat.Classification, boat.ID, detectionScore, float64(boat.DetectionCount), confidenceScore, centerScore, sizeScore, stabilityBonus, enhancementBonus, lostFramesPenalty, totalScore), boat.ID)

	return totalScore
}

// isInPostLockHoldover checks if we're in the holdover period after losing a locked boat
func (si *SpatialIntegration) isInPostLockHoldover() bool {
	if si.lastLockLoss.IsZero() {
		return false
	}
	return time.Since(si.lastLockLoss) < si.postLockHoldover
}

// handlePostLockHoldover manages the holdover period after losing a locked boat
func (si *SpatialIntegration) handlePostLockHoldover() {
	timeRemaining := si.postLockHoldover - time.Since(si.lastLockLoss)

	// Disable scanning during holdover
	if si.spatialTracker.IsScanning() {
		si.debugMsg("HOLDOVER", fmt.Sprintf("Disabling scanning - lingering for %.1fs where locked boat was lost",
			timeRemaining.Seconds()))
		si.spatialTracker.SetScanningMode(false)
	}

	// Set the holdover position ONCE when we first enter holdover
	if !si.holdoverPositionSet {
		si.debugMsg("HOLDOVER", fmt.Sprintf("Setting camera to last lock position: Pan=%.1f, Tilt=%.1f, Zoom=%.1f",
			si.lastLockedPosition.Pan, si.lastLockedPosition.Tilt, si.lastLockedPosition.Zoom))

		// Move camera to where the locked boat was last seen
		roundedPan := math.Round(si.lastLockedPosition.Pan)
		roundedTilt := math.Round(si.lastLockedPosition.Tilt)
		roundedZoom := math.Round(si.lastLockedPosition.Zoom)

		cmd := ptz.PTZCommand{
			Command:      "absolutePosition",
			Reason:       "Post-lock holdover - set position once",
			Duration:     500 * time.Millisecond,
			AbsolutePan:  &roundedPan,
			AbsoluteTilt: &roundedTilt,
			AbsoluteZoom: &roundedZoom,
		}

		// Use camera state manager if available, otherwise fall back to direct control
		if si.cameraStateManager != nil {
			if si.cameraStateManager.SendCommand(cmd) {
				si.debugMsg("HOLDOVER", "Position set successfully - now waiting passively")
				si.holdoverPositionSet = true
			} else {
				si.debugMsg("HOLDOVER", "Position command rejected - camera busy, will retry")
			}
		} else {
			// Fallback to direct PTZ control
			si.ptzCtrl.SendCommand(cmd)
			si.holdoverPositionSet = true
		}
	} else {
		// Just wait passively - no drift detection, no corrections, no monitoring
		// The camera will stay where we put it. If there are small variations in
		// reported position, that's normal hardware behavior, not something to fix.

		si.debugMsg("HOLDOVER", fmt.Sprintf("Waiting passively at last lock position (%.1fs remaining)",
			timeRemaining.Seconds()))
	}

	// Clear holdover when it expires
	if timeRemaining <= 0 {
		si.debugMsg("HOLDOVER", "Holdover period expired - resuming normal operation")
		si.lastLockLoss = time.Time{}  // Clear the timestamp
		si.holdoverPositionSet = false // Reset for next time
	}
}

// handleTargetLoss handles when no suitable target boat is available
func (si *SpatialIntegration) handleTargetLoss() {
	if si.targetBoat != nil {
		si.debugMsg("TARGET_LOSS", fmt.Sprintf("Lost target boat %s, scanning for new targets (total boats: %d)",
			si.targetBoat.ID, len(si.allBoats)), si.targetBoat.ID)
		si.targetBoat = nil
	}
}

// updateCameraTracking moves the camera to track the current target boat
func (si *SpatialIntegration) updateCameraTracking() {
	if si.targetBoat == nil {
		return
	}

	// SAFETY CHECK: Ensure target boat still exists in allBoats
	// This prevents tracking "ghost boats" that were removed but targetBoat wasn't cleared
	if _, exists := si.allBoats[si.targetBoat.ID]; !exists {
		si.debugMsg("DANGLING_TARGET", fmt.Sprintf("🚨 Target boat %s no longer exists in tracking pool - clearing target", si.targetBoat.ID), si.targetBoat.ID)
		si.targetBoat = nil
		return
	}

	// Check if boat is locked for tracking (SECONDARY LOCK CHECK in camera tracking)
	meetsDetectionCriteria := si.targetBoat.DetectionCount >= si.minDetectionsForLock
	meetsConfidenceCriteria := si.targetBoat.Confidence > 0.30

	// DISABLED: Early lock feature that allowed camera movement with only 1 detection + high confidence
	// This was causing premature camera movement in Mode 1, preventing targets from maturing
	// earlyLockThreshold := 0.80 // Same threshold as in selectTargetBoat
	// meetsEarlyLockCriteria := si.targetBoat.Confidence >= earlyLockThreshold && si.targetBoat.DetectionCount >= 1
	meetsEarlyLockCriteria := false // DISABLED: No early lock - camera only moves for mature targets (24+ detections)

	si.debugMsg("CAMERA_LOCK_CHECK", fmt.Sprintf("🔍 Target boat %s: detections=%d≥%d?%v, confidence=%.3f>0.30?%v, earlyLock=DISABLED, currentlyLocked=%v",
		si.targetBoat.ID, si.targetBoat.DetectionCount, si.minDetectionsForLock, meetsDetectionCriteria,
		si.targetBoat.Confidence, meetsConfidenceCriteria, si.targetBoat.IsLocked), si.targetBoat.ID)

	// DUAL LOGGING: Camera lock check analysis
	si.logDebugMessage(fmt.Sprintf("🔍 %s: %d det, %.3f conf, locked=%v",
		si.targetBoat.ID, si.targetBoat.DetectionCount, si.targetBoat.Confidence, si.targetBoat.IsLocked),
		"CAMERA_LOCK_CHECK", 1, map[string]interface{}{
			"boat_id":                   si.targetBoat.ID,
			"detections":                si.targetBoat.DetectionCount,
			"min_detections":            si.minDetectionsForLock,
			"meets_detection_criteria":  meetsDetectionCriteria,
			"confidence":                si.targetBoat.Confidence,
			"meets_confidence_criteria": meetsConfidenceCriteria,
			"early_lock_disabled":       true,
			"meets_early_lock":          meetsEarlyLockCriteria,
			"currently_locked":          si.targetBoat.IsLocked,
		})

	// FIXED: Only lock with mature targets (24+ detections + confidence), no early lock
	if meetsDetectionCriteria && meetsConfidenceCriteria {
		wasAlreadyLocked := si.targetBoat.IsLocked
		si.targetBoat.IsLocked = true

		if !wasAlreadyLocked {
			si.targetBoat.LockStrength = math.Min(1.0, si.targetBoat.LockStrength+0.1)
			si.debugMsg("LOCK_CAMERA", "🔒 Target LOCKED for camera tracking (mature target for SUPER LOCK)!", si.targetBoat.ID)
		} else {
			si.targetBoat.LockStrength = math.Min(1.0, si.targetBoat.LockStrength+0.05)
			si.debugMsg("LOCK_CAMERA", "🔒 Target just became locked in camera tracking!", si.targetBoat.ID)
		}
	} else {
		si.debugMsg("CAMERA_LOCK_CHECK", fmt.Sprintf("🔓 Target boat %s does not meet lock criteria yet (need %.2f confidence OR %d detections)",
			si.targetBoat.ID, 0.30, si.minDetectionsForLock), si.targetBoat.ID)
	}

	// ONLY do predictive tracking if camera is IDLE and boat is locked
	if si.targetBoat.IsLocked {
		// Continue tracking even during movement - rate limiting prevents command flooding

		// SIMPLIFIED PREDICTIVE TRACKING: Don't move camera for lost boats that are off-center
		if si.targetBoat.LostFrames > 0 && si.targetBoat.LostFrames <= 60 { // Reduced to 2 seconds
			// CRITICAL CHECK: Don't track to off-center positions
			offsetX := si.targetBoat.CurrentPixel.X - si.frameCenterX
			offsetY := si.targetBoat.CurrentPixel.Y - si.frameCenterY
			distanceFromCenter := math.Sqrt(float64(offsetX*offsetX + offsetY*offsetY))
			maxReasonableDistance := float64(si.frameWidth) * 0.2 // Max 20% of frame width from center for camera movement

			if distanceFromCenter > maxReasonableDistance {
				si.debugMsg("PREDICTIVE_SKIP", fmt.Sprintf("⏸️ Boat %s lost position (%d,%d) is %.0f pixels from center (max: %.0f) - NOT moving camera",
					si.targetBoat.ID, si.targetBoat.CurrentPixel.X, si.targetBoat.CurrentPixel.Y, distanceFromCenter, maxReasonableDistance), si.targetBoat.ID)
				return // Don't move camera to off-center positions
			}

			// Only do simple prediction if position is reasonable and close to center
			timeSinceLoss := float64(si.targetBoat.LostFrames) / 30.0 // Convert frames to seconds at 30fps
			si.debugMsg("PREDICTIVE_TRACK", fmt.Sprintf("🔮 Boat lost for %.1fs at reasonable position (%d,%d) - maintaining camera position",
				timeSinceLoss, si.targetBoat.CurrentPixel.X, si.targetBoat.CurrentPixel.Y), si.targetBoat.ID)

			// DON'T update spatial position for lost boats - just keep camera where it was
			return
		}

		// Check if boat has invalid spatial position - will be fixed when camera becomes IDLE
		if si.targetBoat.CurrentSpatial.Pan == 0.0 && si.targetBoat.CurrentSpatial.Tilt == 0.0 && si.targetBoat.CurrentSpatial.Zoom == 0.0 {
			si.debugMsg("CAMERA_TRACK", fmt.Sprintf("⚠️ Locked boat %s has invalid spatial position - will be recalculated when camera becomes IDLE", si.targetBoat.ID), si.targetBoat.ID)
			return
		}

		// === NEW PTZ-NATIVE SMART TRACKING SYSTEM ===
		// Replace reactive center-checking with proactive PTZ field-of-view prediction
		si.smartPTZTracking()
	} else {
		si.debugMsg("CAMERA_TRACK", fmt.Sprintf("🔓 Boat not locked yet (%d/%d detections) - using basic centering only",
			si.targetBoat.DetectionCount, si.minDetectionsForLock), si.targetBoat.ID)
	}
}

// shouldSendCommand checks if a command is different enough from the last sent command to warrant sending
func (si *SpatialIntegration) shouldSendCommand(pan, tilt, zoom float64) bool {
	threshold := 0.5 // Minimum change required to send command

	// Check if any value has changed significantly
	if math.Abs(pan-si.lastSentPan) >= threshold ||
		math.Abs(tilt-si.lastSentTilt) >= threshold ||
		math.Abs(zoom-si.lastSentZoom) >= threshold {

		// Update last sent values
		si.lastSentPan = pan
		si.lastSentTilt = tilt
		si.lastSentZoom = zoom
		return true
	}

	return false // Skip identical/tiny changes
}

// updateBoatTracking - DEPRECATED: This function is no longer used with multi-object tracking
func (si *SpatialIntegration) updateBoatTracking(newDetection *TrackedBoat) {
	// This function is deprecated - all tracking is now handled by the multi-object system
	si.debugMsg("DEPRECATED", "updateBoatTracking called - this should not happen with new system")
}

// calculatePixelVelocity - DEPRECATED: Legacy function, velocity now calculated in updateExistingBoat
func (si *SpatialIntegration) calculatePixelVelocity() {
	// This function is deprecated - velocity calculation is now handled in updateExistingBoat
}

// updateSpatialPosition - DEPRECATED: Now handled in updateBoatSpatialPosition
func (si *SpatialIntegration) updateSpatialPosition() {
	// Deprecated - spatial position updates are now handled in the multi-object system
}

// handleBoatLoss - DEPRECATED: Now handled by handleTargetLoss
func (si *SpatialIntegration) handleBoatLoss() {
	si.handleTargetLoss()
}

// GetTrackingMode returns current tracking mode for status display
func (si *SpatialIntegration) GetTrackingMode() string {
	si.mu.RLock()
	defer si.mu.RUnlock()

	if si.targetBoat == nil {
		return "Scanning for boats"
	}

	if si.targetBoat.IsLocked {
		return fmt.Sprintf("Locked on %s (ID: %s)", si.targetBoat.Classification, si.targetBoat.ID)
	} else {
		return fmt.Sprintf("Tracking %s (ID: %s)", si.targetBoat.Classification, si.targetBoat.ID)
	}
}

// GetTrackedObjects returns boats in legacy format for overlay compatibility
func (si *SpatialIntegration) GetTrackedObjects() map[int]*TrackedObject {
	si.mu.RLock()
	defer si.mu.RUnlock()

	// Always return full tracking data - no restrictions during movement
	objects := make(map[int]*TrackedObject)

	i := 0
	for _, boat := range si.allBoats {
		// Create TrackedObject for overlay with actual YOLO sizes
		objects[i] = &TrackedObject{
			ID:             i,       // Use int ID for compatibility
			ObjectID:       boat.ID, // Unified object ID format: 20240125-12-30.001
			CenterX:        boat.CurrentPixel.X,
			CenterY:        boat.CurrentPixel.Y,
			Width:          boat.BoundingBox.Dx(), // Real YOLO width
			Height:         boat.BoundingBox.Dy(), // Real YOLO height
			Area:           boat.PixelArea,        // Real YOLO area
			LastSeen:       boat.LastSeen,
			TrackedFrames:  boat.DetectionCount,
			LostFrames:     boat.LostFrames,
			ClassName:      boat.Classification,
			Confidence:     boat.Confidence,
			DetectionCount: boat.DetectionCount,
		}
		i++
	}

	return objects
}

// GetLastPTZCommand returns current tracking status
func (si *SpatialIntegration) GetLastPTZCommand() string {
	si.mu.RLock()
	defer si.mu.RUnlock()

	if si.targetBoat == nil {
		return "Scanning"
	}

	return fmt.Sprintf("Tracking %s at (%.1f,%.1f,%.1f)",
		si.targetBoat.Classification,
		si.targetBoat.CurrentSpatial.Pan,
		si.targetBoat.CurrentSpatial.Tilt,
		si.targetBoat.CurrentSpatial.Zoom)
}

// GetCurrentMode returns tracking mode enum for compatibility
func (si *SpatialIntegration) GetCurrentMode() TrackingMode {
	si.mu.RLock()
	defer si.mu.RUnlock()

	if si.targetBoat == nil {
		return ModeScanning
	}
	// CRITICAL FIX: If we have a target boat, we're tracking it (locked or not)
	return ModeTracking
}

// GetModeHandler returns nil - we don't use legacy mode handler anymore
func (si *SpatialIntegration) GetModeHandler() *ModeHandler {
	return nil // We don't use the legacy mode handler anymore
}

// GetTotalDetectedObjects returns the session-wide counter of detected objects
func (si *SpatialIntegration) GetTotalDetectedObjects() int64 {
	si.mu.RLock()
	defer si.mu.RUnlock()
	return si.totalDetectedObjectsCounter
}

// GetDetailedTrackingMode returns specific tracking phase information
func (si *SpatialIntegration) GetDetailedTrackingMode() string {
	si.mu.RLock()
	defer si.mu.RUnlock()

	// Check for RECOVERY mode first
	if si.isInRecovery && si.recoveryData != nil {
		return si.recoveryData.CurrentPhase.String()
	}

	if si.targetBoat == nil {
		return "SCANNING"
	}

	if si.targetBoat.IsLocked {
		if si.targetBoat.DetectionCount >= 24 {
			// Check if this is SUPER LOCK with P2-based targeting
			if si.targetBoat.UseP2Target {
				return "SUPER_LOCK_P2"
			}
			// Check if this is SUPER LOCK with people for even more detail
			if si.targetBoat.HasP2Objects || (!si.targetBoat.LastP2Seen.IsZero() && time.Since(si.targetBoat.LastP2Seen) <= 3*time.Second) {
				return "SUPER LOCK + PEOPLE"
			}
			return "SUPER LOCK"
		}
		// Check if this is regular LOCK with P2-based targeting
		if si.targetBoat.UseP2Target {
			return "LOCK_P2"
		}
		// Check if this is regular LOCK with people
		if si.targetBoat.HasP2Objects {
			return "LOCK + PEOPLE"
		}
		return "LOCK"
	}

	// Not locked yet - show building progress
	detectionProgress := fmt.Sprintf("TRACKING PHASE 1 (%d/%d)", si.targetBoat.DetectionCount, si.minDetectionsForLock)
	return detectionProgress
}

// GetCurrentTrackedObject returns the clean objectID of the object being tracked or empty string
func (si *SpatialIntegration) GetCurrentTrackedObject() string {
	si.mu.RLock()
	defer si.mu.RUnlock()

	if si.targetBoat == nil {
		return ""
	}

	return si.targetBoat.ID // CLEAN objectID, no display text pollution
}

// GetCurrentTrackedObjectDisplay returns objectID with status indicators for UI display
func (si *SpatialIntegration) GetCurrentTrackedObjectDisplay() string {
	si.mu.RLock()
	defer si.mu.RUnlock()

	if si.targetBoat == nil {
		return "none"
	}

	// Show object ID with additional status indicators for display
	status := ""
	if si.targetBoat.LostFrames > 0 {
		status = fmt.Sprintf(" (lost:%d)", si.targetBoat.LostFrames)
	} else if si.targetBoat.HasP2Objects {
		status = fmt.Sprintf(" (+people:%d)", si.targetBoat.P2Count)
	}

	return si.targetBoat.ID + status
}

// GetTrackingInfo returns pixel tracking data for overlay visualization
func (si *SpatialIntegration) GetTrackingInfo() ([]DetectionPoint, []DetectionPoint, float64, float64) {
	if si.targetBoat == nil || len(si.pixelTrackingHistory) == 0 {
		return nil, nil, 0, 0
	}

	// Convert pixel history to detection points
	var detectionHistory []DetectionPoint
	for i, pixel := range si.pixelTrackingHistory {
		detectionHistory = append(detectionHistory, DetectionPoint{
			Position: pixel,
			Time:     si.targetBoat.FirstDetected.Add(time.Duration(i) * time.Second),
			Area:     1500, // Placeholder
		})
	}

	// Don't generate predictive tracks if camera is moving - predictions would be invalid
	var futureTrack []DetectionPoint
	if si.cameraStateManager != nil && !si.cameraStateManager.IsIdle() {
		si.debugMsg("PREDICTION_TRACK", "⏸️ Skipping prediction generation - camera is MOVING")
		return detectionHistory, futureTrack, si.targetBoat.PixelVelocity.X, si.targetBoat.PixelVelocity.Y
	}

	// Create future prediction based on lock status - ONLY aggressive prediction for locked boats
	if si.targetBoat.PixelVelocity.X != 0 || si.targetBoat.PixelVelocity.Y != 0 {
		currentPixel := si.targetBoat.CurrentPixel

		// Different prediction strategies based on lock status
		var predictionDuration float64
		var predictionInterval float64
		var predictionLabel string

		if si.targetBoat.IsLocked {
			// LOCKED BOAT: Aggressive 5-second prediction with fine granularity
			predictionDuration = 5.0  // 5 seconds total prediction
			predictionInterval = 0.25 // 250ms intervals (20 points)
			predictionLabel = "LOCKED"
		} else {
			// UNLOCKED BOAT: Conservative 1.5-second prediction with coarser granularity
			predictionDuration = 1.5 // 1.5 seconds total prediction
			predictionInterval = 0.5 // 500ms intervals (3 points)
			predictionLabel = "TRACKING"
		}

		numPoints := int(predictionDuration / predictionInterval)

		for i := 1; i <= numPoints; i++ {
			timeAhead := float64(i) * predictionInterval // Time ahead in seconds

			// Calculate predicted position using current velocity
			predictedX := currentPixel.X + int(si.targetBoat.PixelVelocity.X*timeAhead)
			predictedY := currentPixel.Y + int(si.targetBoat.PixelVelocity.Y*timeAhead)

			// Ensure predictions stay within reasonable frame bounds
			predictedX = int(math.Max(0, math.Min(float64(si.frameWidth), float64(predictedX))))
			predictedY = int(math.Max(0, math.Min(float64(si.frameHeight), float64(predictedY))))

			futureTrack = append(futureTrack, DetectionPoint{
				Position: image.Point{X: predictedX, Y: predictedY},
				Time:     time.Now().Add(time.Duration(timeAhead*1000) * time.Millisecond),
				Area:     1500,
			})
		}

		si.debugMsg("PREDICTION_TRACK", fmt.Sprintf("%s boat %s - generated %d prediction points over %.1fs",
			predictionLabel, si.targetBoat.ID, numPoints, predictionDuration), si.targetBoat.ID)
	}

	return detectionHistory, futureTrack, si.targetBoat.PixelVelocity.X, si.targetBoat.PixelVelocity.Y
}

// GetPredictedMovement returns pixel velocity for compatibility
func (si *SpatialIntegration) GetPredictedMovement() (float64, float64) {
	if si.targetBoat == nil {
		return 0, 0
	}
	return si.targetBoat.PixelVelocity.X, si.targetBoat.PixelVelocity.Y
}

// GetLockedTargetVelocity returns velocity data for the locked target (for overlay display)
func (si *SpatialIntegration) GetLockedTargetVelocity() (hasVelocity bool, velX, velY float64) {
	si.mu.RLock()
	defer si.mu.RUnlock()

	if si.targetBoat == nil || !si.targetBoat.IsLocked {
		return false, 0, 0
	}

	return true, si.targetBoat.PixelVelocity.X, si.targetBoat.PixelVelocity.Y
}

// GetTrackingDecision returns tracking decision for debug overlay
func (si *SpatialIntegration) GetTrackingDecision() *TrackingDecision {
	if si.targetBoat == nil {
		return nil
	}

	// ULTRA SIMPLE APPROACH: Always point camera where boat IS (like July 6th version)
	targetX := si.targetBoat.CurrentPixel.X
	targetY := si.targetBoat.CurrentPixel.Y

	var predictionDescription string
	if si.targetBoat.LostFrames > 0 {
		predictionDescription = fmt.Sprintf("LOST %d frames - tracking last known position", si.targetBoat.LostFrames)
	} else {
		predictionDescription = "VISIBLE - direct tracking"
	}

	// Create debug information
	logic := []string{
		fmt.Sprintf("Boat: %s (conf: %.2f)", si.targetBoat.Classification, si.targetBoat.Confidence),
	}

	// Clear lock status display
	if si.targetBoat.IsLocked {
		logic = append(logic, fmt.Sprintf("STATUS: LOCKED after %d detections (%.0f%% strength)", si.targetBoat.DetectionCount, si.targetBoat.LockStrength*100))
	} else {
		logic = append(logic, fmt.Sprintf("STATUS: Building lock (%d/%d detections needed)", si.targetBoat.DetectionCount, si.minDetectionsForLock))
	}

	logic = append(logic,
		fmt.Sprintf("Pixel pos: (%d,%d)", si.targetBoat.CurrentPixel.X, si.targetBoat.CurrentPixel.Y),
		fmt.Sprintf("Pixel vel: (%.1f,%.1f) px/s", si.targetBoat.PixelVelocity.X, si.targetBoat.PixelVelocity.Y),
		fmt.Sprintf("Spatial: Pan=%.1f Tilt=%.1f", si.targetBoat.CurrentSpatial.Pan, si.targetBoat.CurrentSpatial.Tilt),
		fmt.Sprintf("Target: (%d,%d)", targetX, targetY),
		predictionDescription,
	)

	// Add camera state information
	if si.cameraStateManager != nil {
		if si.cameraStateManager.IsIdle() {
			logic = append(logic, "Camera: IDLE")
		} else {
			logic = append(logic, "Camera: MOVING")
		}
	}

	// Calculate distance from center
	distanceFromCenter := math.Sqrt(
		math.Pow(float64(si.targetBoat.CurrentPixel.X-si.frameCenterX), 2)+
			math.Pow(float64(si.targetBoat.CurrentPixel.Y-si.frameCenterY), 2),
	) / math.Sqrt(math.Pow(float64(si.frameWidth), 2)+math.Pow(float64(si.frameHeight), 2))

	return &TrackingDecision{
		CurrentPosition:    si.targetBoat.CurrentPixel,
		TargetPosition:     image.Point{X: targetX, Y: targetY},
		Command:            fmt.Sprintf("Track %s", si.targetBoat.Classification),
		PanAdjustment:      si.targetBoat.PixelVelocity.X / 10.0, // Convert for display
		TiltAdjustment:     si.targetBoat.PixelVelocity.Y / 10.0,
		ZoomLevel:          si.targetBoat.CurrentSpatial.Zoom,
		Logic:              logic,
		DistanceFromCenter: distanceFromCenter,
		TrackingEffort:     si.targetBoat.LockStrength,
		Confidence:         si.targetBoat.Confidence,
	}
}

// detectAndCleanupCameraMovement clears tracking data when camera moves significantly
func (si *SpatialIntegration) detectAndCleanupCameraMovement() {
	// CRITICAL FIX: Get actual camera position, not cached data
	actualPos := si.ptzCtrl.GetCurrentPosition()
	currentPos := SpatialCoordinate{
		Pan:  actualPos.Pan,
		Tilt: actualPos.Tilt,
		Zoom: actualPos.Zoom,
	}
	now := time.Now()

	// Skip check if this is the first update
	if si.lastPositionUpdate.IsZero() {
		si.lastCameraPosition = currentPos
		si.lastPositionUpdate = now
		return
	}

	// Only check every 500ms
	if now.Sub(si.lastPositionUpdate) < 500*time.Millisecond {
		return
	}

	// Calculate movement
	panMovement := math.Abs(currentPos.Pan - si.lastCameraPosition.Pan)
	tiltMovement := math.Abs(currentPos.Tilt - si.lastCameraPosition.Tilt)
	zoomMovement := math.Abs(currentPos.Zoom - si.lastCameraPosition.Zoom)

	// Define thresholds for significant movement
	significantMovement := panMovement > 30.0 || tiltMovement > 20.0 || zoomMovement > 15.0

	if significantMovement {
		si.debugMsg("CLEAN_MOVEMENT", "Camera moved significantly - selectively clearing tracking history")
		si.debugMsg("CLEAN_MOVEMENT", fmt.Sprintf("Movement: Pan=%.1f, Tilt=%.1f, Zoom=%.1f",
			panMovement, tiltMovement, zoomMovement))

		// Clear pixel tracking history for overlay
		si.pixelTrackingHistory = nil

		// SELECTIVE CLEARING: Only clear for MAJOR movements, preserve velocity for tracking movements
		majorMovementThreshold := struct {
			Pan  float64
			Tilt float64
			Zoom float64
		}{100.0, 50.0, 30.0} // Higher thresholds for velocity preservation

		isMajorMovement := panMovement > majorMovementThreshold.Pan ||
			tiltMovement > majorMovementThreshold.Tilt ||
			zoomMovement > majorMovementThreshold.Zoom

		for _, boat := range si.allBoats {
			if isMajorMovement {
				// Major movement - clear everything
				boat.PixelHistory = nil
				boat.SpatialHistory = nil
				boat.PixelVelocity.X = 0
				boat.PixelVelocity.Y = 0
				boat.SpatialVelocity.Pan = 0
				boat.SpatialVelocity.Tilt = 0
				si.debugMsg("CLEAN_MOVEMENT", fmt.Sprintf("🧹 MAJOR movement - cleared all history for boat %s", boat.ID))
			} else {
				// Minor tracking movement - preserve velocity, limit history
				if len(boat.PixelHistory) > 3 {
					boat.PixelHistory = boat.PixelHistory[len(boat.PixelHistory)-3:] // Keep last 3 points
				}
				if len(boat.SpatialHistory) > 3 {
					boat.SpatialHistory = boat.SpatialHistory[len(boat.SpatialHistory)-3:] // Keep last 3 points
				}
				// PRESERVE VELOCITY for predictive tracking
				si.debugMsg("CLEAN_MOVEMENT", fmt.Sprintf("🔄 MINOR movement - preserved velocity (%.1f,%.1f) for boat %s",
					boat.PixelVelocity.X, boat.PixelVelocity.Y, boat.ID))
			}

			// Reset lost frames (camera moved, not boat lost)
			boat.LostFrames = 0
		}

		// Handle target boat similarly
		if si.targetBoat != nil {
			if isMajorMovement {
				si.targetBoat.PixelHistory = nil
				si.targetBoat.SpatialHistory = nil
				si.targetBoat.PixelVelocity.X = 0
				si.targetBoat.PixelVelocity.Y = 0
				si.targetBoat.SpatialVelocity.Pan = 0
				si.targetBoat.SpatialVelocity.Tilt = 0
				si.debugMsg("CLEAN_MOVEMENT", fmt.Sprintf("🧹 MAJOR movement - cleared target boat %s history", si.targetBoat.ID))
			} else {
				si.debugMsg("CLEAN_MOVEMENT", fmt.Sprintf("🔄 MINOR movement - preserved target boat %s velocity (%.1f,%.1f)",
					si.targetBoat.ID, si.targetBoat.PixelVelocity.X, si.targetBoat.PixelVelocity.Y))
			}
		}

		// Track when we cleared history for adaptive boat matching
		si.lastHistoryClear = time.Now()

		// Reset velocity calculation position tracking after history clearing
		si.lastVelocityCalcTime = time.Time{}
		si.debugMsg("CLEAN_MOVEMENT", "🔄 Reset velocity calculation tracking after history clearing")

		si.debugMsg("CLEAN_MOVEMENT", "✅ Selective tracking history cleanup completed")
	}

	si.lastCameraPosition = currentPos
	si.lastPositionUpdate = now
}

// ClearTrackingHistory immediately clears overlay history but preserves velocity - called when camera starts moving
func (si *SpatialIntegration) ClearTrackingHistory() {
	si.mu.Lock()
	defer si.mu.Unlock()

	si.debugMsg("OVERLAY_CLEAR", "🧹 Clearing overlay history while preserving velocity data (camera started moving)")

	// Clear pixel tracking history for clean overlay
	si.pixelTrackingHistory = nil

	// SELECTIVE CLEARING: Clear overlay history but preserve velocity for predictive tracking
	for _, boat := range si.allBoats {
		// Clear history for clean overlay but keep some for velocity calculation
		if len(boat.PixelHistory) > 2 {
			boat.PixelHistory = boat.PixelHistory[len(boat.PixelHistory)-2:] // Keep last 2 points for velocity
		}
		if len(boat.SpatialHistory) > 2 {
			boat.SpatialHistory = boat.SpatialHistory[len(boat.SpatialHistory)-2:] // Keep last 2 points
		}

		// PRESERVE VELOCITIES - they're still valid with camera compensation
		si.debugMsg("OVERLAY_CLEAR", fmt.Sprintf("🔄 Preserved velocity (%.1f,%.1f) for boat %s during camera movement",
			boat.PixelVelocity.X, boat.PixelVelocity.Y, boat.ID))

		// Reset lost frames (camera moved, not boat lost)
		boat.LostFrames = 0
	}

	// Handle target boat similarly
	if si.targetBoat != nil {
		if len(si.targetBoat.PixelHistory) > 2 {
			si.targetBoat.PixelHistory = si.targetBoat.PixelHistory[len(si.targetBoat.PixelHistory)-2:]
		}
		if len(si.targetBoat.SpatialHistory) > 2 {
			si.targetBoat.SpatialHistory = si.targetBoat.SpatialHistory[len(si.targetBoat.SpatialHistory)-2:]
		}
		// PRESERVE target boat velocity
		si.debugMsg("OVERLAY_CLEAR", fmt.Sprintf("🔄 Preserved target boat %s velocity (%.1f,%.1f)",
			si.targetBoat.ID, si.targetBoat.PixelVelocity.X, si.targetBoat.PixelVelocity.Y))
	}

	// Track when we cleared history for adaptive boat matching
	si.lastHistoryClear = time.Now()

	// Reset velocity calculation position tracking for camera movement
	si.lastVelocityCalcTime = time.Time{}
	si.debugMsg("OVERLAY_CLEAR", "🔄 Reset velocity calculation tracking for camera movement")

	si.debugMsg("OVERLAY_CLEAR", "✅ Overlay history cleared, velocity data preserved for predictive tracking")
}

// GetSpatialTrackingInfo provides current zoom data for calibration calculations
func (si *SpatialIntegration) GetSpatialTrackingInfo() *SpatialTrackingInfo {
	// Get data from spatial tracker
	isScanning := si.spatialTracker.IsScanning()
	currentPos := si.spatialTracker.GetCurrentSpatialPosition() // This now always syncs with actual camera
	scanPattern := si.spatialTracker.GetScanPattern()
	trackedObjects := si.spatialTracker.GetTrackedObjects()
	lockedObject := si.spatialTracker.GetLockedObject()

	return &SpatialTrackingInfo{
		IsScanning:      isScanning,
		CurrentPosition: currentPos,
		ScanPattern:     scanPattern,
		TrackedObjects:  trackedObjects,
		LockedObject:    lockedObject,
		TotalObjects:    len(trackedObjects),
	}
}

// GetPTZController returns the PTZ controller for external access
func (si *SpatialIntegration) GetPTZController() ptz.Controller {
	return si.ptzCtrl
}

// SetCameraStateManager sets the camera state manager for coordinated camera control
func (si *SpatialIntegration) SetCameraStateManager(stateManager *ptz.CameraStateManager) {
	si.mu.Lock()
	defer si.mu.Unlock()

	si.cameraStateManager = stateManager
	// Also set it on the spatial tracker for coordinated scanning
	if si.spatialTracker != nil {
		si.spatialTracker.SetCameraStateManager(stateManager)
	}
	si.debugMsg("SPATIAL_INTEGRATION", "Camera state manager integrated - commands will be coordinated")
}

// GetCameraStateManager returns the camera state manager
func (si *SpatialIntegration) GetCameraStateManager() *ptz.CameraStateManager {
	si.mu.RLock()
	defer si.mu.RUnlock()

	return si.cameraStateManager
}

// RecalculateSpatialPositions recalculates spatial positions for all boats when camera becomes IDLE
func (si *SpatialIntegration) RecalculateSpatialPositions() {
	si.mu.Lock()
	defer si.mu.Unlock()

	si.debugMsg("SPATIAL_RECALC", "🔄 Recalculating spatial positions for ALL boats after camera movement")

	recalculated := 0
	for _, boat := range si.allBoats {
		// CRITICAL FIX: Recalculate for ALL boats, including those in predictive tracking mode
		// This is essential for boats that are being kept alive by predictive tracking
		oldSpatial := boat.CurrentSpatial

		// Special handling for boats in predictive tracking
		if boat.LostFrames > 10 {
			si.debugMsg("SPATIAL_RECALC", fmt.Sprintf("🔮 Recalculating for boat %s in predictive tracking mode (%d frames lost)",
				boat.ID, boat.LostFrames), boat.ID)
		}

		si.updateBoatSpatialPosition(boat)

		si.debugMsg("SPATIAL_RECALC", fmt.Sprintf("🔧 Boat %s: (%.1f,%.1f,%.1f) → (%.1f,%.1f,%.1f) [%d frames lost]",
			boat.ID, oldSpatial.Pan, oldSpatial.Tilt, oldSpatial.Zoom,
			boat.CurrentSpatial.Pan, boat.CurrentSpatial.Tilt, boat.CurrentSpatial.Zoom, boat.LostFrames), boat.ID)
		recalculated++
	}

	si.debugMsg("SPATIAL_RECALC", fmt.Sprintf("✅ Recalculated spatial positions for %d boats", recalculated))
}

// GetLockedTargetForPIP returns locked targets with P2 objects for PIP (SUPER LOCK 24+ ONLY)
func (si *SpatialIntegration) GetLockedTargetForPIP() *TrackedObject {
	si.mu.RLock()
	defer si.mu.RUnlock()

	// Only return target if it's SUPER LOCK (24+) AND has people detected OR recently had people
	if si.targetBoat != nil && si.targetBoat.IsLocked && si.targetBoat.DetectionCount >= 24 {
		// Check if we currently have people OR had people recently (for linger)
		recentlyHadPeople := si.targetBoat.HasP2Objects ||
			(!si.targetBoat.LastP2Seen.IsZero() && time.Since(si.targetBoat.LastP2Seen) <= 3*time.Second)

		if recentlyHadPeople {
			// Allow up to 5 lost frames for SUPER LOCK (more stable than regular LOCK)
			if si.targetBoat.LostFrames <= 5 {
				personStatus := "CURRENT"
				if !si.targetBoat.HasP2Objects {
					timeSince := time.Since(si.targetBoat.LastP2Seen)
					personStatus = fmt.Sprintf("RECENT(%.1fs ago)", timeSince.Seconds())
				}

				// SUPER LOCK PIP: Use P2 centroid for precise person targeting when available
				var pipCenterX, pipCenterY int
				if si.targetBoat.UseP2Target {
					pipCenterX = si.targetBoat.P2Centroid.X
					pipCenterY = si.targetBoat.P2Centroid.Y
					si.debugMsg("PIP_SUPER_LOCK", fmt.Sprintf("👤💎📺 Using SUPER LOCK P2 centroid %s (det:%d, lost:%d) at (%d,%d) for PIP - %d people",
						personStatus, si.targetBoat.DetectionCount, si.targetBoat.LostFrames, pipCenterX, pipCenterY, si.targetBoat.P2Count), si.targetBoat.ID)
				} else {
					pipCenterX = si.targetBoat.CurrentPixel.X
					pipCenterY = si.targetBoat.CurrentPixel.Y
					si.debugMsg("PIP_SUPER_LOCK", fmt.Sprintf("👤💎📺 Using SUPER LOCK boat center %s (det:%d, lost:%d) at (%d,%d) for PIP",
						personStatus, si.targetBoat.DetectionCount, si.targetBoat.LostFrames, pipCenterX, pipCenterY), si.targetBoat.ID)
				}

				return &TrackedObject{
					ID:             0,                // PIP uses single target
					ObjectID:       si.targetBoat.ID, // CRITICAL: Include clean objectID for file naming
					CenterX:        pipCenterX,       // P2 centroid or boat center
					CenterY:        pipCenterY,       // P2 centroid or boat center
					Width:          si.targetBoat.BoundingBox.Dx(),
					Height:         si.targetBoat.BoundingBox.Dy(),
					Area:           si.targetBoat.PixelArea,
					LastSeen:       si.targetBoat.LastSeen,
					TrackedFrames:  si.targetBoat.DetectionCount,
					LostFrames:     si.targetBoat.LostFrames,
					ClassName:      si.targetBoat.Classification,
					Confidence:     si.targetBoat.Confidence,
					DetectionCount: si.targetBoat.DetectionCount,
				}
			} else {
				si.debugMsg("PIP_SUPER_LOCK_SKIP", fmt.Sprintf("⏸️ SUPER LOCK target lost too long (%d > 5 frames) - no PIP",
					si.targetBoat.LostFrames), si.targetBoat.ID)
			}
		} else {
			if si.targetBoat.LastP2Seen.IsZero() {
				si.debugMsg("PIP_SUPER_LOCK_SKIP", "⏸️ SUPER LOCK target never had people detected - no PIP", si.targetBoat.ID)
			} else {
				timeSince := time.Since(si.targetBoat.LastP2Seen)
				si.debugMsg("PIP_SUPER_LOCK_SKIP", fmt.Sprintf("⏸️ SUPER LOCK target people detection too old (%.1fs > 3.0s) - no PIP", timeSince.Seconds()), si.targetBoat.ID)
			}
		}
	}

	return nil // No SUPER LOCK target with people available for PIP
}

// === PTZ-NATIVE ZONE SYSTEM ===

// calculatePTZFieldOfView determines the PTZ area covered by current frame
func (si *SpatialIntegration) calculatePTZFieldOfView() PTZFieldOfView {
	currentSpatial := si.spatialTracker.GetCurrentSpatialPosition()

	// Get calibration for current zoom level
	panPixelsPerUnit := si.spatialTracker.InterpolatePanCalibration(currentSpatial.Zoom)
	tiltPixelsPerUnit := si.spatialTracker.InterpolateTiltCalibration(currentSpatial.Zoom)

	// Calculate PTZ coverage based on frame dimensions
	panRange := float64(si.frameWidth) / panPixelsPerUnit    // Total pan units covered by frame
	tiltRange := float64(si.frameHeight) / tiltPixelsPerUnit // Total tilt units covered by frame

	// Calculate boundaries - current camera position is center of frame
	leftEdge := currentSpatial.Pan - (panRange / 2)
	rightEdge := currentSpatial.Pan + (panRange / 2)
	topEdge := currentSpatial.Tilt - (tiltRange / 2)
	bottomEdge := currentSpatial.Tilt + (tiltRange / 2)

	fov := PTZFieldOfView{
		Center: currentSpatial,
		Coverage: struct {
			PanRange  float64
			TiltRange float64
		}{panRange, tiltRange},
		Boundaries: struct {
			LeftEdge   float64
			RightEdge  float64
			TopEdge    float64
			BottomEdge float64
		}{leftEdge, rightEdge, topEdge, bottomEdge},
	}

	si.debugMsg("PTZ_FOV", fmt.Sprintf("Zoom=%.1f covers Pan=[%.1f to %.1f] (%.1f units), Tilt=[%.1f to %.1f] (%.1f units)",
		currentSpatial.Zoom, leftEdge, rightEdge, panRange, topEdge, bottomEdge, tiltRange))

	return fov
}

// predictPTZMovement predicts if boat will exit current PTZ field of view and calculates optimal camera movement
func (si *SpatialIntegration) predictPTZMovement(boat *TrackedBoat) *PTZPrediction {
	// Get current PTZ field of view
	fov := si.calculatePTZFieldOfView()

	// Convert boat's pixel velocity to PTZ velocity
	panPixelsPerUnit := si.spatialTracker.InterpolatePanCalibration(fov.Center.Zoom)
	tiltPixelsPerUnit := si.spatialTracker.InterpolateTiltCalibration(fov.Center.Zoom)

	ptzVelocity := struct {
		Pan  float64
		Tilt float64
	}{
		Pan:  boat.PixelVelocity.X / panPixelsPerUnit,  // PTZ pan units per second
		Tilt: boat.PixelVelocity.Y / tiltPixelsPerUnit, // PTZ tilt units per second
	}

	// Use configurable prediction parameters
	predictionTime := si.ptzPredictionTime // Look ahead time (configurable)
	minVelocity := si.ptzMinVelocity       // Minimum PTZ velocity to trigger prediction (configurable)

	// Calculate current boat position in PTZ coordinates
	currentPTZ := boat.CurrentSpatial

	// Skip prediction for stationary or very slow boats
	velocity := math.Sqrt(ptzVelocity.Pan*ptzVelocity.Pan + ptzVelocity.Tilt*ptzVelocity.Tilt)
	if velocity < minVelocity {
		return &PTZPrediction{
			CurrentFOV:     fov,
			FuturePTZ:      currentPTZ,
			WillExitFrame:  false,
			ExitDirection:  "NONE",
			MovementType:   "STATIONARY",
			PredictionTime: predictionTime,
			PTZVelocity:    ptzVelocity,
		}
	}

	// === LATENCY COMPENSATION ===
	// Estimate where boat actually is NOW (compensate for YOLO pipeline latency)
	compensatedPTZ := SpatialCoordinate{
		Pan:  currentPTZ.Pan + (ptzVelocity.Pan * si.pipelineLatency),
		Tilt: currentPTZ.Tilt + (ptzVelocity.Tilt * si.pipelineLatency),
		Zoom: currentPTZ.Zoom,
	}

	si.debugMsg("LATENCY_COMP", fmt.Sprintf("Boat detected at PTZ(%.1f,%.1f) but likely at PTZ(%.1f,%.1f) now (+%.1fs compensation)",
		currentPTZ.Pan, currentPTZ.Tilt, compensatedPTZ.Pan, compensatedPTZ.Tilt, si.pipelineLatency), boat.ID)

	// Predict future PTZ position from compensated current position
	futurePTZ := SpatialCoordinate{
		Pan:  compensatedPTZ.Pan + (ptzVelocity.Pan * predictionTime),
		Tilt: compensatedPTZ.Tilt + (ptzVelocity.Tilt * predictionTime),
		Zoom: compensatedPTZ.Zoom,
	}

	// Check if future position will be outside current field of view
	willExitLeft := futurePTZ.Pan < fov.Boundaries.LeftEdge
	willExitRight := futurePTZ.Pan > fov.Boundaries.RightEdge
	willExitTop := futurePTZ.Tilt < fov.Boundaries.TopEdge
	willExitBottom := futurePTZ.Tilt > fov.Boundaries.BottomEdge

	willExitFrame := willExitLeft || willExitRight || willExitTop || willExitBottom

	// Calculate time to exit frame if it will exit (using compensated position)
	var timeToExit float64
	var exitDirection string

	if willExitFrame {
		// Calculate time to reach each boundary from compensated position
		timeToLeft := math.Inf(1)
		timeToRight := math.Inf(1)
		timeToTop := math.Inf(1)
		timeToBottom := math.Inf(1)

		if ptzVelocity.Pan < 0 { // Moving left
			timeToLeft = (fov.Boundaries.LeftEdge - compensatedPTZ.Pan) / ptzVelocity.Pan
		} else if ptzVelocity.Pan > 0 { // Moving right
			timeToRight = (fov.Boundaries.RightEdge - compensatedPTZ.Pan) / ptzVelocity.Pan
		}

		if ptzVelocity.Tilt < 0 { // Moving up
			timeToTop = (fov.Boundaries.TopEdge - compensatedPTZ.Tilt) / ptzVelocity.Tilt
		} else if ptzVelocity.Tilt > 0 { // Moving down
			timeToBottom = (fov.Boundaries.BottomEdge - compensatedPTZ.Tilt) / ptzVelocity.Tilt
		}

		// Find the earliest exit time
		timeToExit = math.Min(math.Min(timeToLeft, timeToRight), math.Min(timeToTop, timeToBottom))

		// Determine exit direction
		if timeToExit == timeToLeft {
			exitDirection = "LEFT"
		} else if timeToExit == timeToRight {
			exitDirection = "RIGHT"
		} else if timeToExit == timeToTop {
			exitDirection = "UP"
		} else if timeToExit == timeToBottom {
			exitDirection = "DOWN"
		}
	}

	// Calculate optimal camera movement if boat will exit
	var newPTZTarget SpatialCoordinate
	var movementType string

	if willExitFrame {
		// Calculate optimal PTZ position to capture future boat position
		// Use a buffer to keep boat slightly off-center for better tracking
		bufferFactor := si.ptzBufferFactor // Keep boat away from center for better tracking (configurable)

		if willExitLeft {
			newPTZTarget.Pan = futurePTZ.Pan + (fov.Coverage.PanRange * bufferFactor)
			movementType = "LEFT_TRACK"
		} else if willExitRight {
			newPTZTarget.Pan = futurePTZ.Pan - (fov.Coverage.PanRange * bufferFactor)
			movementType = "RIGHT_TRACK"
		} else {
			newPTZTarget.Pan = futurePTZ.Pan
		}

		if willExitTop {
			newPTZTarget.Tilt = futurePTZ.Tilt + (fov.Coverage.TiltRange * bufferFactor)
			movementType = "UP_TRACK"
		} else if willExitBottom {
			newPTZTarget.Tilt = futurePTZ.Tilt - (fov.Coverage.TiltRange * bufferFactor)
			movementType = "DOWN_TRACK"
		} else {
			newPTZTarget.Tilt = futurePTZ.Tilt
		}

		// Handle diagonal movement
		if (willExitLeft || willExitRight) && (willExitTop || willExitBottom) {
			movementType = "DIAGONAL_TRACK"
		}

		newPTZTarget.Zoom = fov.Center.Zoom // Keep same zoom level
	} else {
		newPTZTarget = currentPTZ
		movementType = "NONE"
	}

	prediction := &PTZPrediction{
		CurrentFOV:     fov,
		FuturePTZ:      futurePTZ,
		WillExitFrame:  willExitFrame,
		ExitDirection:  exitDirection,
		NewPTZTarget:   newPTZTarget,
		MovementType:   movementType,
		PredictionTime: predictionTime,
		TimeToExit:     timeToExit,
		PTZVelocity:    ptzVelocity,
	}

	// Debug logging
	if willExitFrame {
		si.debugMsg("PTZ_PREDICT", fmt.Sprintf("🎯 Boat %s will exit %s in %.1fs, velocity=(%.2f,%.2f) PTZ units/s",
			boat.ID, exitDirection, timeToExit, ptzVelocity.Pan, ptzVelocity.Tilt), boat.ID)
		si.debugMsg("PTZ_PREDICT", fmt.Sprintf("🎯 Future position: Pan=%.1f Tilt=%.1f → Target: Pan=%.1f Tilt=%.1f",
			futurePTZ.Pan, futurePTZ.Tilt, newPTZTarget.Pan, newPTZTarget.Tilt), boat.ID)
	} else {
		si.debugMsg("PTZ_PREDICT", fmt.Sprintf("✅ Boat %s will remain in frame, velocity=(%.2f,%.2f) PTZ units/s",
			boat.ID, ptzVelocity.Pan, ptzVelocity.Tilt), boat.ID)
	}

	return prediction
}

// executePTZMovement executes camera movement based on PTZ prediction
func (si *SpatialIntegration) executePTZMovement(target SpatialCoordinate, movementType string) {
	// Check if command is different enough to warrant sending
	if !si.shouldSendCommand(target.Pan, target.Tilt, target.Zoom) {
		si.debugMsg("PTZ_MOVE", fmt.Sprintf("📍 Skipping %s command - too similar to last command", movementType))
		return
	}

	// Round positions to integers as required by camera
	roundedPan := math.Round(target.Pan)
	roundedTilt := math.Round(target.Tilt)
	roundedZoom := math.Round(target.Zoom)

	cmd := ptz.PTZCommand{
		Command:      "absolutePosition",
		Reason:       fmt.Sprintf("PTZ Predictive %s", movementType),
		Duration:     2 * time.Second,
		AbsolutePan:  &roundedPan,
		AbsoluteTilt: &roundedTilt,
		AbsoluteZoom: &roundedZoom,
	}

	// Send command through camera state manager for coordination
	var success bool
	objectID := si.getCurrentObjectID() // Safe object ID getter for logging

	if si.cameraStateManager != nil {
		success = si.cameraStateManager.SendCommand(cmd)
		if success {
			si.debugMsg("PTZ_MOVE", fmt.Sprintf("✅ %s: Moving to intercept position Pan=%.1f Tilt=%.1f Zoom=%.1f",
				movementType, roundedPan, roundedTilt, roundedZoom), objectID)
		} else {
			si.debugMsg("PTZ_MOVE", fmt.Sprintf("❌ %s: Command rejected - camera busy", movementType), objectID)
		}
	} else {
		// Fallback to direct PTZ control
		success = si.ptzCtrl.SendCommand(cmd)
		if success {
			si.debugMsg("PTZ_MOVE", fmt.Sprintf("✅ %s: Moving to intercept position Pan=%.1f Tilt=%.1f Zoom=%.1f (direct)",
				movementType, roundedPan, roundedTilt, roundedZoom), objectID)
		} else {
			si.debugMsg("PTZ_MOVE", fmt.Sprintf("❌ %s: Direct command failed", movementType), objectID)
		}
	}
}

// smartPTZTracking - SIMPLIFIED: Just track to where boat actually IS (no predictions)
func (si *SpatialIntegration) smartPTZTracking() {
	if si.targetBoat == nil || !si.targetBoat.IsLocked {
		return
	}

	// Check if smart PTZ tracking is enabled
	if !si.smartPTZEnabled {
		si.debugMsg("PTZ_TRACK", "⚠️ Smart PTZ tracking is disabled - skipping")
		return
	}

	// Only do tracking if camera is IDLE
	if si.cameraStateManager != nil && !si.cameraStateManager.IsIdle() {
		si.debugMsg("PTZ_TRACK", "⏸️ Skipping tracking - camera is MOVING")
		return
	}

	// CRITICAL FIX: Skip ALL predictive movements and just track to actual boat position
	si.debugMsg("PTZ_TRACK", "🔧 PREDICTIVE TRACKING DISABLED - using simple boat position tracking")

	// === SIMPLE CENTER-BASED TRACKING (July 6th working approach) ===
	// Just check if boat is off-center and move camera to boat's actual position

	// Skip tracking if boat is lost (let predictive mode handle it)
	if si.targetBoat.LostFrames > 0 {
		si.debugMsg("PTZ_TRACK", fmt.Sprintf("⏸️ Boat lost for %d frames - staying in position for predictive mode",
			si.targetBoat.LostFrames), si.targetBoat.ID)
		return
	}

	// ENHANCED: Use P2-centric tracking for LOCK/SUPER LOCK boats with quality people detection
	var trackingTargetX, trackingTargetY int
	var trackingMode string

	if si.targetBoat.UseP2Target {
		// Use P2 centroid for precise targeting
		trackingTargetX = si.targetBoat.P2Centroid.X
		trackingTargetY = si.targetBoat.P2Centroid.Y
		trackingMode = "PEOPLE_CENTROID"

		si.debugMsg("PEOPLE_TRACK", fmt.Sprintf("🎯👤 Using P2 centroid (%d,%d) for tracking - %d people, quality %.2f",
			trackingTargetX, trackingTargetY, si.targetBoat.P2Count, si.targetBoat.P2Quality), si.targetBoat.ID)
	} else {
		// Fall back to boat center for standard tracking
		trackingTargetX = si.targetBoat.CurrentPixel.X
		trackingTargetY = si.targetBoat.CurrentPixel.Y
		trackingMode = "BOAT_CENTER"

		if si.targetBoat.HasP2Objects {
			si.debugMsg("PEOPLE_TRACK", fmt.Sprintf("🚤 Using boat center (%d,%d) - people quality %.2f below threshold",
				trackingTargetX, trackingTargetY, si.targetBoat.P2Quality), si.targetBoat.ID)
		}
	}

	// Calculate how far tracking target is from center in pixels
	offsetX := trackingTargetX - si.frameCenterX
	offsetY := trackingTargetY - si.frameCenterY

	// Calculate distance from center as percentage
	distanceFromCenterX := math.Abs(float64(offsetX)) / float64(si.frameWidth)
	distanceFromCenterY := math.Abs(float64(offsetY)) / float64(si.frameHeight)
	maxDistanceFromCenter := math.Max(distanceFromCenterX, distanceFromCenterY)

	// Trigger movement if target is more than 1% off-center
	if maxDistanceFromCenter > si.centerTriggerThreshold {
		si.debugMsg("SIMPLE_TRACK", fmt.Sprintf("🎯 %s %.1f%% off-center (>%.1f%% threshold) - moving to target position",
			trackingMode, maxDistanceFromCenter*100, si.centerTriggerThreshold*100), si.targetBoat.ID)

		// Calculate spatial coordinates for the tracking target
		var target SpatialCoordinate

		if si.targetBoat.UseP2Target {
			// Calculate spatial coordinates for P2 centroid
			target = si.calculateSpatialCoordinatesForPixel(trackingTargetX, trackingTargetY)
			si.debugMsg("PEOPLE_TRACK", fmt.Sprintf("🎯👤 Calculated spatial target for P2 centroid: Pan=%.1f, Tilt=%.1f",
				target.Pan, target.Tilt), si.targetBoat.ID)
		} else {
			// Use boat's existing spatial position
			if si.targetBoat.CurrentSpatial.Pan == 0.0 && si.targetBoat.CurrentSpatial.Tilt == 0.0 {
				si.debugMsg("SIMPLE_TRACK", "⚠️ Boat has invalid spatial position - forcing recalculation", si.targetBoat.ID)
				si.updateBoatSpatialPosition(si.targetBoat)
				return
			}
			target = si.targetBoat.CurrentSpatial
		}

		// Move camera to the calculated target (P2 centroid or boat center)
		si.executePTZMovement(target, trackingMode)
	} else {
		si.debugMsg("SIMPLE_TRACK", fmt.Sprintf("✅ %s centered (%.1f%% off-center) - no movement needed",
			trackingMode, maxDistanceFromCenter*100), si.targetBoat.ID)
	}
}

// === SMART PTZ CONFIGURATION METHODS ===

// EnableSmartPTZTracking enables the smart PTZ tracking system
func (si *SpatialIntegration) EnableSmartPTZTracking() {
	si.mu.Lock()
	defer si.mu.Unlock()

	si.smartPTZEnabled = true
	si.debugMsg("SMART_PTZ", "Smart PTZ tracking enabled")
}

// DisableSmartPTZTracking disables the smart PTZ tracking system
func (si *SpatialIntegration) DisableSmartPTZTracking() {
	si.mu.Lock()
	defer si.mu.Unlock()

	si.smartPTZEnabled = false
	si.debugMsg("SMART_PTZ", "Smart PTZ tracking disabled")
}

// ConfigureSmartPTZ configures the smart PTZ tracking parameters
func (si *SpatialIntegration) ConfigureSmartPTZ(predictionTime, minVelocity, bufferFactor float64) {
	si.mu.Lock()
	defer si.mu.Unlock()

	si.ptzPredictionTime = predictionTime
	si.ptzMinVelocity = minVelocity
	si.ptzBufferFactor = bufferFactor

	si.debugMsg("SMART_PTZ", fmt.Sprintf("Configuration updated: prediction=%.1fs, min_velocity=%.1f, buffer=%.1f%%",
		predictionTime, minVelocity, bufferFactor*100))
}

// ConfigureSmartPTZAdvanced configures all smart PTZ tracking parameters including latency compensation
func (si *SpatialIntegration) ConfigureSmartPTZAdvanced(predictionTime, minVelocity, bufferFactor, pipelineLatency, centerTrigger float64) {
	si.mu.Lock()
	defer si.mu.Unlock()

	si.ptzPredictionTime = predictionTime
	si.ptzMinVelocity = minVelocity
	si.ptzBufferFactor = bufferFactor
	si.pipelineLatency = pipelineLatency
	si.centerTriggerThreshold = centerTrigger

	si.debugMsg("SMART_PTZ", fmt.Sprintf("Advanced configuration updated: prediction=%.1fs, min_velocity=%.1f, buffer=%.1f%%, latency=%.1fs, center_trigger=%.1f%%",
		predictionTime, minVelocity, bufferFactor*100, pipelineLatency, centerTrigger*100))
}

// GetSmartPTZConfig returns current smart PTZ configuration
func (si *SpatialIntegration) GetSmartPTZConfig() (bool, float64, float64, float64) {
	si.mu.RLock()
	defer si.mu.RUnlock()

	return si.smartPTZEnabled, si.ptzPredictionTime, si.ptzMinVelocity, si.ptzBufferFactor
}

// GetSmartPTZConfigAdvanced returns complete smart PTZ configuration including latency compensation
func (si *SpatialIntegration) GetSmartPTZConfigAdvanced() (bool, float64, float64, float64, float64, float64) {
	si.mu.RLock()
	defer si.mu.RUnlock()

	return si.smartPTZEnabled, si.ptzPredictionTime, si.ptzMinVelocity, si.ptzBufferFactor, si.pipelineLatency, si.centerTriggerThreshold
}

// GetAllBoats returns all tracked boats for debug access
func (si *SpatialIntegration) GetAllBoats() map[string]*TrackedBoat {
	si.mu.RLock()
	defer si.mu.RUnlock()
	return si.allBoats
}

// logSpatialCalculationInProgress logs detailed spatial calculation steps to debug sessions
// This creates a debug event that can be picked up by the main loop and written to active debug sessions
func (si *SpatialIntegration) logSpatialCalculationInProgress(boat *TrackedBoat, targetPixelX, targetPixelY, offsetX, offsetY int,
	currentSpatial SpatialCoordinate, panPixelsPerUnit, tiltPixelsPerUnit, panAdjustment, tiltAdjustment float64) {

	// Store detailed spatial calculation data in the boat for debug logging
	// This will be picked up by debug sessions in the main loop where session management happens

	// Create a summary for console (minimal logging)
	si.debugMsg("SPATIAL_CALC", fmt.Sprintf("Boat %s: offset (%d,%d) ÷ calib (%.2f,%.2f) = adj (%.1f,%.1f)",
		boat.ID, offsetX, offsetY, panPixelsPerUnit, tiltPixelsPerUnit, panAdjustment, tiltAdjustment), boat.ID)

	// Store detailed spatial calculation data for debug sessions
	boat.HasSpatialDebugData = true
	boat.SpatialDebugData = map[string]interface{}{
		"Target_Pixel_X":       targetPixelX,
		"Target_Pixel_Y":       targetPixelY,
		"Frame_Center_X":       si.frameCenterX,
		"Frame_Center_Y":       si.frameCenterY,
		"Offset_From_Center_X": offsetX,
		"Offset_From_Center_Y": offsetY,
		"Current_Pan":          currentSpatial.Pan,
		"Current_Tilt":         currentSpatial.Tilt,
		"Current_Zoom":         currentSpatial.Zoom,
		"Pan_Pixels_Per_Unit":  panPixelsPerUnit,
		"Tilt_Pixels_Per_Unit": tiltPixelsPerUnit,
		"Pan_Adjustment":       panAdjustment,
		"Tilt_Adjustment":      tiltAdjustment,
		"Calculation_Formula": fmt.Sprintf("Pan: %d ÷ %.6f = %.1f, Tilt: %d ÷ %.6f = %.1f",
			offsetX, panPixelsPerUnit, panAdjustment, offsetY, tiltPixelsPerUnit, tiltAdjustment),
	}
}

// logDebugMessage sends debug messages to both debug files AND terminal overlay
func (si *SpatialIntegration) logDebugMessage(message, messageType string, priority int, data map[string]interface{}) {
	// Send to terminal overlay if renderer is available
	if si.renderer != nil {
		if r, ok := si.renderer.(interface{ LogDecision(string, string, int) }); ok {
			r.LogDecision(message, messageType, priority)
		}
	}

	// Send to debug files ONLY if debug mode is enabled and we have an active session
	if si.debugManager != nil && si.targetBoat != nil {
		if dm, ok := si.debugManager.(interface {
			GetSession(string) interface{}
			IsEnabled() bool
		}); ok && dm.IsEnabled() {
			session := dm.GetSession(si.targetBoat.ID)
			if session != nil {
				if s, ok := session.(interface {
					LogEvent(string, string, map[string]interface{})
				}); ok {
					s.LogEvent(messageType, message, data)
				}
			}
		}
	}
}

// prepareRecoveryData prepares recovery data for a lost boat
func (si *SpatialIntegration) prepareRecoveryData(lostBoat *TrackedBoat) {
	// Get measurement data from overlay system (need to access this somehow)
	// For now, use basic prediction based on boat's tracked movement
	var avgDirection float64 = 0
	var avgSpeed float64 = 0

	// Try to get measurement data from renderer if available
	if si.renderer != nil {
		if renderer, ok := si.renderer.(interface {
			GetObjectMeasurements(string) interface{}
		}); ok {
			if measurements := renderer.GetObjectMeasurements(lostBoat.ID); measurements != nil {
				if m, ok := measurements.(interface {
					GetAverageDirection() float64
					GetAverages() (float64, float64, float64, float64)
				}); ok {
					avgDirection = m.GetAverageDirection()
					avgSpeed, _, _, _ = m.GetAverages()
				}
			}
		}
	}

	// Create recovery data
	si.recoveryData = &RecoveryData{
		ObjectID:             lostBoat.ID,
		LastKnownPixelPos:    lostBoat.CurrentPixel,
		LastKnownSpatialPos:  lostBoat.CurrentSpatial,
		AverageDirection:     avgDirection,
		AverageSpeedPixelSec: avgSpeed,
		LossTime:             time.Now(),
		OriginalZoom:         lostBoat.CurrentSpatial.Zoom,
		CurrentPhase:         RECOVERY_MOVE_TO_PREDICTED_1,
		RecoveryStartTime:    time.Now(),
		LingerDuration:       5 * time.Second,     // 5-second linger in directional search
		PhaseStartTime:       time.Time{},         // Initialize to zero time
		PhaseTarget:          SpatialCoordinate{}, // Initialize to zero coordinate
		WaitingForArrival:    false,               // Start ready to send first command
	}

	si.isInRecovery = true

	si.debugMsg("RECOVERY_PREP", fmt.Sprintf("🔍 Prepared recovery: dir=%.1f°, speed=%.1fpx/s, pos=(%d,%d)",
		avgDirection*180/math.Pi, avgSpeed, lostBoat.CurrentPixel.X, lostBoat.CurrentPixel.Y), lostBoat.ID)
}

// getCurrentObjectID safely returns the current object ID for logging (handles recovery mode)
func (si *SpatialIntegration) getCurrentObjectID() string {
	// Prioritize target boat ID if it exists (even during recovery)
	if si.targetBoat != nil {
		return si.targetBoat.ID
	}

	// Fallback to recovery data's ObjectID
	if si.isInRecovery && si.recoveryData != nil {
		return si.recoveryData.ObjectID
	}

	// No target available
	return ""
}

// executeRecovery executes the recovery state machine
func (si *SpatialIntegration) executeRecovery(detections []image.Rectangle) {
	if si.recoveryData == nil {
		si.debugMsg("RECOVERY_ERROR", "❌ Recovery data is nil - ending recovery")
		si.endRecovery()
		return
	}

	// Check for timeout
	if time.Since(si.recoveryData.RecoveryStartTime) > si.recoveryTimeout {
		si.debugMsg("RECOVERY_TIMEOUT", fmt.Sprintf("⏰ Recovery timeout after %.1fs - returning to scanning", si.recoveryTimeout.Seconds()), si.recoveryData.ObjectID)
		si.endRecovery()
		return
	}

	// Check for YOLO detections during recovery - ANY detection = success!
	if len(detections) > 0 {
		si.debugMsg("RECOVERY_SUCCESS", fmt.Sprintf("🎉 Found %d detections during recovery - returning to tracking!", len(detections)), si.recoveryData.ObjectID)
		si.resumeTrackingAfterRecovery(detections)
		return
	}

	// Execute recovery phase
	switch si.recoveryData.CurrentPhase {
	case RECOVERY_MOVE_TO_PREDICTED_1:
		si.executePredictiveMove1()
	case RECOVERY_ZOOM_OUT:
		si.executeZoomOut()
	case RECOVERY_MOVE_TO_PREDICTED_2:
		si.executePredictiveMove2()
	case RECOVERY_COMPLETE:
		si.endRecovery()
	}
}

// executePredictiveMove1 moves camera to predicted boat position (1st attempt)
func (si *SpatialIntegration) executePredictiveMove1() {
	// If this is the first call for this phase, send the movement command
	if !si.recoveryData.WaitingForArrival {
		elapsed := time.Since(si.recoveryData.LossTime)

		// SAFETY BOUNDS: Prevent bizarre prediction figures that cause huge camera swings

		// Clamp elapsed time (after 30 seconds, prediction becomes meaningless)
		clampedElapsed := math.Min(elapsed.Seconds(), 30.0)

		// Clamp average speed (max 300 pixels/second - a very fast boat)
		clampedSpeed := math.Min(math.Abs(si.recoveryData.AverageSpeedPixelSec), 300.0)

		// Simple 2x prediction - move twice as far as normal prediction
		deltaX := clampedSpeed * clampedElapsed * math.Cos(si.recoveryData.AverageDirection) * 2.0
		deltaY := clampedSpeed * clampedElapsed * math.Sin(si.recoveryData.AverageDirection) * 2.0

		// Clamp pixel movement to reasonable bounds (max 1.5x frame dimensions)
		maxPixelMovement := float64(si.frameWidth) * 1.5
		deltaX = math.Max(-maxPixelMovement, math.Min(maxPixelMovement, deltaX))
		deltaY = math.Max(-maxPixelMovement, math.Min(maxPixelMovement, deltaY))

		// Predict new pixel position
		predictedPixelX := si.recoveryData.LastKnownPixelPos.X + int(deltaX)
		predictedPixelY := si.recoveryData.LastKnownPixelPos.Y + int(deltaY)

		// Clamp predicted position to expanded frame bounds (allow some overshoot for edge tracking)
		maxX := int(float64(si.frameWidth) * 1.2) // 20% overshoot allowed
		maxY := int(float64(si.frameHeight) * 1.2)
		predictedPixelX = int(math.Max(-float64(si.frameWidth)*0.2, math.Min(float64(maxX), float64(predictedPixelX))))
		predictedPixelY = int(math.Max(-float64(si.frameHeight)*0.2, math.Min(float64(maxY), float64(predictedPixelY))))

		// Convert to spatial coordinates
		predictedSpatial := si.calculateSpatialCoordinatesForPixel(predictedPixelX, predictedPixelY)

		// FINAL SAFETY CHECK: Clamp spatial coordinates to prevent extreme camera movements
		currentPos := si.ptzCtrl.GetCurrentPosition()
		maxPanMovement := 500.0  // Max 500 pan units per recovery move
		maxTiltMovement := 300.0 // Max 300 tilt units per recovery move

		// Clamp pan movement
		if math.Abs(predictedSpatial.Pan-currentPos.Pan) > maxPanMovement {
			if predictedSpatial.Pan > currentPos.Pan {
				predictedSpatial.Pan = currentPos.Pan + maxPanMovement
			} else {
				predictedSpatial.Pan = currentPos.Pan - maxPanMovement
			}
			si.debugMsg("RECOVERY_SAFETY", fmt.Sprintf("🚫 Clamped pan movement from %.1f to %.1f (max: %.0f units)",
				si.calculateSpatialCoordinatesForPixel(predictedPixelX, predictedPixelY).Pan, predictedSpatial.Pan, maxPanMovement), si.recoveryData.ObjectID)
		}

		// Clamp tilt movement
		if math.Abs(predictedSpatial.Tilt-currentPos.Tilt) > maxTiltMovement {
			if predictedSpatial.Tilt > currentPos.Tilt {
				predictedSpatial.Tilt = currentPos.Tilt + maxTiltMovement
			} else {
				predictedSpatial.Tilt = currentPos.Tilt - maxTiltMovement
			}
			si.debugMsg("RECOVERY_SAFETY", fmt.Sprintf("🚫 Clamped tilt movement from %.1f to %.1f (max: %.0f units)",
				si.calculateSpatialCoordinatesForPixel(predictedPixelX, predictedPixelY).Tilt, predictedSpatial.Tilt, maxTiltMovement), si.recoveryData.ObjectID)
		}

		// Store target and start waiting for arrival
		si.recoveryData.PhaseTarget = predictedSpatial
		si.recoveryData.PhaseStartTime = time.Now()
		si.recoveryData.WaitingForArrival = true

		// Move camera to predicted position
		si.executePTZMovement(predictedSpatial, "RECOVERY: Predicted position")

		si.debugMsg("RECOVERY_PHASE1", fmt.Sprintf("🎯 Moving to predicted position (2x simple): elapsed=%.1fs→%.1fs, speed=%.1f→%.1f px/s, moved(%.0f,%.0f)px → spatial(%.1f,%.1f)",
			elapsed.Seconds(), clampedElapsed, si.recoveryData.AverageSpeedPixelSec, clampedSpeed, deltaX, deltaY, predictedSpatial.Pan, predictedSpatial.Tilt), si.recoveryData.ObjectID)
	}

	// Check if camera has stopped moving (simple and reliable)
	if si.recoveryData.WaitingForArrival {
		cameraIdle := si.cameraStateManager != nil && si.cameraStateManager.IsIdle()
		minPhaseTime := 2 * time.Second // Minimum 2 seconds per phase

		if cameraIdle && time.Since(si.recoveryData.PhaseStartTime) >= minPhaseTime {
			si.debugMsg("RECOVERY_PHASE1", "✅ Camera is IDLE - advancing to zoom out", si.recoveryData.ObjectID)
			si.recoveryData.CurrentPhase = RECOVERY_ZOOM_OUT
			si.recoveryData.WaitingForArrival = false
		} else if time.Since(si.recoveryData.PhaseStartTime) > 10*time.Second {
			// Timeout protection - advance anyway after 10 seconds
			si.debugMsg("RECOVERY_PHASE1", "⏰ Timeout waiting for camera - advancing to zoom out", si.recoveryData.ObjectID)
			si.recoveryData.CurrentPhase = RECOVERY_ZOOM_OUT
			si.recoveryData.WaitingForArrival = false
		} else {
			cameraState := "MOVING"
			if cameraIdle {
				cameraState = "IDLE"
			}
			si.debugMsg("RECOVERY_PHASE1", fmt.Sprintf("⏳ Waiting for camera: %s, %.1fs elapsed",
				cameraState, time.Since(si.recoveryData.PhaseStartTime).Seconds()), si.recoveryData.ObjectID)
		}
	}
}

// executePredictiveMove2 moves camera to predicted boat position (final attempt after zoom, simple 2x)
func (si *SpatialIntegration) executePredictiveMove2() {
	// If this is the first call for this phase, send the movement command
	if !si.recoveryData.WaitingForArrival {
		elapsed := time.Since(si.recoveryData.LossTime)

		// SAFETY BOUNDS: Prevent bizarre prediction figures that cause huge camera swings

		// Clamp elapsed time (after 30 seconds, prediction becomes meaningless)
		clampedElapsed := math.Min(elapsed.Seconds(), 30.0)

		// Clamp average speed (max 300 pixels/second - a very fast boat)
		clampedSpeed := math.Min(math.Abs(si.recoveryData.AverageSpeedPixelSec), 300.0)

		// Simple 2x prediction - move twice as far as normal prediction
		deltaX := clampedSpeed * clampedElapsed * math.Cos(si.recoveryData.AverageDirection) * 2.0
		deltaY := clampedSpeed * clampedElapsed * math.Sin(si.recoveryData.AverageDirection) * 2.0

		// Clamp pixel movement to reasonable bounds (max 1.5x frame dimensions)
		maxPixelMovement := float64(si.frameWidth) * 1.5
		deltaX = math.Max(-maxPixelMovement, math.Min(maxPixelMovement, deltaX))
		deltaY = math.Max(-maxPixelMovement, math.Min(maxPixelMovement, deltaY))

		// Predict new pixel position
		predictedPixelX := si.recoveryData.LastKnownPixelPos.X + int(deltaX)
		predictedPixelY := si.recoveryData.LastKnownPixelPos.Y + int(deltaY)

		// Clamp predicted position to expanded frame bounds (allow some overshoot for edge tracking)
		maxX := int(float64(si.frameWidth) * 1.2) // 20% overshoot allowed
		maxY := int(float64(si.frameHeight) * 1.2)
		predictedPixelX = int(math.Max(-float64(si.frameWidth)*0.2, math.Min(float64(maxX), float64(predictedPixelX))))
		predictedPixelY = int(math.Max(-float64(si.frameHeight)*0.2, math.Min(float64(maxY), float64(predictedPixelY))))

		// Convert to spatial coordinates
		predictedSpatial := si.calculateSpatialCoordinatesForPixel(predictedPixelX, predictedPixelY)

		// FINAL SAFETY CHECK: Clamp spatial coordinates to prevent extreme camera movements
		currentPos := si.ptzCtrl.GetCurrentPosition()
		maxPanMovement := 500.0  // Max 500 pan units per recovery move
		maxTiltMovement := 300.0 // Max 300 tilt units per recovery move

		// Clamp pan movement
		if math.Abs(predictedSpatial.Pan-currentPos.Pan) > maxPanMovement {
			if predictedSpatial.Pan > currentPos.Pan {
				predictedSpatial.Pan = currentPos.Pan + maxPanMovement
			} else {
				predictedSpatial.Pan = currentPos.Pan - maxPanMovement
			}
			si.debugMsg("RECOVERY_SAFETY", fmt.Sprintf("🚫 Clamped pan movement from %.1f to %.1f (max: %.0f units)",
				si.calculateSpatialCoordinatesForPixel(predictedPixelX, predictedPixelY).Pan, predictedSpatial.Pan, maxPanMovement), si.recoveryData.ObjectID)
		}

		// Clamp tilt movement
		if math.Abs(predictedSpatial.Tilt-currentPos.Tilt) > maxTiltMovement {
			if predictedSpatial.Tilt > currentPos.Tilt {
				predictedSpatial.Tilt = currentPos.Tilt + maxTiltMovement
			} else {
				predictedSpatial.Tilt = currentPos.Tilt - maxTiltMovement
			}
			si.debugMsg("RECOVERY_SAFETY", fmt.Sprintf("🚫 Clamped tilt movement from %.1f to %.1f (max: %.0f units)",
				si.calculateSpatialCoordinatesForPixel(predictedPixelX, predictedPixelY).Tilt, predictedSpatial.Tilt, maxTiltMovement), si.recoveryData.ObjectID)
		}

		// Store target and start waiting for arrival
		si.recoveryData.PhaseTarget = predictedSpatial
		si.recoveryData.PhaseStartTime = time.Now()
		si.recoveryData.WaitingForArrival = true

		// Move camera to predicted position
		si.executePTZMovement(predictedSpatial, "RECOVERY: Final Predicted position")

		si.debugMsg("RECOVERY_PHASE3", fmt.Sprintf("🎯 Moving to final predicted position (2x simple): elapsed=%.1fs→%.1fs, speed=%.1f→%.1f px/s, moved(%.0f,%.0f)px → spatial(%.1f,%.1f)",
			elapsed.Seconds(), clampedElapsed, si.recoveryData.AverageSpeedPixelSec, clampedSpeed, deltaX, deltaY, predictedSpatial.Pan, predictedSpatial.Tilt), si.recoveryData.ObjectID)
	}

	// Check if camera has stopped moving (simple and reliable)
	if si.recoveryData.WaitingForArrival {
		cameraIdle := si.cameraStateManager != nil && si.cameraStateManager.IsIdle()
		minPhaseTime := 2 * time.Second // Minimum 2 seconds per phase

		if cameraIdle && time.Since(si.recoveryData.PhaseStartTime) >= minPhaseTime {
			si.debugMsg("RECOVERY_PHASE3", "✅ Camera is IDLE - final move complete, ending recovery", si.recoveryData.ObjectID)
			si.recoveryData.CurrentPhase = RECOVERY_COMPLETE
			si.recoveryData.WaitingForArrival = false
		} else if time.Since(si.recoveryData.PhaseStartTime) > 10*time.Second {
			// Timeout protection - advance anyway after 10 seconds
			si.debugMsg("RECOVERY_PHASE3", "⏰ Timeout waiting for camera - ending recovery", si.recoveryData.ObjectID)
			si.recoveryData.CurrentPhase = RECOVERY_COMPLETE
			si.recoveryData.WaitingForArrival = false
		} else {
			cameraState := "MOVING"
			if cameraIdle {
				cameraState = "IDLE"
			}
			si.debugMsg("RECOVERY_PHASE3", fmt.Sprintf("⏳ Waiting for camera: %s, %.1fs elapsed",
				cameraState, time.Since(si.recoveryData.PhaseStartTime).Seconds()), si.recoveryData.ObjectID)
		}
	}
}

// executeZoomOut zooms out 50% to widen search area
func (si *SpatialIntegration) executeZoomOut() {
	// If this is the first call for this phase, send the zoom command
	if !si.recoveryData.WaitingForArrival {
		// 50% zoom out (120 → 60, etc.)
		newZoom := si.recoveryData.OriginalZoom * 0.5

		// Get current position
		currentPos := si.ptzCtrl.GetCurrentPosition()
		zoomOutTarget := SpatialCoordinate{
			Pan:  currentPos.Pan,
			Tilt: currentPos.Tilt,
			Zoom: newZoom,
		}

		// Store target and start waiting for arrival
		si.recoveryData.PhaseTarget = zoomOutTarget
		si.recoveryData.PhaseStartTime = time.Now()
		si.recoveryData.WaitingForArrival = true

		si.executePTZMovement(zoomOutTarget, "RECOVERY: Zoom out 50%")

		si.debugMsg("RECOVERY_PHASE2", fmt.Sprintf("📹 Zooming out 50%: %.0f → %.0f",
			si.recoveryData.OriginalZoom, newZoom), si.recoveryData.ObjectID)
	}

	// Check if camera has stopped moving
	if si.recoveryData.WaitingForArrival {
		cameraIdle := si.cameraStateManager != nil && si.cameraStateManager.IsIdle()
		minPhaseTime := 2 * time.Second // Minimum 2 seconds per phase

		if cameraIdle && time.Since(si.recoveryData.PhaseStartTime) >= minPhaseTime {
			si.debugMsg("RECOVERY_PHASE2", "✅ Camera is IDLE - zoom complete, advancing to final move", si.recoveryData.ObjectID)
			si.recoveryData.CurrentPhase = RECOVERY_MOVE_TO_PREDICTED_2
			si.recoveryData.WaitingForArrival = false
		} else if time.Since(si.recoveryData.PhaseStartTime) > 10*time.Second {
			// Timeout protection - advance anyway after 10 seconds
			si.debugMsg("RECOVERY_PHASE2", "⏰ Timeout waiting for zoom - advancing to final move", si.recoveryData.ObjectID)
			si.recoveryData.CurrentPhase = RECOVERY_MOVE_TO_PREDICTED_2
			si.recoveryData.WaitingForArrival = false
		} else {
			cameraState := "MOVING"
			if cameraIdle {
				cameraState = "IDLE"
			}
			si.debugMsg("RECOVERY_PHASE2", fmt.Sprintf("⏳ Waiting for zoom complete: %s, %.1fs elapsed",
				cameraState, time.Since(si.recoveryData.PhaseStartTime).Seconds()), si.recoveryData.ObjectID)
		}
	}
}

// executeDirectionalSearch moves in predicted direction and lingers
func (si *SpatialIntegration) executeDirectionalSearch() {
	// If this is the first call for this phase, send the directional movement command
	if !si.recoveryData.WaitingForArrival && si.recoveryData.LingerStartTime.IsZero() {
		// Move further in the predicted direction
		elapsed := time.Since(si.recoveryData.LossTime)

		// SAFETY BOUNDS: Prevent bizarre prediction figures that cause huge camera swings

		// Clamp elapsed time (after 30 seconds, prediction becomes meaningless)
		clampedElapsed := math.Min(elapsed.Seconds(), 30.0)

		// Clamp average speed (max 300 pixels/second - a very fast boat)
		clampedSpeed := math.Min(math.Abs(si.recoveryData.AverageSpeedPixelSec), 300.0)

		// Move even further for the search (5x speed * 3x distance = 15x total movement)
		speedMultiplier := 5.0 // Same aggressive speed prediction as Phase 1
		// Clamp the speed multiplier to prevent extreme movements
		clampedSpeedMultiplier := math.Min(speedMultiplier, 8.0) // Max 8x speed multiplier

		deltaX := clampedSpeed * clampedSpeedMultiplier * clampedElapsed * math.Cos(si.recoveryData.AverageDirection) * 3.0
		deltaY := clampedSpeed * clampedSpeedMultiplier * clampedElapsed * math.Sin(si.recoveryData.AverageDirection) * 3.0

		// Clamp pixel movement to reasonable bounds (max 2x frame dimensions for aggressive search)
		maxPixelMovement := float64(si.frameWidth) * 2.0
		deltaX = math.Max(-maxPixelMovement, math.Min(maxPixelMovement, deltaX))
		deltaY = math.Max(-maxPixelMovement, math.Min(maxPixelMovement, deltaY))

		searchPixelX := si.recoveryData.LastKnownPixelPos.X + int(deltaX)
		searchPixelY := si.recoveryData.LastKnownPixelPos.Y + int(deltaY)

		// Clamp search position to expanded frame bounds (allow more overshoot for aggressive search)
		maxX := int(float64(si.frameWidth) * 1.5) // 50% overshoot allowed for search
		maxY := int(float64(si.frameHeight) * 1.5)
		searchPixelX = int(math.Max(-float64(si.frameWidth)*0.5, math.Min(float64(maxX), float64(searchPixelX))))
		searchPixelY = int(math.Max(-float64(si.frameHeight)*0.5, math.Min(float64(maxY), float64(searchPixelY))))

		searchSpatial := si.calculateSpatialCoordinatesForPixel(searchPixelX, searchPixelY)

		// FINAL SAFETY CHECK: Clamp spatial coordinates to prevent extreme camera movements
		currentPos := si.ptzCtrl.GetCurrentPosition()
		maxPanMovement := 800.0  // Max 800 pan units for aggressive search
		maxTiltMovement := 500.0 // Max 500 tilt units for aggressive search

		// Clamp pan movement
		if math.Abs(searchSpatial.Pan-currentPos.Pan) > maxPanMovement {
			if searchSpatial.Pan > currentPos.Pan {
				searchSpatial.Pan = currentPos.Pan + maxPanMovement
			} else {
				searchSpatial.Pan = currentPos.Pan - maxPanMovement
			}
			si.debugMsg("RECOVERY_SAFETY", fmt.Sprintf("🚫 Clamped directional pan movement from %.1f to %.1f (max: %.0f units)",
				si.calculateSpatialCoordinatesForPixel(searchPixelX, searchPixelY).Pan, searchSpatial.Pan, maxPanMovement), si.recoveryData.ObjectID)
		}

		// Clamp tilt movement
		if math.Abs(searchSpatial.Tilt-currentPos.Tilt) > maxTiltMovement {
			if searchSpatial.Tilt > currentPos.Tilt {
				searchSpatial.Tilt = currentPos.Tilt + maxTiltMovement
			} else {
				searchSpatial.Tilt = currentPos.Tilt - maxTiltMovement
			}
			si.debugMsg("RECOVERY_SAFETY", fmt.Sprintf("🚫 Clamped directional tilt movement from %.1f to %.1f (max: %.0f units)",
				si.calculateSpatialCoordinatesForPixel(searchPixelX, searchPixelY).Tilt, searchSpatial.Tilt, maxTiltMovement), si.recoveryData.ObjectID)
		}

		// Store target and start waiting for arrival
		si.recoveryData.PhaseTarget = searchSpatial
		si.recoveryData.PhaseStartTime = time.Now()
		si.recoveryData.WaitingForArrival = true

		si.executePTZMovement(searchSpatial, "RECOVERY: Directional search")

		si.debugMsg("RECOVERY_PHASE3", fmt.Sprintf("🔍 Moving to directional search position (15x aggressive): elapsed=%.1fs→%.1fs, speed=%.1f→%.1f px/s, mult=%.1f→%.1f, moved(%.0f,%.0f)px → spatial(%.1f,%.1f)",
			elapsed.Seconds(), clampedElapsed, si.recoveryData.AverageSpeedPixelSec, clampedSpeed, speedMultiplier, clampedSpeedMultiplier, deltaX, deltaY, searchSpatial.Pan, searchSpatial.Tilt), si.recoveryData.ObjectID)
		return
	}

	// If we're waiting for the camera to reach the search position
	if si.recoveryData.WaitingForArrival {
		cameraIdle := si.cameraStateManager != nil && si.cameraStateManager.IsIdle()

		if cameraIdle && time.Since(si.recoveryData.PhaseStartTime) >= 2*time.Second {
			si.debugMsg("RECOVERY_PHASE3", "✅ Camera reached search position - starting linger", si.recoveryData.ObjectID)
			si.recoveryData.WaitingForArrival = false
			si.recoveryData.LingerStartTime = time.Now() // Now start the linger timer
		} else if time.Since(si.recoveryData.PhaseStartTime) > 10*time.Second {
			si.debugMsg("RECOVERY_PHASE3", "⏰ Timeout waiting for search position - starting linger", si.recoveryData.ObjectID)
			si.recoveryData.WaitingForArrival = false
			si.recoveryData.LingerStartTime = time.Now() // Start linger anyway
		}
		return
	}

	// Now do the lingering/searching part
	if !si.recoveryData.LingerStartTime.IsZero() {
		lingerElapsed := time.Since(si.recoveryData.LingerStartTime)
		if lingerElapsed < si.recoveryData.LingerDuration {
			// Still lingering - stay put and search
			si.debugMsg("RECOVERY_PHASE3", fmt.Sprintf("🔍 Lingering and searching: %.1fs remaining",
				(si.recoveryData.LingerDuration-lingerElapsed).Seconds()), si.recoveryData.ObjectID)
			return
		}
	}

	// Linger complete - move on to final phase
	si.debugMsg("RECOVERY_PHASE3", "✅ Directional search complete - no boat found", si.recoveryData.ObjectID)
	si.recoveryData.CurrentPhase = RECOVERY_COMPLETE
}

// resumeTrackingAfterRecovery resumes tracking when boat is found during recovery
func (si *SpatialIntegration) resumeTrackingAfterRecovery(detections []image.Rectangle) {
	objectID := ""
	if si.recoveryData != nil {
		objectID = si.recoveryData.ObjectID
	} else if si.targetBoat != nil {
		objectID = si.targetBoat.ID
	}

	si.debugMsg("RECOVERY_RESUME", fmt.Sprintf("🎉 Recovery SUCCESS! Found detections, resuming tracking: %s", objectID), objectID)

	// Clear recovery mode but KEEP targetBoat - it might still be valid for tracking
	si.isInRecovery = false
	si.recoveryData = nil

	// Let normal tracking logic handle the detections
	// The targetBoat.ID will be preserved if the boat object is still valid
}

// endRecovery ends recovery mode and returns to scanning
func (si *SpatialIntegration) endRecovery() {
	objectID := ""
	if si.recoveryData != nil {
		objectID = si.recoveryData.ObjectID
		si.debugMsg("RECOVERY_END", fmt.Sprintf("🔄 Recovery failed - boat not found, returning to scanning mode"), objectID)
	}

	// Clear recovery state
	si.isInRecovery = false
	si.recoveryData = nil

	// NOW clear the target boat since recovery failed
	if si.targetBoat != nil {
		si.debugMsg("RECOVERY_CLEAR", fmt.Sprintf("❌ Clearing target boat %s after failed recovery", si.targetBoat.ID), si.targetBoat.ID)
		si.targetBoat = nil
	}

	// Force return to scanning mode
	if !si.spatialTracker.IsScanning() {
		si.debugMsg("RECOVERY_SCAN", "📡 Activating river scanning after failed recovery")
		si.spatialTracker.SetScanningMode(true)
	}
}

// SetDebugReferences sets up references for dual logging
func (si *SpatialIntegration) SetDebugReferences(debugManager interface{}, renderer interface{}) {
	si.debugManager = debugManager
	si.renderer = renderer
}

// updateLockedTargetOnly processes detections only for the currently locked target during camera movement
// This prevents aging out the locked target while avoiding contamination from new detections
func (si *SpatialIntegration) updateLockedTargetOnly(detections []image.Rectangle, classNames []string, confidences []float64) {
	if si.targetBoat == nil || !si.targetBoat.IsLocked {
		si.debugMsg("LOCKED_TARGET_UPDATE", "❌ No locked target to update")
		return
	}

	si.debugMsg("LOCKED_TARGET_UPDATE", fmt.Sprintf("🎯 Processing %d detections for locked target %s only", len(detections), si.targetBoat.ID), si.targetBoat.ID)

	// Try to match detections to the locked target only
	for i, detection := range detections {
		if classNames[i] != "boat" {
			continue // Only process boat detections for the locked target
		}

		// Calculate detection center and area
		centerX := detection.Min.X + (detection.Dx() / 2)
		centerY := detection.Min.Y + (detection.Dy() / 2)
		area := float64(detection.Dx() * detection.Dy())

		// Calculate distance using standard formula
		deltaX := float64(centerX - si.targetBoat.CurrentPixel.X)
		deltaY := float64(centerY - si.targetBoat.CurrentPixel.Y)
		distance := math.Sqrt(deltaX*deltaX + deltaY*deltaY)

		// Use generous matching distance during camera movement (up to 600px)
		maxMatchDistance := 600.0
		if distance <= maxMatchDistance {
			si.debugMsg("LOCKED_TARGET_UPDATE", fmt.Sprintf("✅ Matched detection at (%d,%d) to locked target %s (distance: %.1fpx)",
				centerX, centerY, si.targetBoat.ID, distance), si.targetBoat.ID)

			// Update the locked target with fresh detection data
			si.targetBoat.CurrentPixel = image.Point{X: centerX, Y: centerY}
			si.targetBoat.PixelArea = area
			si.targetBoat.Confidence = confidences[i]
			si.targetBoat.DetectionCount++
			si.targetBoat.LostFrames = 0 // Reset lost frames - we found it!
			si.targetBoat.LastSeen = time.Now()

			// Update spatial position using the standard function
			si.updateBoatSpatialPosition(si.targetBoat)

			si.debugMsg("LOCKED_TARGET_UPDATE", fmt.Sprintf("🔄 Updated locked target: detections=%d, conf=%.3f, lost=0",
				si.targetBoat.DetectionCount, si.targetBoat.Confidence), si.targetBoat.ID)

			return // Found and updated the locked target, stop processing
		}
	}

	// If no matching detection found, increment lost frames but don't remove during movement
	si.targetBoat.LostFrames++
	si.debugMsg("LOCKED_TARGET_UPDATE", fmt.Sprintf("⚠️ No matching detection for locked target %s (lost frames: %d)",
		si.targetBoat.ID, si.targetBoat.LostFrames), si.targetBoat.ID)
}
