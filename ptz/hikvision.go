package ptz

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Use the debug function from camera_state.go (they share the same package)

// PTZCommand represents a PTZ command to be sent to the camera
type PTZCommand struct {
	Command  string
	Reason   string
	Duration time.Duration
	// Add absolute position fields
	AbsolutePan  *float64 // Optional absolute pan position
	AbsoluteTilt *float64 // Optional absolute tilt position
	AbsoluteZoom *float64 // Optional absolute zoom position
}

// Controller defines the interface for PTZ camera controllers
type Controller interface {
	Start()
	Stop()
	SendCommand(cmd PTZCommand) bool
	GetCurrentPosition() PTZPosition
	GetFrameWidth() int
	GetFrameHeight() int
	SetFrameDimensions(width, height int)
}

// PTZPosition represents the current position of the camera
type PTZPosition struct {
	Pan  float64 `xml:"azimuth"`      // 0-3590 degrees
	Tilt float64 `xml:"elevation"`    // 0-900 degrees
	Zoom float64 `xml:"absoluteZoom"` // 10-120 zoom levels
}

// PTZStatus represents the current status of the camera
type PTZStatus struct {
	Position PTZPosition `xml:"AbsoluteHigh"`
}

// HikvisionController implements the Controller interface for Hikvision cameras
type HikvisionController struct {
	ip              string
	port            string
	user            string
	pass            string
	commandChan     chan PTZCommand
	commandLock     chan struct{}
	lastCommandEnd  time.Time
	activeCommand   string
	client          *http.Client
	currentPos      PTZPosition
	statusChan      chan PTZPosition
	OnPresetArrived func(presetName string)
	frameWidth      int // Actual frame width
	frameHeight     int // Actual frame height
}

