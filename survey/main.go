// survey - AI-driven PTZ camera site survey
//
// Two-phase operation for minimal camera downtime:
//
//   Phase 1 - Capture (camera in survey mode, fast):
//     ./survey -mode capture -output /tmp/survey -tilt-max 900
//     Blitzes through all positions, saves JPEGs + positions.json.
//     Camera returns to scanning immediately after.
//
//   Phase 2 - Analyze (camera back to normal, offline):
//     ./survey -mode analyze -output /tmp/survey -openai-key "sk-..."
//     ./survey -mode analyze -output /tmp/survey -api-url http://gpu1.mia2.doxx.net:11434/v1/chat/completions -model qwen3.5:9b
//     Reads saved images, runs AI analysis, generates grid.json + profiles.
//     Can run both models on the same capture data for comparison.
//
//   Combined (old behavior):
//     ./survey -mode all -output /tmp/survey -openai-key "sk-..."
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	runMode    = flag.String("mode", "all", "Run mode: capture, analyze, or all")
	noloAPI    = flag.String("nolo", "http://127.0.0.1:8080", "NOLO API endpoint")
	cameraURL  = flag.String("camera", "http://admin:password1@192.168.0.59", "Camera HTTP base URL")
	outputDir  = flag.String("output", "/home/blyon/NOLO/survey-data", "Output directory for images and analysis")
	openaiKey  = flag.String("openai-key", "", "OpenAI API key (not needed for ollama)")
	apiURL     = flag.String("api-url", "https://api.openai.com/v1/chat/completions", "Chat completions API endpoint")
	modelName  = flag.String("model", "gpt-5.2", "Model name for image analysis")
	panMin     = flag.Int("pan-min", 900, "Minimum pan position")
	panMax     = flag.Int("pan-max", 2550, "Maximum pan position")
	panStep    = flag.Int("pan-step", 100, "Pan step size")
	tiltMin    = flag.Int("tilt-min", 50, "Minimum tilt position")
	tiltMax    = flag.Int("tilt-max", 550, "Maximum tilt position")
	tiltStep   = flag.Int("tilt-step", 100, "Tilt step size")
	zoomLevels = flag.String("zooms", "10,60,120", "Comma-separated zoom levels (ascending, wide to tight)")
	settleTime = flag.Duration("settle", 3*time.Second, "Time to wait after moving camera")
)

// CaptureManifest is saved by capture phase, loaded by analyze phase
type CaptureManifest struct {
	CaptureDate string          `json:"capture_date"`
	PanRange    [2]int          `json:"pan_range"`
	TiltRange   [2]int          `json:"tilt_range"`
	PanStep     int             `json:"pan_step"`
	TiltStep    int             `json:"tilt_step"`
	ZoomLevels  []int           `json:"zoom_levels"`
	Positions   []CaptureEntry  `json:"positions"`
}

type CaptureEntry struct {
	Pan       int    `json:"pan"`
	Tilt      int    `json:"tilt"`
	Zoom      int    `json:"zoom"`
	Key       string `json:"key"`
	ImageFile string `json:"image_file"`
	Timestamp string `json:"timestamp"`
}

type FrameRegion struct {
	Region            string `json:"region"`
	Content           string `json:"content"`
	FalsePositiveRisk string `json:"false_positive_risk"`
}

type ZoomCapture struct {
	Zoom            int           `json:"zoom"`
	ImageFile       string        `json:"image_file"`
	Timestamp       string        `json:"timestamp"`
	Description     string        `json:"description"`
	Features        []string      `json:"features"`
	InterestScore   int           `json:"interest_score"`
	BoatsPossible   bool          `json:"boats_possible"`
	TrackingValue   string        `json:"tracking_value"`
	DetailVsWide    string        `json:"detail_vs_wide"`
	FrameRegions    []FrameRegion `json:"frame_regions"`
	MaskAdvice      string        `json:"mask_advice"`
	ExpectedObjects []string      `json:"expected_objects"`
	BoatCorridor    string        `json:"boat_corridor"`
}

