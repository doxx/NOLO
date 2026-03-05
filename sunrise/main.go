// sunrise - Automated daily sunrise time-lapse creator and publisher
//
// Runs as a daemon (systemd service). Each day it:
// 1. Fetches today's sunrise time from the API
// 2. Waits for the sunrise park window to end (7:30 AM ET)
// 3. Waits for the recorder segment covering the end of the window to finish
// 4. Finds all recording segments that overlap the park window (4:30-7:30 AM)
// 5. Stitches them together, trims 10s from start/end (camera movement buffer)
// 6. Creates a ~4 minute time-lapse with alternating background music
// 7. Generates a GPT-5.2 AI description from 3 frames
// 8. Uploads to YouTube
// 9. Sleeps until tomorrow's sunrise window
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
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

const (
	parkStartMinute = 4*60 + 30 // 4:30 AM
	parkEndMinute   = 7*60 + 30 // 7:30 AM
	trimSeconds     = 10        // Trim from start and end to avoid camera movement
	targetDuration  = 4 * 60    // 4-minute output timelapse
	saturationBoost = "1.15"
)

var (
	credentialsFile = flag.String("credentials", "../youtube-reset/client_secret.json", "OAuth credentials")
	tokenFile       = flag.String("token", "../youtube-reset/token.json", "OAuth token")
	recordingsDir   = flag.String("recordings", "../recordings", "Directory with MP4 segments")
	ffmpegPath      = flag.String("ffmpeg", "/usr/local/bin/ffmpeg", "Path to ffmpeg")
	outputDir       = flag.String("output", "/tmp/sunrise", "Temp directory for processing")
	openaiKey       = flag.String("openai-key", "", "OpenAI API key for generating descriptions")
	dryRun          = flag.Bool("dry-run", false, "Don't upload, just create the timelapse")
	oneShot         = flag.Bool("once", false, "Process today's sunrise and exit (don't loop)")

	// Music files - rotated daily (day of year % 3)
	musicFiles = []string{
		"Miami_Dawn_Circuit_up.wav",
		"Miami_Dawn_Circuit.wav",
		"Sun_Over_the_slow_Horizon.wav",
	}

	// Miami camera location
	cameraLat = 25.7695
	cameraLon = -80.1890
)

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime)

	log.Println("========================================")
	log.Println("  NOLO Sunrise Time-Lapse Daemon")
	log.Println("========================================")

	os.MkdirAll(*outputDir, 0755)

	if *oneShot {
		processSunrise()
		return
	}

	// Daemon loop - process one sunrise per day
	for {
		processSunrise()

		// Sleep until tomorrow 7:35 AM (5 min after park window ends)
		now := time.Now()
		loc, _ := time.LoadLocation("America/New_York")
		tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 7, 35, 0, 0, loc)
		sleepDuration := time.Until(tomorrow)
		log.Printf("[SUNRISE] Sleeping %v until tomorrow's processing window (%s)",
			sleepDuration.Round(time.Minute), tomorrow.Format("Mon Jan 2 3:04 PM"))
		time.Sleep(sleepDuration)
	}
}

