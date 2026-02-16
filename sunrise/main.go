// sunrise - Automated sunrise time-lapse creator and publisher
//
// This standalone program:
// 1. Fetches today's exact sunrise time from the API
// 2. Parks the camera at the sunrise position before civil twilight
// 3. Waits for the sunrise window to complete
// 4. Extracts the sunrise window from the recorder's MP4 segment
// 5. Creates a 2-minute 20x time-lapse with background music
// 6. Uploads to YouTube
//
// Run via systemd or cron. It handles one sunrise per execution then exits.
//
// Usage: ./sunrise -credentials ../youtube-reset/client_secret.json -token ../youtube-reset/token.json
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

var (
	credentialsFile = flag.String("credentials", "../youtube-reset/client_secret.json", "OAuth credentials")
	tokenFile       = flag.String("token", "../youtube-reset/token.json", "OAuth token")
	recordingsDir   = flag.String("recordings", "../recordings", "Directory with MP4 segments")
	musicFile       = flag.String("music", "../broadcast/track.aac", "Background music file")
	ffmpegPath      = flag.String("ffmpeg", "/usr/local/bin/ffmpeg", "Path to ffmpeg")
	outputDir       = flag.String("output", "/tmp/sunrise", "Temp directory for processing")
	cameraPan       = flag.Float64("pan", 1110, "Camera pan position for sunrise")
	cameraTilt      = flag.Float64("tilt", 20, "Camera tilt position for sunrise")
	cameraZoom      = flag.Float64("zoom", 10, "Camera zoom for sunrise")
	noloAPI         = flag.String("nolo-api", "http://127.0.0.1:8080", "NOLO HTTP API")
	preMinutes      = flag.Int("pre", 20, "Minutes before sunrise to start capture window")
	postMinutes     = flag.Int("post", 20, "Minutes after sunrise to end capture window")
	speedFactor     = flag.Int("speed", 20, "Time-lapse speed factor (20 = 20x)")
	dryRun          = flag.Bool("dry-run", false, "Don't upload, just create the timelapse")
	openaiKey       = flag.String("openai-key", "", "OpenAI API key for generating descriptions")

	// Miami camera location
	cameraLat = 25.7695
	cameraLon = -80.1890
)

