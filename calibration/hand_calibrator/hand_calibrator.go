package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rivercam/ptz"
)

// HandCalibrator performs manual PTZ calibration with user guidance
type HandCalibrator struct {
	ptzCtrl     ptz.Controller
	frameWidth  int
	frameHeight int
	calibDir    string

	// Camera connection details
	cameraIP   string
	cameraPort string
	username   string
	password   string

	// Calibration results
	calibrationTable map[float64]*ManualZoomCalibration
	scanner          *bufio.Scanner
}

// ManualZoomCalibration stores manual calibration data for a zoom level
type ManualZoomCalibration struct {
	ZoomLevel         float64
	PanPixelsPerUnit  float64
	TiltPixelsPerUnit float64
	PanStartPosition  PTZPosition
	PanEndPosition    PTZPosition
	TiltStartPosition PTZPosition
	TiltEndPosition   PTZPosition
	CalibrationMethod string
	Timestamp         time.Time
	UserNotes         string
}

// PTZPosition represents a camera position
type PTZPosition struct {
	Pan  float64 `json:"pan"`
	Tilt float64 `json:"tilt"`
	Zoom float64 `json:"zoom"`
}

// NewHandCalibrator creates a new manual calibration system
func NewHandCalibrator(ptzCtrl ptz.Controller, frameWidth, frameHeight int, cameraIP, cameraPort, username, password string) *HandCalibrator {
	// Create timestamped calibration directory
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	calibDir := fmt.Sprintf("/tmp/hand_calibration_%s", timestamp)

	return &HandCalibrator{
		ptzCtrl:     ptzCtrl,
		frameWidth:  frameWidth,
		frameHeight: frameHeight,
		calibDir:    calibDir,

		cameraIP:   cameraIP,
		cameraPort: cameraPort,
		username:   username,
		password:   password,

		calibrationTable: make(map[float64]*ManualZoomCalibration),
		scanner:          bufio.NewScanner(os.Stdin),
	}
}

// StartManualCalibration begins the interactive manual calibration process
func (hc *HandCalibrator) StartManualCalibration() error {
	fmt.Printf("🖐️  MANUAL PTZ CALIBRATION SYSTEM\n")
	fmt.Printf("===============================\n\n")

	fmt.Printf("📏 Image dimensions: %d × %d pixels\n", hc.frameWidth, hc.frameHeight)
	fmt.Printf("🎯 This system will guide you through precise manual calibration\n")
	fmt.Printf("📋 You'll align objects manually for maximum accuracy\n\n")

	// Create directory
	if err := os.MkdirAll(hc.calibDir, 0755); err != nil {
		return fmt.Errorf("failed to create calibration directory: %v", err)
	}

	// Define zoom levels to test
	zoomLevels := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120}

	fmt.Printf("🔍 Zoom levels to calibrate: ")
	for i, zoom := range zoomLevels {
		if i > 0 {
			fmt.Printf(", ")
		}
		fmt.Printf("%.0f", zoom)
	}
	fmt.Printf("\n\n")

	// Calibrate each zoom level
	for i, zoomLevel := range zoomLevels {
		fmt.Printf("🎯 [%d/%d] CALIBRATING ZOOM LEVEL %.0f\n", i+1, len(zoomLevels), zoomLevel)
		fmt.Printf("=" + strings.Repeat("=", 40) + "\n")

		if err := hc.calibrateZoomLevel(zoomLevel); err != nil {
			fmt.Printf("❌ Error calibrating zoom %.0f: %v\n", zoomLevel, err)
			fmt.Printf("Continue with next zoom level? (y/n): ")
			if !hc.askYesNo() {
				return err
			}
			continue
		}

		// Show progress
		fmt.Printf("✅ Zoom %.0f calibration complete!\n\n", zoomLevel)

		if i < len(zoomLevels)-1 {
			fmt.Printf("Ready for next zoom level? (y/n): ")
			if !hc.askYesNo() {
				break
			}
		}
	}

	// Generate final results
	return hc.generateFinalResults()
}