type SurveyPoint struct {
	Pan          int           `json:"pan"`
	Tilt         int           `json:"tilt"`
	Key          string        `json:"key"`
	ZoomLayers   []ZoomCapture `json:"zoom_layers"`
	Scene        string        `json:"scene"`
	Neighbors    *Neighbors    `json:"neighbors,omitempty"`
	Exclusion    bool          `json:"exclusion"`
	BestUse      string        `json:"best_use"`
	ScanDwell    int           `json:"scan_dwell"`
	ScanPriority int           `json:"scan_priority"`
}

type Neighbors struct {
	Left  string `json:"left,omitempty"`
	Right string `json:"right,omitempty"`
	Above string `json:"above,omitempty"`
	Below string `json:"below,omitempty"`
}

type SurveyReport struct {
	SurveyDate     string                  `json:"survey_date"`
	Location       string                  `json:"location"`
	Model          string                  `json:"model"`
	PanRange       [2]int                  `json:"pan_range"`
	TiltRange      [2]int                  `json:"tilt_range"`
	ZoomLevels     []int                   `json:"zoom_levels"`
	TotalPoints    int                     `json:"total_points"`
	Grid           map[string]*SurveyPoint `json:"grid"`
	ScanPattern    []ScanPosition          `json:"recommended_scan_pattern"`
	ExclusionZones []ExclusionZone         `json:"exclusion_zones"`
}

type ScanPosition struct {
	Pan             int      `json:"pan"`
	Tilt            int      `json:"tilt"`
	Zoom            int      `json:"zoom"`
	Dwell           int      `json:"dwell_seconds"`
	Reason          string   `json:"reason"`
	MaskAdvice      string   `json:"mask_advice,omitempty"`
	ExpectedObjects []string `json:"expected_objects,omitempty"`
	BoatCorridor    string   `json:"boat_corridor,omitempty"`
}

type ExclusionZone struct {
	PanMin  int    `json:"pan_min"`
	PanMax  int    `json:"pan_max"`
	TiltMin int    `json:"tilt_min"`
	TiltMax int    `json:"tilt_max"`
	Reason  string `json:"reason"`
}

type ScanProfile struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	P1Track     string         `json:"p1_track"`
	P2Track     string         `json:"p2_track"`
	Positions   []ScanPosition `json:"positions"`
}

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime)

	switch *runMode {
	case "capture":
		runCapture()
	case "analyze":
		runAnalyze()
	case "all":
		runCapture()
		runAnalyze()
	default:
		log.Fatalf("Unknown mode %q - use capture, analyze, or all", *runMode)
	}
}

// ============================================================
// PHASE 1: CAPTURE - fast, camera in survey mode
// ============================================================

