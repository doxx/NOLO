package detection

import (
	"fmt"
	"image"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gocv.io/x/gocv"
)

// DetectionResult represents the output of object detection
type DetectionResult struct {
	Rects       []image.Rectangle
	ClassNames  []string
	Confidences []float64
}

// Global debug function for detection package
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

// InferenceProvider defines the interface for YOLO inference
type InferenceProvider interface {
	Initialize(weightsPath, configPath, namesPath string) error
	Detect(frame gocv.Mat) (*DetectionResult, error)
	Close() error
	GetProviderInfo() ProviderInfo
}

// ProviderInfo contains information about the inference provider
type ProviderInfo struct {
	Type         string        // "GPU" or "CPU"
	Backend      string        // "CUDA", "OpenCL", "CPU"
	Device       string        // Device identifier
	EstimatedFPS int           // Estimated inference FPS
	MemoryUsage  string        // Memory usage info
	InitTime     time.Duration // Time taken to initialize
}

// ProviderManager handles automatic provider selection and fallback
type ProviderManager struct {
	currentProvider InferenceProvider
	providerInfo    ProviderInfo
}

// NewProviderManager creates a new provider manager with auto-detection
func NewProviderManager() *ProviderManager {
	return &ProviderManager{}
}

// Initialize performs auto-detection and initializes the best available provider
func (pm *ProviderManager) Initialize(weightsPath, configPath, namesPath string) error {
	fmt.Println("[PROVIDER] Auto-detecting best inference provider...")

	// Try GPU first
	if hasGPUCapability() {
		fmt.Println("[PROVIDER] GPU capability detected, attempting GPU initialization...")
		gpuProvider := &GPUProvider{}

		startTime := time.Now()
		err := gpuProvider.Initialize(weightsPath, configPath, namesPath)
		if err == nil {
			// Test GPU inference to make sure it really works
			if testProvider(gpuProvider) {
				pm.currentProvider = gpuProvider
				pm.providerInfo = gpuProvider.GetProviderInfo()
				pm.providerInfo.InitTime = time.Since(startTime)
				debugMsg("PROVIDER", fmt.Sprintf("GPU provider successfully initialized (%v)", pm.providerInfo.InitTime))
				return nil
			} else {
				fmt.Println("[PROVIDER] GPU test inference failed, falling back to CPU")
				gpuProvider.Close()
			}
		} else {
			debugMsg("PROVIDER", fmt.Sprintf("GPU initialization failed: %v, falling back to CPU", err))
		}
	} else {
		fmt.Println("[PROVIDER] No GPU capability detected")
	}

	// Fall back to CPU
	fmt.Println("[PROVIDER] Initializing CPU provider...")
	cpuProvider := &CPUProvider{}

	startTime := time.Now()
	err := cpuProvider.Initialize(weightsPath, configPath, namesPath)
	if err != nil {
		return fmt.Errorf("both GPU and CPU providers failed: %v", err)
	}

	pm.currentProvider = cpuProvider
	pm.providerInfo = cpuProvider.GetProviderInfo()
	pm.providerInfo.InitTime = time.Since(startTime)
	debugMsg("PROVIDER", fmt.Sprintf("CPU provider initialized (%v)", pm.providerInfo.InitTime))

	return nil
}

// GetProvider returns the current active provider
func (pm *ProviderManager) GetProvider() InferenceProvider {
	return pm.currentProvider
}

// GetProviderInfo returns information about the current provider
func (pm *ProviderManager) GetProviderInfo() ProviderInfo {
	return pm.providerInfo
}

// Close closes the current provider
func (pm *ProviderManager) Close() error {
	if pm.currentProvider != nil {
		return pm.currentProvider.Close()
	}
	return nil
}

// hasGPUCapability checks if GPU inference is possible
func hasGPUCapability() bool {
	// Check 1: NVIDIA GPU present
	if !hasNVIDIAGPU() {
		fmt.Println("[GPU_DETECT] No NVIDIA GPU detected")
		return false
	}
	fmt.Println("[GPU_DETECT] NVIDIA GPU found")

	// Check 2: NVIDIA drivers loaded
	if !hasNVIDIADriver() {
		fmt.Println("[GPU_DETECT] NVIDIA drivers not loaded")
		return false
	}
	fmt.Println("[GPU_DETECT] NVIDIA drivers loaded")

	// Check 3: We'll test CUDA during actual GPU provider initialization
	fmt.Println("[GPU_DETECT] Hardware checks passed, will test CUDA during initialization")

	return true
}

// hasNVIDIAGPU checks if NVIDIA GPU is present
func hasNVIDIAGPU() bool {
	cmd := exec.Command("lspci")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(output)), "nvidia")
}

// hasNVIDIADriver checks if NVIDIA drivers are loaded
func hasNVIDIADriver() bool {
	// Check nvidia-smi
	cmd := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader")
	err := cmd.Run()
	if err != nil {
		return false
	}

	// Check for nvidia device files
	matches, _ := filepath.Glob("/dev/nvidia*")
	return len(matches) > 0
}

// testProvider performs a quick test inference to verify the provider works
func testProvider(provider InferenceProvider) bool {
	// Create a small test frame
	testFrame := gocv.NewMatWithSize(832, 832, gocv.MatTypeCV8UC3)
	defer testFrame.Close()

	// Try inference
	_, err := provider.Detect(testFrame)
	return err == nil
}