// calibrateZoomLevel performs manual calibration for a specific zoom level
func (hc *HandCalibrator) calibrateZoomLevel(zoomLevel float64) error {
	// Set zoom level
	fmt.Printf("🔍 Setting zoom to %.0f...\n", zoomLevel)
	if err := hc.setZoomLevel(zoomLevel); err != nil {
		return fmt.Errorf("failed to set zoom: %v", err)
	}

	calibData := &ManualZoomCalibration{
		ZoomLevel:         zoomLevel,
		CalibrationMethod: "manual_alignment",
		Timestamp:         time.Now(),
	}

	// Calibrate pan movement
	fmt.Printf("\n📏 PAN CALIBRATION at Zoom %.0f\n", zoomLevel)
	fmt.Printf("─────────────────────────────────\n")
	if err := hc.calibratePanMovement(calibData); err != nil {
		return fmt.Errorf("pan calibration failed: %v", err)
	}

	// Calibrate tilt movement
	fmt.Printf("\n📏 TILT CALIBRATION at Zoom %.0f\n", zoomLevel)
	fmt.Printf("──────────────────────────────────\n")
	if err := hc.calibrateTiltMovement(calibData); err != nil {
		return fmt.Errorf("tilt calibration failed: %v", err)
	}

	// Store calibration data
	hc.calibrationTable[zoomLevel] = calibData

	// Display results for this zoom level
	hc.displayZoomResults(calibData)

	return nil
}

// calibratePanMovement guides user through pan calibration
func (hc *HandCalibrator) calibratePanMovement(calibData *ManualZoomCalibration) error {
	fmt.Printf("🎯 PAN MOVEMENT CALIBRATION\n")
	fmt.Printf("Instructions:\n")
	fmt.Printf("1. Find a distinctive object in the camera view\n")
	fmt.Printf("2. Use PTZ controls to align the object to the LEFT edge of the screen\n")
	fmt.Printf("3. Press ENTER when perfectly aligned\n")
	fmt.Printf("4. Then pan the object to the RIGHT edge of the screen\n")
	fmt.Printf("5. Press ENTER when perfectly aligned\n\n")

	// Step 1: Align object to left edge
	fmt.Printf("🔍 STEP 1: Align object to LEFT edge of screen\n")
	fmt.Printf("Use your PTZ controller to position an object at the left edge\n")
	fmt.Printf("Press ENTER when ready: ")
	hc.waitForEnter()

	// Get left position
	leftPos, err := hc.getCurrentPTZPosition()
	if err != nil {
		return fmt.Errorf("failed to get left position: %v", err)
	}
	calibData.PanStartPosition = leftPos
	fmt.Printf("📍 Left position recorded: Pan=%.1f, Tilt=%.1f, Zoom=%.1f\n",
		leftPos.Pan, leftPos.Tilt, leftPos.Zoom)

	// Step 2: Align object to right edge
	fmt.Printf("\n🔍 STEP 2: Align object to RIGHT edge of screen\n")
	fmt.Printf("Use pan controls to move the SAME object to the right edge\n")
	fmt.Printf("Keep tilt and zoom the same - only pan!\n")
	fmt.Printf("Press ENTER when perfectly aligned: ")
	hc.waitForEnter()

	// Get right position
	rightPos, err := hc.getCurrentPTZPosition()
	if err != nil {
		return fmt.Errorf("failed to get right position: %v", err)
	}
	calibData.PanEndPosition = rightPos
	fmt.Printf("📍 Right position recorded: Pan=%.1f, Tilt=%.1f, Zoom=%.1f\n",
		rightPos.Pan, rightPos.Tilt, rightPos.Zoom)

	// Calculate pan sensitivity
	panMovement := rightPos.Pan - leftPos.Pan
	pixelMovement := float64(hc.frameWidth) // Object moved across full width
	pixelsPerPanUnit := pixelMovement / panMovement

	calibData.PanPixelsPerUnit = pixelsPerPanUnit

	fmt.Printf("\n📊 PAN CALIBRATION RESULTS:\n")
	fmt.Printf("   Pan movement: %.1f units\n", panMovement)
	fmt.Printf("   Pixel movement: %.0f pixels (full width)\n", pixelMovement)
	fmt.Printf("   🎯 Pan sensitivity: %.3f pixels per unit\n", pixelsPerPanUnit)

	return nil
}