func processSunrise() {
	loc, _ := time.LoadLocation("America/New_York")
	now := time.Now().In(loc)

	log.Printf("[SUNRISE] Processing sunrise for %s", now.Format("Monday, January 2, 2006"))

	// Check if today's sunrise was already published
	publishedDir := filepath.Join(*outputDir, "published")
	os.MkdirAll(publishedDir, 0755)
	todayPublished := filepath.Join(publishedDir, fmt.Sprintf("sunrise_%s.mp4", now.Format("2006-01-02")))
	if _, err := os.Stat(todayPublished); err == nil {
		log.Printf("[SUNRISE] Already published today (%s), skipping", filepath.Base(todayPublished))
		return
	}

	// Step 1: Get today's sunrise time for metadata
	sunrise, _, err := fetchSunriseTime()
	if err != nil {
		sunrise = time.Date(now.Year(), now.Month(), now.Day(), 6, 50, 0, 0, loc)
		log.Printf("[SUNRISE] API failed (%v) - using fallback sunrise 6:50 AM", err)
	}
	log.Printf("[SUNRISE] Today's sunrise: %s", sunrise.Format("3:04 PM"))

	// Step 2: Calculate park window for today
	parkStart := time.Date(now.Year(), now.Month(), now.Day(), parkStartMinute/60, parkStartMinute%60, 0, 0, loc)
	parkEnd := time.Date(now.Year(), now.Month(), now.Day(), parkEndMinute/60, parkEndMinute%60, 0, 0, loc)
	log.Printf("[SUNRISE] Park window: %s to %s", parkStart.Format("3:04 PM"), parkEnd.Format("3:04 PM"))

	// Step 3: Wait for park window to end
	if now.Before(parkEnd) {
		waitDuration := time.Until(parkEnd)
		log.Printf("[SUNRISE] Waiting %v for park window to end...", waitDuration.Round(time.Second))
		time.Sleep(waitDuration)
	}

	// Step 4: Wait for the segment covering park end to finish recording
	// Park ends at 7:30, the 7AM segment finishes at 8AM
	segmentEnd := time.Date(now.Year(), now.Month(), now.Day(), (parkEndMinute/60)+1, 5, 0, 0, loc)
	if time.Now().Before(segmentEnd) {
		waitDuration := time.Until(segmentEnd)
		log.Printf("[SUNRISE] Waiting %v for recording segment to complete...", waitDuration.Round(time.Second))
		time.Sleep(waitDuration)
	}

	// Step 5: Find all segments overlapping the park window
	segments := findSegmentsForWindow(parkStart, parkEnd)
	if len(segments) == 0 {
		log.Println("[SUNRISE] ERROR: No recording segments found for the park window")
		return
	}
	log.Printf("[SUNRISE] Found %d segments covering the park window", len(segments))

	// Step 6: Extract and stitch the sunrise footage
	rawFile := filepath.Join(*outputDir, "sunrise_raw.mp4")
	err = extractAndStitch(segments, parkStart, parkEnd, rawFile)
	if err != nil {
		log.Printf("[SUNRISE] ERROR: Failed to extract sunrise footage: %v", err)
		return
	}

	info, err := os.Stat(rawFile)
	if err != nil || info.Size() < 10*1024*1024 {
		log.Printf("[SUNRISE] ERROR: Raw file too small or missing (%.1f MB)", float64(info.Size())/(1024*1024))
		return
	}
	log.Printf("[SUNRISE] Raw footage: %.1f GB", float64(info.Size())/(1024*1024*1024))

	// Step 7: Get raw duration
	rawDuration := getVideoDuration(rawFile)
	if rawDuration <= 0 {
		log.Println("[SUNRISE] ERROR: Could not determine raw footage duration")
		return
	}
	log.Printf("[SUNRISE] Raw duration: %.0fs (%.1f minutes)", rawDuration, rawDuration/60)

	// Step 8: Pick today's music (alternate by day of year)
	musicIdx := now.YearDay() % len(musicFiles)
	musicPath := filepath.Join(filepath.Dir(os.Args[0]), musicFiles[musicIdx])
	// Fallback: check relative to working directory
	if _, err := os.Stat(musicPath); err != nil {
		musicPath = musicFiles[musicIdx]
	}
	log.Printf("[SUNRISE] Using music track: %s", musicFiles[musicIdx])

	// Step 9: Create 1-minute timelapse
	dateStr := now.Format("2006-01-02")
	outputDuration := 60 // 1 minute
	speedFactor := int(math.Round(rawDuration / float64(outputDuration)))
	if speedFactor < 2 {
		speedFactor = 2
	}

	timelapseFile := filepath.Join(*outputDir, fmt.Sprintf("sunrise_%s.mp4", dateStr))
	log.Printf("[SUNRISE] Creating 1-minute timelapse (%dx speed)...", speedFactor)
	err = createTimelapse(rawFile, timelapseFile, musicPath, speedFactor, outputDuration)
	if err != nil {
		log.Printf("[SUNRISE] ERROR: Timelapse creation failed: %v", err)
		return
	}
	info, _ = os.Stat(timelapseFile)
	log.Printf("[SUNRISE] Timelapse: %.1f MB", float64(info.Size())/(1024*1024))

	// Step 10: Generate AI description (title is fixed format)
	frameFiles := extractFramesForAI(timelapseFile)
	_, aiDesc := generateTitleAndDescription(frameFiles, sunrise, now.Format("Monday"), now.Format("Jan 2 2006"))

	// Fixed title format: "Miami Daily Sunrise Timelapse: Wednesday Feb 18 2026"
	title := fmt.Sprintf("Miami Daily Sunrise Timelapse: %s %s",
		now.Format("Monday"), now.Format("Jan 2 2006"))

	description := aiDesc
	if description == "" {
		description = "Daily sunrise time-lapse from the Miami River camera at Brickell Bridge, Miami FL."
	}
	description += fmt.Sprintf("\n\nSunrise: %s ET\n%dx speed time-lapse\nPark window: %s to %s\n\nWatch live: https://www.youtube.com/@MiamiRiverCamera/streams",
		sunrise.Format("3:04 PM"), speedFactor,
		parkStart.Format("3:04 PM"), parkEnd.Format("3:04 PM"))

	log.Printf("[SUNRISE] Title: %s", title)

	// Step 11: Upload
	if *dryRun {
		log.Printf("[SUNRISE] Dry run - skipping upload")
		log.Printf("[SUNRISE] Title: %s", title)
		log.Printf("[SUNRISE] Description:\n%s", description)
		log.Printf("[SUNRISE] File: %s", timelapseFile)
	} else {
		log.Println("[SUNRISE] Uploading to YouTube...")
		err = uploadToYouTube(timelapseFile, title, description)
		if err != nil {
			log.Printf("[SUNRISE] ERROR: Upload failed: %v", err)
			return
		}
	}

	// Save published timelapse (also serves as the "already published" marker)
	os.MkdirAll(publishedDir, 0755)
	pubFile := filepath.Join(publishedDir, fmt.Sprintf("sunrise_%s.mp4", dateStr))
	if out, err := exec.Command("cp", timelapseFile, pubFile).CombinedOutput(); err != nil {
		log.Printf("[SUNRISE] WARNING: Failed to save published copy: %v %s", err, string(out))
	} else {
		log.Printf("[SUNRISE] Saved: %s", pubFile)
	}

	// Cleanup raw file, keep timelapse
	os.Remove(rawFile)
	// Clean up part files
	parts, _ := filepath.Glob(filepath.Join(*outputDir, "part_*.mp4"))
	for _, p := range parts {
		os.Remove(p)
	}
	os.Remove(filepath.Join(*outputDir, "concat.txt"))
	for _, f := range frameFiles {
		os.Remove(f)
	}

	log.Println("[SUNRISE] Done!")
}