func runCapture() {
	log.Println("========================================")
	log.Println("  NOLO Survey - CAPTURE PHASE")
	log.Println("  Fast image capture, no AI processing")
	log.Println("========================================")

	os.MkdirAll(*outputDir, 0755)
	os.MkdirAll(filepath.Join(*outputDir, "images"), 0755)

	zooms := parseZooms(*zoomLevels)
	panCount := (*panMax-*panMin)/ *panStep + 1
	tiltCount := (*tiltMax-*tiltMin)/ *tiltStep + 1
	totalPositions := panCount * tiltCount
	totalCaptures := totalPositions * len(zooms)

	log.Printf("Pan: %d to %d (step %d) = %d positions", *panMin, *panMax, *panStep, panCount)
	log.Printf("Tilt: %d to %d (step %d) = %d positions", *tiltMin, *tiltMax, *tiltStep, tiltCount)
	log.Printf("Zooms: %v", zooms)
	log.Printf("Total: %d positions, %d captures", totalPositions, totalCaptures)
	log.Printf("Estimated time: %d seconds (%.1f minutes)", totalCaptures*4, float64(totalCaptures*4)/60.0)

	log.Println("[CAPTURE] Enabling survey mode...")
	if err := callNOLO("/survey/start"); err != nil {
		log.Fatalf("Failed to enable survey mode: %v", err)
	}
	defer func() {
		log.Println("[CAPTURE] Moving camera to scan start position (P1500 T100 Z10)...")
		moveCamera(1500, 100, 10)
		time.Sleep(2 * time.Second)
		log.Println("[CAPTURE] Disabling survey mode - camera back to scanning")
		callNOLO("/survey/stop")
	}()
	time.Sleep(1 * time.Second)

	var entries []CaptureEntry
	count := 0
	errors := 0
	start := time.Now()

	// Serpentine sweep: at each tilt level, sweep all pan positions left-to-right
	// then step tilt down and sweep right-to-left. Smoother camera movement.
	forward := true
	for tilt := *tiltMin; tilt <= *tiltMax; tilt += *tiltStep {
		var panPositions []int
		if forward {
			for p := *panMin; p <= *panMax; p += *panStep {
				panPositions = append(panPositions, p)
			}
		} else {
			for p := *panMax; p >= *panMin; p -= *panStep {
				panPositions = append(panPositions, p)
			}
		}
		forward = !forward

		for _, pan := range panPositions {
			key := fmt.Sprintf("P%04d_T%04d", pan, tilt)

			for _, zoom := range zooms {
				count++
				if count%20 == 0 {
					elapsed := time.Since(start)
					remaining := time.Duration(float64(elapsed) / float64(count) * float64(totalCaptures-count))
					log.Printf("[%d/%d] %s Z%d (%.0fs elapsed, ~%.0fs remaining)", count, totalCaptures, key, zoom, elapsed.Seconds(), remaining.Seconds())
				}

				if err := moveCamera(pan, tilt, zoom); err != nil {
					log.Printf("  ERROR move %s Z%d: %v", key, zoom, err)
					errors++
					continue
				}
				time.Sleep(*settleTime)

				imgFile := filepath.Join(*outputDir, "images", fmt.Sprintf("%s_Z%03d.jpg", key, zoom))
				if err := captureSnapshot(imgFile); err != nil {
					log.Printf("  ERROR snap %s Z%d: %v", key, zoom, err)
					errors++
					continue
				}

				entries = append(entries, CaptureEntry{
					Pan:       pan,
					Tilt:      tilt,
					Zoom:      zoom,
					Key:       key,
					ImageFile: imgFile,
					Timestamp: time.Now().Format(time.RFC3339),
				})
			}
		}
	}

	manifest := CaptureManifest{
		CaptureDate: time.Now().Format(time.RFC3339),
		PanRange:    [2]int{*panMin, *panMax},
		TiltRange:   [2]int{*tiltMin, *tiltMax},
		PanStep:     *panStep,
		TiltStep:    *tiltStep,
		ZoomLevels:  zooms,
		Positions:   entries,
	}
	saveJSON(filepath.Join(*outputDir, "positions.json"), manifest)

	elapsed := time.Since(start)
	log.Println("[CAPTURE] ========================================")
	log.Printf("[CAPTURE] Done! %d captures, %d errors, %.1f minutes", len(entries), errors, elapsed.Minutes())
	log.Printf("[CAPTURE] Images: %s/images/", *outputDir)
	log.Printf("[CAPTURE] Manifest: %s/positions.json", *outputDir)
	log.Println("[CAPTURE]")
	log.Println("[CAPTURE] Next step - analyze with GPT-5.2:")
	log.Printf("[CAPTURE]   ./survey -mode analyze -output %s -openai-key \"$(cat keys/openai-key.txt)\"", *outputDir)
	log.Println("[CAPTURE] Or analyze with Qwen 3.5 on gpu1:")
	log.Printf("[CAPTURE]   ./survey -mode analyze -output %s -api-url http://gpu1.mia2.doxx.net:11434/v1/chat/completions -model qwen3.5:9b", *outputDir)
}

// ============================================================
// PHASE 2: ANALYZE - offline, reads saved images
// ============================================================

