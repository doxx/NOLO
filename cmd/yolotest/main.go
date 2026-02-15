// yolotest - Quick test tool to verify YOLOv8 ONNX model works with OpenCV DNN
// Build: go build -o yolotest ./cmd/yolotest/
// Run: ./yolotest -model models/yolov8n.onnx -image test.jpg
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"gocv.io/x/gocv"
)

func main() {
	modelPath := flag.String("model", "models/yolov8n.onnx", "Path to ONNX model")
	imagePath := flag.String("image", "", "Path to test image (optional, uses test pattern if empty)")
	inputSize := flag.Int("size", 640, "Model input size (640 for v8, 832 for v3)")
	confThresh := flag.Float64("conf", 0.25, "Confidence threshold")
	nmsThresh := flag.Float64("nms", 0.45, "NMS IoU threshold")
	useGPU := flag.Bool("gpu", true, "Try GPU acceleration")
	outputPath := flag.String("output", "", "Save annotated image to this path")
	v3Mode := flag.Bool("v3", false, "Use YOLOv3-tiny output parsing (for comparison)")
	flag.Parse()

	// Load class names
	classNames := loadCOCONames()

	// Load model
	fmt.Printf("[TEST] Loading model: %s\n", *modelPath)
	var net gocv.Net
	if *v3Mode {
		// v3-tiny uses weights+cfg
		weightsPath := strings.Replace(*modelPath, ".cfg", ".weights", 1)
		if strings.HasSuffix(*modelPath, ".weights") {
			weightsPath = *modelPath
			*modelPath = strings.Replace(*modelPath, ".weights", ".cfg", 1)
		}
		net = gocv.ReadNet(weightsPath, *modelPath)
	} else {
		net = gocv.ReadNetFromONNX(*modelPath)
	}
	if net.Empty() {
		fmt.Println("[ERROR] Failed to load model")
		os.Exit(1)
	}
	defer net.Close()

	// Set backend
	if *useGPU {
		fmt.Println("[TEST] Attempting GPU (CUDA) backend...")
		net.SetPreferableBackend(gocv.NetBackendCUDA)
		net.SetPreferableTarget(gocv.NetTargetCUDA)
	} else {
		net.SetPreferableBackend(gocv.NetBackendDefault)
		net.SetPreferableTarget(gocv.NetTargetCPU)
	}

	// Load or create test image
	var frame gocv.Mat
	if *imagePath != "" {
		frame = gocv.IMRead(*imagePath, gocv.IMReadColor)
		if frame.Empty() {
			fmt.Printf("[ERROR] Failed to read image: %s\n", *imagePath)
			os.Exit(1)
		}
	} else {
		// Create a test pattern (gradient)
		frame = gocv.NewMatWithSize(1520, 2688, gocv.MatTypeCV8UC3)
		fmt.Println("[TEST] Using synthetic 2688x1520 test frame (no image provided)")
	}
	defer frame.Close()
	fmt.Printf("[TEST] Input frame: %dx%d\n", frame.Cols(), frame.Rows())

	// Letterbox preprocessing (same as NOLO)
	size := *inputSize
	blob := letterboxAndBlob(frame, size)
	defer blob.Close()

	// Run inference
	fmt.Println("[TEST] Running inference...")
	net.SetInput(blob, "")

	start := time.Now()
	output := net.Forward("")
	elapsed := time.Since(start)
	defer output.Close()

	fmt.Printf("[TEST] Inference time: %v\n", elapsed)
	fmt.Printf("[TEST] Output shape: rows=%d, cols=%d, channels=%d, type=%d\n",
		output.Rows(), output.Cols(), output.Channels(), output.Type())
	fmt.Printf("[TEST] Output size: %v\n", output.Size())

	// Parse based on model type
	var detections []Detection
	if *v3Mode {
		detections = parseYOLOv3(output, frame, classNames, size, *confThresh)
	} else {
		detections = parseYOLOv8(output, frame, classNames, size, *confThresh, *nmsThresh)
	}

	// Print results
	fmt.Printf("\n[RESULTS] %d detections after NMS:\n", len(detections))
	for i, d := range detections {
		fmt.Printf("  [%d] %s (%.1f%%) at (%d,%d)-(%d,%d) size=%dx%d\n",
			i, d.ClassName, d.Confidence*100,
			d.Rect.Min.X, d.Rect.Min.Y, d.Rect.Max.X, d.Rect.Max.Y,
			d.Rect.Dx(), d.Rect.Dy())
	}

	// Count boats specifically
	boats := 0
	for _, d := range detections {
		if d.ClassName == "boat" {
			boats++
		}
	}
	fmt.Printf("\n[SUMMARY] Total: %d detections, Boats: %d\n", len(detections), boats)

	// Save annotated image if requested
	if *outputPath != "" && *imagePath != "" {
		annotated := frame.Clone()
		defer annotated.Close()
		for _, d := range detections {
			c := color.RGBA{0, 255, 0, 0} // Green
			if d.ClassName == "boat" {
				c = color.RGBA{0, 0, 255, 0} // Red for boats
			}
			gocv.Rectangle(&annotated, d.Rect, c, 2)
			label := fmt.Sprintf("%s %.0f%%", d.ClassName, d.Confidence*100)
			gocv.PutText(&annotated, label,
				image.Pt(d.Rect.Min.X, d.Rect.Min.Y-5),
				gocv.FontHersheyPlain, 1.2, c, 2)
		}
		gocv.IMWrite(*outputPath, annotated)
		fmt.Printf("[TEST] Saved annotated image: %s\n", *outputPath)
	}

	// Warmup + benchmark (5 runs)
	fmt.Println("\n[BENCHMARK] Running 5 inference passes...")
	var times []time.Duration
	for i := 0; i < 5; i++ {
		net.SetInput(blob, "")
		s := time.Now()
		out := net.Forward("")
		times = append(times, time.Since(s))
		out.Close()
	}
	var total time.Duration
	for _, t := range times {
		total += t
	}
	fmt.Printf("[BENCHMARK] Avg: %v, Times: %v\n", total/5, times)
	fmt.Printf("[BENCHMARK] Estimated FPS: %.1f\n", float64(time.Second)/float64(total/5))
}