// NewHikvisionController creates a new Hikvision PTZ controller
func NewHikvisionController(ip, port, user, pass string) Controller {
	return &HikvisionController{
		ip:          ip,
		port:        port,
		user:        user,
		pass:        pass,
		commandChan: make(chan PTZCommand, 10),
		commandLock: make(chan struct{}, 1),
		statusChan:  make(chan PTZPosition, 10),
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// Start begins processing PTZ commands
func (c *HikvisionController) Start() {
	// Get initial PTZ status
	status, err := c.getStatus()
	if err != nil {
		debugMsg("PTZ_WARN", fmt.Sprintf("Failed to get initial status: %v", err))
	} else {
		c.currentPos = status.Position
		debugMsg("PTZ", fmt.Sprintf("Initial position - Pan: %.2f, Tilt: %.2f, Zoom: Z%03d",
			c.currentPos.Pan, c.currentPos.Tilt, int((c.currentPos.Zoom-1)/10)+1))
	}

	go c.processCommands()
	go c.monitorStatus()
}

// Stop stops the PTZ controller
func (c *HikvisionController) Stop() {
	close(c.commandChan)
}

// SendCommand sends a PTZ command to the camera
func (c *HikvisionController) SendCommand(cmd PTZCommand) bool {
	select {
	case c.commandChan <- cmd:
		return true // Command sent successfully
	default:
		return false // Channel full, skip command
	}
}

// processCommands handles the PTZ command processing loop
func (c *HikvisionController) processCommands() {
	for cmd := range c.commandChan {
		// Acquire command lock to ensure sequential processing
		c.commandLock <- struct{}{} // Block until we can acquire lock

		debugMsg("PTZ", fmt.Sprintf("Executing command: %s (Reason: %s)", cmd.Command, cmd.Reason))

		// Handle absolute positioning command
		if cmd.Command == "absolutePosition" {
			// Use camera values directly
			targetPos := PTZPosition{
				Pan:  *cmd.AbsolutePan,
				Tilt: *cmd.AbsoluteTilt,
				Zoom: *cmd.AbsoluteZoom,
			}

			// NOTE: Limit checking is now handled in Camera State Manager
			// All commands are validated there before reaching the PTZ controller

			// Create XML payload for absolute positioning
			xmlPayload := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<PTZData>
    <AbsoluteHigh>
        <elevation>%.0f</elevation>
        <azimuth>%.0f</azimuth>
        <absoluteZoom>%.0f</absoluteZoom>
    </AbsoluteHigh>
</PTZData>`, targetPos.Tilt, targetPos.Pan, targetPos.Zoom)

			// Debug print
			debugMsg("PTZ_DEBUG", fmt.Sprintf("Absolute position command - Pan: %.0f, Tilt: %.0f, Zoom: %.0f",
				targetPos.Pan, targetPos.Tilt, targetPos.Zoom))

			url := fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/absolute", c.ip, c.port)
			uri := "/ISAPI/PTZCtrl/channels/1/absolute"

			// Send command with retry
			var lastErr error
			for retries := 0; retries < 3; retries++ {
				body := strings.NewReader(xmlPayload)
				req, err := http.NewRequest("PUT", url, body)
				if err != nil {
					lastErr = fmt.Errorf("failed to create request: %v", err)
					continue
				}

				req.Header.Set("Content-Type", "application/xml")
				req.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
				req.Header.Set("User-Agent", "Go-http-client/1.1")
				req.ContentLength = int64(len(xmlPayload))

				resp, err := c.client.Do(req)
				if err != nil {
					lastErr = fmt.Errorf("request failed: %v", err)
					continue
				}

				bodyBytes, _ := io.ReadAll(resp.Body)
				resp.Body.Close()

				if resp.StatusCode == 200 {
					debugMsg("PTZ_DEBUG", "Absolute position command successful")
					break
				}

				if resp.StatusCode == 401 {
					authHeader := resp.Header.Get("WWW-Authenticate")
					if authHeader == "" {
						lastErr = fmt.Errorf("no WWW-Authenticate header in response")
						continue
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
						lastErr = fmt.Errorf("invalid WWW-Authenticate header: %s", authHeader)
						continue
					}

					// Create new request with digest authentication
					body = strings.NewReader(xmlPayload)
					req, err = http.NewRequest("PUT", url, body)
					if err != nil {
						lastErr = fmt.Errorf("failed to create request: %v", err)
						continue
					}

					req.Header.Set("Authorization", c.getDigestAuth("PUT", uri, realm, nonce))
					req.Header.Set("Content-Type", "application/xml")
					req.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
					req.Header.Set("User-Agent", "Go-http-client/1.1")
					req.ContentLength = int64(len(xmlPayload))

					resp, err = c.client.Do(req)
					if err != nil {
						lastErr = fmt.Errorf("request failed: %v", err)
						continue
					}

					bodyBytes, _ = io.ReadAll(resp.Body)
					resp.Body.Close()

					if resp.StatusCode == 200 {
						debugMsg("PTZ_DEBUG", "Absolute position command successful")
						break
					}
				}

				lastErr = fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(bodyBytes))
				debugMsg("PTZ_WARN", fmt.Sprintf("Command failed (attempt %d/3): %v", retries+1, lastErr))
				time.Sleep(100 * time.Millisecond)
			}

			if lastErr != nil {
				debugMsg("PTZ_ERROR", fmt.Sprintf("All command retries failed: %v", lastErr))
				<-c.commandLock
				continue
			}

			c.activeCommand = "absolutePosition"
			c.lastCommandEnd = time.Now()
			<-c.commandLock
			continue
		}

		// Handle preset commands
		if strings.HasPrefix(cmd.Command, "ISAPI/PTZCtrl/channels/1/presets/") {
			// Extract preset name from the URL
			parts := strings.Split(cmd.Command, "name=")
			if len(parts) != 2 {
				debugMsg("PTZ_ERROR", fmt.Sprintf("Invalid preset command format: %s", cmd.Command))
				<-c.commandLock
				continue
			}
			presetName := parts[1]

			if err := c.sendPresetCommand(presetName); err != nil {
				debugMsg("PTZ_ERROR", fmt.Sprintf("Failed to send preset command: %v", err))
			}
			c.lastCommandEnd = time.Now()
			<-c.commandLock
			continue
		}

		// Handle zoom commands differently
		if cmd.Command == "zoomIn" || cmd.Command == "zoomOut" {
			// Get current zoom value (10-120)
			currentZoom := c.currentPos.Zoom
			var newZoom float64

			if cmd.Command == "zoomIn" {
				// Move to next zoom level
				newZoom = math.Min(currentZoom+10, 120)
			} else {
				// Move to previous zoom level
				newZoom = math.Max(currentZoom-10, 10)
			}

			debugMsg("PTZ_DEBUG", fmt.Sprintf("Zoom command - Current zoom: %.0f, Target zoom: %.0f",
				currentZoom, newZoom))

			// Create XML payload for absolute positioning
			xmlPayload := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><PTZData><AbsoluteHigh><azimuth>%.0f</azimuth><elevation>%.0f</elevation><absoluteZoom>%.0f</absoluteZoom></AbsoluteHigh></PTZData>`,
				c.currentPos.Pan, c.currentPos.Tilt, newZoom)

			debugMsg("PTZ_DEBUG", fmt.Sprintf("Sending zoom command with payload:\n%s", xmlPayload))

			url := fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/absolute", c.ip, c.port)
			uri := "/ISAPI/PTZCtrl/channels/1/absolute"

			// First request to get the WWW-Authenticate header
			body := strings.NewReader(xmlPayload)
			req, err := http.NewRequest("PUT", url, body)
			if err != nil {
				debugMsg("PTZ_ERROR", fmt.Sprintf("Failed to create request: %v", err))
				<-c.commandLock
				continue
			}

			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
			req.Header.Set("User-Agent", "Go-http-client/1.1")
			req.ContentLength = int64(len(xmlPayload))

			// Send command with retry
			var lastErr error
			for retries := 0; retries < 3; retries++ {
				// Create a new body reader for each retry
				body = strings.NewReader(xmlPayload)
				req.Body = io.NopCloser(body)
				req.ContentLength = int64(len(xmlPayload))

				resp, err := c.client.Do(req)
				if err != nil {
					lastErr = fmt.Errorf("request failed: %v", err)
					debugMsg("PTZ_WARN", fmt.Sprintf("Command failed (attempt %d/3): %v", retries+1, err))
					time.Sleep(100 * time.Millisecond)
					continue
				}

				// Read and close the body
				bodyBytes, _ := io.ReadAll(resp.Body)
				resp.Body.Close()

				if resp.StatusCode == 200 {
					debugMsg("PTZ_DEBUG", "Zoom command successful")
					break
				}

				// If we got a 401, extract the digest authentication parameters
				if resp.StatusCode == 401 {
					authHeader := resp.Header.Get("WWW-Authenticate")
					if authHeader == "" {
						lastErr = fmt.Errorf("no WWW-Authenticate header in response")
						continue
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
						lastErr = fmt.Errorf("invalid WWW-Authenticate header: %s", authHeader)
						continue
					}

					// Create new request with digest authentication
					body = strings.NewReader(xmlPayload)
					req, err = http.NewRequest("PUT", url, body)
					if err != nil {
						lastErr = fmt.Errorf("failed to create request: %v", err)
						continue
					}

					req.Header.Set("Authorization", c.getDigestAuth("PUT", uri, realm, nonce))
					req.Header.Set("Content-Type", "application/xml")
					req.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
					req.Header.Set("User-Agent", "Go-http-client/1.1")
					req.ContentLength = int64(len(xmlPayload))

					resp, err = c.client.Do(req)
					if err != nil {
						lastErr = fmt.Errorf("request failed: %v", err)
						continue
					}

					bodyBytes, _ = io.ReadAll(resp.Body)
					resp.Body.Close()

					if resp.StatusCode == 200 {
						debugMsg("PTZ_DEBUG", "Zoom command successful")
						break
					}
				}

				lastErr = fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(bodyBytes))
				debugMsg("PTZ_WARN", fmt.Sprintf("Command failed (attempt %d/3): %v", retries+1, lastErr))
				time.Sleep(100 * time.Millisecond)
			}

			if lastErr != nil {
				debugMsg("PTZ_ERROR", fmt.Sprintf("All command retries failed: %v", lastErr))
				<-c.commandLock
				continue
			}

			// Wait for the command duration
			time.Sleep(cmd.Duration)

			// Send stop command after duration
			stopXML := `<?xml version="1.0" encoding="UTF-8"?>
<PTZData>
    <AbsoluteHigh>
        <azimuth>0</azimuth>
        <elevation>0</elevation>
        <absoluteZoom>0</absoluteZoom>
    </AbsoluteHigh>
</PTZData>`

			stopBody := strings.NewReader(stopXML)
			stopReq, err := http.NewRequest("PUT", url, stopBody)
			if err != nil {
				debugMsg("PTZ_ERROR", fmt.Sprintf("Failed to create stop request: %v", err))
				<-c.commandLock
				continue
			}

			stopReq.Header.Set("Content-Type", "application/xml")
			stopReq.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
			stopReq.Header.Set("User-Agent", "Go-http-client/1.1")
			stopReq.ContentLength = int64(len(stopXML))

			stopResp, err := c.client.Do(stopReq)
			if err != nil {
				debugMsg("PTZ_ERROR", fmt.Sprintf("Failed to send stop command: %v", err))
			} else {
				stopResp.Body.Close()
			}

			c.activeCommand = cmd.Command
			c.lastCommandEnd = time.Now()
			<-c.commandLock
			continue
		}

		// Handle other movement commands (pan/tilt)
		if cmd.Command == "ptzMoveLeft" || cmd.Command == "ptzMoveRight" {
			// Get current pan value (0-3590)
			currentPan := c.currentPos.Pan
			var newPan float64
			step := 10.0 // Move by 10 degrees per command
			if cmd.Command == "ptzMoveLeft" {
				newPan = math.Max(currentPan-step, 0)
			} else {
				newPan = math.Min(currentPan+step, 3590)
			}

			// Create XML payload for absolute positioning
			xmlPayload := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><PTZData><AbsoluteHigh><azimuth>%.0f</azimuth><elevation>%.0f</elevation><absoluteZoom>%.0f</absoluteZoom></AbsoluteHigh></PTZData>`,
				newPan, c.currentPos.Tilt, c.currentPos.Zoom)

			debugMsg("PTZ_DEBUG", fmt.Sprintf("Pan command - Current P: %.0f, Target P: %.0f",
				currentPan, newPan))

			url := fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/absolute", c.ip, c.port)
			uri := "/ISAPI/PTZCtrl/channels/1/absolute"
			// First request to get the WWW-Authenticate header
			body := strings.NewReader(xmlPayload)
			req, err := http.NewRequest("PUT", url, body)
			if err != nil {
				debugMsg("PTZ_ERROR", fmt.Sprintf("Failed to create request: %v", err))
				<-c.commandLock
				continue
			}
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
			req.Header.Set("User-Agent", "Go-http-client/1.1")
			req.ContentLength = int64(len(xmlPayload))
			// Send command with retry
			var lastErr error
			for retries := 0; retries < 3; retries++ {
				body = strings.NewReader(xmlPayload)
				req.Body = io.NopCloser(body)
				req.ContentLength = int64(len(xmlPayload))
				resp, err := c.client.Do(req)
				if err != nil {
					lastErr = fmt.Errorf("request failed: %v", err)
					debugMsg("PTZ_WARN", fmt.Sprintf("Command failed (attempt %d/3): %v", retries+1, err))
					time.Sleep(100 * time.Millisecond)
					continue
				}
				bodyBytes, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 200 {
					debugMsg("PTZ_DEBUG", "Pan command successful")
					break
				}
				if resp.StatusCode == 401 {
					authHeader := resp.Header.Get("WWW-Authenticate")
					if authHeader == "" {
						lastErr = fmt.Errorf("no WWW-Authenticate header in response")
						continue
					}
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
						lastErr = fmt.Errorf("invalid WWW-Authenticate header: %s", authHeader)
						continue
					}
					body = strings.NewReader(xmlPayload)
					req, err = http.NewRequest("PUT", url, body)
					if err != nil {
						lastErr = fmt.Errorf("failed to create request: %v", err)
						continue
					}
					req.Header.Set("Authorization", c.getDigestAuth("PUT", uri, realm, nonce))
					req.Header.Set("Content-Type", "application/xml")
					req.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
					req.Header.Set("User-Agent", "Go-http-client/1.1")
					req.ContentLength = int64(len(xmlPayload))
					resp, err = c.client.Do(req)
					if err != nil {
						lastErr = fmt.Errorf("request failed: %v", err)
						continue
					}
					bodyBytes, _ = io.ReadAll(resp.Body)
					resp.Body.Close()
					if resp.StatusCode == 200 {
						debugMsg("PTZ_DEBUG", "Pan command successful")
						break
					}
				}
				lastErr = fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(bodyBytes))
				debugMsg("PTZ_WARN", fmt.Sprintf("Command failed (attempt %d/3): %v", retries+1, lastErr))
				time.Sleep(100 * time.Millisecond)
			}
			if lastErr != nil {
				debugMsg("PTZ_ERROR", fmt.Sprintf("All command retries failed: %v", lastErr))
				<-c.commandLock
				continue
			}
			c.activeCommand = cmd.Command
			c.lastCommandEnd = time.Now()
			<-c.commandLock
			continue
		}

		// Handle other movement commands (pan/tilt)
		if cmd.Command == "ptzMoveUp" || cmd.Command == "ptzMoveDown" {
			// Get current tilt value (0-900)
			currentTilt := c.currentPos.Tilt
			var newTilt float64
			step := 10.0 // Move by 10 degrees per command
			if cmd.Command == "ptzMoveUp" {
				newTilt = math.Min(currentTilt+step, 900) // Camera hardware maximum
			} else {
				newTilt = math.Max(currentTilt-step, 0) // Camera hardware minimum
			}

			debugMsg("PTZ_DEBUG", fmt.Sprintf("Tilt command - Current T: %.0f, Target T: %.0f",
				currentTilt, newTilt))

			// Create XML payload for absolute positioning
			xmlPayload := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><PTZData><AbsoluteHigh><azimuth>%.0f</azimuth><elevation>%.0f</elevation><absoluteZoom>%.0f</absoluteZoom></AbsoluteHigh></PTZData>`,
				c.currentPos.Pan, newTilt, c.currentPos.Zoom)

			url := fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/absolute", c.ip, c.port)
			uri := "/ISAPI/PTZCtrl/channels/1/absolute"
			// First request to get the WWW-Authenticate header
			body := strings.NewReader(xmlPayload)
			req, err := http.NewRequest("PUT", url, body)
			if err != nil {
				debugMsg("PTZ_ERROR", fmt.Sprintf("Failed to create request: %v", err))
				<-c.commandLock
				continue
			}
			req.Header.Set("Content-Type", "application/xml")
			req.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
			req.Header.Set("User-Agent", "Go-http-client/1.1")
			req.ContentLength = int64(len(xmlPayload))
			// Send command with retry
			var lastErr error
			for retries := 0; retries < 3; retries++ {
				body = strings.NewReader(xmlPayload)
				req.Body = io.NopCloser(body)
				req.ContentLength = int64(len(xmlPayload))
				resp, err := c.client.Do(req)
				if err != nil {
					lastErr = fmt.Errorf("request failed: %v", err)
					debugMsg("PTZ_WARN", fmt.Sprintf("Command failed (attempt %d/3): %v", retries+1, err))
					time.Sleep(100 * time.Millisecond)
					continue
				}
				bodyBytes, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 200 {
					debugMsg("PTZ_DEBUG", "Tilt command successful")
					break
				}
				if resp.StatusCode == 401 {
					authHeader := resp.Header.Get("WWW-Authenticate")
					if authHeader == "" {
						lastErr = fmt.Errorf("no WWW-Authenticate header in response")
						continue
					}
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
						lastErr = fmt.Errorf("invalid WWW-Authenticate header: %s", authHeader)
						continue
					}
					body = strings.NewReader(xmlPayload)
					req, err = http.NewRequest("PUT", url, body)
					if err != nil {
						lastErr = fmt.Errorf("failed to create request: %v", err)
						continue
					}
					req.Header.Set("Authorization", c.getDigestAuth("PUT", uri, realm, nonce))
					req.Header.Set("Content-Type", "application/xml")
					req.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
					req.Header.Set("User-Agent", "Go-http-client/1.1")
					req.ContentLength = int64(len(xmlPayload))
					resp, err = c.client.Do(req)
					if err != nil {
						lastErr = fmt.Errorf("request failed: %v", err)
						continue
					}
					bodyBytes, _ = io.ReadAll(resp.Body)
					resp.Body.Close()
					if resp.StatusCode == 200 {
						debugMsg("PTZ_DEBUG", "Tilt command successful")
						break
					}
				}
				lastErr = fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(bodyBytes))
				debugMsg("PTZ_WARN", fmt.Sprintf("Command failed (attempt %d/3): %v", retries+1, lastErr))
				time.Sleep(100 * time.Millisecond)
			}
			if lastErr != nil {
				debugMsg("PTZ_ERROR", fmt.Sprintf("All command retries failed: %v", lastErr))
				<-c.commandLock
				continue
			}
			c.activeCommand = cmd.Command
			c.lastCommandEnd = time.Now()
			<-c.commandLock
			continue
		}

		// Handle other movement commands (pan/tilt)
		hikCmd := convertToHikvisionCommand(cmd.Command)
		if hikCmd == "" {
			debugMsg("PTZ_ERROR", fmt.Sprintf("Unknown command: %s", cmd.Command))
			<-c.commandLock
			continue
		}

		// Calculate relative movement based on current position
		panSpeed, tiltSpeed, _ := calculateRelativeSpeed(c.currentPos, hikCmd)

		// Create XML payload for continuous movement
		xmlPayload := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<PTZData>
    <Continuous>
        <pan>%.2f</pan>
        <tilt>%.2f</tilt>
        <zoom>0</zoom>
    </Continuous>