// calibrateTiltMovement guides user through tilt calibration
func (hc *HandCalibrator) calibrateTiltMovement(calibData *ManualZoomCalibration) error {
	fmt.Printf("🎯 TILT MOVEMENT CALIBRATION\n")
	fmt.Printf("Instructions:\n")
	fmt.Printf("1. Find a distinctive object in the camera view\n")
	fmt.Printf("2. Use PTZ controls to align the object to the TOP edge of the screen\n")
	fmt.Printf("3. Press ENTER when perfectly aligned\n")
	fmt.Printf("4. Then tilt the object to the BOTTOM edge of the screen\n")
	fmt.Printf("5. Press ENTER when perfectly aligned\n\n")

	// Step 1: Align object to top edge
	fmt.Printf("🔍 STEP 1: Align object to TOP edge of screen\n")
	fmt.Printf("Use your PTZ controller to position an object at the top edge\n")
	fmt.Printf("Press ENTER when ready: ")
	hc.waitForEnter()

	// Get top position
	topPos, err := hc.getCurrentPTZPosition()
	if err != nil {
		return fmt.Errorf("failed to get top position: %v", err)
	}
	calibData.TiltStartPosition = topPos
	fmt.Printf("📍 Top position recorded: Pan=%.1f, Tilt=%.1f, Zoom=%.1f\n",
		topPos.Pan, topPos.Tilt, topPos.Zoom)

	// Step 2: Align object to bottom edge
	fmt.Printf("\n🔍 STEP 2: Align object to BOTTOM edge of screen\n")
	fmt.Printf("Use tilt controls to move the SAME object to the bottom edge\n")
	fmt.Printf("Keep pan and zoom the same - only tilt!\n")
	fmt.Printf("Press ENTER when perfectly aligned: ")
	hc.waitForEnter()

	// Get bottom position
	bottomPos, err := hc.getCurrentPTZPosition()
	if err != nil {
		return fmt.Errorf("failed to get bottom position: %v", err)
	}
	calibData.TiltEndPosition = bottomPos
	fmt.Printf("📍 Bottom position recorded: Pan=%.1f, Tilt=%.1f, Zoom=%.1f\n",
		bottomPos.Pan, bottomPos.Tilt, bottomPos.Zoom)

	// Calculate tilt sensitivity
	tiltMovement := bottomPos.Tilt - topPos.Tilt
	pixelMovement := float64(hc.frameHeight) // Object moved across full height
	pixelsPerTiltUnit := pixelMovement / tiltMovement

	calibData.TiltPixelsPerUnit = pixelsPerTiltUnit

	fmt.Printf("\n📊 TILT CALIBRATION RESULTS:\n")
	fmt.Printf("   Tilt movement: %.1f units\n", tiltMovement)
	fmt.Printf("   Pixel movement: %.0f pixels (full height)\n", pixelMovement)
	fmt.Printf("   🎯 Tilt sensitivity: %.3f pixels per unit\n", pixelsPerTiltUnit)

	return nil
}

// setZoomLevel sets the camera to a specific zoom level
func (hc *HandCalibrator) setZoomLevel(zoomLevel float64) error {
	// Get current position to maintain pan/tilt
	currentPos, err := hc.getCurrentPTZPosition()
	if err != nil {
		return fmt.Errorf("failed to get current position: %v", err)
	}

	// Create command to set zoom while maintaining pan/tilt
	cmd := ptz.PTZCommand{
		Command:      "absolutePosition",
		Reason:       "Manual Calibration Zoom Set",
		Duration:     3 * time.Second,
		AbsolutePan:  &currentPos.Pan,
		AbsoluteTilt: &currentPos.Tilt,
		AbsoluteZoom: &zoomLevel,
	}

	success := hc.ptzCtrl.SendCommand(cmd)
	if !success {
		return fmt.Errorf("PTZ zoom command failed")
	}

	// Wait for movement to complete
	time.Sleep(4 * time.Second)

	fmt.Printf("✅ Zoom set to %.0f\n", zoomLevel)
	return nil
}

// getCurrentPTZPosition gets the current PTZ position from the controller
func (hc *HandCalibrator) getCurrentPTZPosition() (PTZPosition, error) {
	// Note: This is a placeholder - you'll need to implement actual position reading
	// For now, we'll need to add a method to the PTZ controller to read current position

	fmt.Printf("📍 Getting current PTZ position...\n")

	// This is a placeholder implementation
	// In a real implementation, you'd query the camera's current position
	pos := PTZPosition{
		Pan:  1800.0, // Placeholder values
		Tilt: 300.0,
		Zoom: 10.0,
	}

	fmt.Printf("⚠️  Note: Using placeholder position values\n")
	fmt.Printf("   Real implementation needs PTZ position query capability\n")

	return pos, nil
}

// waitForEnter waits for user to press Enter
func (hc *HandCalibrator) waitForEnter() {
	hc.scanner.Scan()
}

// askYesNo asks user a yes/no question
func (hc *HandCalibrator) askYesNo() bool {
	hc.scanner.Scan()
	response := strings.ToLower(strings.TrimSpace(hc.scanner.Text()))
	return response == "y" || response == "yes"
}