type Detection struct {
	Rect       image.Rectangle
	ClassName  string
	ClassID    int
	Confidence float64
}

func parseYOLOv8(output gocv.Mat, frame gocv.Mat, classNames []string, inputSize int, confThresh, nmsThresh float64) []Detection {
	// YOLOv8 output: [1, 84, 8400] as a 3D tensor from OpenCV DNN
	// We need to access it as [84, 8400] and read column-wise (each column = one detection)

	outputSize := output.Size()
	fmt.Printf("[PARSE v8] Raw output size: %v, rows=%d, cols=%d, channels=%d, type=%d\n",
		outputSize, output.Rows(), output.Cols(), output.Channels(), output.Type())

	numClasses := 80
	var numDetections int
	var numFields int

	// Handle 3D tensor [1, 84, 8400] - reshape to 2D [84, 8400]
	var data2D gocv.Mat
	needClose := false
	if len(outputSize) == 3 {
		// 3D: [batch=1, fields=84, detections=8400]
		numFields = outputSize[1]     // 84
		numDetections = outputSize[2] // 8400
		// Reshape [1, 84, 8400] -> [84, 8400]
		data2D = output.Reshape(1, numFields)
		needClose = true
		fmt.Printf("[PARSE v8] Reshaped 3D [%d,%d,%d] -> 2D [%d,%d]\n",
			outputSize[0], outputSize[1], outputSize[2], numFields, numDetections)
	} else {
		// Already 2D
		data2D = output
		numFields = output.Rows()
		numDetections = output.Cols()
	}
	if needClose {
		defer data2D.Close()
	}

	fmt.Printf("[PARSE v8] Fields: %d (expect %d), Detections: %d\n", numFields, numClasses+4, numDetections)

	// Verify shape
	if numFields != numClasses+4 {
		fmt.Printf("[PARSE v8] ERROR: unexpected field count %d (expected %d)\n", numFields, numClasses+4)
		return nil
	}

	// Letterbox parameters
	originalWidth := float64(frame.Cols())
	originalHeight := float64(frame.Rows())
	yoloSize := float64(inputSize)
	aspectRatio := originalWidth / originalHeight
	contentHeight := yoloSize / aspectRatio
	yOffset := (yoloSize - contentHeight) / 2.0
	scaleX := originalWidth / yoloSize
	scaleY := originalHeight / contentHeight

	// Collect candidates
	var boxes []image.Rectangle
	var confidences []float32
	var classIDs []int
	var classNamesList []string

	for i := 0; i < numDetections; i++ {
		var cx, cy, w, h float64
		var maxScore float64
		var maxClassID int

		// data2D is [84, 8400] - row = field, col = detection
		cx = float64(data2D.GetFloatAt(0, i))
		cy = float64(data2D.GetFloatAt(1, i))
		w = float64(data2D.GetFloatAt(2, i))
		h = float64(data2D.GetFloatAt(3, i))
		for c := 0; c < numClasses; c++ {
			score := float64(data2D.GetFloatAt(c+4, i))
			if score > maxScore {
				maxScore = score
				maxClassID = c
			}
		}

		if maxScore < confThresh {
			continue
		}

		// v8 coordinates are in input pixel space (0-640)
		// Remove letterbox offset and scale to original frame
		origX := cx * scaleX
		origY := (cy - yOffset) * scaleY
		origW := w * scaleX
		origH := h * scaleY

		left := int(origX - origW/2)
		top := int(origY - origH/2)
		right := int(origX + origW/2)
		bottom := int(origY + origH/2)

		// Clamp to frame
		if left < 0 {
			left = 0
		}
		if top < 0 {
			top = 0
		}
		if right > int(originalWidth) {
			right = int(originalWidth)
		}
		if bottom > int(originalHeight) {
			bottom = int(originalHeight)
		}

		className := ""
		if maxClassID < len(classNames) {
			className = classNames[maxClassID]
		}

		boxes = append(boxes, image.Rect(left, top, right, bottom))
		confidences = append(confidences, float32(maxScore))
		classIDs = append(classIDs, maxClassID)
		classNamesList = append(classNamesList, className)
	}

	fmt.Printf("[PARSE v8] Candidates after confidence filter: %d\n", len(boxes))

	// Apply NMS
	indices := make([]int, 0)
	if len(boxes) > 0 {
		indices = gocv.NMSBoxes(boxes, confidences, float32(confThresh), float32(nmsThresh))
	}

	fmt.Printf("[PARSE v8] Detections after NMS: %d\n", len(indices))

	var detections []Detection
	for _, idx := range indices {
		detections = append(detections, Detection{
			Rect:       boxes[idx],
			ClassName:  classNamesList[idx],
			ClassID:    classIDs[idx],
			Confidence: float64(confidences[idx]),
		})
	}

	// Sort by confidence descending
	sort.Slice(detections, func(i, j int) bool {
		return detections[i].Confidence > detections[j].Confidence
	})

	return detections
}