</PTZData>`, panSpeed, tiltSpeed)

		// Create request with proper body
		body := strings.NewReader(xmlPayload)
		req, err := http.NewRequest("PUT", fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/continuous", c.ip, c.port), body)
		if err != nil {
			debugMsg("PTZ_ERROR", fmt.Sprintf("Failed to create request: %v", err))
			<-c.commandLock
			continue
		}

		// Set headers
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
		req.Header.Set("User-Agent", "Go-http-client/1.1")
		req.ContentLength = int64(len(xmlPayload))

		// Send command with retry
		var resp *http.Response
		for retries := 0; retries < 3; retries++ {
			// Create a new body reader for each retry
			body = strings.NewReader(xmlPayload)
			req.Body = io.NopCloser(body)
			req.ContentLength = int64(len(xmlPayload))

			resp, err = c.client.Do(req)
			if err == nil && resp.StatusCode == 200 {
				break
			}
			debugMsg("PTZ_WARN", fmt.Sprintf("Command failed (attempt %d/3): %v", retries+1, err))
			time.Sleep(100 * time.Millisecond)
		}

		if err != nil {
			debugMsg("PTZ_ERROR", fmt.Sprintf("All command retries failed: %v", err))
			<-c.commandLock
			continue
		}
		resp.Body.Close()

		c.activeCommand = cmd.Command

		// Wait for the command duration
		time.Sleep(cmd.Duration)

		// Send stop command after duration
		stopXML := `<?xml version="1.0" encoding="UTF-8"?>
