package ptz

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// Global debug function for PTZ package
var debugMsgFunc func(string, string, ...string)

// SetDebugFunction allows main package to provide debug function
func SetDebugFunction(fn func(string, string, ...string)) {
	debugMsgFunc = fn
}

// debugMsg is a wrapper that handles nil checks
func debugMsg(component, message string, boatID ...string) {
	if debugMsgFunc != nil {
		debugMsgFunc(component, message, boatID...)
	}
}

// CameraState represents the current state of the camera
type CameraState int

const (
	IDLE CameraState = iota
	MOVING
)

func (s CameraState) String() string {
	switch s {
	case IDLE:
		return "IDLE"
	case MOVING:
		return "MOVING"
	default:
		return "UNKNOWN"
	}
}

// PTZLimits defines the camera position limits
type PTZLimits struct {
	// Software limits - reasonable viewing area for river monitoring
	SoftMinPan  float64
	SoftMaxPan  float64
	SoftMinTilt float64
	SoftMaxTilt float64
	SoftMinZoom float64
	SoftMaxZoom float64

	// Hardware limits - physical camera constraints
	HardMinPan  float64
	HardMaxPan  float64
	HardMinTilt float64
	HardMaxTilt float64
	HardMinZoom float64
	HardMaxZoom float64
}

// CameraStateManager manages camera state and position tracking
type CameraStateManager struct {
	controller     Controller
	state          CameraState
	targetPosition *PTZPosition
	mutex          sync.RWMutex
	limits         PTZLimits

	// Monitoring
	monitorInterval time.Duration
	stopMonitor     chan bool
	onStateChanged  func(oldState, newState CameraState)
	onArrived       func(target PTZPosition)

	// Timeout handling
	commandStartTime time.Time     // When the current command started
	maxCommandTime   time.Duration // Maximum time to wait for command completion

	// Rate limiting
	lastCommandTime time.Time     // When the last command was sent
	rateLimitDelay  time.Duration // Minimum delay between commands

	// Settling delay - time to wait after arrival before transitioning to IDLE
	settlingDelay time.Duration // Time to wait after position arrival before declaring IDLE
	arrivalTime   time.Time     // When camera first arrived at target position

	// Manual override (chat/API control)
	manualOverrideUntil time.Time // When override expires (zero = no override)

	// External movement detection — detect when someone else controls the camera
	lastKnownPosition  *PTZPosition  // Last position NOLO saw
	lastNOLOCommand    time.Time     // When NOLO last sent a command
	externalTakeover   bool          // True when external control detected
	externalIdleSince  time.Time     // When external control stopped moving
	externalResumeTime time.Duration // How long camera must be idle to resume (30s)

	// Clamp tracking - detect when boat is past PTZ limits
	consecutiveClamps int
	lastClampedPan    float64
	lastClampedTilt   float64
}

// NewCameraStateManager creates a new camera state manager
func NewCameraStateManager(controller Controller) *CameraStateManager {
	csm := &CameraStateManager{
		controller:      controller,
		state:           IDLE, // Start in IDLE state
		monitorInterval: 800 * time.Millisecond,
		stopMonitor:     make(chan bool, 1),
		maxCommandTime:  15 * time.Second,
		rateLimitDelay:  100 * time.Millisecond, // 100ms rate limiting (faster response during idle periods)
		settlingDelay:   100 * time.Millisecond, // 100ms settling delay after arrival

		// External movement detection
		externalResumeTime: 30 * time.Second, // Resume AI after 30s of idle

		// Set default limits - software limits should match hardware limits unless user overrides
		limits: PTZLimits{
			// Software limits - default to full hardware range (user can restrict with flags)
			SoftMinPan:  0,    // Same as hardware minimum - full range by default
			SoftMaxPan:  3590, // Same as hardware maximum - full range by default
			SoftMinTilt: 0,    // Same as hardware minimum - full range by default
			SoftMaxTilt: 900,  // Same as hardware maximum - full range by default
			SoftMinZoom: 10,   // Same as hardware minimum - full range by default
			SoftMaxZoom: 120,  // Same as hardware maximum - full range by default

			// Hardware limits - physical camera constraints (fallback)
			HardMinPan:  0,    // Camera hardware minimum
			HardMaxPan:  3590, // Camera hardware maximum
			HardMinTilt: 0,    // Camera hardware minimum
			HardMaxTilt: 900,  // Camera hardware maximum
			HardMinZoom: 10,   // Camera hardware minimum
			HardMaxZoom: 120,  // Camera hardware maximum
		},
	}

	debugMsg("CAMERA_STATE", fmt.Sprintf("Initialized with software limits: Pan(%.0f-%.0f) Tilt(%.0f-%.0f) Zoom(%.0f-%.0f)",
		csm.limits.SoftMinPan, csm.limits.SoftMaxPan,
		csm.limits.SoftMinTilt, csm.limits.SoftMaxTilt,
		csm.limits.SoftMinZoom, csm.limits.SoftMaxZoom))

	return csm
}

