package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

const (
	segmentDuration  = 1 * time.Hour
	maxUploadRetries = 3
	retryBaseDelay   = 30 * time.Second
	retentionPeriod  = 48 * time.Hour
)

// YouTubeProject holds credentials and service for one Google Cloud project
type YouTubeProject struct {
	Name    string
	Service *youtube.Service
}

var (
	rtmpInput      = flag.String("input", "rtmp://localhost/live/stream", "RTMP input URL (SRS)")
	recordingDir   = flag.String("dir", "/home/blyon/NOLO/recordings", "Directory for recordings")
	credentialsDir = flag.String("credentials-dir", "../youtube-reset/credentials", "Directory containing pub*/client_secret.json and pub*/token.json")
	uploadEnabled  = flag.Bool("upload", true, "Enable YouTube uploads")
	deleteAfter    = flag.Bool("delete-after-upload", false, "Delete local file after successful upload (overridden by retention)")
	retention      = flag.Duration("retention", retentionPeriod, "Keep raw recordings for this duration, delete older")
	channelTitle   = flag.String("title-prefix", "Miami River Camera", "Video title prefix")
	ffmpegPath     = flag.String("ffmpeg", "/usr/local/bin/ffmpeg", "Path to ffmpeg")

	// Upload tracking
	uploadInProgress atomic.Bool
	uploadMu         sync.Mutex
	currentUpload    string
)

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime)

	log.Println("========================================")
	log.Println("  NOLO Stream Recorder v2")
	log.Println("  1-hour segments, multi-project upload")
	log.Println("========================================")
	log.Printf("Input: %s", *rtmpInput)
	log.Printf("Recordings: %s", *recordingDir)
	log.Printf("Segment duration: %v", segmentDuration)
	log.Printf("Upload enabled: %v", *uploadEnabled)
	log.Printf("Retention: %v (rolling)", *retention)

	if err := os.MkdirAll(*recordingDir, 0755); err != nil {
		log.Fatalf("Failed to create recording directory: %v", err)
	}

	// Load all YouTube projects
	var projects []*YouTubeProject
	if *uploadEnabled {
		var err error
		projects, err = loadAllProjects(*credentialsDir)
		if err != nil {
			log.Printf("[UPLOAD] Failed to load projects: %v", err)
			log.Printf("[UPLOAD] Uploads disabled - will record only")
			*uploadEnabled = false
		} else {
			log.Printf("[UPLOAD] Loaded %d YouTube projects (%.0f uploads/day capacity)",
				len(projects), float64(len(projects))*6.25)
		}
	}

	// Start upload worker
	uploadChan := make(chan string, 30)
	go uploadWorker(uploadChan, projects)

	// Start status server
	go startStatusServer(projects)

	// Upload any leftover recordings from previous runs
	go uploadLeftovers(uploadChan)

	// Rolling cleanup of old recordings
	go cleanupOldRecordings()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Main recording loop
	go func() {
		for {
			now := time.Now()
			blockStart := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
			blockEnd := blockStart.Add(segmentDuration)

			remaining := time.Until(blockEnd)
			if remaining < 1*time.Minute {
				time.Sleep(remaining + 1*time.Second)
				continue
			}

			filename := fmt.Sprintf("cam_%s.mp4", blockStart.Format("20060102_1504"))
			filePath := filepath.Join(*recordingDir, filename)

			log.Printf("[RECORD] Starting segment: %s (%.0f minutes remaining in block)",
				filename, remaining.Minutes())

			err := recordSegment(filePath, int(remaining.Seconds()))
			if err != nil {
				log.Printf("[RECORD_ERROR] %v", err)
				time.Sleep(10 * time.Second)
				continue
			}

			log.Printf("[RECORD] Segment complete: %s", filename)

			info, err := os.Stat(filePath)
			if err != nil || info.Size() < 1024*1024 {
				log.Printf("[RECORD] Segment too small or missing, skipping upload: %s", filename)
				continue
			}

			log.Printf("[RECORD] Segment size: %.1f GB", float64(info.Size())/(1024*1024*1024))

			if *uploadEnabled {
				uploadChan <- filePath
			}
		}
	}()

	sig := <-sigChan
	log.Printf("Received %v, shutting down...", sig)

	if uploadInProgress.Load() {
		log.Printf("Upload in progress for %s - waiting up to 60s for completion...", currentUpload)
		deadline := time.After(60 * time.Second)
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-deadline:
				log.Printf("Upload deadline exceeded, forcing shutdown")
				return
			case <-tick.C:
				if !uploadInProgress.Load() {
					log.Printf("Upload completed, shutting down cleanly")
					return
				}
			}
		}
	}
}