<PTZData>
    <Continuous>
        <pan>0</pan>
        <tilt>0</tilt>
        <zoom>0</zoom>
    </Continuous>
</PTZData>`

		stopBody := strings.NewReader(stopXML)
		stopReq, err := http.NewRequest("PUT", fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/continuous", c.ip, c.port), stopBody)
		if err != nil {
			debugMsg("PTZ_ERROR", fmt.Sprintf("Failed to create stop request: %v", err))
			<-c.commandLock
			continue
		}

		stopReq.Header.Set("Content-Type", "application/xml")
		stopReq.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
		stopReq.Header.Set("User-Agent", "Go-http-client/1.1")
		stopReq.ContentLength = int64(len(stopXML))

		stopResp, err := c.client.Do(stopReq)
		if err != nil {
			debugMsg("PTZ_ERROR", fmt.Sprintf("Failed to send stop command: %v", err))
		} else {
			stopResp.Body.Close()
		}

		<-c.commandLock // Release lock
	}
}

// convertToHikvisionCommand converts our command format to Hikvision format
func convertToHikvisionCommand(cmd string) string {
	switch cmd {
	case "ptzMoveLeft":
		return "left"
	case "ptzMoveRight":
		return "right"
	case "ptzMoveUp":
		return "up"
	case "ptzMoveDown":
		return "down"
	case "zoomIn":
		return "absolute"
	case "zoomOut":
		return "absolute"
	case "absolutePosition": // Add new command type
		return "absolute"
	default:
		return ""
	}
}

// calculateRelativeSpeed calculates the relative movement speed based on current position and command
func calculateRelativeSpeed(current PTZPosition, cmd string) (float64, float64, float64) {
	// Base speed for movement (in camera's -100 to 100 range)
	baseSpeed := 50.0

	// Calculate relative speeds based on current position and command
	var panSpeed, tiltSpeed, zoomSpeed float64

	switch cmd {
	case "left":
		// If we're already at the left edge, reduce speed
		if current.Pan <= 10 {
			panSpeed = -20.0
		} else {
			panSpeed = -baseSpeed
		}
	case "right":
		// If we're already at the right edge, reduce speed
		if current.Pan >= 3580 {
			panSpeed = 20.0
		} else {
			panSpeed = baseSpeed
		}
	case "up":
		// If we're already at the top edge, reduce speed
		if current.Tilt >= 890 {
			tiltSpeed = 20.0
		} else {
			tiltSpeed = baseSpeed
		}
	case "down":
		// If we're already at the bottom edge, reduce speed
		if current.Tilt <= 10 {
			tiltSpeed = -20.0
		} else {
			tiltSpeed = -baseSpeed
		}
	case "absolute":
		// For absolute zoom, we'll handle this separately in processCommands
		zoomSpeed = 0
	}

	return panSpeed, tiltSpeed, zoomSpeed
}

// monitorStatus continuously monitors the camera's PTZ position
func (c *HikvisionController) monitorStatus() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		status, err := c.getStatus()
		if err != nil {
			debugMsg("PTZ_ERROR", fmt.Sprintf("Failed to get status: %v", err))
			continue
		}

		// Update current position
		c.currentPos = status.Position

		// Log significant position changes
		if c.activeCommand != "" {
			debugMsg("PTZ", fmt.Sprintf("Current position - Pan: %.0f, Tilt: %.0f, Zoom: %.0f (Active: %s)",
				c.currentPos.Pan, c.currentPos.Tilt, c.currentPos.Zoom, c.activeCommand))
		}

		// Send position update through channel
		select {
		case c.statusChan <- status.Position:
		default:
			// Channel full, skip update
		}
	}
}

// getDigestAuth generates a digest authentication header based on the WWW-Authenticate response
func (c *HikvisionController) getDigestAuth(method, uri, realm, nonce string) string {
	// Generate client nonce (cnonce) and encode it in base64
	cnonceBytes := md5.Sum([]byte(time.Now().String()))
	cnonce := base64.StdEncoding.EncodeToString(cnonceBytes[:])

	// Calculate HA1 = MD5(username:realm:password)
	ha1 := fmt.Sprintf("%x", md5.Sum([]byte(fmt.Sprintf("%s:%s:%s", c.user, realm, c.pass))))

	// Calculate HA2 = MD5(method:uri)
	ha2 := fmt.Sprintf("%x", md5.Sum([]byte(fmt.Sprintf("%s:%s", method, uri))))

	// Calculate response = MD5(HA1:nonce:nc:cnonce:qop:HA2)
	response := fmt.Sprintf("%x", md5.Sum([]byte(fmt.Sprintf("%s:%s:00000001:%s:auth:%s", ha1, nonce, cnonce, ha2))))

	// Construct Authorization header with all required fields
	return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", cnonce="%s", nc=00000001, qop=auth, response="%s"`,
		c.user, realm, nonce, uri, cnonce, response)
}

