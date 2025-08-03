package tracking

import (
	"time"
)

// KalmanFilter implements a 2D Kalman filter for position and velocity tracking
type KalmanFilter struct {
	// State vector [x, y, vx, vy]
	state [4]float64
	// Covariance matrix
	P [4][4]float64
	// Process noise
	Q [4][4]float64
	// Measurement noise
	R [2][2]float64
	// Last update time
	lastUpdate time.Time
	// Initialized flag
	initialized bool
}

// NewKalmanFilter creates a new Kalman filter
func NewKalmanFilter() *KalmanFilter {
	kf := &KalmanFilter{
		lastUpdate: time.Now(),
	}

	// Initialize covariance matrix
	for i := 0; i < 4; i++ {
		kf.P[i][i] = 1000.0 // High initial uncertainty
	}

	// Initialize process noise
	dt := 0.1 // Expected time step
	q := 0.1  // Process noise scale
	kf.Q = [4][4]float64{
		{q * dt * dt * dt * dt / 4, 0, q * dt * dt * dt / 2, 0},
		{0, q * dt * dt * dt * dt / 4, 0, q * dt * dt * dt / 2},
		{q * dt * dt * dt / 2, 0, q * dt * dt, 0},
		{0, q * dt * dt * dt / 2, 0, q * dt * dt},
	}

	// Initialize measurement noise
	kf.R = [2][2]float64{
		{10.0, 0},
		{0, 10.0},
	}

	return kf
}

// Update updates the Kalman filter with a new measurement
func (kf *KalmanFilter) Update(x, y float64) (float64, float64, float64, float64) {
	if !kf.initialized {
		// Initialize state
		kf.state = [4]float64{x, y, 0, 0}
		kf.initialized = true
		kf.lastUpdate = time.Now()
		return x, y, 0, 0
	}

	// Calculate time step
	dt := time.Since(kf.lastUpdate).Seconds()
	if dt < 0.001 {
		dt = 0.001 // Minimum time step
	}
	kf.lastUpdate = time.Now()

	// Predict step
	// State transition matrix
	F := [4][4]float64{
		{1, 0, dt, 0},
		{0, 1, 0, dt},
		{0, 0, 1, 0},
		{0, 0, 0, 1},
	}

	// Predict state
	newState := [4]float64{
		kf.state[0] + kf.state[2]*dt,
		kf.state[1] + kf.state[3]*dt,
		kf.state[2],
		kf.state[3],
	}

	// Predict covariance
	newP := kf.predictCovariance(F, dt)

	// Update step
	// Measurement matrix
	H := [2][4]float64{
		{1, 0, 0, 0},
		{0, 1, 0, 0},
	}

	// Innovation
	innovation := [2]float64{
		x - newState[0],
		y - newState[1],
	}

	// Innovation covariance
	S := kf.calculateInnovationCovariance(H, newP)

	// Kalman gain
	K := kf.calculateKalmanGain(H, newP, S)

	// Update state
	for i := 0; i < 4; i++ {
		kf.state[i] = newState[i] + K[0][i]*innovation[0] + K[1][i]*innovation[1]
	}

	// Update covariance
	kf.updateCovariance(K, H, newP)

	// Return position and velocity
	return kf.state[0], kf.state[1], kf.state[2], kf.state[3]
}

// Predict predicts the future state
func (kf *KalmanFilter) Predict(dt float64) (float64, float64, float64, float64) {
	if !kf.initialized {
		return 0, 0, 0, 0
	}

	// Predict position
	x := kf.state[0] + kf.state[2]*dt
	y := kf.state[1] + kf.state[3]*dt

	return x, y, kf.state[2], kf.state[3]
}

// predictCovariance predicts the covariance matrix
func (kf *KalmanFilter) predictCovariance(F [4][4]float64, dt float64) [4][4]float64 {
	var newP [4][4]float64

	// P = F * P * F' + Q
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			sum := 0.0
			for k := 0; k < 4; k++ {
				for l := 0; l < 4; l++ {
					sum += F[i][k] * kf.P[k][l] * F[j][l]
				}
			}
			newP[i][j] = sum + kf.Q[i][j]
		}
	}

	return newP
}

// calculateInnovationCovariance calculates the innovation covariance
func (kf *KalmanFilter) calculateInnovationCovariance(H [2][4]float64, P [4][4]float64) [2][2]float64 {
	var S [2][2]float64

	// S = H * P * H' + R
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			sum := 0.0
			for k := 0; k < 4; k++ {
				for l := 0; l < 4; l++ {
					sum += H[i][k] * P[k][l] * H[j][l]
				}
			}
			S[i][j] = sum + kf.R[i][j]
		}
	}

	return S
}

// calculateKalmanGain calculates the Kalman gain
func (kf *KalmanFilter) calculateKalmanGain(H [2][4]float64, P [4][4]float64, S [2][2]float64) [2][4]float64 {
	var K [2][4]float64

	// K = P * H' * inv(S)
	for i := 0; i < 2; i++ {
		for j := 0; j < 4; j++ {
			sum := 0.0
			for k := 0; k < 4; k++ {
				for l := 0; l < 2; l++ {
					sum += P[j][k] * H[l][k] * (1.0 / S[i][i])
				}
			}
			K[i][j] = sum
		}
	}

	return K
}

// updateCovariance updates the covariance matrix
func (kf *KalmanFilter) updateCovariance(K [2][4]float64, H [2][4]float64, P [4][4]float64) {
	// P = (I - K*H) * P
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			sum := 0.0
			for k := 0; k < 2; k++ {
				for l := 0; l < 4; l++ {
					sum += K[k][i] * H[k][l] * P[l][j]
				}
			}
			kf.P[i][j] = P[i][j] - sum
		}
	}
}

// GetVelocity returns the current velocity estimate
func (kf *KalmanFilter) GetVelocity() (float64, float64) {
	if !kf.initialized {
		return 0, 0
	}
	return kf.state[2], kf.state[3]
}

// GetPosition returns the current position estimate
func (kf *KalmanFilter) GetPosition() (float64, float64) {
	if !kf.initialized {
		return 0, 0
	}
	return kf.state[0], kf.state[1]
}

// Reset resets the Kalman filter
func (kf *KalmanFilter) Reset() {
	kf.initialized = false
	for i := 0; i < 4; i++ {
		kf.state[i] = 0
		for j := 0; j < 4; j++ {
			kf.P[i][j] = 1000.0
		}
	}
}