// segmentInfo holds info about a recording segment
type segmentInfo struct {
	Path      string
	StartTime time.Time
	Duration  float64
}

// findSegmentsForWindow finds all recording segments that overlap with the given time window
func findSegmentsForWindow(windowStart, windowEnd time.Time) []segmentInfo {
	loc := windowStart.Location()

	// Search both recordings/ and uploaded/ directories
	dirs := []string{*recordingsDir, filepath.Join(*recordingsDir, "uploaded")}

	var candidates []segmentInfo

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, "cam_") || !strings.HasSuffix(name, ".mp4") {
				continue
			}

			parts := strings.TrimPrefix(name, "cam_")
			parts = strings.TrimSuffix(parts, ".mp4")
			segStart, err := time.ParseInLocation("20060102_1504", parts, loc)
			if err != nil {
				continue
			}

			// Segment covers segStart to segStart + 1 hour (approximately)
			segEnd := segStart.Add(1 * time.Hour)

			// Check overlap with our window
			if segStart.Before(windowEnd) && segEnd.After(windowStart) {
				fp := filepath.Join(dir, name)
				info, err := os.Stat(fp)
				if err != nil || info.Size() < 10*1024*1024 {
					continue
				}
				candidates = append(candidates, segmentInfo{
					Path:      fp,
					StartTime: segStart,
				})
			}
		}
	}

	// Sort by start time
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].StartTime.Before(candidates[j].StartTime)
	})

	// Deduplicate (same segment might be in both recordings/ and uploaded/)
	var unique []segmentInfo
	seen := make(map[string]bool)
	for _, c := range candidates {
		base := filepath.Base(c.Path)
		if !seen[base] {
			seen[base] = true
			unique = append(unique, c)
		}
	}

	return unique
}

