// clipper - Extract, caption, and publish clips from NOLO recordings
//
// Usage:
//   ./clipper -file cam_20260216_0800.mp4 -start 57:00 -end 60:00 \
//     -title "Tugs and Container Ship" -openai-key sk-...
//
// It extracts the clip, sends 3 frames to GPT-5.2 for a description,
// combines with the recording's event metadata (weather, tide, vessels),
// and uploads to YouTube.
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
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

var (
	inputFile       = flag.String("file", "", "Input MP4 file (required)")
	startTime       = flag.String("start", "", "Start time in MM:SS or HH:MM:SS (required)")
	endTime         = flag.String("end", "", "End time in MM:SS or HH:MM:SS (if empty, goes to end of file)")
	duration        = flag.String("duration", "", "Duration in MM:SS (alternative to -end)")
	clipTitle       = flag.String("title", "", "Clip title (required)")
	extraInfo       = flag.String("info", "", "Extra info to include in the AI prompt (vessel names, events, etc)")
	musicFile       = flag.String("music", "../broadcast/track.aac", "Background music file (empty = keep original audio)")
	musicVolume     = flag.String("music-vol", "0.03", "Music volume (0.0-1.0, only if -music is set)")
	credentialsFile = flag.String("credentials", "../youtube-reset/client_secret.json", "OAuth credentials")
	tokenFile       = flag.String("token", "../youtube-reset/token.json", "OAuth token")
	openaiKey       = flag.String("openai-key", "", "OpenAI API key for AI-generated description")
	ffmpegPath      = flag.String("ffmpeg", "/usr/local/bin/ffmpeg", "Path to ffmpeg")
	outputDir       = flag.String("output", "/tmp/clipper", "Temp directory for processing")
	eventsDir       = flag.String("events", "../recordings", "Directory with event JSON files")
	dryRun          = flag.Bool("dry-run", false, "Don't upload, just create the clip")
	tags            = flag.String("tags", "miami,miamiriver,brickell,boats,livestream", "Comma-separated YouTube tags")
	speedup         = flag.Int("speed", 1, "Speed factor (1 = real-time, 2 = 2x, etc)")
)

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime)

	if *inputFile == "" || *startTime == "" || *clipTitle == "" {
		fmt.Println("Usage: clipper -file <mp4> -start <MM:SS> -title <title> [-end <MM:SS>] [-duration <MM:SS>]")
		flag.PrintDefaults()
		os.Exit(1)
	}

	log.Println("========================================")
	log.Println("  NOLO Clip Publisher")
	log.Println("========================================")

	os.MkdirAll(*outputDir, 0755)

	// Parse times
	startSec := parseTime(*startTime)
	var durationSec float64
	if *endTime != "" {
		durationSec = parseTime(*endTime) - startSec
	} else if *duration != "" {
		durationSec = parseTime(*duration)
	} else {
		durationSec = 0 // FFmpeg will go to end of file
	}

	log.Printf("[CLIP] Input: %s", *inputFile)
	log.Printf("[CLIP] Start: %s (%.0fs), Duration: %.0fs", *startTime, startSec, durationSec)

	// Step 1: Extract clip
	clipFile := filepath.Join(*outputDir, "clip_raw.mp4")
	log.Println("[CLIP] Extracting clip...")
	err := extractClip(*inputFile, clipFile, startSec, durationSec)
	if err != nil {
		log.Fatalf("[CLIP] Extract failed: %v", err)
	}

	info, _ := os.Stat(clipFile)
	log.Printf("[CLIP] Raw clip: %.1f MB", float64(info.Size())/(1024*1024))

	// Step 2: Process clip (add music, speed up if needed)
	finalFile := clipFile
	if *speedup > 1 || *musicFile != "" {
		finalFile = filepath.Join(*outputDir, "clip_final.mp4")
		log.Printf("[CLIP] Processing (speed: %dx, music: %v)...", *speedup, *musicFile != "")
		err = processClip(clipFile, finalFile)
		if err != nil {
			log.Fatalf("[CLIP] Processing failed: %v", err)
		}
		info, _ = os.Stat(finalFile)
		log.Printf("[CLIP] Final clip: %.1f MB", float64(info.Size())/(1024*1024))
	}

	// Step 3: Find event metadata
	metadata := findEventMetadata()

	// Step 4: Generate AI description
	log.Println("[CLIP] Extracting frames for AI description...")
	frameFiles := extractFrames(finalFile)
	aiDesc := generateDescription(frameFiles, metadata)
	if aiDesc != "" {
		log.Printf("[AI] Description: %s", aiDesc)
	}

	// Step 5: Build full description
	description := buildDescription(aiDesc, metadata)

	// Step 6: Upload
	if *dryRun {
		log.Println("[CLIP] Dry run - skipping upload")
		log.Printf("[CLIP] Title: %s", *clipTitle)
		log.Printf("[CLIP] Description:\n%s", description)
		log.Printf("[CLIP] File: %s", finalFile)
	} else {
		log.Println("[CLIP] Uploading to YouTube...")
		err = uploadToYouTube(finalFile, *clipTitle, description)
		if err != nil {
			log.Fatalf("[CLIP] Upload failed: %v", err)
		}
	}

	// Cleanup
	os.Remove(clipFile)
	if finalFile != clipFile {
		os.Remove(finalFile)
	}
	for _, f := range frameFiles {
		os.Remove(f)
	}
	log.Println("[CLIP] Done!")
}