// SetLimits allows updating the camera limits
func (csm *CameraStateManager) SetLimits(limits PTZLimits) {
	csm.mutex.Lock()
	defer csm.mutex.Unlock()

	csm.limits = limits
	debugMsg("CAMERA_STATE", fmt.Sprintf("Updated limits: Pan(%.0f-%.0f) Tilt(%.0f-%.0f) Zoom(%.0f-%.0f)",
		limits.SoftMinPan, limits.SoftMaxPan,
		limits.SoftMinTilt, limits.SoftMaxTilt,
		limits.SoftMinZoom, limits.SoftMaxZoom))
}

// GetLimits returns the current camera limits
func (csm *CameraStateManager) GetLimits() PTZLimits {
	csm.mutex.RLock()
	defer csm.mutex.RUnlock()
	return csm.limits
}

// validateAndClampPosition validates and clamps position to limits
func (csm *CameraStateManager) validateAndClampPosition(pan, tilt, zoom float64) (float64, float64, float64, bool) {
	originalPan, originalTilt, originalZoom := pan, tilt, zoom

	// Apply software limits first (preferred range)
	pan = math.Max(csm.limits.SoftMinPan, math.Min(csm.limits.SoftMaxPan, pan))
	tilt = math.Max(csm.limits.SoftMinTilt, math.Min(csm.limits.SoftMaxTilt, tilt))
	zoom = math.Max(csm.limits.SoftMinZoom, math.Min(csm.limits.SoftMaxZoom, zoom))

	// Apply hardware limits as final safety (should never be needed if software limits are correct)
	pan = math.Max(csm.limits.HardMinPan, math.Min(csm.limits.HardMaxPan, pan))
	tilt = math.Max(csm.limits.HardMinTilt, math.Min(csm.limits.HardMaxTilt, tilt))
	zoom = math.Max(csm.limits.HardMinZoom, math.Min(csm.limits.HardMaxZoom, zoom))

	// Check if any values were clamped
	clamped := (originalPan != pan) || (originalTilt != tilt) || (originalZoom != zoom)

	if clamped {
		debugMsg("CAMERA_STATE", fmt.Sprintf("🚫 Position clamped: (%.1f,%.1f,%.1f) → (%.1f,%.1f,%.1f)",
			originalPan, originalTilt, originalZoom, pan, tilt, zoom))
	}

	return pan, tilt, zoom, clamped
}

// SetTolerances is deprecated - we use exact position matching now
func (csm *CameraStateManager) SetTolerances(pan, tilt, zoom float64) {
	// No-op - we don't use tolerances anymore
}

// SetOnStateChanged sets callback for state change notifications
func (csm *CameraStateManager) SetOnStateChanged(callback func(oldState, newState CameraState)) {
	csm.mutex.Lock()
	defer csm.mutex.Unlock()
	csm.onStateChanged = callback
}

// SetOnArrived sets callback for arrival notifications
func (csm *CameraStateManager) SetOnArrived(callback func(target PTZPosition)) {
	csm.mutex.Lock()
	defer csm.mutex.Unlock()
	csm.onArrived = callback
}

// GetState returns the current camera state
func (csm *CameraStateManager) GetState() CameraState {
	csm.mutex.RLock()
	defer csm.mutex.RUnlock()
	return csm.state
}

// IsIdle returns true if camera is in IDLE state
func (csm *CameraStateManager) IsIdle() bool {
	return csm.GetState() == IDLE
}

// IsMoving returns true if camera is in MOVING state
func (csm *CameraStateManager) IsMoving() bool {
	return csm.GetState() == MOVING
}

// GetTargetPosition returns the current target position (if any)
func (csm *CameraStateManager) GetTargetPosition() *PTZPosition {
	csm.mutex.RLock()
	defer csm.mutex.RUnlock()

	if csm.targetPosition == nil {
		return nil
	}

	// Return a copy to prevent external modification
	target := *csm.targetPosition
	return &target
}

