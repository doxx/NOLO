package tracking

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"sync"
	"time"

	"rivercam/ptz"
)

// spatialDebugMsgFunc is a function that will be set by main package to use unified logging
var spatialDebugMsgFunc func(component, message string, boatID ...string)

// spatialDebugMsgVerboseFunc is a function that will be set by main package for verbose logging only
var spatialDebugMsgVerboseFunc func(component, message string, boatID ...string)

// SetSpatialDebugFunction allows main package to provide the debug logger
func SetSpatialDebugFunction(fn func(component, message string, boatID ...string)) {
	spatialDebugMsgFunc = fn
}

// SetSpatialDebugVerboseFunction allows main package to provide the verbose debug logger
func SetSpatialDebugVerboseFunction(fn func(component, message string, boatID ...string)) {
	spatialDebugMsgVerboseFunc = fn
}

// spatialDebugMsg is a wrapper that handles nil checks
func spatialDebugMsg(component, message string, boatID ...string) {
	if spatialDebugMsgFunc != nil {
		spatialDebugMsgFunc(component, message, boatID...)
	}
}

// spatialDebugMsgVerbose is a wrapper that handles nil checks for verbose messages
func spatialDebugMsgVerbose(component, message string, boatID ...string) {
	if spatialDebugMsgVerboseFunc != nil {
		spatialDebugMsgVerboseFunc(component, message, boatID...)
	}
}

// SpatialCoordinate represents a position in the PTZ spatial coordinate system
type SpatialCoordinate struct {
	Pan    float64 // PTZ pan position
	Tilt   float64 // PTZ tilt position
	Zoom   float64 // PTZ zoom level
	PixelX int     // Corresponding pixel X in frame at this PTZ position
	PixelY int     // Corresponding pixel Y in frame at this PTZ position
}

// CustomScanPosition represents a position from scanning.json
type CustomScanPosition struct {
	ID        int             `json:"id"`
	Name      string          `json:"name"`
	Position  ptz.PTZPosition `json:"position"`
	DwellTime int             `json:"dwell_time_seconds"`
	Timestamp time.Time       `json:"timestamp"`
}

// CustomScanningPattern represents the complete scanning pattern from scanning.json
type CustomScanningPattern struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
	Positions   []CustomScanPosition `json:"positions"`
	TotalCount  int                  `json:"total_count"`
}

// SpatialObject represents a detected object with spatial awareness
type SpatialObject struct {
	ID             string    // Unique identifier
	Classification string    // boat, boat+person, surfboard, surfboard+person
	Confidence     float64   // Detection confidence
	FirstDetected  time.Time // When first detected
	LastSeen       time.Time // Last detection time
	DetectionCount int       // Number of times detected

	// Spatial tracking
	CurrentPosition   SpatialCoordinate   // Current spatial position
	PositionHistory   []SpatialCoordinate // Historical positions
	PredictedPosition SpatialCoordinate   // Predicted next position

	// Movement tracking
	Velocity struct {
		PanPerSecond  float64
		TiltPerSecond float64
	}

	// Object properties
	BoundingBox  image.Rectangle // Current bounding box
	PixelArea    float64         // Object area in pixels
	IsLocked     bool            // Whether object is locked for tracking
	LockStrength float64         // How confident we are in the lock (0-1)
}

// SpatialTracker provides spatial awareness for PTZ tracking
type SpatialTracker struct {
	mu sync.Mutex

	// PTZ controller and calibration
	ptzCtrl            ptz.Controller
	cameraStateManager *ptz.CameraStateManager // Camera state management for coordinated commands
	calibration        *ZoomCalibration

	// Frame properties
	frameWidth   int
	frameHeight  int
	frameCenterX int
	frameCenterY int

	// Spatial tracking
	currentPTZPosition SpatialCoordinate
	trackedObjects     map[string]*SpatialObject
	lockedObject       *SpatialObject // Currently locked/tracked object

	// Scanning state
	scanningMode     bool
	scanPattern      []SpatialCoordinate // Predefined scan positions
	currentScanIndex int
	lastScanTime     time.Time

	// Custom scanning from scanning.json
	customScanPattern     *CustomScanningPattern
	scanPositionStartTime time.Time

	// Object classification
	classificationRules map[string]ClassificationRule
	p2MinConfidence     float64 // Confidence threshold for P2 (secondary) objects
}

// ClassificationRule defines how to classify detected objects
type ClassificationRule struct {
	PrimaryClass     string   // boat, surfboard
	SecondaryClasses []string // person
	MinConfidence    float64  // Minimum confidence to consider
	MinDetections    int      // Minimum detections to lock
	LockThreshold    float64  // Confidence threshold for locking
}

// ZoomCalibration contains calibration data for different zoom levels
type ZoomCalibration struct {
	PanPixelsPerUnit  map[int]float64
	TiltPixelsPerUnit map[int]float64
	// NOTE: PTZ limits removed - now handled in Camera State Manager
}

// loadCustomScanningPattern loads the scanning pattern from scanning.json
func loadCustomScanningPattern() (*CustomScanningPattern, error) {
	data, err := os.ReadFile("scanning.json")
	if err != nil {
		return nil, fmt.Errorf("failed to read scanning.json: %v", err)
	}

	var pattern CustomScanningPattern
	if err := json.Unmarshal(data, &pattern); err != nil {
		return nil, fmt.Errorf("failed to parse scanning.json: %v", err)
	}

	return &pattern, nil
}