func parseYOLOv3(output gocv.Mat, frame gocv.Mat, classNames []string, inputSize int, confThresh float64) []Detection {
	// YOLOv3 output: [N, 85] - x,y,w,h,objectness,class_scores...
	originalWidth := float64(frame.Cols())
	originalHeight := float64(frame.Rows())
	yoloSize := float64(inputSize)
	aspectRatio := originalWidth / originalHeight
	contentHeight := yoloSize / aspectRatio
	yOffset := (yoloSize - contentHeight) / 2.0

	var detections []Detection

	for i := 0; i < output.Rows(); i++ {
		row := output.RowRange(i, i+1)
		data := row.Clone()
		scores := data.ColRange(5, data.Cols())
		_, maxVal, _, maxLoc := gocv.MinMaxLoc(scores)

		if float64(maxVal) < confThresh {
			scores.Close()
			data.Close()
			row.Close()
			continue
		}

		classID := maxLoc.X
		xNorm := float64(data.GetFloatAt(0, 0))
		yNorm := float64(data.GetFloatAt(0, 1))
		wNorm := float64(data.GetFloatAt(0, 2))
		hNorm := float64(data.GetFloatAt(0, 3))

		xPixel := xNorm * yoloSize
		yPixel := yNorm * yoloSize
		wPixel := wNorm * yoloSize
		hPixel := hNorm * yoloSize

		yContent := yPixel - yOffset
		centerX := int(xPixel * (originalWidth / yoloSize))
		centerY := int(yContent * (originalHeight / contentHeight))
		width := int(wPixel * (originalWidth / yoloSize))
		height := int(hPixel * (originalHeight / contentHeight))

		left := centerX - width/2
		top := centerY - height/2

		className := ""
		if classID < len(classNames) {
			className = classNames[classID]
		}

		detections = append(detections, Detection{
			Rect:       image.Rect(left, top, left+width, top+height),
			ClassName:  className,
			ClassID:    classID,
			Confidence: float64(maxVal),
		})

		scores.Close()
		data.Close()
		row.Close()
	}

	return detections
}