// SetManualOverride sets the manual override expiry time
func (csm *CameraStateManager) SetManualOverride(until time.Time) {
	csm.mutex.Lock()
	defer csm.mutex.Unlock()
	csm.manualOverrideUntil = until
	debugMsg("CAMERA_STATE", fmt.Sprintf("Manual override set until %s (%.0fs from now)",
		until.Format("15:04:05"), time.Until(until).Seconds()))
}

// ClearManualOverride clears the manual override immediately
func (csm *CameraStateManager) ClearManualOverride() {
	csm.mutex.Lock()
	defer csm.mutex.Unlock()
	csm.manualOverrideUntil = time.Time{}
	debugMsg("CAMERA_STATE", "Manual override cleared - AI tracking resumed")
}

// IsManualOverrideActive returns true if manual override is currently active
func (csm *CameraStateManager) IsManualOverrideActive() bool {
	csm.mutex.RLock()
	defer csm.mutex.RUnlock()
	return !csm.manualOverrideUntil.IsZero() && time.Now().Before(csm.manualOverrideUntil)
}

// ManualOverrideRemaining returns seconds remaining on override, 0 if not active
func (csm *CameraStateManager) ManualOverrideRemaining() float64 {
	csm.mutex.RLock()
	defer csm.mutex.RUnlock()
	if csm.manualOverrideUntil.IsZero() || time.Now().After(csm.manualOverrideUntil) {
		return 0
	}
	return time.Until(csm.manualOverrideUntil).Seconds()
}

// SendManualCommand sends a PTZ command during manual override (bypasses override check)
func (csm *CameraStateManager) SendManualCommand(cmd PTZCommand) bool {
	// This is the same as SendCommand but doesn't check override
	// Used by the HTTP API to send commands during override
	return csm.sendCommandInternal(cmd)
}

// SendCommand sends a PTZ command with rate limiting
func (csm *CameraStateManager) SendCommand(cmd PTZCommand) bool {
	// Check manual override - block AI commands while chat/API is controlling
	if csm.IsManualOverrideActive() {
		return false
	}
	// Check external takeover - block AI commands while someone else controls the camera
	if csm.IsExternalTakeover() {
		return false
	}
	return csm.sendCommandInternal(cmd)
}

func (csm *CameraStateManager) sendCommandInternal(cmd PTZCommand) bool {
	csm.mutex.Lock()
	defer csm.mutex.Unlock()

	// Track that NOLO is sending a command (for external movement detection)
	csm.lastNOLOCommand = time.Now()

	// Rate limit commands - reject if too soon after last command
	now := time.Now()
	if !csm.lastCommandTime.IsZero() && now.Sub(csm.lastCommandTime) < csm.rateLimitDelay {
		remainingDelay := csm.rateLimitDelay - now.Sub(csm.lastCommandTime)
		debugMsg("CAMERA_STATE", fmt.Sprintf("Rate limiting command %s - %dms remaining", cmd.Command, remainingDelay.Milliseconds()))
		return false
	}

	// For absolute position commands, validate and clamp positions
	if cmd.Command == "absolutePosition" && cmd.AbsolutePan != nil && cmd.AbsoluteTilt != nil && cmd.AbsoluteZoom != nil {

		// Validate and clamp positions to limits
		clampedPan, clampedTilt, clampedZoom, wasClamped := csm.validateAndClampPosition(*cmd.AbsolutePan, *cmd.AbsoluteTilt, *cmd.AbsoluteZoom)

		// Round all positions to integers before storing as target
		roundedPan := math.Round(clampedPan)
		roundedTilt := math.Round(clampedTilt)
		roundedZoom := math.Round(clampedZoom)

		if wasClamped {
			debugMsg("CAMERA_STATE", fmt.Sprintf("⚠️ Command %s had invalid position - clamped to safe limits", cmd.Reason))
			// Track consecutive clamps to detect stuck-at-limit condition
			if csm.lastClampedPan == roundedPan && csm.lastClampedTilt == roundedTilt {
				csm.consecutiveClamps++
				if csm.consecutiveClamps >= 3 {
					debugMsg("CAMERA_STATE", fmt.Sprintf("🚫 %d consecutive clamps to same position (Pan=%.0f, Tilt=%.0f) - boat past limits, rejecting",
						csm.consecutiveClamps, roundedPan, roundedTilt))
					return false
				}
			} else {
				csm.consecutiveClamps = 1
				csm.lastClampedPan = roundedPan
				csm.lastClampedTilt = roundedTilt
			}
		} else {
			csm.consecutiveClamps = 0
		}

		csm.targetPosition = &PTZPosition{
			Pan:  roundedPan,
			Tilt: roundedTilt,
			Zoom: roundedZoom,
		}

		// Create new command with validated and rounded values to send to camera
		validatedCmd := PTZCommand{
			Command:      cmd.Command,
			Reason:       cmd.Reason + " (validated & integer-rounded)",
			Duration:     cmd.Duration,
			AbsolutePan:  &roundedPan,
			AbsoluteTilt: &roundedTilt,
			AbsoluteZoom: &roundedZoom,
		}

		// Send the validated command to PTZ controller
		success := csm.controller.SendCommand(validatedCmd)
		if !success {
			debugMsg("CAMERA_STATE", fmt.Sprintf("Failed to send command %s to PTZ controller", cmd.Command))
			return false
		}

		// Update rate limiting and state tracking
		csm.lastCommandTime = now
		csm.commandStartTime = now
		csm.changeState(MOVING)

		debugMsg("CAMERA_STATE", fmt.Sprintf("✅ Command sent - transitioning to MOVING, target: Pan=%.0f, Tilt=%.0f, Zoom=%.0f",
			csm.targetPosition.Pan, csm.targetPosition.Tilt, csm.targetPosition.Zoom))
	} else {
		// For non-absolute commands (relative movements), send directly
		success := csm.controller.SendCommand(cmd)
		if !success {
			debugMsg("CAMERA_STATE", fmt.Sprintf("Failed to send command %s to PTZ controller", cmd.Command))
			return false
		}

		// Update rate limiting for non-absolute commands too
		csm.lastCommandTime = now

		// These commands complete quickly and don't need arrival detection
		debugMsg("CAMERA_STATE", fmt.Sprintf("Sent non-absolute command %s", cmd.Command))
	}

	return true
}

