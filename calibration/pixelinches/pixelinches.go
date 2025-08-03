package main

import (
	"bufio"
	"crypto/md5"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rivercam/ptz"
)

// PixelInchesCalibrator performs pixel-to-inches calibration at different zoom levels
type PixelInchesCalibrator struct {
	frameWidth  int
	frameHeight int
	calibDir    string

	// Camera connection details
	cameraIP   string
	cameraPort string
	username   string
	password   string

	// Calibration results
	calibrationData map[float64]*ZoomPixelData
	scanner         *bufio.Scanner
}

// ZoomPixelData stores pixel-to-inches data for a zoom level
type ZoomPixelData struct {
	ZoomLevel          float64   `json:"zoom_level"`
	PixelsFor369Inches int       `json:"pixels_for_369_inches"`
	PixelsPerInch      float64   `json:"pixels_per_inch"`
	PhotoPath          string    `json:"photo_path"`
	Timestamp          time.Time `json:"timestamp"`
	UserNotes          string    `json:"user_notes"`
}

// PTZStatus represents the XML response structure from the camera
type PTZStatus struct {
	Position struct {
		Pan  float64 `xml:"azimuth"`
		Tilt float64 `xml:"elevation"`
		Zoom float64 `xml:"absoluteZoom"`
	} `xml:"AbsoluteHigh"`
}

// NewPixelInchesCalibrator creates a new pixel-to-inches calibration system
func NewPixelInchesCalibrator(frameWidth, frameHeight int, cameraIP, cameraPort, username, password string) *PixelInchesCalibrator {
	// Create timestamped calibration directory
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	calibDir := fmt.Sprintf("calibration/pixelinches/session_%s", timestamp)

	return &PixelInchesCalibrator{
		frameWidth:  frameWidth,
		frameHeight: frameHeight,
		calibDir:    calibDir,

		cameraIP:   cameraIP,
		cameraPort: cameraPort,
		username:   username,
		password:   password,

		calibrationData: make(map[float64]*ZoomPixelData),
		scanner:         bufio.NewScanner(os.Stdin),
	}
}

