package main

import (
	"crypto/md5"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// PTZPosition represents the camera's current position
type PTZPosition struct {
	Pan  float64 `xml:"azimuth"`
	Tilt float64 `xml:"elevation"`
	Zoom float64 `xml:"absoluteZoom"`
}

// PTZStatus from Hikvision ISAPI
type PTZStatus struct {
	Position PTZPosition `xml:"AbsoluteHigh"`
}

// PTZLimits defines the safe operating range
type PTZLimits struct {
	MinPan  float64
	MaxPan  float64
	MinTilt float64
	MaxTilt float64
	MinZoom float64
	MaxZoom float64
}

// ScanPosition from scanning.json
type ScanPosition struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Position struct {
		Pan  float64 `json:"Pan"`
		Tilt float64 `json:"Tilt"`
		Zoom float64 `json:"Zoom"`
	} `json:"position"`
	DwellTimeSeconds int `json:"dwell_time_seconds"`
}

// ScanPattern from scanning.json
type ScanPattern struct {
	Name      string         `json:"name"`
	Positions []ScanPosition `json:"positions"`
}

// CameraController talks directly to the Hikvision camera
type CameraController struct {
	ip       string
	port     string
	user     string
	pass     string
	client   *http.Client
	mu       sync.Mutex
	limits   PTZLimits
	presets  map[string]PTZPosition // name -> position
	panStep  float64                // Units to move per #up/#down etc
	tiltStep float64
}

// NewCameraController creates a controller for the Hikvision camera
func NewCameraController(ip, port, user, pass string) *CameraController {
	cc := &CameraController{
		ip:   ip,
		port: port,
		user: user,
		pass: pass,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
		limits: PTZLimits{
			MinPan:  900,
			MaxPan:  2550,
			MinTilt: 0,
			MaxTilt: 900,
			MinZoom: 10,
			MaxZoom: 120,
		},
		presets:  make(map[string]PTZPosition),
		panStep:  80, // ~5% of total range (1650 units)
		tiltStep: 30, // Small tilt adjustments
	}
	return cc
}

// LoadPresets loads preset positions from scanning.json
func (cc *CameraController) LoadPresets(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read %s: %v", path, err)
	}

	var pattern ScanPattern
	if err := json.Unmarshal(data, &pattern); err != nil {
		return fmt.Errorf("failed to parse %s: %v", path, err)
	}

	for _, pos := range pattern.Positions {
		name := strings.ToLower(strings.ReplaceAll(pos.Name, " ", ""))
		cc.presets[name] = PTZPosition{
			Pan:  pos.Position.Pan,
			Tilt: pos.Position.Tilt,
			Zoom: pos.Position.Zoom,
		}
		log.Printf("[PTZ] Loaded preset: %s -> Pan=%.0f Tilt=%.0f Zoom=%.0f",
			pos.Name, pos.Position.Pan, pos.Position.Tilt, pos.Position.Zoom)
	}

	log.Printf("[PTZ] Loaded %d presets from %s", len(cc.presets), path)
	return nil
}

// GetPosition reads the current PTZ position from the camera
func (cc *CameraController) GetPosition() (PTZPosition, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	url := fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/status", cc.ip, cc.port)
	uri := "/ISAPI/PTZCtrl/channels/1/status"

	body, err := cc.doRequest("GET", url, uri, "")
	if err != nil {
		return PTZPosition{}, fmt.Errorf("failed to get position: %v", err)
	}

	var status PTZStatus
	if err := xml.Unmarshal(body, &status); err != nil {
		return PTZPosition{}, fmt.Errorf("failed to parse position: %v", err)
	}

	return status.Position, nil
}