// NewSpatialTracker creates a new spatial awareness tracker
func NewSpatialTracker(ptzCtrl ptz.Controller, frameWidth, frameHeight int, p1TrackList, p2TrackList []string, p1TrackAll, p2TrackAll bool, p1MinConfidence, p2MinConfidence float64) *SpatialTracker {
	calibration := &ZoomCalibration{
		// UPDATED: More accurate calibration data from detailed measurements
		PanPixelsPerUnit: map[int]float64{
			10:  4.87,  // Zoom 10: 4.87 pixels per pan unit (from calibration table)
			20:  7.91,  // Zoom 20: 7.91 pixels per pan unit
			30:  10.38, // Zoom 30: 10.38 pixels per pan unit
			40:  12.74, // Zoom 40: 12.74 pixels per pan unit
			50:  15.27, // Zoom 50: 15.27 pixels per pan unit
			60:  18.41, // Zoom 60: 18.41 pixels per pan unit
			70:  19.91, // Zoom 70: 19.91 pixels per pan unit
			80:  21.68, // Zoom 80: 21.68 pixels per pan unit
			90:  26.10, // Zoom 90: 26.10 pixels per pan unit
			100: 27.71, // Zoom 100: 27.71 pixels per pan unit
			110: 30.20, // Zoom 110: 30.20 pixels per pan unit
			120: 36.32, // Zoom 120: 36.32 pixels per pan unit
		},
		TiltPixelsPerUnit: map[int]float64{
			10:  5.00,  // Zoom 10: 5.00 pixels per tilt unit (from calibration table)
			20:  7.76,  // Zoom 20: 7.76 pixels per tilt unit
			30:  10.86, // Zoom 30: 10.86 pixels per tilt unit
			40:  12.67, // Zoom 40: 12.67 pixels per tilt unit
			50:  15.83, // Zoom 50: 15.83 pixels per tilt unit
			60:  18.54, // Zoom 60: 18.54 pixels per tilt unit
			70:  22.69, // Zoom 70: 22.69 pixels per tilt unit
			80:  23.75, // Zoom 80: 23.75 pixels per tilt unit
			90:  26.67, // Zoom 90: 26.67 pixels per tilt unit
			100: 30.40, // Zoom 100: 30.40 pixels per tilt unit
			110: 31.67, // Zoom 110: 31.67 pixels per tilt unit
			120: 33.78, // Zoom 120: 33.78 pixels per tilt unit
		},
		// NOTE: PTZ limits removed - now handled in Camera State Manager
	}

	// Generate dynamic classification rules from P1 tracking list
	classificationRules := make(map[string]ClassificationRule)

	// Define all YOLO/COCO classes for P1 "all" support
	allYOLOClasses := []string{
		"person", "bicycle", "car", "motorbike", "aeroplane", "bus", "train", "truck", "boat",
		"traffic light", "fire hydrant", "stop sign", "parking meter", "bench", "bird", "cat", "dog",
		"horse", "sheep", "cow", "elephant", "bear", "zebra", "giraffe", "backpack", "umbrella",
		"handbag", "tie", "suitcase", "frisbee", "skis", "snowboard", "sports ball", "kite",
		"baseball bat", "baseball glove", "skateboard", "surfboard", "tennis racket", "bottle",
		"wine glass", "cup", "fork", "knife", "spoon", "bowl", "banana", "apple", "sandwich",
		"orange", "broccoli", "carrot", "hot dog", "pizza", "donut", "cake", "chair", "sofa",
		"pottedplant", "bed", "diningtable", "toilet", "tvmonitor", "laptop", "mouse", "remote",
		"keyboard", "cell phone", "microwave", "oven", "toaster", "sink", "refrigerator", "book",
		"clock", "vase", "scissors", "teddy bear", "hair drier", "toothbrush",
	}

	// Determine which classes to create rules for
	var targetClasses []string
	if p1TrackAll {
		targetClasses = allYOLOClasses
	} else {
		targetClasses = p1TrackList
	}

	for _, p1Class := range targetClasses {
		// For each P1 object type, create a classification rule
		// P2 objects (or all non-P1 if p2TrackAll) can be secondary classes
		var secondaryClasses []string
		if p1TrackAll {
			// When P1 is "all", no objects can be P2 (everything is already P1)
			secondaryClasses = []string{}
		} else if p2TrackAll {
			// When P2 is "all", we'll handle secondary detection dynamically
			// For now, keep the common case of "person" as secondary
			secondaryClasses = []string{"person"}
		} else {
			secondaryClasses = p2TrackList
		}

		classificationRules[p1Class] = ClassificationRule{
			PrimaryClass:     p1Class,
			SecondaryClasses: secondaryClasses,
			MinConfidence:    p1MinConfidence, // Use P1 confidence for primary targets
			MinDetections:    5,
			LockThreshold:    0.5,
		}
	}

	// Load scanning pattern from scanning.json (required)
	customPattern, err := loadCustomScanningPattern()
	if err != nil {
		panic(fmt.Sprintf("Failed to load scanning.json: %v - This file is required for operation", err))
	}

	spatialDebugMsg("SCANNING", fmt.Sprintf("Successfully loaded custom scanning pattern '%s' with %d positions",
		customPattern.Name, len(customPattern.Positions)))

	// Convert custom positions to spatial coordinates for compatibility
	var scanPattern []SpatialCoordinate
	for _, pos := range customPattern.Positions {
		scanPattern = append(scanPattern, SpatialCoordinate{
			Pan:  pos.Position.Pan,
			Tilt: pos.Position.Tilt,
			Zoom: pos.Position.Zoom,
		})
	}

	tracker := &SpatialTracker{
		ptzCtrl:               ptzCtrl,
		calibration:           calibration,
		frameWidth:            frameWidth,
		frameHeight:           frameHeight,
		frameCenterX:          frameWidth / 2,
		frameCenterY:          frameHeight / 2,
		trackedObjects:        make(map[string]*SpatialObject),
		scanningMode:          true,
		scanPattern:           scanPattern,
		customScanPattern:     customPattern,
		currentScanIndex:      0, // Start at first position
		scanPositionStartTime: time.Now(),
		classificationRules:   classificationRules,
		p2MinConfidence:       p2MinConfidence,
	}

	// Show tracking configuration
	if p1TrackAll {
		spatialDebugMsg("TRACKING_CONFIG", "🎯 P1 TRACKING: ALL detected objects (80 YOLO classes)")
	} else {
		spatialDebugMsg("TRACKING_CONFIG", fmt.Sprintf("🎯 P1 TRACKING: %v", p1TrackList))
	}

	if p2TrackAll && !p1TrackAll {
		spatialDebugMsg("TRACKING_CONFIG", "✨ P2 ENHANCEMENT: ALL non-P1 objects")
	} else if len(p2TrackList) > 0 && !p1TrackAll {
		spatialDebugMsg("TRACKING_CONFIG", fmt.Sprintf("✨ P2 ENHANCEMENT: %v", p2TrackList))
	} else if p1TrackAll {
		spatialDebugMsg("TRACKING_CONFIG", "✨ P2 ENHANCEMENT: None (P1 is already ALL objects)")
	}

	// Show loaded pattern summary
	spatialDebugMsg("MIAMI_RIVER_SCAN", "📋 Pattern Summary:")
	for i, pos := range customPattern.Positions {
		spatialDebugMsg("MIAMI_RIVER_SCAN", fmt.Sprintf("%2d. %-20s | Pan:%4.0f Tilt:%3.0f Zoom:%3.0f | %2ds",
			i+1, pos.Name, pos.Position.Pan, pos.Position.Tilt, pos.Position.Zoom, pos.DwellTime))
	}
	spatialDebugMsg("MIAMI_RIVER_SCAN", fmt.Sprintf("🚀 Starting at Position 1: %s", customPattern.Positions[0].Name))

	// Move to first position immediately
	firstPos := customPattern.Positions[0]
	firstTarget := SpatialCoordinate{
		Pan:  firstPos.Position.Pan,
		Tilt: firstPos.Position.Tilt,
		Zoom: firstPos.Position.Zoom,
	}
	spatialDebugMsg("MIAMI_RIVER_SCAN", fmt.Sprintf("📍 Moving to initial position: %s (Pan=%.0f, Tilt=%.0f, Zoom=%.0f)",
		firstPos.Name, firstPos.Position.Pan, firstPos.Position.Tilt, firstPos.Position.Zoom))
	tracker.moveToPTZPosition(firstTarget)

	// Initialize current PTZ position
	currentPos := ptzCtrl.GetCurrentPosition()
	tracker.currentPTZPosition = SpatialCoordinate{
		Pan:  currentPos.Pan,
		Tilt: currentPos.Tilt,
		Zoom: currentPos.Zoom,
	}

	return tracker
}