// Start begins position monitoring
func (csm *CameraStateManager) Start() {
	debugMsg("CAMERA_STATE", "Starting camera state manager")
	go csm.monitorPosition()
}

// Stop stops position monitoring
func (csm *CameraStateManager) Stop() {
	debugMsg("CAMERA_STATE", "Stopping camera state manager")
	select {
	case csm.stopMonitor <- true:
	default:
	}
}

// HasArrived checks if camera has reached the target position
func (csm *CameraStateManager) HasArrived() bool {
	csm.mutex.RLock()
	defer csm.mutex.RUnlock()

	if csm.targetPosition == nil {
		return false
	}

	current := csm.controller.GetCurrentPosition()
	return csm.isAtTarget(current, *csm.targetPosition)
}

// isAtTarget checks if current position equals target position
func (csm *CameraStateManager) isAtTarget(current, target PTZPosition) bool {
	// Simple integer matching - if camera is at target, it's done moving
	panMatch := math.Round(current.Pan) == math.Round(target.Pan)
	tiltMatch := math.Round(current.Tilt) == math.Round(target.Tilt)
	zoomMatch := math.Round(current.Zoom) == math.Round(target.Zoom)

	return panMatch && tiltMatch && zoomMatch
}

// changeState transitions to a new state and triggers callbacks
func (csm *CameraStateManager) changeState(newState CameraState) {
	oldState := csm.state
	csm.state = newState

	debugMsg("CAMERA_STATE", fmt.Sprintf("State change: %s → %s", oldState, newState))

	// Trigger state change callback
	if csm.onStateChanged != nil {
		go csm.onStateChanged(oldState, newState)
	}
}

// monitorPosition continuously monitors camera position for arrival detection
func (csm *CameraStateManager) monitorPosition() {
	ticker := time.NewTicker(csm.monitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-csm.stopMonitor:
			debugMsg("CAMERA_STATE", "Position monitoring stopped")
			return

		case <-ticker.C:
			csm.checkExternalMovement()
			csm.checkArrival()
		}
	}
}