// extractAndStitch extracts the park window from segments and stitches into one file
func extractAndStitch(segments []segmentInfo, windowStart, windowEnd time.Time, outputFile string) error {
	var partFiles []string

	for i, seg := range segments {
		partFile := filepath.Join(*outputDir, fmt.Sprintf("part_%d.mp4", i))

		// Calculate offset and duration within this segment
		offset := 0.0
		if windowStart.After(seg.StartTime) {
			offset = windowStart.Sub(seg.StartTime).Seconds()
		}

		// Add trim buffer at start of first segment
		if i == 0 {
			offset += float64(trimSeconds)
		}

		segEnd := seg.StartTime.Add(1 * time.Hour)
		endTime := windowEnd
		if segEnd.Before(endTime) {
			endTime = segEnd
		}

		duration := endTime.Sub(seg.StartTime).Seconds() - offset
		// Trim from end of last segment
		if i == len(segments)-1 {
			duration -= float64(trimSeconds)
		}

		if duration <= 0 {
			continue
		}

		log.Printf("[SUNRISE] Extracting from %s: offset=%.0fs, duration=%.0fs",
			filepath.Base(seg.Path), offset, duration)

		args := []string{
			"-hide_banner", "-loglevel", "warning",
			"-ss", fmt.Sprintf("%.0f", offset),
			"-t", fmt.Sprintf("%.0f", duration),
			"-i", seg.Path,
			"-c", "copy",
			"-y", partFile,
		}

		cmd := exec.Command(*ffmpegPath, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Printf("[SUNRISE] WARNING: Extract failed for %s: %v", filepath.Base(seg.Path), err)
			continue
		}

		info, err := os.Stat(partFile)
		if err == nil && info.Size() > 1024*1024 {
			partFiles = append(partFiles, partFile)
		}
	}

	if len(partFiles) == 0 {
		return fmt.Errorf("no valid parts extracted")
	}

	if len(partFiles) == 1 {
		return os.Rename(partFiles[0], outputFile)
	}

	// Concat multiple parts
	concatFile := filepath.Join(*outputDir, "concat.txt")
	var concatLines []string
	for _, p := range partFiles {
		concatLines = append(concatLines, fmt.Sprintf("file '%s'", p))
	}
	os.WriteFile(concatFile, []byte(strings.Join(concatLines, "\n")), 0644)

	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-f", "concat", "-safe", "0",
		"-i", concatFile,
		"-c", "copy",
		"-y", outputFile,
	}

	cmd := exec.Command(*ffmpegPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func getVideoDuration(file string) float64 {
	// Use -i only (reads headers, doesn't decode the entire file)
	cmd := exec.Command(*ffmpegPath, "-hide_banner", "-i", file)
	output, _ := cmd.CombinedOutput()

	s := string(output)
	idx := strings.Index(s, "Duration: ")
	if idx < 0 {
		return 0
	}
	// Extract "HH:MM:SS.xx" after "Duration: "
	end := strings.Index(s[idx:], ",")
	if end < 0 {
		end = 22
	}
	durStr := s[idx+10 : idx+end]
	parts := strings.Split(strings.TrimSpace(durStr), ":")
	if len(parts) != 3 {
		return 0
	}

	var h, m float64
	fmt.Sscanf(parts[0], "%f", &h)
	fmt.Sscanf(parts[1], "%f", &m)
	var sec float64
	fmt.Sscanf(parts[2], "%f", &sec)
	return h*3600 + m*60 + sec
}

func createTimelapse(inputFile, outputFile, musicFile string, speedFactor, outputDur int) error {
	videoFilter := fmt.Sprintf("setpts=PTS/%d,eq=saturation=%s", speedFactor, saturationBoost)

	fadeOutStart := outputDur - 3

	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-i", inputFile,
	}

	// Check if music file exists
	_, musicErr := os.Stat(musicFile)
	if musicErr == nil {
		args = append(args, "-i", musicFile)
		audioFilter := fmt.Sprintf("afade=t=in:d=3,afade=t=out:st=%d:d=3", fadeOutStart)
		args = append(args,
			"-filter:v", videoFilter,
			"-filter:a", audioFilter,
			"-map", "0:v", "-map", "1:a",
			"-c:a", "aac", "-b:a", "192k", "-ac", "2", "-ar", "48000",
		)
	} else {
		log.Printf("[SUNRISE] Music file not found (%s), creating video-only", musicFile)
		args = append(args, "-filter:v", videoFilter, "-an")
	}

	args = append(args,
		"-t", fmt.Sprintf("%d", outputDur),
		"-r", "30",
		"-c:v", "h264_nvenc", "-preset", "p7", "-profile:v", "high",
		"-b:v", "20000k", "-maxrate", "22000k", "-bufsize", "40000k",
		"-pix_fmt", "yuv420p",
		"-y", outputFile,
	)

	cmd := exec.Command(*ffmpegPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// SunriseResponse from api.sunrise-sunset.org
type SunriseResponse struct {
	Results struct {
		Sunrise            string `json:"sunrise"`
		CivilTwilightBegin string `json:"civil_twilight_begin"`
	} `json:"results"`
	Status string `json:"status"`
}

func fetchSunriseTime() (sunrise, twilight time.Time, err error) {
	url := fmt.Sprintf("https://api.sunrise-sunset.org/json?lat=%.4f&lng=%.4f&formatted=0",
		cameraLat, cameraLon)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("API request failed: %v", err)
	}
	defer resp.Body.Close()

	var result SunriseResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("JSON decode failed: %v", err)
	}

	if result.Status != "OK" {
		return time.Time{}, time.Time{}, fmt.Errorf("API status: %s", result.Status)
	}

	sunrise, err = time.Parse(time.RFC3339, result.Results.Sunrise)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse sunrise: %v", err)
	}

	twilight, err = time.Parse(time.RFC3339, result.Results.CivilTwilightBegin)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse twilight: %v", err)
	}

	loc, _ := time.LoadLocation("America/New_York")
	return sunrise.In(loc), twilight.In(loc), nil
}

