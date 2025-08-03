package tracking

import (
	"image"
	"time"
)

// TrackingMode represents the current mode of the tracking system
type TrackingMode int

const (
	ModeScanning TrackingMode = iota
	ModeTracking
	ModeRecovery
)

// CalibrationEntry represents a single calibration point
type CalibrationEntry struct {
	Zoom      float64
	PanRatio  float64 // Pixels per pan unit (negative values)
	TiltRatio float64 // Pixels per tilt unit (negative values)
}

// CalibrationTable holds the complete calibration data from manual calibration
var CalibrationTable = []CalibrationEntry{
	{Zoom: 10, PanRatio: -4.870, TiltRatio: -5.000},
	{Zoom: 20, PanRatio: -7.906, TiltRatio: -7.755},
	{Zoom: 30, PanRatio: -10.378, TiltRatio: -10.857},
	{Zoom: 40, PanRatio: -12.739, TiltRatio: -12.667},
	{Zoom: 50, PanRatio: -15.273, TiltRatio: -15.833},
	{Zoom: 60, PanRatio: -18.411, TiltRatio: -18.537},
	{Zoom: 70, PanRatio: -19.911, TiltRatio: -22.687},
	{Zoom: 80, PanRatio: -21.677, TiltRatio: -23.750},
	{Zoom: 90, PanRatio: -26.097, TiltRatio: -26.667},
	{Zoom: 100, PanRatio: -27.711, TiltRatio: -30.400},
	{Zoom: 110, PanRatio: -30.202, TiltRatio: -31.667},
	{Zoom: 120, PanRatio: -36.324, TiltRatio: -33.778},
}

// GetCalibrationRatios returns the pixel-to-PTZ ratios for a given zoom level
// Uses linear interpolation between calibration points
func GetCalibrationRatios(currentZoom float64) (panRatio, tiltRatio float64) {
	// Clamp zoom to calibration range
	if currentZoom <= CalibrationTable[0].Zoom {
		return CalibrationTable[0].PanRatio, CalibrationTable[0].TiltRatio
	}
	if currentZoom >= CalibrationTable[len(CalibrationTable)-1].Zoom {
		lastEntry := CalibrationTable[len(CalibrationTable)-1]
		return lastEntry.PanRatio, lastEntry.TiltRatio
	}

	// Find the two calibration points to interpolate between
	for i := 0; i < len(CalibrationTable)-1; i++ {
		lower := CalibrationTable[i]
		upper := CalibrationTable[i+1]

		if currentZoom >= lower.Zoom && currentZoom <= upper.Zoom {
			// Linear interpolation
			factor := (currentZoom - lower.Zoom) / (upper.Zoom - lower.Zoom)

			panRatio = lower.PanRatio + factor*(upper.PanRatio-lower.PanRatio)
			tiltRatio = lower.TiltRatio + factor*(upper.TiltRatio-lower.TiltRatio)

			return panRatio, tiltRatio
		}
	}

	// Fallback (shouldn't reach here)
	return CalibrationTable[0].PanRatio, CalibrationTable[0].TiltRatio
}

// TrackedObject represents a tracked object in the frame (for overlay compatibility)
type TrackedObject struct {
	ID             int
	ObjectID       string // Unified object ID in format: 20240125-12-30.001
	CenterX        int
	CenterY        int
	Width          int
	Height         int
	Area           float64
	LastSeen       time.Time
	TrackedFrames  int
	LostFrames     int
	ClassName      string
	Confidence     float64
	DetectionCount int
}

// DetectionPoint represents a historical detection point
type DetectionPoint struct {
	Position image.Point
	Time     time.Time
	Area     float64
}

// TrackingDecision represents tracking decision information for overlay
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

// ModeHandler - stub type for compatibility (not actually used)
type ModeHandler struct{}