// loadAllProjects scans the credentials directory for pub* subdirectories
func loadAllProjects(dir string) ([]*YouTubeProject, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read credentials dir %s: %v", dir, err)
	}

	var projects []*YouTubeProject
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "pub") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		credFile := filepath.Join(dir, name, "client_secret.json")
		tokFile := filepath.Join(dir, name, "token.json")

		svc, err := setupYouTube(credFile, tokFile)
		if err != nil {
			log.Printf("[UPLOAD] WARNING: Failed to load project %s: %v (skipping)", name, err)
			continue
		}

		projects = append(projects, &YouTubeProject{
			Name:    name,
			Service: svc,
		})
		log.Printf("[UPLOAD] Loaded project: %s", name)
	}

	if len(projects) == 0 {
		return nil, fmt.Errorf("no valid YouTube projects found in %s", dir)
	}

	return projects, nil
}

// recordSegment runs FFmpeg to record a segment
func recordSegment(outputPath string, durationSec int) error {
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-i", *rtmpInput,
		"-c:v", "copy",
		"-t", fmt.Sprintf("%d", durationSec),
		"-movflags", "+faststart",
		"-y",
		outputPath,
	}

	log.Printf("[FFMPEG] %s %s", *ffmpegPath, strings.Join(args, " "))

	cmd := exec.Command(*ffmpegPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ffmpeg: %v", err)
	}

	return cmd.Wait()
}

// uploadWorker processes the upload queue, rotating across projects
func uploadWorker(fileChan <-chan string, projects []*YouTubeProject) {
	if len(projects) == 0 {
		for fp := range fileChan {
			log.Printf("[UPLOAD] Skipping upload (no YouTube projects): %s", fp)
		}
		return
	}

	var counter uint64

	for filePath := range fileChan {
		// Pick the next project via round-robin
		idx := counter % uint64(len(projects))
		counter++
		project := projects[idx]

		uploadMu.Lock()
		currentUpload = filepath.Base(filePath)
		uploadMu.Unlock()
		uploadInProgress.Store(true)

		log.Printf("[UPLOAD] Starting upload via %s: %s", project.Name, filepath.Base(filePath))

		var err error
		for attempt := 1; attempt <= maxUploadRetries; attempt++ {
			err = uploadToYouTube(project, filePath)
			if err == nil {
				break
			}

			log.Printf("[UPLOAD_ERROR] %s (attempt %d/%d via %s): %v",
				filepath.Base(filePath), attempt, maxUploadRetries, project.Name, err)

			if attempt < maxUploadRetries {
				// Try the next project on retry
				idx = counter % uint64(len(projects))
				counter++
				project = projects[idx]
				delay := retryBaseDelay * time.Duration(attempt)
				log.Printf("[UPLOAD] Retrying in %v via %s...", delay, project.Name)
				time.Sleep(delay)
			}
		}

		uploadInProgress.Store(false)

		if err != nil {
			log.Printf("[UPLOAD_FAILED] All %d attempts failed for %s", maxUploadRetries, filepath.Base(filePath))
			continue
		}

		log.Printf("[UPLOAD] Success via %s: %s", project.Name, filepath.Base(filePath))

		if *deleteAfter {
			if err := os.Remove(filePath); err != nil {
				log.Printf("[CLEANUP_ERROR] Failed to delete %s: %v", filePath, err)
			} else {
				log.Printf("[CLEANUP] Deleted: %s", filePath)
			}
		}
	}
}

// uploadToYouTube uploads a video file using the given project's credentials
func uploadToYouTube(project *YouTubeProject, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file: %v", err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %v", err)
	}

	// Parse timestamp from filename: cam_20260214_0800.mp4
	base := filepath.Base(filePath)
	base = strings.TrimPrefix(base, "cam_")
	base = strings.TrimSuffix(base, ".mp4")
	segTime, err := time.ParseInLocation("20060102_1504", base, time.Now().Location())
	if err != nil {
		segTime = time.Now()
	}

	endTime := segTime.Add(segmentDuration)

	// Format times nicely for 1-hour segments
	title := fmt.Sprintf("%s - %s %s to %s",
		*channelTitle,
		segTime.Format("Jan 2, 2006"),
		segTime.Format("3:04 PM"),
		endTime.Format("3:04 PM"))

	// Build enriched description from event data
	description := buildDescription(segTime, endTime)

	video := &youtube.Video{
		Snippet: &youtube.VideoSnippet{
			Title:       title,
			Description: description,
			Tags:        []string{"miami", "miami river", "brickell", "boats", "live camera", "PTZ", "AI camera", "river cam"},
			CategoryId:  "19",
		},
		Status: &youtube.VideoStatus{
			PrivacyStatus:           "public",
			MadeForKids:             false,
			SelfDeclaredMadeForKids: false,
		},
	}

	log.Printf("[UPLOAD] Title: %s (%.1f GB via %s)", title,
		float64(fileInfo.Size())/(1024*1024*1024), project.Name)

	call := project.Service.Videos.Insert([]string{"snippet", "status"}, video)

	// Use resumable upload with progress tracking
	call.Media(file, googleapi.ChunkSize(16*1024*1024)) // 16MB chunks for resumable upload

	call.ProgressUpdater(func(current, total int64) {
		if total > 0 {
			pct := float64(current) / float64(total) * 100
			log.Printf("[UPLOAD] %s: %.1f%% (%.0f MB / %.0f MB)",
				filepath.Base(filePath),
				pct,
				float64(current)/(1024*1024),
				float64(total)/(1024*1024))
		}
	})

	response, err := call.Do()
	if err != nil {
		return fmt.Errorf("upload failed: %v", err)
	}

	log.Printf("[UPLOAD] Published: https://youtube.com/watch?v=%s", response.Id)
	return nil
}