// getStatus retrieves the current PTZ status from the camera
func (c *HikvisionController) getStatus() (*PTZStatus, error) {
	url := fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/status", c.ip, c.port)
	uri := "/ISAPI/PTZCtrl/channels/1/status"

	// First request to get the WWW-Authenticate header
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
	req.Header.Set("User-Agent", "Go-http-client/1.1")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get status: %v", err)
	}

	// Read and close the body
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// If we got a 401, extract the digest authentication parameters
	if resp.StatusCode == 401 {
		authHeader := resp.Header.Get("WWW-Authenticate")
		if authHeader == "" {
			return nil, fmt.Errorf("no WWW-Authenticate header in response")
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
			return nil, fmt.Errorf("invalid WWW-Authenticate header: %s", authHeader)
		}

		// Create new request with digest authentication
		req, err = http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %v", err)
		}

		req.Header.Set("Authorization", c.getDigestAuth("GET", uri, realm, nonce))
		req.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
		req.Header.Set("User-Agent", "Go-http-client/1.1")

		resp, err = c.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to get status: %v", err)
		}

		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	var status PTZStatus
	if err := xml.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("failed to decode status: %v", err)
	}

	// No need to convert values - they're already in camera units
	return &status, nil
}

// sendPresetCommand sends a preset command to the camera
func (c *HikvisionController) sendPresetCommand(presetName string) error {
	// Map preset names to their IDs
	var presetID int
	switch presetName {
	case "MainBridgeView":
		presetID = 1
	case "river1":
		presetID = 2
	case "river2":
		presetID = 3
	case "river3":
		presetID = 4
	default:
		return fmt.Errorf("unknown preset name: %s (valid presets are: MainBridgeView, river1, river2, river3)", presetName)
	}

	debugMsg("PTZ", fmt.Sprintf("Moving to preset %d (%s)", presetID, presetName))

	url := fmt.Sprintf("http://%s:%s/ISAPI/PTZCtrl/channels/1/presets/%d/goto", c.ip, c.port, presetID)
	uri := fmt.Sprintf("/ISAPI/PTZCtrl/channels/1/presets/%d/goto", presetID)

	// First request to get the WWW-Authenticate header
	req, err := http.NewRequest("PUT", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
	req.Header.Set("User-Agent", "Go-http-client/1.1")

	// Send command with retry
	var lastErr error
	for retries := 0; retries < 3; retries++ {
		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Read and close the body
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 200 {
			debugMsg("PTZ", fmt.Sprintf("Successfully moved to preset %d (%s)", presetID, presetName))
			// Notify tracking system that camera is no longer moving
			if c.statusChan != nil {
				// Send a special status or call a callback if needed
			}
			// Set CameraMoving to false in the tracking system
			if c.OnPresetArrived != nil {
				c.OnPresetArrived(presetName)
			}
			return nil
		}

		// If we got a 401, extract the digest authentication parameters
		if resp.StatusCode == 401 {
			authHeader := resp.Header.Get("WWW-Authenticate")
			if authHeader == "" {
				lastErr = fmt.Errorf("no WWW-Authenticate header in response")
				continue
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
				lastErr = fmt.Errorf("invalid WWW-Authenticate header: %s", authHeader)
				continue
			}

			// Create new request with digest authentication
			req, err = http.NewRequest("PUT", url, nil)
			if err != nil {
				lastErr = fmt.Errorf("failed to create request: %v", err)
				continue
			}

			req.Header.Set("Authorization", c.getDigestAuth("PUT", uri, realm, nonce))
			req.Header.Set("Host", fmt.Sprintf("%s:%s", c.ip, c.port))
			req.Header.Set("User-Agent", "Go-http-client/1.1")

			resp, err = c.client.Do(req)
			if err != nil {
				lastErr = fmt.Errorf("request failed: %v", err)
				continue
			}

			body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode == 200 {
				debugMsg("PTZ", fmt.Sprintf("Successfully moved to preset %d (%s)", presetID, presetName))
				return nil
			}
		}

		lastErr = fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
		debugMsg("PTZ_WARN", fmt.Sprintf("Preset command failed (attempt %d/3): %v", retries+1, lastErr))
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("all preset command retries failed: %v", lastErr)
}

// calculateTargetPosition calculates the target position based on current position and command
func (c *HikvisionController) calculateTargetPosition(current PTZPosition, cmd PTZCommand) PTZPosition {
	target := current

	// Calculate movement based on command
	switch cmd.Command {
	case "ptzMoveLeft":
		target.Pan -= 0.1 // Move left by 10%
	case "ptzMoveRight":
		target.Pan += 0.1 // Move right by 10%
	case "ptzMoveUp":
		target.Tilt += 0.1 // Move up by 10%
	case "ptzMoveDown":
		target.Tilt -= 0.1 // Move down by 10%
	case "zoomIn":
		target.Zoom += 0.1 // Zoom in by 10%
	case "zoomOut":
		target.Zoom -= 0.1 // Zoom out by 10%
	}

	// Ensure values stay within valid ranges
	target.Pan = clamp(target.Pan, -1.0, 1.0)
	target.Tilt = clamp(target.Tilt, -1.0, 1.0)
	target.Zoom = clamp(target.Zoom, 0.0, 1.0)

	return target
}

// clamp ensures a value stays within the specified range
func clamp(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// GetCurrentPosition returns the current PTZ position
func (c *HikvisionController) GetCurrentPosition() PTZPosition {
	return c.currentPos
}

// GetStatusChannel returns the channel for receiving position updates
func (c *HikvisionController) GetStatusChannel() <-chan PTZPosition {
	return c.statusChan
}

// GetFrameWidth returns the frame width in pixels
func (c *HikvisionController) GetFrameWidth() int {
	if c.frameWidth > 0 {
		return c.frameWidth
	}
	return 2688 // Fallback to default if not set
}

// GetFrameHeight returns the frame height in pixels
func (c *HikvisionController) GetFrameHeight() int {
	if c.frameHeight > 0 {
		return c.frameHeight
	}
	return 1520 // Fallback to default if not set
}

// SetFrameDimensions sets the actual frame dimensions
func (c *HikvisionController) SetFrameDimensions(width, height int) {
	c.frameWidth = width
	c.frameHeight = height
}

func (c *HikvisionController) SetOnPresetArrived(cb func(presetName string)) {
	c.OnPresetArrived = cb
}