func parseTime(t string) float64 {
	// Parse MM:SS or HH:MM:SS
	parts := strings.Split(t, ":")
	switch len(parts) {
	case 2:
		m, _ := strconv.ParseFloat(parts[0], 64)
		s, _ := strconv.ParseFloat(parts[1], 64)
		return m*60 + s
	case 3:
		h, _ := strconv.ParseFloat(parts[0], 64)
		m, _ := strconv.ParseFloat(parts[1], 64)
		s, _ := strconv.ParseFloat(parts[2], 64)
		return h*3600 + m*60 + s
	default:
		s, _ := strconv.ParseFloat(t, 64)
		return s
	}
}

func extractClip(input, output string, startSec, durationSec float64) error {
	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-ss", fmt.Sprintf("%.1f", startSec),
	}
	if durationSec > 0 {
		args = append(args, "-t", fmt.Sprintf("%.1f", durationSec))
	}
	args = append(args, "-i", input, "-c", "copy", "-y", output)

	cmd := exec.Command(*ffmpegPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func processClip(input, output string) error {
	args := []string{"-hide_banner", "-loglevel", "warning", "-i", input}

	var videoFilter string
	if *speedup > 1 {
		videoFilter = fmt.Sprintf("setpts=PTS/%d", *speedup)
	}

	if *musicFile != "" {
		args = append(args, "-stream_loop", "-1", "-i", *musicFile)

		audioFilter := fmt.Sprintf("[0:a]volume=0.7[orig];[1:a]volume=%s[music];[orig][music]amix=inputs=2:duration=first[aout]", *musicVolume)
		if videoFilter != "" {
			args = append(args, "-filter_complex", audioFilter, "-filter:v", videoFilter, "-map", "0:v", "-map", "[aout]")
		} else {
			args = append(args, "-filter_complex", audioFilter, "-map", "0:v", "-map", "[aout]")
		}
	} else if videoFilter != "" {
		args = append(args, "-filter:v", videoFilter)
	}

	args = append(args,
		"-c:v", "h264_nvenc", "-preset", "p7", "-profile:v", "high",
		"-b:v", "20000k", "-maxrate", "22000k", "-bufsize", "40000k",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k", "-ac", "2", "-ar", "48000",
		"-y", output,
	)

	cmd := exec.Command(*ffmpegPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func findEventMetadata() string {
	// Try to find event JSON files matching the input file's time
	// Event files are named events_YYYYMMDD_HHMM.json
	baseName := filepath.Base(*inputFile)

	// Extract date from filename: cam_20260216_0800.mp4
	re := regexp.MustCompile(`cam_(\d{8})_(\d{4})`)
	match := re.FindStringSubmatch(baseName)
	if match == nil {
		return ""
	}

	dateStr := match[1]
	hourStr := match[2]

	// Look for event files around this time
	var allEvents []string
	entries, err := os.ReadDir(*eventsDir)
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "events_"+dateStr+"_"+hourStr[:2]) && strings.HasSuffix(name, ".json") {
			data, err := os.ReadFile(filepath.Join(*eventsDir, name))
			if err == nil {
				allEvents = append(allEvents, string(data))
			}
		}
	}

	if len(allEvents) == 0 {
		return ""
	}

	// Combine events
	return strings.Join(allEvents, "\n")
}

func extractFrames(videoFile string) []string {
	var files []string
	// Get video duration first
	cmd := exec.Command(*ffmpegPath, "-hide_banner", "-i", videoFile, "-f", "null", "-")
	output, _ := cmd.CombinedOutput()
	// Default to extracting at 10s, 30s, 50s
	offsets := []float64{10, 30, 50}

	// Try to parse duration from ffmpeg output
	durRe := regexp.MustCompile(`Duration: (\d+):(\d+):(\d+)`)
	if match := durRe.FindStringSubmatch(string(output)); match != nil {
		h, _ := strconv.Atoi(match[1])
		m, _ := strconv.Atoi(match[2])
		s, _ := strconv.Atoi(match[3])
		total := float64(h*3600 + m*60 + s)
		if total > 10 {
			offsets = []float64{total * 0.15, total * 0.5, total * 0.85}
		}
	}

	for i, offset := range offsets {
		outFile := filepath.Join(*outputDir, fmt.Sprintf("clip_frame_%d.jpg", i))
		cmd := exec.Command(*ffmpegPath,
			"-hide_banner", "-loglevel", "error",
			"-ss", fmt.Sprintf("%.0f", offset),
			"-i", videoFile,
			"-frames:v", "1", "-q:v", "2",
			"-y", outFile,
		)
		if err := cmd.Run(); err != nil {
			continue
		}
		files = append(files, outFile)
	}
	return files
}

func generateDescription(frameFiles []string, metadata string) string {
	if *openaiKey == "" || len(frameFiles) == 0 {
		return ""
	}

	var parts []map[string]interface{}

	prompt := "These are frames from a video clip recorded by an AI-powered PTZ camera on the Miami River near Brickell Bridge, Miami FL. "
	if *extraInfo != "" {
		prompt += *extraInfo + " "
	}
	if metadata != "" {
		prompt += "Event metadata from the recording: " + metadata[:min(len(metadata), 500)] + " "
	}
	prompt += "Write a YouTube video description (2-3 sentences). Be informative and descriptive about what's visible. Mention any vessels, weather, or notable activity."

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
		"max_completion_tokens": 250,
	}

	jsonData, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+*openaiKey)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		log.Printf("[AI] Request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Printf("[AI] Error %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
		return ""
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.Unmarshal(body, &result)

	if len(result.Choices) > 0 {
		return strings.TrimSpace(result.Choices[0].Message.Content)
	}
	return ""
}

func buildDescription(aiDesc, metadata string) string {
	var parts []string
	if aiDesc != "" {
		parts = append(parts, aiDesc)
	}
	parts = append(parts, "")
	parts = append(parts, "AI-powered PTZ camera tracking on the Miami River at Brickell Bridge, Miami FL")
	parts = append(parts, "")
	parts = append(parts, "Watch live: https://www.youtube.com/@MiamiRiverCam")
	parts = append(parts, "")
	parts = append(parts, "#miami #miamiriver #brickell #boats #livestream")
	return strings.Join(parts, "\n")
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

	tagList := strings.Split(*tags, ",")

	video := &youtube.Video{
		Snippet: &youtube.VideoSnippet{
			Title:       title,
			Description: description,
			Tags:        tagList,
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