// UpdateDetections processes new YOLO detections and updates spatial tracking
func (st *SpatialTracker) UpdateDetections(detections []image.Rectangle, classNames []string, confidences []float64) {
	st.mu.Lock()
	defer st.mu.Unlock()

	now := time.Now()

	// CRITICAL FIX: Sync with actual camera position before doing any spatial calculations
	actualPos := st.ptzCtrl.GetCurrentPosition()
	st.currentPTZPosition = SpatialCoordinate{
		Pan:  actualPos.Pan,
		Tilt: actualPos.Tilt,
		Zoom: actualPos.Zoom,
	}

	// Use the ACTUAL camera position for spatial calculations
	currentSpatial := st.currentPTZPosition

	spatialDebugMsg("SPATIAL_SYNC", fmt.Sprintf("Using actual camera position: Pan=%.1f Tilt=%.1f Zoom=%.1f",
		currentSpatial.Pan, currentSpatial.Tilt, currentSpatial.Zoom))

	// Process each detection
	for i, detection := range detections {
		className := classNames[i]
		confidence := confidences[i]

		// Check if this is a class we care about
		rule, exists := st.classificationRules[className]
		if !exists || confidence < rule.MinConfidence {
			continue
		}

		// Calculate spatial coordinate for this detection
		detectionSpatial := st.pixelToSpatialCoordinate(detection, currentSpatial)

		// Create classification string
		classification := st.classifyDetection(detections, classNames, confidences, i)

		// Find or create spatial object
		obj := st.findOrCreateSpatialObject(detectionSpatial, classification, confidence, detection)

		// Update object tracking
		st.updateSpatialObject(obj, detectionSpatial, detection, confidence, now)

		// Check for object locking
		if st.shouldLockObject(obj, rule) {
			st.lockObject(obj)
		}
	}

	// Update tracking behavior based on current state
	st.updateTrackingBehavior()

	// Clean up old objects
	st.cleanupOldObjects(now)
}

// pixelToSpatialCoordinate converts a pixel detection to spatial coordinates
func (st *SpatialTracker) pixelToSpatialCoordinate(detection image.Rectangle, currentSpatial SpatialCoordinate) SpatialCoordinate {
	// Calculate center of detection
	centerX := detection.Min.X + detection.Dx()/2
	centerY := detection.Min.Y + detection.Dy()/2

	// Calculate offset from frame center
	offsetX := centerX - st.frameCenterX
	offsetY := centerY - st.frameCenterY

	// Convert pixel offset to PTZ units using calibration
	panPixelsPerUnit := st.InterpolatePanCalibration(currentSpatial.Zoom)
	tiltPixelsPerUnit := st.InterpolateTiltCalibration(currentSpatial.Zoom)

	panOffset := float64(offsetX) / panPixelsPerUnit
	tiltOffset := float64(offsetY) / tiltPixelsPerUnit

	// Calculate target spatial coordinate
	targetPan := currentSpatial.Pan + panOffset
	targetTilt := currentSpatial.Tilt + tiltOffset

	// NOTE: Limit checking is now handled in Camera State Manager
	// All PTZ commands go through validation there, so we don't need to clamp here

	// BRIDGE ZONE PROTECTION: Check if tracking would point at bridge infrastructure
	if st.isBridgeZone(targetPan, targetTilt) {
		spatialDebugMsg("BRIDGE_PROTECT", fmt.Sprintf("Target Pan:%.1f Tilt:%.1f would point at bridge - adjusting",
			targetPan, targetTilt))

		// Try to adjust tilt to avoid bridge zone while keeping pan
		adjustedTilt := targetTilt
		if targetTilt >= 150 && targetTilt <= 350 && targetPan >= 2200 {
			// Move tilt below or above bridge zone
			if targetTilt < 250 {
				adjustedTilt = 130 // Move below bridge zone
			} else {
				adjustedTilt = 360 // Move above bridge zone
			}

			spatialDebugMsg("BRIDGE_ADJUST", fmt.Sprintf("Adjusted tilt from %.1f to %.1f to avoid bridge",
				targetTilt, adjustedTilt))
			targetTilt = adjustedTilt
		}

		// If still in bridge zone, adjust pan instead
		if st.isBridgeZone(targetPan, targetTilt) {
			if targetPan >= 2200 {
				targetPan = 2190 // Move just outside bridge zone
				spatialDebugMsg("BRIDGE_ADJUST", fmt.Sprintf("Adjusted pan from original to %.1f to avoid bridge", targetPan))
			}
		}
	}

	return SpatialCoordinate{
		Pan:    targetPan,
		Tilt:   targetTilt,
		Zoom:   currentSpatial.Zoom,
		PixelX: centerX,
		PixelY: centerY,
	}
}