// checkExternalMovement detects when something other than NOLO moves the camera
func (csm *CameraStateManager) checkExternalMovement() {
	current := csm.controller.GetCurrentPosition()

	csm.mutex.Lock()
	defer csm.mutex.Unlock()

	// Initialize on first run
	if csm.lastKnownPosition == nil {
		pos := current
		csm.lastKnownPosition = &pos
		return
	}

	// Calculate how much the camera moved since last check
	deltaPan := math.Abs(current.Pan - csm.lastKnownPosition.Pan)
	deltaTilt := math.Abs(current.Tilt - csm.lastKnownPosition.Tilt)
	deltaZoom := math.Abs(current.Zoom - csm.lastKnownPosition.Zoom)
	moved := deltaPan > 3 || deltaTilt > 3 || deltaZoom > 3

	// Update last known position
	pos := current
	csm.lastKnownPosition = &pos

	// EXTERNAL DETECTION: Compare actual position to NOLO's target.
	// If the camera is NOT where NOLO told it to go, something else moved it.
	// This works in ANY state (IDLE, MOVING, tracking, scanning).
	if csm.targetPosition != nil && !csm.externalTakeover {
		targetDeltaPan := math.Abs(current.Pan - csm.targetPosition.Pan)
		targetDeltaTilt := math.Abs(current.Tilt - csm.targetPosition.Tilt)
		targetDeltaZoom := math.Abs(current.Zoom - csm.targetPosition.Zoom)

		// If camera diverged significantly from NOLO's target AND NOLO sent the command
		// more than 3 seconds ago (give time for camera to reach target), it's external.
		timeSinceCmd := time.Since(csm.lastNOLOCommand)
		diverged := targetDeltaPan > 50 || targetDeltaTilt > 50 || targetDeltaZoom > 20

		if diverged && timeSinceCmd > 3*time.Second {
			csm.externalTakeover = true
			csm.externalIdleSince = time.Time{}
			debugMsg("CAMERA_STATE", fmt.Sprintf("EXTERNAL CONTROL DETECTED - camera diverged from target (Pan:%+.0f Tilt:%+.0f Zoom:%+.0f). AI paused.",
				current.Pan-csm.targetPosition.Pan, current.Tilt-csm.targetPosition.Tilt, current.Zoom-csm.targetPosition.Zoom))
			return
		}
	}

	// Also detect external movement when IDLE with no target (scanning pauses, etc.)
	timeSinceNOLO := time.Since(csm.lastNOLOCommand)
	if moved && timeSinceNOLO > 2*time.Second && !csm.externalTakeover {
		csm.externalTakeover = true
		csm.externalIdleSince = time.Time{}
		debugMsg("CAMERA_STATE", fmt.Sprintf("EXTERNAL CONTROL DETECTED - unexpected movement (last NOLO cmd %.1fs ago). AI paused.",
			timeSinceNOLO.Seconds()))
		return
	}

	// If in external takeover mode, check if camera has been idle long enough to resume
	if csm.externalTakeover {
		if moved {
			// Still moving externally
			csm.externalIdleSince = time.Time{}
		} else {
			// Camera is idle
			if csm.externalIdleSince.IsZero() {
				csm.externalIdleSince = time.Now()
				debugMsg("CAMERA_STATE", "External control stopped - waiting 30s idle before resuming AI")
			} else if time.Since(csm.externalIdleSince) > csm.externalResumeTime {
				csm.externalTakeover = false
				csm.externalIdleSince = time.Time{}
				csm.targetPosition = nil // Clear stale target
				debugMsg("CAMERA_STATE", fmt.Sprintf("Camera idle for %.0fs - resuming AI tracking",
					csm.externalResumeTime.Seconds()))
			}
		}
	}
}

// IsExternalTakeover returns true if external control has been detected
func (csm *CameraStateManager) IsExternalTakeover() bool {
	csm.mutex.RLock()
	defer csm.mutex.RUnlock()
	return csm.externalTakeover
}