func extractFramesForAI(timelapseFile string) []string {
	var files []string
	dur := getVideoDuration(timelapseFile)
	if dur <= 0 {
		dur = float64(targetDuration)
	}

	offsets := []float64{dur * 0.15, dur * 0.5, dur * 0.85}
	for i, offset := range offsets {
		outFile := filepath.Join(*outputDir, fmt.Sprintf("ai_frame_%d.jpg", i))
		cmd := exec.Command(*ffmpegPath,
			"-hide_banner", "-loglevel", "error",
			"-ss", fmt.Sprintf("%.0f", offset),
			"-i", timelapseFile,
			"-frames:v", "1", "-q:v", "2",
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

func generateTitleAndDescription(frameFiles []string, sunrise time.Time, dayName, dateForTitle string) (string, string) {
	if *openaiKey == "" || len(frameFiles) == 0 {
		return "", ""
	}

	var parts []map[string]interface{}

	prompt := fmt.Sprintf(
		"These are 3 frames from a sunrise time-lapse video taken from a camera overlooking the Miami River near Brickell Bridge, Miami FL. "+
			"Today is %s, %s. Sunrise was at %s ET. "+
			"Respond in this exact JSON format:\n"+
			`{"title": "%s %s - <descriptive title about the sunrise, include 'Miami River' or 'Brickell'>", "description": "2-3 sentence YouTube description. Be informative about what's visible - sky, water, buildings, weather, boats. Don't repeat the title."}`,
		dayName, dateForTitle, sunrise.Format("3:04 PM"),
		dayName, dateForTitle)

	parts = append(parts, map[string]interface{}{
		"type": "text",
		"text": prompt,
	})

	for _, f := range frameFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		parts = append(parts, map[string]interface{}{
			"type": "image_url",
			"image_url": map[string]string{
				"url":    "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data),
				"detail": "low",
			},
		})
	}

	reqBody := map[string]interface{}{
		"model": "gpt-5.2",
		"messages": []map[string]interface{}{
			{"role": "user", "content": parts},
		},
		"max_completion_tokens": 300,
	}

	jsonData, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+*openaiKey)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		log.Printf("[AI] Request failed: %v", err)
		return "", ""
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Printf("[AI] Error %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
		return "", ""
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
		return "", ""
	}

	content := strings.TrimSpace(result.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var aiResponse struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(content), &aiResponse); err != nil {
		log.Printf("[AI] Failed to parse JSON: %v", err)
		return "", content
	}

	return aiResponse.Title, aiResponse.Description
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
	json.Unmarshal(tokenData, &token)

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
			CategoryId:  "19",
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

	// Refresh token
	tokenSource := config.TokenSource(oauth2.NoContext, &token)
	newToken, _ := tokenSource.Token()
	if newToken != nil {
		tokenJSON, _ := json.MarshalIndent(newToken, "", "  ")
		os.WriteFile(*tokenFile, tokenJSON, 0600)
	}

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ = math.Abs