// SunriseResponse from api.sunrise-sunset.org
type SunriseResponse struct {
	Results struct {
		Sunrise            string `json:"sunrise"`
		CivilTwilightBegin string `json:"civil_twilight_begin"`
	} `json:"results"`
	Status string `json:"status"`
}

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime)

	log.Println("========================================")
	log.Println("  NOLO Sunrise Time-Lapse Creator")
	log.Println("========================================")

	// Create output directory
	os.MkdirAll(*outputDir, 0755)

	// Step 1: Get today's sunrise time
	loc, _ := time.LoadLocation("America/New_York")
	sunrise, twilight, err := fetchSunriseTime()
	if err != nil {
		// Fallback: use 7:00 AM ET if API fails
		log.Printf("[SUNRISE] API failed (%v) - using fallback 6:00 AM to 8:00 AM window", err)
		today := time.Now().In(loc)
		sunrise = time.Date(today.Year(), today.Month(), today.Day(), 7, 0, 0, 0, loc)
		twilight = time.Date(today.Year(), today.Month(), today.Day(), 6, 30, 0, 0, loc)
	}
	log.Printf("[SUNRISE] Today's sunrise: %s", sunrise.Format("3:04 PM"))
	log.Printf("[SUNRISE] Civil twilight: %s", twilight.Format("3:04 PM"))

	// Calculate capture window
	var captureStart, captureEnd time.Time
	if err != nil {
		// Fallback: fixed 6 AM to 8 AM window
		today := time.Now().In(loc)
		captureStart = time.Date(today.Year(), today.Month(), today.Day(), 6, 0, 0, 0, loc)
		captureEnd = time.Date(today.Year(), today.Month(), today.Day(), 8, 0, 0, 0, loc)
	} else {
		captureStart = sunrise.Add(-time.Duration(*preMinutes) * time.Minute)
		captureEnd = sunrise.Add(time.Duration(*postMinutes) * time.Minute)
	}
	captureDuration := captureEnd.Sub(captureStart)
	log.Printf("[SUNRISE] Capture window: %s to %s (%v)",
		captureStart.Format("3:04 PM"), captureEnd.Format("3:04 PM"), captureDuration)

	// Step 2: Wait for park time (5 minutes before capture start for camera to settle)
	parkTime := captureStart.Add(-5 * time.Minute)
	now := time.Now()

	if now.After(captureEnd) {
		log.Println("[SUNRISE] Sunrise already passed today. Checking for existing recording to process...")
		processExistingRecording(sunrise, captureStart, captureEnd)
		return
	}

	if now.Before(parkTime) {
		waitDuration := parkTime.Sub(now)
		log.Printf("[SUNRISE] Waiting %v until park time (%s)...", waitDuration.Round(time.Second), parkTime.Format("3:04 PM"))
		time.Sleep(waitDuration)
	}

	// Step 3: Park the camera
	log.Println("[SUNRISE] Parking camera for sunrise...")
	parkCamera()

	// Step 4: Wait for capture window to complete
	now = time.Now()
	if now.Before(captureEnd) {
		waitDuration := captureEnd.Sub(now)
		log.Printf("[SUNRISE] Waiting %v for sunrise to complete...", waitDuration.Round(time.Second))
		// Keep parking the camera every 30 seconds in case tracking tries to take over
		parkTicker := time.NewTicker(30 * time.Second)
		defer parkTicker.Stop()
		for {
			select {
			case <-parkTicker.C:
				parkCamera()
				if time.Now().After(captureEnd) {
					goto sunriseComplete
				}
			}
		}
	}

sunriseComplete:
	log.Println("[SUNRISE] Sunrise window complete!")

	// Step 5: Wait for the segment MP4 to be available
	// The recorder writes segments on boundaries (midnight, 8AM, etc.)
	// We need the segment that contains our sunrise window
	log.Println("[SUNRISE] Waiting 2 minutes for recorder to flush...")
	time.Sleep(2 * time.Minute)

	// Step 6: Process the recording
	processExistingRecording(sunrise, captureStart, captureEnd)
}