// Event represents a single logged event from the chat bot
type Event struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
}

// buildDescription creates an enriched video description from event data
func buildDescription(segStart, segEnd time.Time) string {
	events := loadEventsForWindow(segStart, segEnd)

	var vessels []string
	var weather []string
	var tides []string
	vesselSeen := make(map[string]bool)

	for _, ev := range events {
		switch ev.Type {
		case "vessel":
			// Extract vessel name (first word or up to " at " / " near " / " approaching ")
			name := extractVesselName(ev.Message)
			if name != "" && !vesselSeen[name] {
				vesselSeen[name] = true
				timecode := formatTimecode(ev.Time, segStart)
				vessels = append(vessels, fmt.Sprintf("  %s - %s", timecode, ev.Message))
			}
		case "weather":
			weather = append(weather, ev.Message)
		case "tide":
			tides = append(tides, ev.Message)
		}
	}

	var desc strings.Builder
	desc.WriteString(fmt.Sprintf("%s\n\n", *channelTitle))
	desc.WriteString(fmt.Sprintf("Recorded %s to %s\n",
		segStart.Format("Monday, January 2, 2006 3:04 PM"),
		segEnd.Format("3:04 PM MST")))
	desc.WriteString("AI-powered PTZ camera tracking on the Miami River at Brickell Bridge, Miami FL\n")

	if len(weather) > 0 {
		desc.WriteString(fmt.Sprintf("\nWeather: %s\n", weather[len(weather)-1]))
	}

	if len(tides) > 0 {
		desc.WriteString(fmt.Sprintf("%s\n", tides[len(tides)-1]))
	}

	if len(vessels) > 0 {
		desc.WriteString(fmt.Sprintf("\nVessels spotted (%d):\n", len(vessels)))
		for _, v := range vessels {
			desc.WriteString(v + "\n")
		}
	}

	desc.WriteString("\nLive stream: https://www.youtube.com/@MiamiRiverCam\n")
	desc.WriteString("\n#miami #miamiriver #brickell #boats #livestream")

	return desc.String()
}

// loadEventsForWindow reads all event JSON files that fall within the time window
func loadEventsForWindow(start, end time.Time) []Event {
	entries, err := os.ReadDir(*recordingDir)
	if err != nil {
		log.Printf("[EVENTS] Cannot read recording dir: %v", err)
		return nil
	}

	var allEvents []Event
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "events_") || !strings.HasSuffix(name, ".json") {
			continue
		}

		// Parse file timestamp: events_20260215_1631.json
		ts := strings.TrimPrefix(name, "events_")
		ts = strings.TrimSuffix(ts, ".json")
		fileTime, err := time.ParseInLocation("20060102_1504", ts, start.Location())
		if err != nil {
			continue
		}

		// Include files within a generous window (events are logged every 15 min)
		if fileTime.Before(start.Add(-20*time.Minute)) || fileTime.After(end.Add(5*time.Minute)) {
			continue
		}

		fp := filepath.Join(*recordingDir, name)
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}

		var events []Event
		if err := json.Unmarshal(data, &events); err != nil {
			continue
		}

		// Only include events within our segment window
		for _, ev := range events {
			if !ev.Time.Before(start) && ev.Time.Before(end) {
				allEvents = append(allEvents, ev)
			}
		}
	}

	// Sort by time
	sort.Slice(allEvents, func(i, j int) bool {
		return allEvents[i].Time.Before(allEvents[j].Time)
	})

	return allEvents
}

// extractVesselName gets the vessel name from a message like "Summer at Brickell Bridge"
func extractVesselName(msg string) string {
	for _, sep := range []string{" at ", " near ", " approaching ", " heading "} {
		if idx := strings.Index(msg, sep); idx > 0 {
			return msg[:idx]
		}
	}
	return msg
}