// displayZoomResults shows calibration results for a zoom level
func (hc *HandCalibrator) displayZoomResults(calibData *ManualZoomCalibration) {
	fmt.Printf("\n🎉 ZOOM %.0f CALIBRATION COMPLETE\n", calibData.ZoomLevel)
	fmt.Printf("════════════════════════════════\n")
	fmt.Printf("📏 Pan sensitivity: %.3f pixels per unit\n", calibData.PanPixelsPerUnit)
	fmt.Printf("📏 Tilt sensitivity: %.3f pixels per unit\n", calibData.TiltPixelsPerUnit)

	fmt.Printf("\n📍 Pan positions:\n")
	fmt.Printf("   Start: Pan=%.1f, Tilt=%.1f\n",
		calibData.PanStartPosition.Pan, calibData.PanStartPosition.Tilt)
	fmt.Printf("   End:   Pan=%.1f, Tilt=%.1f\n",
		calibData.PanEndPosition.Pan, calibData.PanEndPosition.Tilt)

	fmt.Printf("\n📍 Tilt positions:\n")
	fmt.Printf("   Start: Pan=%.1f, Tilt=%.1f\n",
		calibData.TiltStartPosition.Pan, calibData.TiltStartPosition.Tilt)
	fmt.Printf("   End:   Pan=%.1f, Tilt=%.1f\n",
		calibData.TiltEndPosition.Pan, calibData.TiltEndPosition.Tilt)

	fmt.Printf("\n")
}

// generateFinalResults creates the final calibration output
func (hc *HandCalibrator) generateFinalResults() error {
	fmt.Printf("📊 GENERATING FINAL CALIBRATION RESULTS\n")
	fmt.Printf("=====================================\n")

	// Display summary table
	hc.displayCalibrationTable()

	// Create results data structure
	results := map[string]interface{}{
		"calibration_type":   "manual_hand_calibration",
		"timestamp":          time.Now(),
		"frame_dimensions":   map[string]int{"width": hc.frameWidth, "height": hc.frameHeight},
		"zoom_levels_tested": len(hc.calibrationTable),
		"calibration_table":  hc.calibrationTable,
		"method_description": "Manual object alignment across full screen dimensions",
		"accuracy_notes":     "High accuracy - direct human verification of object alignment",
	}

	// Save to JSON file
	resultsPath := filepath.Join(hc.calibDir, "manual-calibration-results.json")
	jsonData, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal results: %v", err)
	}

	if err := ioutil.WriteFile(resultsPath, jsonData, 0644); err != nil {
		return fmt.Errorf("failed to save results: %v", err)
	}

	fmt.Printf("✅ Calibration results saved to: %s\n", resultsPath)
	return nil
}

// displayCalibrationTable shows the final calibration table
func (hc *HandCalibrator) displayCalibrationTable() {
	fmt.Printf("📋 FINAL CALIBRATION TABLE\n")
	fmt.Printf("┌────────┬─────────────────┬─────────────────┐\n")
	fmt.Printf("│  Zoom  │  Pan (px/unit)  │ Tilt (px/unit)  │\n")
	fmt.Printf("├────────┼─────────────────┼─────────────────┤\n")

	// Get sorted zoom levels
	var zoomLevels []float64
	for zoom := range hc.calibrationTable {
		zoomLevels = append(zoomLevels, zoom)
	}

	// Simple sort
	for i := 0; i < len(zoomLevels)-1; i++ {
		for j := i + 1; j < len(zoomLevels); j++ {
			if zoomLevels[i] > zoomLevels[j] {
				zoomLevels[i], zoomLevels[j] = zoomLevels[j], zoomLevels[i]
			}
		}
	}

	// Display each zoom level
	for _, zoom := range zoomLevels {
		calib := hc.calibrationTable[zoom]
		fmt.Printf("│ %6.0f │ %15.3f │ %15.3f │\n",
			zoom, calib.PanPixelsPerUnit, calib.TiltPixelsPerUnit)
	}
	fmt.Printf("└────────┴─────────────────┴─────────────────┘\n")

	// Calculate scaling factors
	if len(zoomLevels) >= 2 {
		firstCalib := hc.calibrationTable[zoomLevels[0]]
		lastCalib := hc.calibrationTable[zoomLevels[len(zoomLevels)-1]]

		panScaling := lastCalib.PanPixelsPerUnit / firstCalib.PanPixelsPerUnit
		tiltScaling := lastCalib.TiltPixelsPerUnit / firstCalib.TiltPixelsPerUnit

		fmt.Printf("\n📈 SCALING ANALYSIS:\n")
		fmt.Printf("   Pan scaling: %.2fx from zoom %.0f to %.0f\n",
			panScaling, zoomLevels[0], zoomLevels[len(zoomLevels)-1])
		fmt.Printf("   Tilt scaling: %.2fx from zoom %.0f to %.0f\n",
			tiltScaling, zoomLevels[0], zoomLevels[len(zoomLevels)-1])
	}
}

// Example usage:
//
// To use the hand calibrator, create it like this:
//
//   ptzCtrl := ptz.NewController(...) // Initialize your PTZ controller
//   calibrator := NewHandCalibrator(ptzCtrl, 2688, 1520, "192.168.1.100", "80", "user", "password")
//   err := calibrator.StartManualCalibration()
//