func runAnalyze() {
	log.Println("========================================")
	log.Println("  NOLO Survey - ANALYZE PHASE")
	log.Printf("  Model: %s", *modelName)
	log.Println("========================================")

	manifestFile := filepath.Join(*outputDir, "positions.json")
	data, err := os.ReadFile(manifestFile)
	if err != nil {
		log.Fatalf("Cannot read %s - run capture phase first: %v", manifestFile, err)
	}

	var manifest CaptureManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		log.Fatalf("Bad manifest: %v", err)
	}

	// Restore step sizes from manifest for neighbor calculations
	*panStep = manifest.PanStep
	*tiltStep = manifest.TiltStep

	log.Printf("Loaded %d captures from %s", len(manifest.Positions), manifest.CaptureDate)
	log.Printf("Pan %d-%d (step %d), Tilt %d-%d (step %d), Zooms %v",
		manifest.PanRange[0], manifest.PanRange[1], manifest.PanStep,
		manifest.TiltRange[0], manifest.TiltRange[1], manifest.TiltStep,
		manifest.ZoomLevels)

	hasBackend := *openaiKey != "" || strings.Contains(*apiURL, "11434") || !strings.Contains(*apiURL, "openai.com")
	if !hasBackend {
		log.Fatal("No AI backend configured. Use -openai-key or -api-url")
	}

	// Group captures by position key for chained zoom analysis
	type posGroup struct {
		pan, tilt int
		captures  []CaptureEntry
	}
	groups := make(map[string]*posGroup)
	var groupOrder []string
	for _, e := range manifest.Positions {
		if g, ok := groups[e.Key]; ok {
			g.captures = append(g.captures, e)
		} else {
			groups[e.Key] = &posGroup{pan: e.Pan, tilt: e.Tilt, captures: []CaptureEntry{e}}
			groupOrder = append(groupOrder, e.Key)
		}
	}

	grid := make(map[string]*SurveyPoint)
	total := len(groupOrder)
	start := time.Now()

	for i, key := range groupOrder {
		g := groups[key]
		if (i+1)%5 == 0 || i == 0 {
			elapsed := time.Since(start)
			perPos := elapsed / time.Duration(max(i, 1))
			remaining := perPos * time.Duration(total-i)
			log.Printf("[%d/%d] %s (%d zooms) ~%.0fs remaining", i+1, total, key, len(g.captures), remaining.Seconds())
		}

		point := &SurveyPoint{
			Pan:  g.pan,
			Tilt: g.tilt,
			Key:  key,
		}

		var prevZoomDesc string
		for _, entry := range g.captures {
			capture := ZoomCapture{
				Zoom:      entry.Zoom,
				ImageFile: entry.ImageFile,
				Timestamp: entry.Timestamp,
			}

			analyzeZoomCapture(&capture, entry.Pan, entry.Tilt, prevZoomDesc)
			prevZoomDesc = capture.Description

			log.Printf("  Z%d score:%d boats:%-5v corridor:%-10s %s",
				capture.Zoom, capture.InterestScore, capture.BoatsPossible,
				capture.BoatCorridor, truncate(capture.Description, 60))

			point.ZoomLayers = append(point.ZoomLayers, capture)
		}

		if len(point.ZoomLayers) > 0 {
			point.Scene = point.ZoomLayers[0].Description
		}
		grid[key] = point

		if (i+1)%20 == 0 {
			saveJSON(filepath.Join(*outputDir, "grid_progress_"+*modelName+".json"), grid)
		}
	}

	// Build neighbor context and classify positions
	log.Println("[ANALYZE] Building neighbor context and classifying positions...")
	for _, point := range grid {
		point.Neighbors = buildNeighbors(grid, point.Pan, point.Tilt)

		hasBoats := false
		hasCorridor := false
		maxInterest := 0
		highFPRisk := false
		for _, zl := range point.ZoomLayers {
			if zl.BoatsPossible {
				hasBoats = true
			}
			if zl.BoatCorridor != "" && zl.BoatCorridor != "none" {
				hasCorridor = true
			}
			if zl.InterestScore > maxInterest {
				maxInterest = zl.InterestScore
			}
			for _, fr := range zl.FrameRegions {
				if fr.FalsePositiveRisk == "high" {
					highFPRisk = true
				}
			}
		}

		if !hasBoats && maxInterest <= 3 {
			point.Exclusion = true
			point.BestUse = "skip"
			point.ScanDwell = 0
			point.ScanPriority = 0
		} else if hasCorridor && maxInterest >= 7 {
			point.BestUse = "tracking"
			point.ScanDwell = 5
			point.ScanPriority = maxInterest
		} else if hasBoats && !highFPRisk {
			point.BestUse = "scanning"
			point.ScanDwell = 3
			point.ScanPriority = maxInterest
		} else if hasBoats && highFPRisk {
			point.BestUse = "scanning"
			point.ScanDwell = 2
			point.ScanPriority = max(maxInterest-2, 1)
		} else {
			point.BestUse = "skip"
			point.ScanDwell = 0
			point.ScanPriority = 0
		}
	}

	// Use model name in output filenames so GPT and Qwen results don't overwrite each other
	safeName := strings.ReplaceAll(*modelName, ":", "-")
	safeName = strings.ReplaceAll(safeName, "/", "-")

	report := buildReport(grid, manifest.ZoomLevels)
	report.Model = *modelName
	saveJSON(filepath.Join(*outputDir, "report_"+safeName+".json"), report)
	saveJSON(filepath.Join(*outputDir, "grid_"+safeName+".json"), grid)

	profiles := generateProfiles(grid, manifest.ZoomLevels)
	profileDir := filepath.Join(*outputDir, "profiles_"+safeName)
	os.MkdirAll(profileDir, 0755)
	for _, p := range profiles {
		saveJSON(filepath.Join(profileDir, p.Name+".json"), p)
		log.Printf("[PROFILE] %s: %d positions - %s", p.Name, len(p.Positions), p.Description)
	}

	// Summary stats
	var trackCount, scanCount, skipCount int
	for _, p := range grid {
		switch p.BestUse {
		case "tracking":
			trackCount++
		case "scanning":
			scanCount++
		default:
			skipCount++
		}
	}

	elapsed := time.Since(start)
	log.Println("[ANALYZE] ========================================")
	log.Printf("[ANALYZE] Done! %d positions analyzed in %.1f minutes", len(grid), elapsed.Minutes())
	log.Printf("[ANALYZE] Model: %s", *modelName)
	log.Printf("[ANALYZE] Tracking: %d | Scanning: %d | Skip: %d", trackCount, scanCount, skipCount)
	log.Printf("[ANALYZE] Exclusion zones: %d", len(report.ExclusionZones))
	log.Printf("[ANALYZE] Results: %s/*_%s.*", *outputDir, safeName)
}