// formatTimecode creates a timecode relative to segment start (e.g., "0:12:34")
func formatTimecode(eventTime, segStart time.Time) string {
	d := eventTime.Sub(segStart)
	if d < 0 {
		d = 0
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60
	return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
}

// uploadLeftovers finds and queues any .mp4 files from previous runs that weren't uploaded
func uploadLeftovers(uploadChan chan<- string) {
	time.Sleep(5 * time.Second) // Let the system settle

	entries, err := os.ReadDir(*recordingDir)
	if err != nil {
		log.Printf("[LEFTOVER] Cannot read recording dir: %v", err)
		return
	}

	// Find completed segments (not the currently recording one)
	now := time.Now()
	currentSegment := fmt.Sprintf("cam_%s.mp4", time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location()).Format("20060102_1504"))

	var leftovers []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "cam_") || !strings.HasSuffix(name, ".mp4") {
			continue
		}
		if name == currentSegment {
			continue // Skip the segment currently being recorded
		}

		info, err := entry.Info()
		if err != nil || info.Size() < 1024*1024 {
			continue // Skip tiny/broken files
		}

		leftovers = append(leftovers, filepath.Join(*recordingDir, name))
	}

	if len(leftovers) > 0 {
		sort.Strings(leftovers) // Upload oldest first
		log.Printf("[LEFTOVER] Found %d unuploaded segments from previous runs", len(leftovers))
		for _, fp := range leftovers {
			info, _ := os.Stat(fp)
			log.Printf("[LEFTOVER] Queuing: %s (%.1f GB)", filepath.Base(fp), float64(info.Size())/(1024*1024*1024))
			uploadChan <- fp
		}
	}
}

// cleanupOldRecordings deletes recordings and event files older than the retention period
func cleanupOldRecordings() {
	time.Sleep(30 * time.Second) // Let uploads queue first

	for {
		cutoff := time.Now().Add(-*retention)
		entries, err := os.ReadDir(*recordingDir)
		if err != nil {
			log.Printf("[CLEANUP] Cannot read recording dir: %v", err)
			time.Sleep(15 * time.Minute)
			continue
		}

		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				continue
			}

			name := entry.Name()

			// Clean up old .mp4 recordings
			if strings.HasSuffix(name, ".mp4") && info.ModTime().Before(cutoff) {
				fp := filepath.Join(*recordingDir, name)
				log.Printf("[CLEANUP] Deleting old recording: %s (%.1f GB, age: %s)",
					name, float64(info.Size())/(1024*1024*1024),
					time.Since(info.ModTime()).Round(time.Hour))
				if err := os.Remove(fp); err != nil {
					log.Printf("[CLEANUP_ERROR] %v", err)
				}
			}

			// Clean up old event JSON files
			if strings.HasPrefix(name, "events_") && strings.HasSuffix(name, ".json") && info.ModTime().Before(cutoff) {
				fp := filepath.Join(*recordingDir, name)
				if err := os.Remove(fp); err != nil {
					log.Printf("[CLEANUP_ERROR] %v", err)
				}
			}
		}

		time.Sleep(15 * time.Minute) // Check every 15 minutes
	}
}

// setupYouTube initializes a YouTube API client from credential files
func setupYouTube(credentialsFile, tokenFile string) (*youtube.Service, error) {
	credentials, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("read credentials %s: %v", credentialsFile, err)
	}

	config, err := google.ConfigFromJSON(credentials, youtube.YoutubeUploadScope)
	if err != nil {
		return nil, fmt.Errorf("parse credentials: %v", err)
	}

	token, err := tokenFromFile(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("read token %s: %v (run youtube-reset to authorize first)", tokenFile, err)
	}

	ctx := context.Background()
	client := config.Client(ctx, token)

	service, err := youtube.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("create service: %v", err)
	}

	return service, nil
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	token := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(token)
	return token, err
}

// startStatusServer provides a health check endpoint
func startStatusServer(projects []*YouTubeProject) {
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		projectNames := make([]string, len(projects))
		for i, p := range projects {
			projectNames[i] = p.Name
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"service":            "nolo-recorder",
			"version":            "v2",
			"segment_duration":   segmentDuration.String(),
			"upload_enabled":     *uploadEnabled,
			"projects":           projectNames,
			"project_count":      len(projects),
			"upload_in_progress": uploadInProgress.Load(),
			"current_upload":     currentUpload,
		})
	})

	// Readiness check - returns 503 if upload is in progress (don't restart me)
	http.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if uploadInProgress.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "upload in progress: "+currentUpload)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ready")
	})

	http.ListenAndServe("127.0.0.1:8082", nil)
}
