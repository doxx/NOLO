package main

import (
	"bufio"
	"crypto/md5"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rivercam/ptz"
)

// HandCalibrator performs manual PTZ calibration with user guidance
type HandCalibrator struct {
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
	PanStartPosition  ptz.PTZPosition
	PanEndPosition    ptz.PTZPosition
	TiltStartPosition ptz.PTZPosition
	TiltEndPosition   ptz.PTZPosition
	CalibrationMethod string
	Timestamp         time.Time
	UserNotes         string
}

// PTZStatus represents the XML response structure from the camera
type PTZStatus struct {
	Position struct {
		Pan  float64 `xml:"azimuth"`
		Tilt float64 `xml:"elevation"`
		Zoom float64 `xml:"absoluteZoom"`
	} `xml:"AbsoluteHigh"`
}

// Use PTZPosition from the ptz package

// NewHandCalibrator creates a new manual calibration system
func NewHandCalibrator(frameWidth, frameHeight int, cameraIP, cameraPort, username, password string) *HandCalibrator {
	// Create timestamped calibration directory
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	calibDir := fmt.Sprintf("/tmp/hand_calibration_%s", timestamp)

	return &HandCalibrator{
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

// setZoomLevel sets the camera to a specific zoom level using direct HTTP commands
func (hc *HandCalibrator) setZoomLevel(zoomLevel float64) error {
	// Get current position to maintain pan/tilt
	currentPos, err := hc.getCurrentPTZPosition()
	if err != nil {
		return fmt.Errorf("failed to get current position: %v", err)
	}

	fmt.Printf("🎛️  Setting zoom to %.0f (maintaining Pan=%.1f, Tilt=%.1f)...\n",
		zoomLevel, currentPos.Pan, currentPos.Tilt)

	// Send zoom command directly via HTTP (no PTZ controller status monitoring)
	err = hc.sendAbsolutePositionCommand(currentPos.Pan, currentPos.Tilt, zoomLevel)
	if err != nil {
		return fmt.Errorf("failed to send zoom command: %v", err)
	}

	// Wait for movement to complete
	fmt.Printf("⏳ Waiting for zoom to complete...\n")
	time.Sleep(4 * time.Second)

	fmt.Printf("✅ Zoom set to %.0f\n", zoomLevel)
	return nil
}

// sendAbsolutePositionCommand sends a PTZ absolute position command directly via HTTP
func (hc *HandCalibrator) sendAbsolutePositionCommand(pan, tilt, zoom float64) error {
	// Create XML payload for absolute positioning
	xmlPayload := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<PTZData>
    <AbsoluteHigh>
        <elevation>%.0f</elevation>
        <azimuth>%.0f</azimuth>
        <absoluteZoom>%.0f</absoluteZoom>
    </AbsoluteHigh>
</PTZData>`, tilt, pan, zoom)

	url := fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/absolute", hc.cameraIP, hc.cameraPort)
	uri := "/ISAPI/PTZCtrl/channels/1/absolute"

	// First request to get WWW-Authenticate header
	req, err := http.NewRequest("PUT", url, strings.NewReader(xmlPayload))
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/xml")
	req.ContentLength = int64(len(xmlPayload))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// If we get 401, use digest authentication
	if resp.StatusCode == 401 {
		authHeader := resp.Header.Get("WWW-Authenticate")
		if authHeader == "" {
			return fmt.Errorf("no WWW-Authenticate header in response")
		}

		// Parse the WWW-Authenticate header
		var realm, nonce string
		parts := strings.Split(authHeader, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "realm=") {
				realm = strings.Trim(part[6:], "\"")
			} else if strings.HasPrefix(part, "nonce=") {
				nonce = strings.Trim(part[6:], "\"")
			}
		}

		if realm == "" || nonce == "" {
			return fmt.Errorf("invalid WWW-Authenticate header: %s", authHeader)
		}

		// Create new request with digest authentication
		req, err = http.NewRequest("PUT", url, strings.NewReader(xmlPayload))
		if err != nil {
			return fmt.Errorf("failed to create authenticated request: %v", err)
		}

		req.Header.Set("Authorization", hc.getDigestAuth("PUT", uri, realm, nonce))
		req.Header.Set("Content-Type", "application/xml")
		req.ContentLength = int64(len(xmlPayload))

		resp, err = client.Do(req)
		if err != nil {
			return fmt.Errorf("authenticated request failed: %v", err)
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	fmt.Printf("📡 PTZ command sent successfully\n")
	return nil
}

// getCurrentPTZPosition gets the current PTZ position by querying the camera directly
func (hc *HandCalibrator) getCurrentPTZPosition() (ptz.PTZPosition, error) {
	fmt.Printf("📍 Querying camera for current position...\n")

	// Query the camera directly for current status
	status, err := hc.queryPTZStatus()
	if err != nil {
		return ptz.PTZPosition{}, fmt.Errorf("failed to query PTZ status: %v", err)
	}

	fmt.Printf("📍 Position: Pan=%.1f, Tilt=%.1f, Zoom=%.1f\n",
		status.Pan, status.Tilt, status.Zoom)

	return status, nil
}

// queryPTZStatus directly queries the camera for its current PTZ status
func (hc *HandCalibrator) queryPTZStatus() (ptz.PTZPosition, error) {
	// Query the camera directly for status using digest auth
	url := fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/status", hc.cameraIP, hc.cameraPort)
	uri := "/ISAPI/PTZCtrl/channels/1/status"

	// First request to get WWW-Authenticate header
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return ptz.PTZPosition{}, fmt.Errorf("failed to create request: %v", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ptz.PTZPosition{}, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// If we get 401, extract digest auth parameters and retry
	if resp.StatusCode == 401 {
		authHeader := resp.Header.Get("WWW-Authenticate")
		if authHeader == "" {
			return ptz.PTZPosition{}, fmt.Errorf("no WWW-Authenticate header in response")
		}

		// Parse the WWW-Authenticate header
		var realm, nonce string
		parts := strings.Split(authHeader, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "realm=") {
				realm = strings.Trim(part[6:], "\"")
			} else if strings.HasPrefix(part, "nonce=") {
				nonce = strings.Trim(part[6:], "\"")
			}
		}

		if realm == "" || nonce == "" {
			return ptz.PTZPosition{}, fmt.Errorf("invalid WWW-Authenticate header: %s", authHeader)
		}

		// Create new request with digest authentication
		req, err = http.NewRequest("GET", url, nil)
		if err != nil {
			return ptz.PTZPosition{}, fmt.Errorf("failed to create authenticated request: %v", err)
		}

		// Add digest auth header
		req.Header.Set("Authorization", hc.getDigestAuth("GET", uri, realm, nonce))
		req.Header.Set("Content-Type", "application/xml")

		resp, err = client.Do(req)
		if err != nil {
			return ptz.PTZPosition{}, fmt.Errorf("authenticated request failed: %v", err)
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != 200 {
		return ptz.PTZPosition{}, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Read and parse the XML response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ptz.PTZPosition{}, fmt.Errorf("failed to read response: %v", err)
	}

	// Parse XML response to get actual position
	var status PTZStatus
	if err := xml.Unmarshal(body, &status); err != nil {
		// If XML parsing fails, log the response and try a simpler approach
		fmt.Printf("⚠️  XML parsing failed, response: %s\n", string(body))

		// Fallback: try to extract values with string parsing
		xmlStr := string(body)
		pos := ptz.PTZPosition{
			Pan:  hc.extractValueFromXML(xmlStr, "azimuth"),
			Tilt: hc.extractValueFromXML(xmlStr, "elevation"),
			Zoom: hc.extractValueFromXML(xmlStr, "absoluteZoom"),
		}

		fmt.Printf("📊 Camera position extracted (fallback parsing)\n")
		return pos, nil
	}

	// Successfully parsed XML
	pos := ptz.PTZPosition{
		Pan:  status.Position.Pan,
		Tilt: status.Position.Tilt,
		Zoom: status.Position.Zoom,
	}

	fmt.Printf("📊 Camera position parsed from XML successfully\n")
	return pos, nil
}

// extractValueFromXML extracts a numeric value from XML using simple string parsing
func (hc *HandCalibrator) extractValueFromXML(xmlStr, tagName string) float64 {
	startTag := fmt.Sprintf("<%s>", tagName)
	endTag := fmt.Sprintf("</%s>", tagName)

	startIndex := strings.Index(xmlStr, startTag)
	if startIndex == -1 {
		return 0.0
	}

	startIndex += len(startTag)
	endIndex := strings.Index(xmlStr[startIndex:], endTag)
	if endIndex == -1 {
		return 0.0
	}

	valueStr := strings.TrimSpace(xmlStr[startIndex : startIndex+endIndex])

	// Convert to float
	var value float64
	fmt.Sscanf(valueStr, "%f", &value)

	return value
}

// getDigestAuth creates digest authentication header (proper implementation)
func (hc *HandCalibrator) getDigestAuth(method, uri, realm, nonce string) string {
	// Proper digest auth calculation using MD5 (same as PTZ controller)
	ha1 := fmt.Sprintf("%x", md5.Sum([]byte(fmt.Sprintf("%s:%s:%s", hc.username, realm, hc.password))))
	ha2 := fmt.Sprintf("%x", md5.Sum([]byte(fmt.Sprintf("%s:%s", method, uri))))
	response := fmt.Sprintf("%x", md5.Sum([]byte(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2))))

	return fmt.Sprintf("Digest username=\"%s\", realm=\"%s\", nonce=\"%s\", uri=\"%s\", response=\"%s\"",
		hc.username, realm, nonce, uri, response)
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

// main function for standalone execution
func main() {
	fmt.Printf("🖐️  MANUAL PTZ CALIBRATOR\n")
	fmt.Printf("=======================\n\n")

	// Camera configuration (adjust these for your setup)
	cameraIP := "192.168.1.100" // Your camera IP
	cameraPort := "80"         // Update with your camera port
	username := "user"        // Update with your username
	password := "password"    // Update with your password

	// Image dimensions (2688x1520 for typical Hikvision)
	frameWidth := 2688
	frameHeight := 1520

	fmt.Printf("📷 Camera: %s:%s\n", cameraIP, cameraPort)
	fmt.Printf("📏 Frame: %dx%d pixels\n", frameWidth, frameHeight)
	fmt.Printf("👤 Auth: %s / %s\n\n", username, password)

	// Create hand calibrator
	calibrator := NewHandCalibrator(frameWidth, frameHeight, cameraIP, cameraPort, username, password)

	// Start manual calibration
	if err := calibrator.StartManualCalibration(); err != nil {
		fmt.Printf("❌ Calibration failed: %v\n", err)
		return
	}

	fmt.Printf("🎉 Manual calibration completed successfully!\n")
}