// SendAbsolute sends an absolute position command to the camera
func (cc *CameraController) SendAbsolute(pan, tilt, zoom float64) error {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	// Clamp to limits
	pan = clampValue(pan, cc.limits.MinPan, cc.limits.MaxPan)
	tilt = clampValue(tilt, cc.limits.MinTilt, cc.limits.MaxTilt)
	zoom = clampValue(zoom, cc.limits.MinZoom, cc.limits.MaxZoom)

	// Round to integers
	pan = math.Round(pan)
	tilt = math.Round(tilt)
	zoom = math.Round(zoom)

	xmlPayload := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<PTZData>
    <AbsoluteHigh>
        <elevation>%.0f</elevation>
        <azimuth>%.0f</azimuth>
        <absoluteZoom>%.0f</absoluteZoom>
    </AbsoluteHigh>
</PTZData>`, tilt, pan, zoom)

	url := fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/absolute", cc.ip, cc.port)
	uri := "/ISAPI/PTZCtrl/channels/1/absolute"

	_, err := cc.doRequest("PUT", url, uri, xmlPayload)
	if err != nil {
		return fmt.Errorf("failed to send position: %v", err)
	}

	log.Printf("[PTZ] Sent: Pan=%.0f Tilt=%.0f Zoom=%.0f", pan, tilt, zoom)
	return nil
}

// MoveRelative moves the camera relative to its current position
func (cc *CameraController) MoveRelative(panDelta, tiltDelta, zoomDelta float64) error {
	pos, err := cc.GetPosition()
	if err != nil {
		return err
	}

	newPan := pos.Pan + panDelta
	newTilt := pos.Tilt + tiltDelta
	newZoom := pos.Zoom + zoomDelta

	log.Printf("[PTZ] Relative move: Pan %.0f%+.0f Tilt %.0f%+.0f Zoom %.0f%+.0f",
		pos.Pan, panDelta, pos.Tilt, tiltDelta, pos.Zoom, zoomDelta)

	return cc.SendAbsolute(newPan, newTilt, newZoom)
}

// GoToPreset moves to a named preset position
func (cc *CameraController) GoToPreset(name string) error {
	pos, ok := cc.presets[name]
	if !ok {
		return fmt.Errorf("unknown preset: %s", name)
	}
	log.Printf("[PTZ] Going to preset: %s", name)
	return cc.SendAbsolute(pos.Pan, pos.Tilt, pos.Zoom)
}

// SetZoomLevel sets zoom to a 1-10 scale (maps to 10-120 camera units)
func (cc *CameraController) SetZoomLevel(level int) error {
	if level < 1 || level > 10 {
		return fmt.Errorf("zoom level must be 1-10, got %d", level)
	}

	// Map 1-10 to camera's 10-120 range
	// level 1 = zoom 10, level 10 = zoom 120
	zoom := float64(10 + (level-1)*((120-10)/9))

	pos, err := cc.GetPosition()
	if err != nil {
		return err
	}

	log.Printf("[PTZ] Setting zoom level %d (camera: %.0f)", level, zoom)
	return cc.SendAbsolute(pos.Pan, pos.Tilt, zoom)
}

// doRequest performs an HTTP request with Hikvision digest auth
func (cc *CameraController) doRequest(method, url, uri, payload string) ([]byte, error) {
	var bodyReader io.Reader
	if payload != "" {
		bodyReader = strings.NewReader(payload)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	if payload != "" {
		req.Header.Set("Content-Type", "application/xml")
	}

	// First request - expect 401 with digest challenge
	resp, err := cc.client.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode == 200 {
		return body, nil
	}

	if resp.StatusCode != 401 {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Parse WWW-Authenticate header
	authHeader := resp.Header.Get("WWW-Authenticate")
	if authHeader == "" {
		return nil, fmt.Errorf("no WWW-Authenticate header")
	}

	realm, nonce := parseDigestChallenge(authHeader)
	if realm == "" || nonce == "" {
		return nil, fmt.Errorf("invalid digest challenge: %s", authHeader)
	}

	// Retry with digest auth
	if payload != "" {
		bodyReader = strings.NewReader(payload)
	}
	req, err = http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	if payload != "" {
		req.Header.Set("Content-Type", "application/xml")
	}
	req.Header.Set("Authorization", cc.digestAuth(method, uri, realm, nonce))

	resp, err = cc.client.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("auth failed, status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// digestAuth creates a Digest Authentication header
func (cc *CameraController) digestAuth(method, uri, realm, nonce string) string {
	ha1 := md5sum(fmt.Sprintf("%s:%s:%s", cc.user, realm, cc.pass))
	ha2 := md5sum(fmt.Sprintf("%s:%s", method, uri))
	response := md5sum(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2))

	return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		cc.user, realm, nonce, uri, response)
}

func md5sum(s string) string {
	h := md5.New()
	h.Write([]byte(s))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func parseDigestChallenge(header string) (realm, nonce string) {
	// Remove "Digest " prefix
	header = strings.TrimPrefix(header, "Digest ")

	parts := strings.Split(header, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "realm=") {
			realm = strings.Trim(strings.TrimPrefix(part, "realm="), "\"")
		} else if strings.HasPrefix(part, "nonce=") {
			nonce = strings.Trim(strings.TrimPrefix(part, "nonce="), "\"")
		}
	}
	return
}

func clampValue(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
