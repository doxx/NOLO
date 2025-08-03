//go:build ignore
// +build ignore

package detection

import (
	"fmt"
	"image"
	"io/ioutil"
	"strings"
	"sync"

	"gocv.io/x/gocv"
	"gorgonia.org/tensor"
	"gorgonia.org/tensor/native"
)

// DetectionResult represents the output of object detection
type DetectionResult struct {
	Rects       []image.Rectangle
	ClassNames  []string
	Confidences []float64
}

// Model defines the interface for object detection models
type Model interface {
	// Detect performs object detection on a frame
	Detect(frame gocv.Mat) (*DetectionResult, error)
	// Close releases any resources used by the model
	Close() error
}

// YOLOv3Model implements the Model interface for YOLOv3-tiny
type YOLOv3Model struct {
	net        gocv.Net
	classNames []string
	mu         sync.Mutex
}

// NewYOLOv3Model creates a new YOLOv3-tiny model
func NewYOLOv3Model(weightsPath, configPath, namesPath string) (*YOLOv3Model, error) {
	net := gocv.ReadNet(weightsPath, configPath)
	net.SetPreferableBackend(gocv.NetBackendDefault)
	net.SetPreferableTarget(gocv.NetTargetCPU)

	namesBytes, err := ioutil.ReadFile(namesPath)
	if err != nil {
		return nil, fmt.Errorf("could not read class names: %v", err)
	}
	classNames := strings.Split(string(namesBytes), "\n")

	return &YOLOv3Model{
		net:        net,
		classNames: classNames,
	}, nil
}

// Detect implements the Model interface for YOLOv3-tiny
func (m *YOLOv3Model) Detect(frame gocv.Mat) (*DetectionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	blob := gocv.BlobFromImage(frame, 1.0/255.0, image.Pt(832, 832), gocv.NewScalar(0, 0, 0, 0), true, false)
	defer blob.Close()

	m.net.SetInput(blob, "")
	output := m.net.Forward("")
	defer output.Close()

	var rects []image.Rectangle
	var classNames []string
	var confidences []float64

	for i := 0; i < output.Rows(); i++ {
		row := output.RowRange(i, i+1)
		data := row.Clone()
		scores := data.ColRange(5, data.Cols())
		_, maxVal, _, maxLoc := gocv.MinMaxLoc(scores)
		classID := maxLoc.X
		confidence := maxVal

		if confidence > 0.3 && classID < len(m.classNames) {
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

			// NOTE: Size filtering is now handled dynamically in main.go based on P1/P2 configuration
			className := m.classNames[classID]

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

// Close implements the Model interface for YOLOv3-tiny
func (m *YOLOv3Model) Close() error {
	m.net.Close()
	return nil
}

// YOLOv8Model implements the Model interface for YOLOv8
type YOLOv8Model struct {
	model      *tensor.Dense
	classNames []string
	mu         sync.Mutex
}

// NewYOLOv8Model creates a new YOLOv8 model
func NewYOLOv8Model(modelPath, namesPath string) (*YOLOv8Model, error) {
	// Load ONNX model
	model, err := tensor.LoadFromFile(modelPath)
	if err != nil {
		return nil, fmt.Errorf("could not load ONNX model: %v", err)
	}

	namesBytes, err := ioutil.ReadFile(namesPath)
	if err != nil {
		return nil, fmt.Errorf("could not read class names: %v", err)
	}
	classNames := strings.Split(string(namesBytes), "\n")

	return &YOLOv8Model{
		model:      model,
		classNames: classNames,
	}, nil
}

// Detect implements the Model interface for YOLOv8
func (m *YOLOv8Model) Detect(frame gocv.Mat) (*DetectionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Preprocess image
	blob := gocv.BlobFromImage(frame, 1.0/255.0, image.Pt(640, 640), gocv.NewScalar(0, 0, 0, 0), true, false)
	defer blob.Close()

	// Convert blob to tensor
	inputData, err := native.MatrixF32(blob)
	if err != nil {
		return nil, fmt.Errorf("could not convert blob to tensor: %v", err)
	}

	// Create input tensor
	inputTensor := tensor.New(tensor.WithShape(1, 3, 640, 640), tensor.WithBacking(inputData))

	// Run inference
	outputTensor, err := m.model.Apply(inputTensor)
	if err != nil {
		return nil, fmt.Errorf("inference failed: %v", err)
	}

	// Get output data
	outputData, err := native.MatrixF32(outputTensor)
	if err != nil {
		return nil, fmt.Errorf("could not get output data: %v", err)
	}

	var rects []image.Rectangle
	var classNames []string
	var confidences []float64

	// Process outputs
	numDetections := outputTensor.Shape()[1]
	for i := 0; i < numDetections; i++ {
		// YOLOv8 output format: [x1, y1, x2, y2, confidence, class_scores...]
		confidence := float64(outputData[i*85+4])
		if confidence < 0.3 {
			continue
		}

		// Get class scores
		classScores := outputData[i*85+5 : (i+1)*85]
		classID := 0
		maxScore := float32(0)
		for j, score := range classScores {
			if score > maxScore {
				maxScore = score
				classID = j
			}
		}

		if classID >= len(m.classNames) {
			continue
		}

		// Convert normalized coordinates to pixel coordinates
		x1 := int(outputData[i*85] * float32(frame.Cols()))
		y1 := int(outputData[i*85+1] * float32(frame.Rows()))
		x2 := int(outputData[i*85+2] * float32(frame.Cols()))
		y2 := int(outputData[i*85+3] * float32(frame.Rows()))

		rect := image.Rect(x1, y1, x2, y2)

		// NOTE: Size filtering is now handled dynamically in main.go based on P1/P2 configuration
		className := m.classNames[classID]

		rects = append(rects, rect)
		classNames = append(classNames, className)
		confidences = append(confidences, float64(confidence))
	}

	return &DetectionResult{
		Rects:       rects,
		ClassNames:  classNames,
		Confidences: confidences,
	}, nil
}

// Close implements the Model interface for YOLOv8
func (m *YOLOv8Model) Close() error {
	m.model.Close()
	return nil
}

// FallbackModel implements the Model interface with fallback capability
type FallbackModel struct {
	primary     Model
	fallback    Model
	useFallback bool
	mu          sync.Mutex
}

// NewFallbackModel creates a new model with fallback capability
func NewFallbackModel(primary, fallback Model) *FallbackModel {
	return &FallbackModel{
		primary:     primary,
		fallback:    fallback,
		useFallback: false,
	}
}

// Detect implements the Model interface with fallback capability
func (m *FallbackModel) Detect(frame gocv.Mat) (*DetectionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result *DetectionResult
	var err error

	if !m.useFallback {
		result, err = m.primary.Detect(frame)
		if err != nil {
			debugMsg("PROVIDER_WARN", fmt.Sprintf("Primary model failed, switching to fallback: %v", err))
			m.useFallback = true
		}
	}

	if m.useFallback {
		result, err = m.fallback.Detect(frame)
		if err != nil {
			return nil, fmt.Errorf("both models failed: %v", err)
		}
	}

	return result, nil
}

// Close implements the Model interface for FallbackModel
func (m *FallbackModel) Close() error {
	m.primary.Close()
	m.fallback.Close()
	return nil
}