// checkArrival checks if camera has arrived at target and transitions to IDLE
func (csm *CameraStateManager) checkArrival() {
	csm.mutex.Lock()
	defer csm.mutex.Unlock()

	// Only check arrival when in MOVING state
	if csm.state != MOVING {
		return
	}

	// Check for timeout - if we've been in MOVING state too long, force IDLE
	now := time.Now()
	if !csm.commandStartTime.IsZero() && now.Sub(csm.commandStartTime) > csm.maxCommandTime {
		debugMsg("CAMERA_STATE", fmt.Sprintf("⏰ Command timeout after %.1fs - forcing IDLE state",
			now.Sub(csm.commandStartTime).Seconds()))
		csm.declareArrival()
		return
	}

	// If no target position is set, transition back to IDLE (safety mechanism)
	if csm.targetPosition == nil {
		debugMsg("CAMERA_STATE", "No target position set but in MOVING state - transitioning to IDLE")
		csm.declareArrival()
		return
	}

	// Get current position from camera
	current := csm.controller.GetCurrentPosition()

	// Check if we're at the target position
	if csm.isAtTarget(current, *csm.targetPosition) {
		// Camera is at target position
		if csm.arrivalTime.IsZero() {
			// First time we've detected arrival - start settling timer
			csm.arrivalTime = now
			if debugMsg != nil {
				debugMsg("CAMERA_STATE", fmt.Sprintf("📍 Camera arrived at target - settling for %.0fms before IDLE", csm.settlingDelay.Seconds()*1000))
			}
		} else {
			// Check if settling time has elapsed
			settlingElapsed := now.Sub(csm.arrivalTime)
			if settlingElapsed >= csm.settlingDelay {
				if debugMsg != nil {
					debugMsg("CAMERA_STATE", fmt.Sprintf("✅ Camera settled after %.0fms - transitioning to IDLE", settlingElapsed.Seconds()*1000))
				}
				csm.declareArrival()
			} else {
				// Still settling - show progress occasionally
				if debugMsg != nil && int(settlingElapsed.Milliseconds())%50 == 0 {
					remainingMs := (csm.settlingDelay - settlingElapsed).Milliseconds()
					debugMsg("CAMERA_STATE", fmt.Sprintf("🕐 Settling... %dms remaining", remainingMs))
				}
			}
		}
	} else {
		// Camera moved away from target - reset arrival time
		if !csm.arrivalTime.IsZero() {
			csm.arrivalTime = time.Time{}
			if debugMsg != nil {
				debugMsg("CAMERA_STATE", "❌ Camera moved away from target during settling - resetting")
			}
		}

		// Show movement progress occasionally
		elapsed := now.Sub(csm.commandStartTime).Seconds()
		if int(elapsed)%3 == 0 && elapsed > 0 {
			debugMsg("CAMERA_STATE", fmt.Sprintf("Moving (%.1fs)... Current: Pan=%.0f Tilt=%.0f Zoom=%.0f, Target: Pan=%.0f Tilt=%.0f Zoom=%.0f",
				elapsed, current.Pan, current.Tilt, current.Zoom,
				csm.targetPosition.Pan, csm.targetPosition.Tilt, csm.targetPosition.Zoom))
		}
	}
}

// declareArrival transitions to IDLE and triggers callbacks
func (csm *CameraStateManager) declareArrival() {
	arrivedTarget := PTZPosition{}
	if csm.targetPosition != nil {
		arrivedTarget = *csm.targetPosition
	}

	// Clear state
	csm.targetPosition = nil
	csm.commandStartTime = time.Time{}
	csm.arrivalTime = time.Time{} // Clear arrival time for next movement cycle

	csm.changeState(IDLE)

	// Trigger arrival callback
	if csm.onArrived != nil && arrivedTarget.Pan != 0 && arrivedTarget.Tilt != 0 && arrivedTarget.Zoom != 0 {
		go csm.onArrived(arrivedTarget)
	}
}

// ForceIdle forces the camera state to IDLE (emergency reset)
func (csm *CameraStateManager) ForceIdle() {
	csm.mutex.Lock()
	defer csm.mutex.Unlock()

	if csm.state != IDLE {
		if debugMsg != nil {
			debugMsg("CAMERA_STATE", "🚨 Force resetting to IDLE state")
		}
		csm.declareArrival()
	}
}

// GetStateInfo returns detailed state information for debugging
func (csm *CameraStateManager) GetStateInfo() string {
	csm.mutex.RLock()
	defer csm.mutex.RUnlock()

	current := csm.controller.GetCurrentPosition()

	if csm.targetPosition == nil {
		return fmt.Sprintf("State: %s, Current: Pan=%.1f Tilt=%.1f Zoom=%.1f, Target: none",
			csm.state, current.Pan, current.Tilt, current.Zoom)
	}

	elapsed := ""
	if !csm.commandStartTime.IsZero() {
		elapsed = fmt.Sprintf(", Elapsed: %.1fs", time.Since(csm.commandStartTime).Seconds())
	}

	return fmt.Sprintf("State: %s, Current: Pan=%.1f Tilt=%.1f Zoom=%.1f, Target: Pan=%.1f Tilt=%.1f Zoom=%.1f%s",
		csm.state, current.Pan, current.Tilt, current.Zoom,
		csm.targetPosition.Pan, csm.targetPosition.Tilt, csm.targetPosition.Zoom, elapsed)
}