// interpolatePanCalibration gets pan pixels per unit for any zoom level
func (st *SpatialTracker) InterpolatePanCalibration(zoomLevel float64) float64 {
	// Get all available zoom levels sorted
	zoomLevels := []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120}

	// Handle edge cases
	if zoomLevel <= float64(zoomLevels[0]) {
		return st.calibration.PanPixelsPerUnit[zoomLevels[0]]
	}
	if zoomLevel >= float64(zoomLevels[len(zoomLevels)-1]) {
		return st.calibration.PanPixelsPerUnit[zoomLevels[len(zoomLevels)-1]]
	}

	// Find the two closest zoom levels for interpolation
	for i := 0; i < len(zoomLevels)-1; i++ {
		lowerZoom := float64(zoomLevels[i])
		upperZoom := float64(zoomLevels[i+1])

		if zoomLevel >= lowerZoom && zoomLevel <= upperZoom {
			// Linear interpolation between the two points
			ratio := (zoomLevel - lowerZoom) / (upperZoom - lowerZoom)
			lowerValue := st.calibration.PanPixelsPerUnit[zoomLevels[i]]
			upperValue := st.calibration.PanPixelsPerUnit[zoomLevels[i+1]]

			interpolated := lowerValue + ratio*(upperValue-lowerValue)

			spatialDebugMsgVerbose("CALIBRATION", fmt.Sprintf("Pan interpolation: zoom %.1f between %d--%d, ratio %.3f, result %.2f px/unit",
				zoomLevel, zoomLevels[i], zoomLevels[i+1], ratio, interpolated))

			return interpolated
		}
	}

	// Fallback (should not reach here)
	return st.calibration.PanPixelsPerUnit[60]
}

// interpolateTiltCalibration gets tilt pixels per unit for any zoom level
func (st *SpatialTracker) InterpolateTiltCalibration(zoomLevel float64) float64 {
	// Get all available zoom levels sorted
	zoomLevels := []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120}

	// Handle edge cases
	if zoomLevel <= float64(zoomLevels[0]) {
		return st.calibration.TiltPixelsPerUnit[zoomLevels[0]]
	}
	if zoomLevel >= float64(zoomLevels[len(zoomLevels)-1]) {
		return st.calibration.TiltPixelsPerUnit[zoomLevels[len(zoomLevels)-1]]
	}

	// Find the two closest zoom levels for interpolation
	for i := 0; i < len(zoomLevels)-1; i++ {
		lowerZoom := float64(zoomLevels[i])
		upperZoom := float64(zoomLevels[i+1])

		if zoomLevel >= lowerZoom && zoomLevel <= upperZoom {
			// Linear interpolation between the two points
			ratio := (zoomLevel - lowerZoom) / (upperZoom - lowerZoom)
			lowerValue := st.calibration.TiltPixelsPerUnit[zoomLevels[i]]
			upperValue := st.calibration.TiltPixelsPerUnit[zoomLevels[i+1]]

			interpolated := lowerValue + ratio*(upperValue-lowerValue)

			spatialDebugMsgVerbose("CALIBRATION", fmt.Sprintf("Tilt interpolation: zoom %.1f between %d--%d, ratio %.3f, result %.2f px/unit",
				zoomLevel, zoomLevels[i], zoomLevels[i+1], ratio, interpolated))

			return interpolated
		}
	}

	// Fallback (should not reach here)
	return st.calibration.TiltPixelsPerUnit[60]
}

// classifyDetection creates a classification string based on detected objects
func (st *SpatialTracker) classifyDetection(detections []image.Rectangle, classNames []string, confidences []float64, primaryIndex int) string {
	primaryClass := classNames[primaryIndex]
	primaryBox := detections[primaryIndex]

	// Look for secondary objects (like people) overlapping with primary object
	var secondaryClasses []string

	rule := st.classificationRules[primaryClass]
	for _, secondaryClass := range rule.SecondaryClasses {
		for i, detection := range detections {
			if i == primaryIndex {
				continue
			}
			if classNames[i] == secondaryClass && confidences[i] >= st.p2MinConfidence {
				// Check if secondary object overlaps with primary
				if st.boxesOverlap(primaryBox, detection) {
					secondaryClasses = append(secondaryClasses, secondaryClass)
					break
				}
			}
		}
	}

	// Create classification string
	classification := primaryClass
	if len(secondaryClasses) > 0 {
		for _, secondary := range secondaryClasses {
			classification += "+" + secondary
		}
	}

	return classification
}

// boxesOverlap checks if two bounding boxes overlap
func (st *SpatialTracker) boxesOverlap(box1, box2 image.Rectangle) bool {
	return box1.Overlaps(box2)
}

// findOrCreateSpatialObject finds existing object or creates new one
func (st *SpatialTracker) findOrCreateSpatialObject(spatial SpatialCoordinate, classification string, confidence float64, boundingBox image.Rectangle) *SpatialObject {
	// Look for existing object near this spatial position
	searchRadius := 50.0 // PTZ units

	for _, obj := range st.trackedObjects {
		panDiff := math.Abs(obj.CurrentPosition.Pan - spatial.Pan)
		tiltDiff := math.Abs(obj.CurrentPosition.Tilt - spatial.Tilt)

		if panDiff < searchRadius && tiltDiff < searchRadius {
			return obj
		}
	}

	// Create new object
	now := time.Now()
	obj := &SpatialObject{
		ID:              fmt.Sprintf("obj_%d", now.Unix()),
		Classification:  classification,
		Confidence:      confidence,
		FirstDetected:   now,
		LastSeen:        now,
		DetectionCount:  1,
		CurrentPosition: spatial,
		BoundingBox:     boundingBox,
		PixelArea:       float64(boundingBox.Dx() * boundingBox.Dy()),
		LockStrength:    0.1,
	}

	st.trackedObjects[obj.ID] = obj
	return obj
}

// updateSpatialObject updates an existing spatial object
func (st *SpatialTracker) updateSpatialObject(obj *SpatialObject, spatial SpatialCoordinate, boundingBox image.Rectangle, confidence float64, now time.Time) {
	// Update position history
	obj.PositionHistory = append(obj.PositionHistory, obj.CurrentPosition)
	if len(obj.PositionHistory) > 20 {
		obj.PositionHistory = obj.PositionHistory[1:]
	}

	// Update current position
	obj.CurrentPosition = spatial
	obj.BoundingBox = boundingBox
	obj.PixelArea = float64(boundingBox.Dx() * boundingBox.Dy())
	obj.LastSeen = now
	obj.DetectionCount++
	obj.Confidence = math.Max(obj.Confidence, confidence)

	// Update lock strength
	obj.LockStrength = math.Min(1.0, obj.LockStrength+0.1)

	// BRIDGE ZONE WARNING: Check if object is consistently in bridge zone
	if st.isBridgeZone(spatial.Pan, spatial.Tilt) {
		spatialDebugMsg("BRIDGE_WARNING", fmt.Sprintf("Object %s at spatial (%.1f,%.1f) is in bridge zone",
			obj.ID, spatial.Pan, spatial.Tilt))

		// If this object is locked and consistently in bridge zone, consider unlocking it
		if obj.IsLocked && st.lockedObject == obj {
			// Count recent bridge zone positions
			bridgeZoneCount := 0
			recentPositions := len(obj.PositionHistory)
			if recentPositions > 5 {
				recentPositions = 5 // Check last 5 positions
			}

			for i := len(obj.PositionHistory) - recentPositions; i < len(obj.PositionHistory); i++ {
				if st.isBridgeZone(obj.PositionHistory[i].Pan, obj.PositionHistory[i].Tilt) {
					bridgeZoneCount++
				}
			}

			// If majority of recent positions are in bridge zone, abandon tracking
			if bridgeZoneCount >= recentPositions/2 {
				spatialDebugMsg("BRIDGE_ABANDON", fmt.Sprintf("Object %s consistently in bridge zone (%d/%d recent positions) - abandoning tracking",
					obj.ID, bridgeZoneCount, recentPositions))
				st.unlockObject("Object consistently in bridge zone")
				return
			}
		}
	}

	// Calculate velocity
	st.calculateObjectVelocity(obj)

	// Predict next position
	st.predictObjectPosition(obj)
}