func letterboxAndBlob(frame gocv.Mat, size int) gocv.Mat {
	yoloSize := size
	originalWidth := float64(frame.Cols())
	originalHeight := float64(frame.Rows())
	aspectRatio := originalWidth / originalHeight
	contentHeight := int(math.Round(float64(yoloSize) / aspectRatio))
	yOffset := (yoloSize - contentHeight) / 2

	// Create letterboxed canvas (gray 114 for YOLOv8 standard, black was used for v3)
	letterboxed := gocv.NewMatWithSize(yoloSize, yoloSize, gocv.MatTypeCV8UC3)
	letterboxed.SetTo(gocv.NewScalar(114, 114, 114, 0))

	// Resize frame to fit width, preserving aspect ratio
	resized := gocv.NewMat()
	defer resized.Close()
	gocv.Resize(frame, &resized, image.Pt(yoloSize, contentHeight), 0, 0, gocv.InterpolationLinear)

	// Copy to center of letterboxed image
	contentROI := letterboxed.Region(image.Rect(0, yOffset, yoloSize, yOffset+contentHeight))
	defer contentROI.Close()
	resized.CopyTo(&contentROI)

	// Create blob
	blob := gocv.BlobFromImage(letterboxed, 1.0/255.0,
		image.Pt(yoloSize, yoloSize),
		gocv.NewScalar(0, 0, 0, 0), true, false)
	letterboxed.Close()

	return blob
}

func loadCOCONames() []string {
	return []string{
		"person", "bicycle", "car", "motorbike", "aeroplane", "bus", "train", "truck",
		"boat", "traffic light", "fire hydrant", "stop sign", "parking meter", "bench",
		"bird", "cat", "dog", "horse", "sheep", "cow", "elephant", "bear", "zebra",
		"giraffe", "backpack", "umbrella", "handbag", "tie", "suitcase", "frisbee",
		"skis", "snowboard", "sports ball", "kite", "baseball bat", "baseball glove",
		"skateboard", "surfboard", "tennis racket", "bottle", "wine glass", "cup",
		"fork", "knife", "spoon", "bowl", "banana", "apple", "sandwich", "orange",
		"broccoli", "carrot", "hot dog", "pizza", "donut", "cake", "chair", "sofa",
		"pottedplant", "bed", "diningtable", "toilet", "tvmonitor", "laptop", "mouse",
		"remote", "keyboard", "cell phone", "microwave", "oven", "toaster", "sink",
		"refrigerator", "book", "clock", "vase", "scissors", "teddy bear",
		"hair drier", "toothbrush",
	}
}