// ============================================================
// AI Analysis
// ============================================================

func analyzeZoomCapture(capture *ZoomCapture, pan, tilt int, prevZoomDesc string) {
	imgData, err := os.ReadFile(capture.ImageFile)
	if err != nil {
		log.Printf("    ERROR reading image: %v", err)
		return
	}

	var contextLine string
	if prevZoomDesc != "" {
		contextLine = fmt.Sprintf(
			"\n\nCONTEXT: At the previous (wider) zoom level, this same position showed: \"%s\". "+
				"Now you are zoomed in tighter. Note what NEW detail is visible in detail_vs_wide.",
			prevZoomDesc)
	} else {
		contextLine = "\n\nThis is the widest zoom level. Describe the full scene."
	}

	prompt := fmt.Sprintf(
		"PTZ camera image: Pan=%d, Tilt=%d, Zoom=%d. Location: Miami River near Brickell Bridge, Miami FL. "+
			"This camera uses YOLO to detect and track boats on the river 24/7. "+
			"We need to build a spatial map so the tracking system knows what is WHERE in each frame to reduce false positives and optimize scanning.%s\n\n"+
			"Respond with ONLY this JSON (no markdown):\n"+
			"{\n"+
			`  "description": "what you see in 1-2 sentences",`+"\n"+
			`  "features": ["water","river","seawall","bridge","buildings","sky","marina","dock","boats","people","road","vegetation"],`+"\n"+
			`  "interest_score": 1-10,`+"\n"+
			`  "boats_possible": true/false,`+"\n"+
			`  "tracking_value": "high/medium/low/none",`+"\n"+
			`  "detail_vs_wide": "what this zoom reveals vs wider",`+"\n"+
			`  "frame_regions": [`+"\n"+
			`    {"region": "top_half or bottom_half or left_half or right_half or center or bottom_third or top_third", "content": "what occupies this region (water/seawall/bridge/sky/buildings)", "false_positive_risk": "high/medium/low/none - would YOLO mistake this for a boat?"}` + "\n"+
			`  ],`+"\n"+
			`  "mask_advice": "which frame regions should be masked to avoid false positives, e.g. 'mask bottom 30%% - seawall' or 'no masking needed - all open water'",`+"\n"+
			`  "expected_objects": ["list of objects that SHOULD legitimately appear here, e.g. boat, kayak, jetski, tugboat, cargo_ship, sailboat, person, car"],`+"\n"+
			`  "boat_corridor": "none/horizontal/vertical/diagonal - which direction do boats travel through this frame, or none if no boat traffic expected"`+"\n"+
			"}",
		pan, tilt, capture.Zoom, contextLine)

	reqBody := map[string]interface{}{
		"model": *modelName,
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": prompt},
					map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]string{
							"url":    "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(imgData),
							"detail": "low",
						},
					},
				},
			},
		},
		"max_completion_tokens": 500,
	}

	jsonData, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", *apiURL, bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	if *openaiKey != "" {
		req.Header.Set("Authorization", "Bearer "+*openaiKey)
	}

	timeout := 30 * time.Second
	if !strings.Contains(*apiURL, "openai.com") {
		timeout = 300 * time.Second
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		log.Printf("    API error: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Printf("    API %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
		return
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.Unmarshal(body, &result)
	if len(result.Choices) == 0 {
		return
	}

	content := strings.TrimSpace(result.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var parsed struct {
		Description     string        `json:"description"`
		Features        []string      `json:"features"`
		InterestScore   float64       `json:"interest_score"`
		BoatsPossible   bool          `json:"boats_possible"`
		TrackingValue   string        `json:"tracking_value"`
		DetailVsWide    string        `json:"detail_vs_wide"`
		FrameRegions    []FrameRegion `json:"frame_regions"`
		MaskAdvice      string        `json:"mask_advice"`
		ExpectedObjects []string      `json:"expected_objects"`
		BoatCorridor    string        `json:"boat_corridor"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		log.Printf("    Parse error: %v (raw: %s)", err, truncate(content, 80))
		capture.Description = content
		capture.InterestScore = 5
		return
	}

	capture.Description = parsed.Description
	capture.Features = parsed.Features
	capture.InterestScore = int(parsed.InterestScore + 0.5)
	capture.BoatsPossible = parsed.BoatsPossible
	capture.TrackingValue = parsed.TrackingValue
	capture.DetailVsWide = parsed.DetailVsWide
	capture.FrameRegions = parsed.FrameRegions
	capture.MaskAdvice = parsed.MaskAdvice
	capture.ExpectedObjects = parsed.ExpectedObjects
	capture.BoatCorridor = parsed.BoatCorridor
}

// ============================================================
// Post-processing: neighbors, reports, profiles
// ============================================================

func buildNeighbors(grid map[string]*SurveyPoint, pan, tilt int) *Neighbors {
	n := &Neighbors{}
	if p, ok := grid[fmt.Sprintf("P%04d_T%04d", pan-*panStep, tilt)]; ok && p.Scene != "" {
		n.Left = fmt.Sprintf("%s - %s", p.Key, p.Scene)
	}
	if p, ok := grid[fmt.Sprintf("P%04d_T%04d", pan+*panStep, tilt)]; ok && p.Scene != "" {
		n.Right = fmt.Sprintf("%s - %s", p.Key, p.Scene)
	}
	if p, ok := grid[fmt.Sprintf("P%04d_T%04d", pan, tilt+*tiltStep)]; ok && p.Scene != "" {
		n.Above = fmt.Sprintf("%s - %s", p.Key, p.Scene)
	}
	if p, ok := grid[fmt.Sprintf("P%04d_T%04d", pan, tilt-*tiltStep)]; ok && p.Scene != "" {
		n.Below = fmt.Sprintf("%s - %s", p.Key, p.Scene)
	}
	if n.Left == "" && n.Right == "" && n.Above == "" && n.Below == "" {
		return nil
	}
	return n
}

func buildReport(grid map[string]*SurveyPoint, zooms []int) *SurveyReport {
	report := &SurveyReport{
		SurveyDate:  time.Now().Format("2006-01-02 15:04"),
		Location:    "Brickell Bridge, Miami River, Miami FL",
		Model:       *modelName,
		PanRange:    [2]int{*panMin, *panMax},
		TiltRange:   [2]int{*tiltMin, *tiltMax},
		ZoomLevels:  zooms,
		TotalPoints: len(grid),
		Grid:        grid,
	}

	for _, point := range grid {
		if point.BestUse != "tracking" && point.BestUse != "scanning" {
			continue
		}
		bestZoom := zooms[0]
		bestInterest := 0
		for _, zl := range point.ZoomLayers {
			if zl.BoatsPossible && zl.InterestScore > bestInterest {
				bestInterest = zl.InterestScore
				bestZoom = zl.Zoom
			}
		}
		dwell := 3
		if point.BestUse == "tracking" {
			dwell = 5
		}
		report.ScanPattern = append(report.ScanPattern, ScanPosition{
			Pan:    point.Pan,
			Tilt:   point.Tilt,
			Zoom:   bestZoom,
			Dwell:  dwell,
			Reason: point.Scene,
		})
	}

	for _, point := range grid {
		if !point.Exclusion {
			continue
		}
		report.ExclusionZones = append(report.ExclusionZones, ExclusionZone{
			PanMin:  point.Pan,
			PanMax:  point.Pan,
			TiltMin: point.Tilt,
			TiltMax: point.Tilt,
			Reason:  point.Scene,
		})
	}

	return report
}

func generateProfiles(grid map[string]*SurveyPoint, zooms []int) []ScanProfile {
	var profiles []ScanProfile

	bestZoomForBoats := func(point *SurveyPoint) *ZoomCapture {
		var best *ZoomCapture
		for i := range point.ZoomLayers {
			zl := &point.ZoomLayers[i]
			if zl.BoatsPossible && (best == nil || zl.InterestScore > best.InterestScore) {
				best = zl
			}
		}
		return best
	}

	bestZoomForPeople := func(point *SurveyPoint) *ZoomCapture {
		var best *ZoomCapture
		for i := range point.ZoomLayers {
			zl := &point.ZoomLayers[i]
			for _, obj := range zl.ExpectedObjects {
				if obj == "person" || obj == "people" {
					if best == nil || zl.Zoom > best.Zoom {
						best = zl
					}
				}
			}
		}
		return best
	}

	makeScanPos := func(point *SurveyPoint, zl *ZoomCapture) ScanPosition {
		return ScanPosition{
			Pan:             point.Pan,
			Tilt:            point.Tilt,
			Zoom:            zl.Zoom,
			Dwell:           point.ScanDwell,
			Reason:          point.Scene,
			MaskAdvice:      zl.MaskAdvice,
			ExpectedObjects: zl.ExpectedObjects,
			BoatCorridor:    zl.BoatCorridor,
		}
	}

	// River profile
	river := ScanProfile{
		Name:        "river",
		Description: "River boat tracking - positions with active boat corridors, seawall/bridge excluded",
		P1Track:     "boat,surfboard",
		P2Track:     "all",
	}
	for _, point := range grid {
		if point.Exclusion {
			continue
		}
		zl := bestZoomForBoats(point)
		if zl == nil || zl.BoatCorridor == "" || zl.BoatCorridor == "none" {
			continue
		}
		river.Positions = append(river.Positions, makeScanPos(point, zl))
	}
	sortByPan(river.Positions)
	profiles = append(profiles, river)

	// Bridge profile
	bridge := ScanProfile{
		Name:        "bridge",
		Description: "Bridge activity monitoring - pedestrians, vehicles, bridge structure",
		P1Track:     "person,car,truck,bus,bicycle",
		P2Track:     "boat",
	}
	for _, point := range grid {
		hasBridge := false
		for _, zl := range point.ZoomLayers {
			for _, f := range zl.Features {
				if f == "bridge" || f == "road" {
					hasBridge = true
				}
			}
		}
		if !hasBridge {
			continue
		}
		zl := bestZoomForPeople(point)
		if zl == nil {
			for i := range point.ZoomLayers {
				for _, f := range point.ZoomLayers[i].Features {
					if f == "bridge" || f == "road" {
						zl = &point.ZoomLayers[i]
						break
					}
				}
				if zl != nil {
					break
				}
			}
		}
		if zl != nil {
			bridge.Positions = append(bridge.Positions, makeScanPos(point, zl))
		}
	}
	sortByPan(bridge.Positions)
	profiles = append(profiles, bridge)

	// Full profile
	full := ScanProfile{
		Name:        "full",
		Description: "Full coverage - river + bridge + all points of interest with appropriate masking",
		P1Track:     "boat,surfboard,person",
		P2Track:     "all",
	}
	for _, point := range grid {
		if point.Exclusion {
			continue
		}
		zl := bestZoomForBoats(point)
		if zl == nil && len(point.ZoomLayers) > 0 {
			zl = &point.ZoomLayers[0]
		}
		if zl != nil {
			full.Positions = append(full.Positions, makeScanPos(point, zl))
		}
	}
	sortByPan(full.Positions)
	profiles = append(profiles, full)

	// Event profile
	event := ScanProfile{
		Name:        "event",
		Description: "Event mode (Ultra, etc) - bridge focused with person tracking, river as secondary",
		P1Track:     "boat,person",
		P2Track:     "all",
	}
	for _, point := range grid {
		if point.Exclusion {
			continue
		}
		var zl *ZoomCapture
		for i := range point.ZoomLayers {
			if point.ZoomLayers[i].InterestScore >= 5 {
				zl = &point.ZoomLayers[i]
				break
			}
		}
		if zl != nil {
			pos := makeScanPos(point, zl)
			if pos.Dwell < 3 {
				pos.Dwell = 3
			}
			event.Positions = append(event.Positions, pos)
		}
	}
	sortByPan(event.Positions)
	profiles = append(profiles, event)

	return profiles
}

// ============================================================
// Camera control and utilities
// ============================================================

func sortByPan(positions []ScanPosition) {
	for i := 0; i < len(positions)-1; i++ {
		for j := i + 1; j < len(positions); j++ {
			if positions[j].Pan < positions[i].Pan ||
				(positions[j].Pan == positions[i].Pan && positions[j].Tilt < positions[i].Tilt) {
				positions[i], positions[j] = positions[j], positions[i]
			}
		}
	}
}

func parseZooms(s string) []int {
	var zooms []int
	for _, part := range strings.Split(s, ",") {
		var z int
		fmt.Sscanf(strings.TrimSpace(part), "%d", &z)
		if z > 0 {
			zooms = append(zooms, z)
		}
	}
	return zooms
}

func callNOLO(path string) error {
	resp, err := http.Get(*noloAPI + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
	return nil
}

func moveCamera(pan, tilt, zoom int) error {
	moveURL := fmt.Sprintf("%s/ISAPI/PTZCtrl/channels/1/absolute", *cameraURL)
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><PTZData><AbsoluteHigh><elevation>%d</elevation><azimuth>%d</azimuth><absoluteZoom>%d</absoluteZoom></AbsoluteHigh></PTZData>`,
		tilt, pan, zoom)

	cmd := exec.Command("curl", "-s", "--digest", "-u", extractCredentials(*cameraURL),
		"-X", "PUT", "-H", "Content-Type: application/xml",
		"-d", body, "--max-time", "5", "-w", "%{http_code}", "-o", "/dev/null", moveURL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("curl move failed: %v", err)
	}
	if !strings.Contains(string(out), "200") {
		return fmt.Errorf("move returned %s", string(out))
	}
	return nil
}

func captureSnapshot(outputFile string) error {
	snapshotURL := fmt.Sprintf("%s/ISAPI/Streaming/channels/101/picture", *cameraURL)
	cmd := exec.Command("curl", "-s", "--digest", "-u", extractCredentials(*cameraURL),
		"--max-time", "10", "-o", outputFile, snapshotURL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("curl snapshot failed: %v (%s)", err, string(out))
	}
	info, err := os.Stat(outputFile)
	if err != nil || info.Size() < 1000 {
		return fmt.Errorf("snapshot too small or missing (%v)", err)
	}
	return nil
}

func extractCredentials(urlStr string) string {
	if idx := strings.Index(urlStr, "://"); idx >= 0 {
		rest := urlStr[idx+3:]
		if atIdx := strings.Index(rest, "@"); atIdx >= 0 {
			return rest[:atIdx]
		}
	}
	return "admin:password1"
}

func saveJSON(path string, data interface{}) {
	f, err := os.Create(path)
	if err != nil {
		log.Printf("ERROR saving %s: %v", path, err)
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.Encode(data)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