// calculateObjectVelocity calculates object velocity in PTZ units per second
func (st *SpatialTracker) calculateObjectVelocity(obj *SpatialObject) {
	if len(obj.PositionHistory) < 2 {
		return
	}

	// Use last few positions for velocity calculation
	historyCount := int(math.Min(5, float64(len(obj.PositionHistory))))
	if historyCount < 2 {
		return
	}

	positions := obj.PositionHistory[len(obj.PositionHistory)-historyCount:]
	totalTime := obj.LastSeen.Sub(obj.FirstDetected).Seconds()

	if totalTime <= 0 {
		return
	}

	// Calculate average velocity
	panDiff := obj.CurrentPosition.Pan - positions[0].Pan
	tiltDiff := obj.CurrentPosition.Tilt - positions[0].Tilt

	obj.Velocity.PanPerSecond = panDiff / totalTime
	obj.Velocity.TiltPerSecond = tiltDiff / totalTime
}

// predictObjectPosition predicts where object will be in the near future
func (st *SpatialTracker) predictObjectPosition(obj *SpatialObject) {
	predictionTime := 2.0 // Predict 2 seconds ahead

	obj.PredictedPosition = SpatialCoordinate{
		Pan:  obj.CurrentPosition.Pan + (obj.Velocity.PanPerSecond * predictionTime),
		Tilt: obj.CurrentPosition.Tilt + (obj.Velocity.TiltPerSecond * predictionTime),
		Zoom: obj.CurrentPosition.Zoom,
	}

	// NOTE: Limit checking is now handled in Camera State Manager
	// All PTZ commands go through validation there, so we don't need to clamp predictions

	// BRIDGE ZONE PROTECTION: Adjust prediction if it would point at bridge
	if st.isBridgeZone(obj.PredictedPosition.Pan, obj.PredictedPosition.Tilt) {
		spatialDebugMsg("BRIDGE_PREDICT", fmt.Sprintf("Predicted position Pan:%.1f Tilt:%.1f would point at bridge - adjusting",
			obj.PredictedPosition.Pan, obj.PredictedPosition.Tilt))

		// If prediction points at bridge, use current position instead (stop prediction)
		obj.PredictedPosition = obj.CurrentPosition
		spatialDebugMsg("BRIDGE_PREDICT", "Using current position instead of bridge-pointing prediction")
	}
}

// shouldLockObject determines if we should lock onto this object
func (st *SpatialTracker) shouldLockObject(obj *SpatialObject, rule ClassificationRule) bool {
	return obj.DetectionCount >= rule.MinDetections &&
		obj.Confidence >= rule.LockThreshold &&
		!obj.IsLocked &&
		st.lockedObject == nil // Only one locked object at a time
}

// lockObject locks onto a spatial object for tracking
func (st *SpatialTracker) lockObject(obj *SpatialObject) {
	obj.IsLocked = true
	st.lockedObject = obj
	st.scanningMode = false

	spatialDebugMsg("SPATIAL", fmt.Sprintf("Locked onto object %s (%s) at Pan:%.1f Tilt:%.1f",
		obj.ID, obj.Classification, obj.CurrentPosition.Pan, obj.CurrentPosition.Tilt), obj.ID)
}

// updateTrackingBehavior updates camera behavior based on current tracking state
func (st *SpatialTracker) updateTrackingBehavior() {
	if st.lockedObject != nil {
		// Track the locked object
		st.trackLockedObject()
	} else if st.scanningMode {
		// Continue scanning - use custom pattern if available
		if st.customScanPattern != nil && len(st.customScanPattern.Positions) > 0 {
			st.executeCustomScanPattern()
		} else {
			st.executeScanPattern()
		}
	}
}

// trackLockedObject implements smooth tracking of the locked object
func (st *SpatialTracker) trackLockedObject() {
	if st.lockedObject == nil {
		return
	}

	obj := st.lockedObject

	// Check if object is still valid
	if time.Since(obj.LastSeen) > 5*time.Second {
		st.unlockObject("Object lost")
		return
	}

	// Calculate target position (use prediction for smoother tracking)
	target := obj.PredictedPosition

	// Calculate smooth path to target
	path := st.calculateSmoothPath(st.currentPTZPosition, target)

	// Execute movement
	if len(path) > 0 {
		st.moveToPTZPosition(path[0])
	}
}

// calculateSmoothPath calculates a smooth path between two spatial coordinates
func (st *SpatialTracker) calculateSmoothPath(from, to SpatialCoordinate) []SpatialCoordinate {
	// For now, implement direct movement with proper zoom scaling
	// TODO: Implement smooth interpolated path for very large movements

	// Calculate movement required
	panDiff := to.Pan - from.Pan
	tiltDiff := to.Tilt - from.Tilt
	zoomDiff := to.Zoom - from.Zoom

	// Use smaller movements for pan due to backlash issues discovered in calibration
	maxPanStep := 20.0
	maxTiltStep := 30.0
	maxZoomStep := 10.0

	// Calculate number of steps needed
	panSteps := int(math.Ceil(math.Abs(panDiff) / maxPanStep))
	tiltSteps := int(math.Ceil(math.Abs(tiltDiff) / maxTiltStep))
	zoomSteps := int(math.Ceil(math.Abs(zoomDiff) / maxZoomStep))

	maxSteps := int(math.Max(float64(panSteps), math.Max(float64(tiltSteps), float64(zoomSteps))))

	if maxSteps <= 1 {
		return []SpatialCoordinate{to}
	}

	// Create interpolated path
	var path []SpatialCoordinate
	for step := 1; step <= maxSteps; step++ {
		ratio := float64(step) / float64(maxSteps)

		stepCoord := SpatialCoordinate{
			Pan:  from.Pan + (panDiff * ratio),
			Tilt: from.Tilt + (tiltDiff * ratio),
			Zoom: from.Zoom + (zoomDiff * ratio),
		}

		path = append(path, stepCoord)
	}

	return path
}