// StartPixelInchesCalibration begins the interactive pixel-to-inches calibration process
func (pic *PixelInchesCalibrator) StartPixelInchesCalibration() error {
	fmt.Printf("📏 PIXEL-TO-INCHES CALIBRATION SYSTEM\n")
	fmt.Printf("====================================\n\n")

	fmt.Printf("📐 Image dimensions: %d × %d pixels\n", pic.frameWidth, pic.frameHeight)
	fmt.Printf("📏 Reference distance: 369 inches\n")
	fmt.Printf("📸 This system will capture photos at each zoom level\n")
	fmt.Printf("📋 You'll measure pixels for the 369-inch reference distance\n\n")

	// Create directory
	if err := os.MkdirAll(pic.calibDir, 0755); err != nil {
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

	fmt.Printf("📋 SETUP INSTRUCTIONS:\n")
	fmt.Printf("1. Position your camera to view the calibration area\n")
	fmt.Printf("2. Make sure the 369-inch reference is clearly visible\n")
	fmt.Printf("3. The system will capture photos at each zoom level\n")
	fmt.Printf("4. You'll measure pixels in each photo for the 369-inch distance\n\n")

	fmt.Printf("Ready to start calibration? (y/n): ")
	if !pic.askYesNo() {
		fmt.Printf("Calibration cancelled.\n")
		return nil
	}

	// Calibrate each zoom level
	for i, zoomLevel := range zoomLevels {
		fmt.Printf("\n🎯 [%d/%d] CALIBRATING ZOOM LEVEL %.0f\n", i+1, len(zoomLevels), zoomLevel)
		fmt.Printf("=" + strings.Repeat("=", 40) + "\n")

		if err := pic.calibrateZoomLevel(zoomLevel); err != nil {
			fmt.Printf("❌ Error calibrating zoom %.0f: %v\n", zoomLevel, err)
			fmt.Printf("Continue with next zoom level? (y/n): ")
			if !pic.askYesNo() {
				return err
			}
			continue
		}

		// Show progress
		fmt.Printf("✅ Zoom %.0f calibration complete!\n", zoomLevel)

		if i < len(zoomLevels)-1 {
			fmt.Printf("Ready for next zoom level? (y/n): ")
			if !pic.askYesNo() {
				break
			}
		}
	}

	// Generate final results
	return pic.generateFinalResults()
}

// calibrateZoomLevel performs pixel-to-inches calibration for a specific zoom level
func (pic *PixelInchesCalibrator) calibrateZoomLevel(zoomLevel float64) error {
	// Set zoom level
	fmt.Printf("🔍 Setting zoom to %.0f...\n", zoomLevel)
	if err := pic.setZoomLevel(zoomLevel); err != nil {
		return fmt.Errorf("failed to set zoom: %v", err)
	}

	// Wait for zoom to stabilize
	time.Sleep(3 * time.Second)

	// Capture photo
	photoPath := filepath.Join(pic.calibDir, fmt.Sprintf("Z%.0f.jpg", zoomLevel))
	fmt.Printf("📸 Capturing photo: %s\n", photoPath)

	if err := pic.capturePhoto(photoPath); err != nil {
		return fmt.Errorf("failed to capture photo: %v", err)
	}

	fmt.Printf("✅ Photo saved: %s\n", photoPath)

	// Get pixel measurement from user
	fmt.Printf("\n📏 PIXEL MEASUREMENT REQUIRED\n")
	fmt.Printf("─────────────────────────────\n")
	fmt.Printf("1. Open the photo: %s\n", photoPath)
	fmt.Printf("2. Measure the pixel distance for your 369-inch reference\n")
	fmt.Printf("3. Enter the number of pixels below\n\n")

	var pixels int
	for {
		fmt.Printf("Enter pixels for 369 inches at zoom %.0f: ", zoomLevel)
		pic.scanner.Scan()
		input := strings.TrimSpace(pic.scanner.Text())

		var err error
		pixels, err = strconv.Atoi(input)
		if err != nil || pixels <= 0 {
			fmt.Printf("❌ Please enter a valid positive number of pixels\n")
			continue
		}
		break
	}

	// Calculate pixels per inch
	pixelsPerInch := float64(pixels) / 369.0

	// Store calibration data
	calibData := &ZoomPixelData{
		ZoomLevel:          zoomLevel,
		PixelsFor369Inches: pixels,
		PixelsPerInch:      pixelsPerInch,
		PhotoPath:          photoPath,
		Timestamp:          time.Now(),
	}

	pic.calibrationData[zoomLevel] = calibData

	// Display results for this zoom level
	fmt.Printf("\n📊 ZOOM %.0f RESULTS:\n", zoomLevel)
	fmt.Printf("   Pixels for 369 inches: %d\n", pixels)
	fmt.Printf("   Pixels per inch: %.3f\n", pixelsPerInch)
	fmt.Printf("   Photo: %s\n", photoPath)

	return nil
}

// setZoomLevel sets the camera to a specific zoom level
func (pic *PixelInchesCalibrator) setZoomLevel(zoomLevel float64) error {
	// Get current position to maintain pan/tilt
	currentPos, err := pic.getCurrentPTZPosition()
	if err != nil {
		return fmt.Errorf("failed to get current position: %v", err)
	}

	fmt.Printf("🎛️  Setting zoom to %.0f (maintaining Pan=%.1f, Tilt=%.1f)...\n",
		zoomLevel, currentPos.Pan, currentPos.Tilt)

	// Send zoom command directly via HTTP
	err = pic.sendAbsolutePositionCommand(currentPos.Pan, currentPos.Tilt, zoomLevel)
	if err != nil {
		return fmt.Errorf("failed to send zoom command: %v", err)
	}

	// Wait for movement to complete
	fmt.Printf("⏳ Waiting for zoom to complete...\n")
	time.Sleep(4 * time.Second)

	fmt.Printf("✅ Zoom set to %.0f\n", zoomLevel)
	return nil
}

// capturePhoto captures a photo from the camera at the current settings
func (pic *PixelInchesCalibrator) capturePhoto(photoPath string) error {
	// Use camera snapshot API to capture photo
	url := fmt.Sprintf("http://%s:%s/ISAPI/Streaming/channels/101/picture", pic.cameraIP, pic.cameraPort)
	uri := "/ISAPI/Streaming/channels/101/picture"

	// First request to get WWW-Authenticate header
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

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
		req, err = http.NewRequest("GET", url, nil)
		if err != nil {
			return fmt.Errorf("failed to create authenticated request: %v", err)
		}

		req.Header.Set("Authorization", pic.getDigestAuth("GET", uri, realm, nonce))

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

	// Create the photo file
	photoFile, err := os.Create(photoPath)
	if err != nil {
		return fmt.Errorf("failed to create photo file: %v", err)
	}
	defer photoFile.Close()

	// Copy the image data
	_, err = io.Copy(photoFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to save photo: %v", err)
	}

	return nil
}

// sendAbsolutePositionCommand sends a PTZ absolute position command directly via HTTP
func (pic *PixelInchesCalibrator) sendAbsolutePositionCommand(pan, tilt, zoom float64) error {
	// Create XML payload for absolute positioning
	xmlPayload := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<PTZData>
    <AbsoluteHigh>
        <elevation>%.0f</elevation>
        <azimuth>%.0f</azimuth>
        <absoluteZoom>%.0f</absoluteZoom>
    </AbsoluteHigh>
</PTZData>`, tilt, pan, zoom)

	url := fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/absolute", pic.cameraIP, pic.cameraPort)
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

		req.Header.Set("Authorization", pic.getDigestAuth("PUT", uri, realm, nonce))
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

	return nil
}

// getCurrentPTZPosition gets the current PTZ position by querying the camera directly
func (pic *PixelInchesCalibrator) getCurrentPTZPosition() (ptz.PTZPosition, error) {
	// Query the camera directly for current status
	status, err := pic.queryPTZStatus()
	if err != nil {
		return ptz.PTZPosition{}, fmt.Errorf("failed to query PTZ status: %v", err)
	}

	return status, nil
}

// queryPTZStatus directly queries the camera for its current PTZ status
func (pic *PixelInchesCalibrator) queryPTZStatus() (ptz.PTZPosition, error) {
	// Query the camera directly for status using digest auth
	url := fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/status", pic.cameraIP, pic.cameraPort)
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
		req.Header.Set("Authorization", pic.getDigestAuth("GET", uri, realm, nonce))
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
		// If XML parsing fails, try fallback parsing
		xmlStr := string(body)
		pos := ptz.PTZPosition{
			Pan:  pic.extractValueFromXML(xmlStr, "azimuth"),
			Tilt: pic.extractValueFromXML(xmlStr, "elevation"),
			Zoom: pic.extractValueFromXML(xmlStr, "absoluteZoom"),
		}
		return pos, nil
	}

	// Successfully parsed XML
	pos := ptz.PTZPosition{
		Pan:  status.Position.Pan,
		Tilt: status.Position.Tilt,
		Zoom: status.Position.Zoom,
	}

	return pos, nil
}

// extractValueFromXML extracts a numeric value from XML using simple string parsing
func (pic *PixelInchesCalibrator) extractValueFromXML(xmlStr, tagName string) float64 {
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

// getDigestAuth creates digest authentication header
func (pic *PixelInchesCalibrator) getDigestAuth(method, uri, realm, nonce string) string {
	// Proper digest auth calculation using MD5
	ha1 := fmt.Sprintf("%x", md5.Sum([]byte(fmt.Sprintf("%s:%s:%s", pic.username, realm, pic.password))))
	ha2 := fmt.Sprintf("%x", md5.Sum([]byte(fmt.Sprintf("%s:%s", method, uri))))
	response := fmt.Sprintf("%x", md5.Sum([]byte(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2))))

	return fmt.Sprintf("Digest username=\"%s\", realm=\"%s\", nonce=\"%s\", uri=\"%s\", response=\"%s\"",
		pic.username, realm, nonce, uri, response)
}

// askYesNo asks user a yes/no question
func (pic *PixelInchesCalibrator) askYesNo() bool {
	pic.scanner.Scan()
	response := strings.ToLower(strings.TrimSpace(pic.scanner.Text()))
	return response == "y" || response == "yes"
}

// generateFinalResults creates the final calibration output
func (pic *PixelInchesCalibrator) generateFinalResults() error {
	fmt.Printf("\n📊 GENERATING FINAL CALIBRATION RESULTS\n")
	fmt.Printf("=====================================\n")

	// Display summary table
	pic.displayCalibrationTable()

	// Convert calibration data to slice for JSON compatibility
	var calibrationList []*ZoomPixelData
	for _, data := range pic.calibrationData {
		calibrationList = append(calibrationList, data)
	}

	// Create results data structure
	results := map[string]interface{}{
		"calibration_type":   "pixel_inches_calibration",
		"timestamp":          time.Now(),
		"frame_dimensions":   map[string]int{"width": pic.frameWidth, "height": pic.frameHeight},
		"reference_distance": 369,
		"reference_unit":     "inches",
		"zoom_levels_tested": len(pic.calibrationData),
		"calibration_data":   calibrationList,
		"method_description": "Manual pixel measurement for 369-inch reference distance at multiple zoom levels",
		"session_directory":  pic.calibDir,
	}

	// Save to JSON file in root directory
	rootResultsPath := "pixels-inches-cal.json"
	jsonData, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal results: %v", err)
	}

	if err := os.WriteFile(rootResultsPath, jsonData, 0644); err != nil {
		return fmt.Errorf("failed to save results: %v", err)
	}

	fmt.Printf("✅ Calibration results saved to: %s\n", rootResultsPath)

	// Also save a copy in the session directory
	sessionResultsPath := filepath.Join(pic.calibDir, "pixel-inches-results.json")
	if err := os.WriteFile(sessionResultsPath, jsonData, 0644); err != nil {
		fmt.Printf("⚠️  Warning: failed to save session copy: %v\n", err)
	} else {
		fmt.Printf("📋 Session copy saved to: %s\n", sessionResultsPath)
	}

	return nil
}

// displayCalibrationTable shows the final calibration table
func (pic *PixelInchesCalibrator) displayCalibrationTable() {
	fmt.Printf("📋 FINAL PIXEL-TO-INCHES CALIBRATION TABLE\n")
	fmt.Printf("┌────────┬─────────────────┬─────────────────┐\n")
	fmt.Printf("│  Zoom  │ Pixels (369in)  │ Pixels per Inch │\n")
	fmt.Printf("├────────┼─────────────────┼─────────────────┤\n")

	// Get sorted zoom levels
	var zoomLevels []float64
	for zoom := range pic.calibrationData {
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
		data := pic.calibrationData[zoom]
		fmt.Printf("│ %6.0f │ %15d │ %15.3f │\n",
			zoom, data.PixelsFor369Inches, data.PixelsPerInch)
	}
	fmt.Printf("└────────┴─────────────────┴─────────────────┘\n")

	// Calculate scaling analysis
	if len(zoomLevels) >= 2 {
		firstData := pic.calibrationData[zoomLevels[0]]
		lastData := pic.calibrationData[zoomLevels[len(zoomLevels)-1]]

		pixelScaling := float64(lastData.PixelsFor369Inches) / float64(firstData.PixelsFor369Inches)
		ppiScaling := lastData.PixelsPerInch / firstData.PixelsPerInch

		fmt.Printf("\n📈 SCALING ANALYSIS:\n")
		fmt.Printf("   Pixel count scaling: %.2fx from zoom %.0f to %.0f\n",
			pixelScaling, zoomLevels[0], zoomLevels[len(zoomLevels)-1])
		fmt.Printf("   Pixels-per-inch scaling: %.2fx from zoom %.0f to %.0f\n",
			ppiScaling, zoomLevels[0], zoomLevels[len(zoomLevels)-1])

		// Calculate average pixels per inch across all zoom levels
		var totalPPI float64
		for _, data := range pic.calibrationData {
			totalPPI += data.PixelsPerInch
		}
		avgPPI := totalPPI / float64(len(pic.calibrationData))
		fmt.Printf("   Average pixels per inch: %.3f\n", avgPPI)
	}
}

// main function for standalone execution
func main() {
	fmt.Printf("📏 PIXEL-TO-INCHES CALIBRATOR\n")
	fmt.Printf("=============================\n\n")

	// Camera configuration (adjust these for your setup)
	cameraIP := "192.168.0.59" // Your camera IP
	cameraPort := "80"         // Update with your camera port
	username := "admin"        // Update with your username
	password := "password1"    // Update with your password

	// Image dimensions (2688x1520 for typical Hikvision)
	frameWidth := 2688
	frameHeight := 1520

	fmt.Printf("📷 Camera: %s:%s\n", cameraIP, cameraPort)
	fmt.Printf("📏 Frame: %dx%d pixels\n", frameWidth, frameHeight)
	fmt.Printf("👤 Auth: %s / %s\n", username, password)
	fmt.Printf("📐 Reference: 369 inches\n\n")

	// Create pixel-inches calibrator
	calibrator := NewPixelInchesCalibrator(frameWidth, frameHeight, cameraIP, cameraPort, username, password)

	// Start pixel-inches calibration
	if err := calibrator.StartPixelInchesCalibration(); err != nil {
		fmt.Printf("❌ Calibration failed: %v\n", err)
		return
	}

	fmt.Printf("🎉 Pixel-to-inches calibration completed successfully!\n")
}