func processExistingRecording(sunrise, captureStart, captureEnd time.Time) {
	// Find the segment MP4 that contains the sunrise
	segmentFile, segmentStart, err := findSegmentForTime(captureStart)
	if err != nil {
		log.Fatalf("[SUNRISE] Failed to find segment: %v", err)
	}
	log.Printf("[SUNRISE] Found segment: %s (starts at %s)", filepath.Base(segmentFile), segmentStart.Format("3:04 PM"))

	// Calculate offset into the segment
	offsetSeconds := captureStart.Sub(segmentStart).Seconds()
	durationSeconds := captureEnd.Sub(captureStart).Seconds()
	log.Printf("[SUNRISE] Extracting: offset=%.0fs, duration=%.0fs", offsetSeconds, durationSeconds)

	// Step 7: Extract the sunrise window
	rawFile := filepath.Join(*outputDir, "sunrise_raw.mp4")
	log.Println("[SUNRISE] Extracting sunrise window from segment...")
	err = extractWindow(segmentFile, rawFile, offsetSeconds, durationSeconds)
	if err != nil {
		log.Fatalf("[SUNRISE] Extract failed: %v", err)
	}

	// Check file size
	info, err := os.Stat(rawFile)
	if err != nil || info.Size() < 1024*1024 {
		log.Fatalf("[SUNRISE] Raw file too small or missing: %v", err)
	}
	log.Printf("[SUNRISE] Raw extract: %.1f GB", float64(info.Size())/(1024*1024*1024))

	// Step 8: Create time-lapse
	dateStr := sunrise.Format("2006-01-02")
	timelapseFile := filepath.Join(*outputDir, fmt.Sprintf("sunrise_%s.mp4", dateStr))
	log.Printf("[SUNRISE] Creating %dx time-lapse with music...", *speedFactor)
	err = createTimelapse(rawFile, timelapseFile)
	if err != nil {
		log.Fatalf("[SUNRISE] Timelapse creation failed: %v", err)
	}

	info, err = os.Stat(timelapseFile)
	if err != nil {
		log.Fatalf("[SUNRISE] Timelapse file missing: %v", err)
	}
	log.Printf("[SUNRISE] Timelapse: %.1f MB", float64(info.Size())/(1024*1024))

	// Step 9: Generate AI description from 3 frames
	log.Println("[SUNRISE] Extracting frames for AI description...")
	frameFiles := extractFramesForAI(timelapseFile)
	aiDescription := generateAIDescription(frameFiles, sunrise)
	if aiDescription != "" {
		log.Printf("[AI] Description: %s", aiDescription)
	}

	// Step 10: Upload to YouTube
	if *dryRun {
		log.Printf("[SUNRISE] Dry run - skipping upload. File: %s", timelapseFile)
	} else {
		title := fmt.Sprintf("Miami River Sunrise - %s", sunrise.Format("January 2, 2006"))
		description := aiDescription
		if description == "" {
			description = "Sunrise time-lapse from the Miami River camera."
		}
		description += fmt.Sprintf("\n\nSunrise: %s ET\n%dx speed time-lapse\n\nWatch live: https://www.youtube.com/@MiamiRiverCamera/streams",
			sunrise.Format("3:04 PM"), *speedFactor)
		log.Println("[SUNRISE] Uploading to YouTube...")
		err = uploadToYouTube(timelapseFile, title, description)
		if err != nil {
			log.Fatalf("[SUNRISE] Upload failed: %v", err)
		}
	}

	// Cleanup raw file (keep timelapse for 7 days)
	os.Remove(rawFile)
	log.Println("[SUNRISE] Done!")
}

func fetchSunriseTime() (sunrise, twilight time.Time, err error) {
	url := fmt.Sprintf("https://api.sunrise-sunset.org/json?lat=%.4f&lng=%.4f&formatted=0",
		cameraLat, cameraLon)

	resp, err := http.Get(url)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("API request failed: %v", err)
	}
	defer resp.Body.Close()

	var result SunriseResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("JSON decode failed: %v", err)
	}

	if result.Status != "OK" {
		return time.Time{}, time.Time{}, fmt.Errorf("API returned status: %s", result.Status)
	}

	sunrise, err = time.Parse(time.RFC3339, result.Results.Sunrise)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse sunrise: %v", err)
	}

	twilight, err = time.Parse(time.RFC3339, result.Results.CivilTwilightBegin)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse twilight: %v", err)
	}

	// Convert to local time
	loc, _ := time.LoadLocation("America/New_York")
	return sunrise.In(loc), twilight.In(loc), nil
}

func parkCamera() {
	// Send park command via NOLO API
	url := fmt.Sprintf("%s/ptz/preset/river", *noloAPI)
	resp, err := http.Get(url)
	if err != nil {
		// API might not be available, try direct PTZ
		log.Printf("[PARK] NOLO API unavailable, camera should be parked by tracking code")
		return
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
}

func findSegmentForTime(targetTime time.Time) (string, time.Time, error) {
	loc, _ := time.LoadLocation("America/New_York")

	// Segments are named cam_YYYYMMDD_HHMM.mp4
	// They start at boundaries: 0000, 0800, 1600 (8-hour segments)
	// Find which segment contains our target time

	entries, err := os.ReadDir(*recordingsDir)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read recordings dir: %v", err)
	}

	var bestFile string
	var bestStart time.Time

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "cam_") || !strings.HasSuffix(name, ".mp4") {
			continue
		}

		// Parse: cam_20260215_0000.mp4
		parts := strings.TrimPrefix(name, "cam_")
		parts = strings.TrimSuffix(parts, ".mp4")

		parsed, err := time.ParseInLocation("20060102_1504", parts, loc)
		if err != nil {
			continue
		}

		// Check if this segment could contain our target time
		// Segment covers parsed -> parsed + 8 hours (approx)
		if !parsed.After(targetTime) {
			if bestStart.IsZero() || parsed.After(bestStart) {
				bestStart = parsed
				bestFile = filepath.Join(*recordingsDir, name)
			}
		}
	}

	if bestFile == "" {
		return "", time.Time{}, fmt.Errorf("no segment found containing %s", targetTime.Format("15:04"))
	}

	// Verify file exists and is large enough
	info, err := os.Stat(bestFile)
	if err != nil || info.Size() < 100*1024*1024 { // At least 100MB
		return "", time.Time{}, fmt.Errorf("segment file too small or missing: %s", bestFile)
	}

	return bestFile, bestStart, nil
}