// moveToPTZPosition sends camera to specified spatial coordinate
func (st *SpatialTracker) moveToPTZPosition(target SpatialCoordinate) {
	// Store previous position for movement detection
	previousPos := st.currentPTZPosition

	// CRITICAL FIX: Round all positions to integers before sending to camera
	// This prevents waiting for impossible decimal positions like 1000.3
	roundedPan := math.Round(target.Pan)
	roundedTilt := math.Round(target.Tilt)
	roundedZoom := math.Round(target.Zoom)

	cmd := ptz.PTZCommand{
		Command:      "absolutePosition",
		Reason:       "Spatial tracking",
		Duration:     2 * time.Second, // Allow 2 seconds for movement
		AbsolutePan:  &roundedPan,
		AbsoluteTilt: &roundedTilt,
		AbsoluteZoom: &roundedZoom,
	}

	spatialDebugMsg("SPATIAL", fmt.Sprintf("🚀 Sending command to move to Pan:%.0f Tilt:%.0f Zoom:%.0f (rounded from %.1f,%.1f,%.1f)",
		roundedPan, roundedTilt, roundedZoom, target.Pan, target.Tilt, target.Zoom))

	// Use camera state manager if available, otherwise fall back to direct control
	var success bool
	if st.cameraStateManager != nil {
		success = st.cameraStateManager.SendCommand(cmd)
		if !success {
			spatialDebugMsg("SPATIAL", "Command rejected - camera is busy")
		}
	} else {
		// Fallback to direct PTZ control
		success = st.ptzCtrl.SendCommand(cmd)
	}

	if success {
		spatialDebugMsg("SPATIAL", "✅ Command sent successfully")

		// Clean up position history for significant movements
		panMovement := math.Abs(roundedPan - previousPos.Pan)
		tiltMovement := math.Abs(roundedTilt - previousPos.Tilt)
		zoomMovement := math.Abs(roundedZoom - previousPos.Zoom)

		if panMovement > 50 || tiltMovement > 30 || zoomMovement > 20 {
			spatialDebugMsg("SPATIAL", "Significant movement detected - clearing position history")
			st.cleanupAfterCameraMovement(previousPos, SpatialCoordinate{
				Pan:  roundedPan,
				Tilt: roundedTilt,
				Zoom: roundedZoom,
			})
		}

		// NOTE: Position will be updated on next UpdateDetections call via actual camera sync
		// This prevents the scanning/tracking sync issues we had before
	} else {
		spatialDebugMsg("SPATIAL", "❌ Failed to send PTZ command")
	}
}

// SetCameraStateManager sets the camera state manager for coordinated commands
func (st *SpatialTracker) SetCameraStateManager(stateManager *ptz.CameraStateManager) {
	st.mu.Lock()
	defer st.mu.Unlock()

	st.cameraStateManager = stateManager
	spatialDebugMsg("SPATIAL_TRACKER", "Camera state manager integrated - commands will be coordinated")
}

// GetCameraStateManager returns the camera state manager
func (st *SpatialTracker) GetCameraStateManager() *ptz.CameraStateManager {
	st.mu.Lock()
	defer st.mu.Unlock()

	return st.cameraStateManager
}

// cleanupAfterCameraMovement clears position history when camera moves significantly
func (st *SpatialTracker) cleanupAfterCameraMovement(previousPos, newPos SpatialCoordinate) {
	// Calculate movement magnitude
	panMovement := math.Abs(newPos.Pan - previousPos.Pan)
	tiltMovement := math.Abs(newPos.Tilt - previousPos.Tilt)
	zoomMovement := math.Abs(newPos.Zoom - previousPos.Zoom)

	// Define thresholds for "significant" movement
	significantPanMovement := 50.0  // 50 pan units
	significantTiltMovement := 30.0 // 30 tilt units
	significantZoomMovement := 20.0 // 20 zoom units

	// Check if this is a significant camera movement
	significantMovement := panMovement > significantPanMovement ||
		tiltMovement > significantTiltMovement ||
		zoomMovement > significantZoomMovement

	if significantMovement {
		spatialDebugMsgVerbose("SPATIAL_CLEANUP", "Significant camera movement detected - clearing position history")
		spatialDebugMsgVerbose("SPATIAL_CLEANUP", fmt.Sprintf("Movement: Pan=%.1f, Tilt=%.1f, Zoom=%.1f",
			panMovement, tiltMovement, zoomMovement))

		// Clear position history for all tracked objects to prevent invalid overlay lines
		for _, obj := range st.trackedObjects {
			if len(obj.PositionHistory) > 0 {
				spatialDebugMsgVerbose("SPATIAL_CLEANUP", fmt.Sprintf("Clearing %d position history points for object %s",
					len(obj.PositionHistory), obj.ID), obj.ID)
				obj.PositionHistory = nil // Clear all history
			}

			// Reset velocity calculations since position context has changed
			obj.Velocity.PanPerSecond = 0
			obj.Velocity.TiltPerSecond = 0
		}

		// If we're in scanning mode, this is expected behavior
		if st.scanningMode {
			spatialDebugMsgVerbose("SPATIAL_CLEANUP", "Camera movement during scanning - this is normal")
		} else {
			spatialDebugMsgVerbose("SPATIAL_CLEANUP", "Camera movement during tracking - may need to recalibrate")
		}
	}
}

