package detection

import (
	"fmt"
	"image"
	"io/ioutil"
	"strings"
	"sync"

	"gocv.io/x/gocv"
)

// GPUProvider implements YOLO inference using OpenCV CUDA backend
type GPUProvider struct {
	net        gocv.Net
	classNames []string
	mu         sync.Mutex
}

// Initialize initializes the GPU provider with model files
func (gp *GPUProvider) Initialize(weightsPath, configPath, namesPath string) error {
	// Load the network
	gp.net = gocv.ReadNet(weightsPath, configPath)
	if gp.net.Empty() {
		return fmt.Errorf("failed to load YOLO network from %s and %s", weightsPath, configPath)
	}

	// Try to set CUDA backend
	gp.net.SetPreferableBackend(gocv.NetBackendCUDA)
	gp.net.SetPreferableTarget(gocv.NetTargetCUDA)

	// Load class names
	namesBytes, err := ioutil.ReadFile(namesPath)
	if err != nil {
		return fmt.Errorf("could not read class names: %v", err)
	}
	gp.classNames = strings.Split(string(namesBytes), "\n")

	return nil
}

// Detect performs object detection on a frame using GPU
func (gp *GPUProvider) Detect(frame gocv.Mat) (*DetectionResult, error) {
	gp.mu.Lock()
	defer gp.mu.Unlock()

	// Create blob from image
	blob := gocv.BlobFromImage(frame, 1.0/255.0, image.Pt(832, 832), gocv.NewScalar(0, 0, 0, 0), true, false)
	defer blob.Close()

	// Set input
	gp.net.SetInput(blob, "")

	// Forward pass (this should run on GPU)
	output := gp.net.Forward("")
	defer output.Close()

	var rects []image.Rectangle
	var classNames []string
	var confidences []float64

	// Process output (same logic as CPU)
	for i := 0; i < output.Rows(); i++ {
		row := output.RowRange(i, i+1)
		data := row.Clone()
		scores := data.ColRange(5, data.Cols())
		_, maxVal, _, maxLoc := gocv.MinMaxLoc(scores)
		classID := maxLoc.X
		confidence := maxVal

		if confidence > 0.3 && classID < len(gp.classNames) {
			// PROPER LETTERBOX COORDINATE TRANSFORMATION
			originalWidth := float32(frame.Cols())  // 2688
			originalHeight := float32(frame.Rows()) // 1520
			yoloSize := float32(832)                // 832x832 YOLO input

			// Calculate letterbox parameters
			aspectRatio := originalWidth / originalHeight // 1.768
			contentHeight := yoloSize / aspectRatio       // 470px (actual content height)
			yOffset := (yoloSize - contentHeight) / 2     // 181px (black bar offset)

			// Get normalized YOLO coordinates and convert properly
			xNorm := data.GetFloatAt(0, 0)
			yNorm := data.GetFloatAt(0, 1)
			wNorm := data.GetFloatAt(0, 2)
			hNorm := data.GetFloatAt(0, 3)

			// Convert to 832x832 space then remove letterbox offset
			xPixel832 := xNorm * yoloSize
			yPixel832 := yNorm * yoloSize
			wPixel832 := wNorm * yoloSize
			hPixel832 := hNorm * yoloSize

			yContentPixel := yPixel832 - yOffset // Remove letterbox offset

			// Scale to original frame dimensions
			centerX := int(xPixel832 * (originalWidth / yoloSize))
			centerY := int(yContentPixel * (originalHeight / contentHeight))
			width := int(wPixel832 * (originalWidth / yoloSize))
			height := int(hPixel832 * (originalHeight / contentHeight))
			left := centerX - width/2
			top := centerY - height/2
			rect := image.Rect(left, top, left+width, top+height)

			// Apply the same filtering as original code
			className := gp.classNames[classID]
			if (className == "boat" || className == "surfboard") && (width <= 50 || height <= 50) {
				scores.Close()
				data.Close()
				row.Close()
				continue
			}

			rects = append(rects, rect)
			classNames = append(classNames, className)
			confidences = append(confidences, float64(confidence))
		}

		scores.Close()
		data.Close()
		row.Close()
	}

	return &DetectionResult{
		Rects:       rects,
		ClassNames:  classNames,
		Confidences: confidences,
	}, nil
}

// Close releases resources used by the GPU provider
func (gp *GPUProvider) Close() error {
	gp.net.Close()
	return nil
}

// GetProviderInfo returns information about the GPU provider
func (gp *GPUProvider) GetProviderInfo() ProviderInfo {
	return ProviderInfo{
		Type:         "GPU",
		Backend:      "OpenCV CUDA",
		Device:       "NVIDIA GPU",
		EstimatedFPS: 200, // Optimistic estimate for GPU inference
		MemoryUsage:  "~2GB VRAM",
	}
}