func extractWindow(inputFile, outputFile string, offsetSec, durationSec float64) error {
	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-ss", fmt.Sprintf("%.0f", offsetSec),
		"-t", fmt.Sprintf("%.0f", durationSec),
		"-i", inputFile,
		"-c", "copy",
		"-y", outputFile,
	}

	cmd := exec.Command(*ffmpegPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func createTimelapse(inputFile, outputFile string) error {
	// Calculate output duration for audio fade-out
	// Input duration / speed factor = output duration
	// 40 min / 20 = 2 min = 120 seconds
	outputDuration := 120 // approximate
	fadeOutStart := outputDuration - 3

	// Build filter: speed up video + slight saturation boost for sunrise colors
	videoFilter := fmt.Sprintf("setpts=PTS/%d,eq=saturation=1.15", *speedFactor)

	// Audio filter: trim to output duration, fade in/out
	audioFilter := fmt.Sprintf("afade=t=in:d=3,afade=t=out:st=%d:d=3", fadeOutStart)

	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-i", inputFile,
		"-stream_loop", "-1", "-i", *musicFile, // Loop music
		"-filter:v", videoFilter,
		"-filter:a", audioFilter,
		"-map", "0:v", "-map", "1:a",
		"-t", fmt.Sprintf("%d", outputDuration), // Cap at 2 minutes
		"-r", "30", // 30fps output
		"-c:v", "h264_nvenc",
		"-preset", "p7",
		"-profile:v", "high",
		"-b:v", "20000k",
		"-maxrate", "22000k",
		"-bufsize", "40000k",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-b:a", "192k",
		"-ac", "2",
		"-ar", "48000",
		"-y", outputFile,
	}

	cmd := exec.Command(*ffmpegPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func uploadToYouTube(filePath, title, description string) error {
	credentials, err := os.ReadFile(*credentialsFile)
	if err != nil {
		return fmt.Errorf("read credentials: %v", err)
	}

	config, err := google.ConfigFromJSON(credentials, youtube.YoutubeUploadScope)
	if err != nil {
		return fmt.Errorf("parse credentials: %v", err)
	}

	tokenData, err := os.ReadFile(*tokenFile)
	if err != nil {
		return fmt.Errorf("read token: %v", err)
	}

	var token oauth2.Token
	if err := json.Unmarshal(tokenData, &token); err != nil {
		return fmt.Errorf("parse token: %v", err)
	}

	client := config.Client(oauth2.NoContext, &token)
	service, err := youtube.NewService(oauth2.NoContext, option.WithHTTPClient(client))
	if err != nil {
		return fmt.Errorf("create service: %v", err)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file: %v", err)
	}
	defer file.Close()

	video := &youtube.Video{
		Snippet: &youtube.VideoSnippet{
			Title:       title,
			Description: description,
			Tags:        []string{"miami", "sunrise", "time-lapse", "timelapse", "miami river", "brickell", "4K"},
			CategoryId:  "19", // Travel & Events
		},
		Status: &youtube.VideoStatus{
			PrivacyStatus:           "public",
			MadeForKids:             false,
			SelfDeclaredMadeForKids: false,
		},
	}

	call := service.Videos.Insert([]string{"snippet", "status"}, video)
	call.Media(file)

	response, err := call.Do()
	if err != nil {
		return fmt.Errorf("upload: %v", err)
	}

	log.Printf("[UPLOAD] Published: https://youtube.com/watch?v=%s", response.Id)

	// Save refreshed token
	tokenSource := config.TokenSource(oauth2.NoContext, &token)
	newToken, err := tokenSource.Token()
	if err == nil {
		tokenJSON, _ := json.MarshalIndent(newToken, "", "  ")
		os.WriteFile(*tokenFile, tokenJSON, 0600)
	}

	return nil
}

// extractFramesForAI pulls 3 frames from the timelapse at 20%, 50%, 80% through
func extractFramesForAI(timelapseFile string) []string {
	var files []string
	for i, pct := range []int{15, 50, 85} {
		// 2-minute clip = 120 seconds, stay away from edges
		offset := float64(pct) * 115.0 / 100.0 // cap at 115s to avoid seeking past end
		outFile := filepath.Join(*outputDir, fmt.Sprintf("ai_frame_%d.jpg", i))
		cmd := exec.Command(*ffmpegPath,
			"-hide_banner", "-loglevel", "error",
			"-ss", fmt.Sprintf("%.0f", offset),
			"-i", timelapseFile,
			"-frames:v", "1",
			"-q:v", "2",
			"-y", outFile,
		)
		if err := cmd.Run(); err != nil {
			log.Printf("[AI] Failed to extract frame %d: %v", i, err)
			continue
		}
		files = append(files, outFile)
	}
	return files
}

// generateAIDescription sends frames to GPT-5.2 vision and gets a description
func generateAIDescription(frameFiles []string, sunrise time.Time) string {
	if *openaiKey == "" {
		log.Println("[AI] No OpenAI key provided, skipping AI description")
		return ""
	}
	if len(frameFiles) == 0 {
		return ""
	}

	// Build image content parts
	var imageParts []map[string]interface{}

	// Add text prompt
	prompt := fmt.Sprintf(
		"These are 3 frames from a sunrise time-lapse video taken from a camera overlooking the Miami River near Brickell, Miami FL. "+
			"Sunrise was at %s ET on %s. "+
			"Write a brief YouTube video description (2-3 sentences). Be informative and descriptive about what's visible - the sky, water, boats, buildings, weather conditions. "+
			"Don't be overly poetic. Just describe what happened during this sunrise for someone who might want to watch the clip.",
		sunrise.Format("3:04 PM"), sunrise.Format("January 2, 2006"))

	imageParts = append(imageParts, map[string]interface{}{
		"type": "text",
		"text": prompt,
	})

	// Add each frame as base64 image
	for _, f := range frameFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			log.Printf("[AI] Failed to read frame %s: %v", f, err)
			continue
		}
		b64 := base64.StdEncoding.EncodeToString(data)
		imageParts = append(imageParts, map[string]interface{}{
			"type": "image_url",
			"image_url": map[string]string{
				"url":    "data:image/jpeg;base64," + b64,
				"detail": "low",
			},
		})
	}

	// Build request
	reqBody := map[string]interface{}{
		"model": "gpt-5.2",
		"messages": []map[string]interface{}{
			{
				"role":    "user",
				"content": imageParts,
			},
		},
		"max_completion_tokens": 200,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		log.Printf("[AI] JSON marshal failed: %v", err)
		return ""
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[AI] Request creation failed: %v", err)
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+*openaiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[AI] API request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		log.Printf("[AI] API error %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
		return ""
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("[AI] Response parse failed: %v", err)
		return ""
	}

	if len(result.Choices) > 0 {
		desc := strings.TrimSpace(result.Choices[0].Message.Content)
		// Clean up any AI artifacts
		desc = strings.TrimPrefix(desc, "\"")
		desc = strings.TrimSuffix(desc, "\"")
		return desc
	}

	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// unused but keeping for reference
var _ = math.Abs