// executeScanPattern executes the scanning pattern
func (st *SpatialTracker) executeScanPattern() {
	// Get current scan position
	if st.currentScanIndex >= len(st.scanPattern) {
		st.currentScanIndex = 0
		spatialDebugMsg("SCAN_RESET", "Completed scan pattern, restarting from position 1")
	}

	currentPoint := st.scanPattern[st.currentScanIndex]

	// FAST DWELL TIMES: Much faster scanning for smooth movement
	var dwellTime time.Duration
	if currentPoint.Zoom >= 25 {
		// Watch points with higher zoom - REDUCED from 15s to 3s
		dwellTime = 3 * time.Second
	} else if currentPoint.Zoom >= 15 {
		// Medium zoom - REDUCED from 8s to 1.5s
		dwellTime = 1500 * time.Millisecond
	} else {
		// Wide angle scanning - REDUCED from 5s to 1s
		dwellTime = 1 * time.Second
	}

	// Check if it's time to move to next scan position
	if time.Since(st.lastScanTime) < dwellTime {
		// Still dwelling - show progress occasionally
		elapsed := time.Since(st.lastScanTime)
		remaining := dwellTime - elapsed
		if elapsed.Milliseconds()%1000 < 100 { // Log every second
			spatialDebugMsg("FAST_SCAN", fmt.Sprintf("Point %d/%d dwelling at Pan=%.0f Tilt=%.0f Zoom=%.0f: %.1fs left",
				st.currentScanIndex+1, len(st.scanPattern),
				st.scanPattern[st.currentScanIndex].Pan,
				st.scanPattern[st.currentScanIndex].Tilt,
				st.scanPattern[st.currentScanIndex].Zoom,
				remaining.Seconds()))
		}
		return
	}

	// Move to next scan position
	st.currentScanIndex++
	if st.currentScanIndex >= len(st.scanPattern) {
		st.currentScanIndex = 0
	}

	target := st.scanPattern[st.currentScanIndex]

	// BRIDGE ZONE CHECK: Don't move to bridge zones
	if st.isBridgeZone(target.Pan, target.Tilt) {
		spatialDebugMsg("BRIDGE_SKIP", fmt.Sprintf("Skipping bridge zone point %d: Pan=%.0f Tilt=%.0f - moving to next",
			st.currentScanIndex+1, st.scanPattern[st.currentScanIndex].Pan, st.scanPattern[st.currentScanIndex].Tilt))
		// Skip this point and try the next one
		st.currentScanIndex++
		if st.currentScanIndex >= len(st.scanPattern) {
			st.currentScanIndex = 0
		}
		target = st.scanPattern[st.currentScanIndex]
	}

	st.moveToPTZPosition(target)

	spatialDebugMsg("FAST_SCAN", fmt.Sprintf("Moving to point %d/%d: Pan=%.0f Tilt=%.0f Zoom=%.0f (dwell: %.1fs)",
		st.currentScanIndex+1, len(st.scanPattern),
		target.Pan, target.Tilt, target.Zoom, float64(dwellTime)))

	st.lastScanTime = time.Now()
}

// unlockObject unlocks the current object and returns to scanning
func (st *SpatialTracker) unlockObject(reason string) {
	if st.lockedObject != nil {
		spatialDebugMsg("SPATIAL", fmt.Sprintf("Unlocking object %s: %s", st.lockedObject.ID, reason), st.lockedObject.ID)
		st.lockedObject.IsLocked = false
		st.lockedObject = nil
	}

	st.scanningMode = true
}

// cleanupOldObjects removes objects that haven't been seen recently
func (st *SpatialTracker) cleanupOldObjects(now time.Time) {
	maxAge := 30 * time.Second

	for id, obj := range st.trackedObjects {
		if now.Sub(obj.LastSeen) > maxAge {
			if obj == st.lockedObject {
				st.unlockObject("Object expired")
			}
			delete(st.trackedObjects, id)
		}
	}
}

// GetTrackedObjects returns current tracked objects
func (st *SpatialTracker) GetTrackedObjects() map[string]*SpatialObject {
	st.mu.Lock()
	defer st.mu.Unlock()

	// Return copy to avoid concurrent access issues
	result := make(map[string]*SpatialObject)
	for id, obj := range st.trackedObjects {
		result[id] = obj
	}
	return result
}

// GetLockedObject returns the currently locked object
func (st *SpatialTracker) GetLockedObject() *SpatialObject {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.lockedObject
}

// GetCurrentSpatialPosition returns current PTZ position as spatial coordinate
func (st *SpatialTracker) GetCurrentSpatialPosition() SpatialCoordinate {
	st.mu.Lock()
	defer st.mu.Unlock()

	// CRITICAL FIX: Always sync with actual camera position to prevent stale data
	actualPos := st.ptzCtrl.GetCurrentPosition()
	st.currentPTZPosition = SpatialCoordinate{
		Pan:  actualPos.Pan,
		Tilt: actualPos.Tilt,
		Zoom: actualPos.Zoom,
	}

	return st.currentPTZPosition
}

// GetScanPattern returns the current scan pattern
func (st *SpatialTracker) GetScanPattern() []SpatialCoordinate {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.scanPattern
}

// IsScanning returns whether currently in scanning mode
func (st *SpatialTracker) IsScanning() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.scanningMode
}

// SetScanningMode enables or disables river scanning
func (st *SpatialTracker) SetScanningMode(enabled bool) {
	st.mu.Lock()
	defer st.mu.Unlock()

	if st.scanningMode != enabled {
		st.scanningMode = enabled
		if enabled {
			spatialDebugMsg("RIVER_SCAN", "River scanning enabled")
		} else {
			spatialDebugMsg("RIVER_SCAN", "River scanning disabled")
		}
	}
}

// ExecuteRiverScan executes one step of the river scanning pattern
func (st *SpatialTracker) ExecuteRiverScan() {
	st.mu.Lock()
	defer st.mu.Unlock()

	if !st.scanningMode {
		return
	}

	// Use custom scanning pattern if available
	if st.customScanPattern != nil && len(st.customScanPattern.Positions) > 0 {
		st.executeCustomScanPattern()
	} else if len(st.scanPattern) > 0 {
		// Fallback to legacy scan pattern
		st.executeScanPattern()
	}
}

// GetCalibration returns the calibration data for external access
func (st *SpatialTracker) GetCalibration() *ZoomCalibration {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.calibration
}

// === BRIDGE ZONE AVOIDANCE ===

// isBridgeZone determines if a pan/tilt combination points at bridge infrastructure
func (st *SpatialTracker) isBridgeZone(pan, tilt float64) bool {
	// BRIDGE DETECTION DISABLED: User requested removal of bridge limitations
	return false
}

// isGoodScanningLocation determines if coordinates are suitable for boat scanning
func (st *SpatialTracker) isGoodScanningLocation(pan, tilt float64) bool {
	// NOTE: Limit checking is now handled in Camera State Manager
	// All PTZ commands go through validation there, so we don't need to check limits here

	// Bridge zone detection is still relevant for scanning logic
	return !st.isBridgeZone(pan, tilt)
}

// IsBridgeZone is a public method to check if coordinates point at bridge infrastructure
func (st *SpatialTracker) IsBridgeZone(pan, tilt float64) bool {
	return st.isBridgeZone(pan, tilt)
}

// IsGoodScanningLocation is a public method to check if coordinates are suitable for scanning
func (st *SpatialTracker) IsGoodScanningLocation(pan, tilt float64) bool {
	return st.isGoodScanningLocation(pan, tilt)
}

// isAtTargetPosition checks if current position is within tolerance of target
func (st *SpatialTracker) isAtTargetPosition(actual ptz.PTZPosition, target ptz.PTZPosition, panTolerance, tiltTolerance, zoomTolerance float64) bool {
	panDiff := math.Abs(actual.Pan - target.Pan)
	tiltDiff := math.Abs(actual.Tilt - target.Tilt)
	zoomDiff := math.Abs(actual.Zoom - target.Zoom)

	return panDiff <= panTolerance && tiltDiff <= tiltTolerance && zoomDiff <= zoomTolerance
}

// executeCustomScanPattern executes the custom scanning pattern from scanning.json
func (st *SpatialTracker) executeCustomScanPattern() {
	if st.customScanPattern == nil || len(st.customScanPattern.Positions) == 0 {
		return
	}

	// RATE LIMITING: Only execute once per second to prevent spam
	now := time.Now()
	if st.lastScanTime.IsZero() {
		st.lastScanTime = now
	} else if now.Sub(st.lastScanTime) < 1*time.Second {
		// Too soon since last execution - skip to prevent spam
		return
	}
	st.lastScanTime = now

	// Get current scan position - follow exact JSON sequence
	if st.currentScanIndex >= len(st.customScanPattern.Positions) {
		st.currentScanIndex = 0
		spatialDebugMsg("MIAMI_RIVER_SCAN", fmt.Sprintf("✅ Completed full '%s' pattern cycle (18 positions), restarting...", st.customScanPattern.Name))
		st.scanPositionStartTime = time.Time{} // Reset timer
	}

	currentPosition := st.customScanPattern.Positions[st.currentScanIndex]

	// Get actual camera position to check if we've arrived
	actualPos := st.ptzCtrl.GetCurrentPosition()

	// Sync our internal position with actual camera (same as in UpdateDetections)
	st.currentPTZPosition = SpatialCoordinate{
		Pan:  actualPos.Pan,
		Tilt: actualPos.Tilt,
		Zoom: actualPos.Zoom,
	}

	// Check if camera has reached the target position (within tolerance)
	targetReached := st.isAtTargetPosition(actualPos, currentPosition.Position, 30.0, 20.0, 20.0) // Pan±30, Tilt±20, Zoom±20

	// If we haven't started timing for this position yet, check if we've arrived
	if st.scanPositionStartTime.IsZero() {
		if targetReached {
			// Camera has reached target - start dwell timer
			st.scanPositionStartTime = time.Now()
			spatialDebugMsg("MIAMI_SCAN", fmt.Sprintf("✅ Arrived at Position %d: %s | Starting %ds dwell",
				currentPosition.ID, currentPosition.Name, currentPosition.DwellTime))
		} else {
			// Need to move to target position - SEND THE ACTUAL COMMAND
			spatialDebugMsg("MIAMI_SCAN", fmt.Sprintf("🎯 Moving to Position %d: %s | Current: Pan=%.0f Tilt=%.0f Zoom=%.0f → Target: Pan=%.0f Tilt=%.0f Zoom=%.0f",
				currentPosition.ID, currentPosition.Name,
				actualPos.Pan, actualPos.Tilt, actualPos.Zoom,
				currentPosition.Position.Pan, currentPosition.Position.Tilt, currentPosition.Position.Zoom))

			// CRITICAL FIX: Actually send the movement command
			target := SpatialCoordinate{
				Pan:  currentPosition.Position.Pan,
				Tilt: currentPosition.Position.Tilt,
				Zoom: currentPosition.Position.Zoom,
			}
			st.moveToPTZPosition(target)
		}
		return
	}

	// We're in dwell mode - check if dwell time is complete
	dwellDuration := time.Duration(currentPosition.DwellTime) * time.Second
	timeAtPosition := time.Since(st.scanPositionStartTime)

	if timeAtPosition < dwellDuration {
		// Still dwelling at current position - show progress every 3 seconds
		remaining := dwellDuration - timeAtPosition
		if int(timeAtPosition.Seconds())%3 == 0 && timeAtPosition.Milliseconds()%1000 < 100 {
			spatialDebugMsg("MIAMI_SCAN", fmt.Sprintf("🎥 Position %d/%d: %s | %.0fs remaining | Actual: Pan=%.0f Tilt=%.0f Zoom=%.0f",
				currentPosition.ID, len(st.customScanPattern.Positions), currentPosition.Name,
				remaining.Seconds(), actualPos.Pan, actualPos.Tilt, actualPos.Zoom))
		}
		return
	}

	// Dwell time complete - move to next position in sequence
	st.currentScanIndex++
	st.scanPositionStartTime = time.Time{} // Reset timer for next position

	if st.currentScanIndex >= len(st.customScanPattern.Positions) {
		// Will wrap around on next call
		spatialDebugMsg("MIAMI_SCAN", fmt.Sprintf("🔄 Completed position %d (%s) - pattern cycle complete!",
			currentPosition.ID, currentPosition.Name))
		return
	}

	nextPosition := st.customScanPattern.Positions[st.currentScanIndex]

	// Determine scanning section for context
	var section string
	switch {
	case nextPosition.ID >= 1 && nextPosition.ID <= 6:
		section = "🌊 UPPER RIVER"
	case nextPosition.ID >= 7 && nextPosition.ID <= 10:
		section = "🌉 BRIDGE AREA"
	case nextPosition.ID >= 11 && nextPosition.ID <= 16:
		section = "🏞️ RIVER MOUTH"
	case nextPosition.ID == 17:
		section = "🌊 BISCAYNE BAY"
	case nextPosition.ID == 18:
		section = "🎯 FINAL POSITION"
	default:
		section = "📍 SCANNING"
	}

	spatialDebugMsg("MIAMI_SCAN", fmt.Sprintf("🎯 Moving to Position %d/%d: %s | %s",
		nextPosition.ID, len(st.customScanPattern.Positions), nextPosition.Name, section))
	spatialDebugMsg("MIAMI_SCAN", fmt.Sprintf("📍 Target: Pan=%.0f, Tilt=%.0f, Zoom=%.0f | Dwell: %ds",
		nextPosition.Position.Pan, nextPosition.Position.Tilt, nextPosition.Position.Zoom, nextPosition.DwellTime))

	// Convert to spatial coordinate and move
	target := SpatialCoordinate{
		Pan:  nextPosition.Position.Pan,
		Tilt: nextPosition.Position.Tilt,
		Zoom: nextPosition.Position.Zoom,
	}

	st.moveToPTZPosition(target)
	// DON'T set scanPositionStartTime here - wait until camera actually arrives
}
