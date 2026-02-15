package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

const (
	segmentDuration = 8 * time.Hour
	segmentSeconds  = 8 * 60 * 60 // 28800
)

var (
	rtmpInput       = flag.String("input", "rtmp://localhost/live/stream", "RTMP input URL (SRS)")
	recordingDir    = flag.String("dir", "/home/blyon/NOLO/recordings", "Directory for recordings")
	credentialsFile = flag.String("credentials", "../youtube-reset/client_secret.json", "OAuth credentials")
	tokenFile       = flag.String("token", "../youtube-reset/token.json", "OAuth token")
	uploadEnabled   = flag.Bool("upload", true, "Enable YouTube uploads")
	deleteAfter     = flag.Bool("delete-after-upload", true, "Delete local file after successful upload")
	channelTitle    = flag.String("title-prefix", "Miami River Camera", "Video title prefix")
	ffmpegPath      = flag.String("ffmpeg", "/usr/local/bin/ffmpeg", "Path to ffmpeg")
)

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime)

	log.Println("========================================")
	log.Println("  NOLO Stream Recorder")
	log.Println("========================================")
	log.Printf("Input: %s", *rtmpInput)
	log.Printf("Recordings: %s", *recordingDir)
	log.Printf("Segment duration: %v", segmentDuration)
	log.Printf("Upload enabled: %v", *uploadEnabled)

	// Create recording directory
	if err := os.MkdirAll(*recordingDir, 0755); err != nil {
		log.Fatalf("Failed to create recording directory: %v", err)
	}

	// Set up YouTube service for uploads
	var ytService *youtube.Service
	if *uploadEnabled {
		svc, err := setupYouTube()
		if err != nil {
			log.Printf("[UPLOAD] YouTube setup failed: %v", err)
			log.Printf("[UPLOAD] Uploads disabled - will record only")
			*uploadEnabled = false
		} else {
			ytService = svc
			log.Println("[UPLOAD] YouTube API ready")
		}
	}

	// Start upload worker
	uploadChan := make(chan string, 10) // Queue of files to upload
	go uploadWorker(uploadChan, ytService)

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Main recording loop
	go func() {
		for {
			// Calculate segment start/end aligned to 8-hour blocks (0, 8, 16)
			now := time.Now()
			currentBlock := (now.Hour() / 8) * 8
			blockStart := time.Date(now.Year(), now.Month(), now.Day(), currentBlock, 0, 0, 0, now.Location())
			blockEnd := blockStart.Add(segmentDuration)

			// How long until this block ends
			remaining := time.Until(blockEnd)
			if remaining < 1*time.Minute {
				// Less than a minute left in block, wait for next
				time.Sleep(remaining)
				continue
			}

			// Generate filename
			filename := fmt.Sprintf("cam_%s.mp4", blockStart.Format("20060102_1504"))
			filepath := filepath.Join(*recordingDir, filename)

			log.Printf("[RECORD] Starting segment: %s (%.0f minutes remaining in block)",
				filename, remaining.Minutes())

			// Record until block end
			err := recordSegment(filepath, int(remaining.Seconds()))
			if err != nil {
				log.Printf("[RECORD_ERROR] %v", err)
				time.Sleep(10 * time.Second) // Brief pause before retry
				continue
			}

			log.Printf("[RECORD] Segment complete: %s", filename)

			// Check file exists and has size
			info, err := os.Stat(filepath)
			if err != nil || info.Size() < 1024*1024 { // Less than 1MB = probably bad
				log.Printf("[RECORD] Segment too small or missing, skipping upload: %s", filename)
				continue
			}

			log.Printf("[RECORD] Segment size: %.1f GB", float64(info.Size())/(1024*1024*1024))

			// Queue for upload
			if *uploadEnabled {
				uploadChan <- filepath
			}
		}
	}()

	// Wait for signal
	sig := <-sigChan
	log.Printf("Received %v, shutting down...", sig)
}

// recordSegment runs FFmpeg to record a segment
func recordSegment(outputPath string, durationSec int) error {
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",

		// Input from SRS
		"-i", *rtmpInput,

		// Copy video (already encoded by NOLO)
		"-c:v", "copy",

		// No audio from SRS (NOLO doesn't include audio)
		// If we want audio, we'd add the RTSP input here

		// Duration
		"-t", fmt.Sprintf("%d", durationSec),

		// Output
		"-movflags", "+faststart", // Move moov atom to start for streaming
		"-y", // Overwrite
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

// uploadWorker processes the upload queue
func uploadWorker(fileChan <-chan string, service *youtube.Service) {
	for filepath := range fileChan {
		if service == nil {
			log.Printf("[UPLOAD] Skipping upload (no YouTube service): %s", filepath)
			continue
		}

		log.Printf("[UPLOAD] Starting upload: %s", filepath)
		err := uploadToYouTube(service, filepath)
		if err != nil {
			log.Printf("[UPLOAD_ERROR] %s: %v", filepath, err)
			continue
		}

		log.Printf("[UPLOAD] Success: %s", filepath)

		if *deleteAfter {
			if err := os.Remove(filepath); err != nil {
				log.Printf("[CLEANUP_ERROR] Failed to delete %s: %v", filepath, err)
			} else {
				log.Printf("[CLEANUP] Deleted: %s", filepath)
			}
		}
	}
}

// uploadToYouTube uploads a video file
func uploadToYouTube(service *youtube.Service, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file: %v", err)
	}
	defer file.Close()

	// Parse timestamp from filename: cam_20260214_0800.mp4
	base := filepath.Base(filePath)
	base = strings.TrimPrefix(base, "cam_")
	base = strings.TrimSuffix(base, ".mp4")
	segTime, err := time.ParseInLocation("20060102_1504", base, time.Now().Location())
	if err != nil {
		segTime = time.Now()
	}

	endTime := segTime.Add(segmentDuration)
	title := fmt.Sprintf("%s - %s to %s",
		*channelTitle,
		segTime.Format("Jan 2, 2006 3PM"),
		endTime.Format("3PM"))

	description := fmt.Sprintf("%s\n\nRecorded %s to %s\nAI-powered PTZ camera tracking on the Miami River at Brickell Bridge, Miami FL\n\n#miami #miamiriver #brickell #boats #livestream",
		*channelTitle,
		segTime.Format("Monday, January 2, 2006 3:04 PM"),
		endTime.Format("3:04 PM MST"))

	video := &youtube.Video{
		Snippet: &youtube.VideoSnippet{
			Title:       title,
			Description: description,
			Tags:        []string{"miami", "miami river", "brickell", "boats", "live camera", "PTZ", "AI camera", "river cam"},
			CategoryId:  "19", // Travel & Events
		},
		Status: &youtube.VideoStatus{
			PrivacyStatus:           "public",
			MadeForKids:             false,
			SelfDeclaredMadeForKids: false,
		},
	}

	log.Printf("[UPLOAD] Title: %s", title)

	call := service.Videos.Insert([]string{"snippet", "status"}, video)
	call.Media(file)

	response, err := call.Do()
	if err != nil {
		return fmt.Errorf("upload failed: %v", err)
	}

	log.Printf("[UPLOAD] Published: https://youtube.com/watch?v=%s", response.Id)
	return nil
}

// setupYouTube initializes the YouTube API client
func setupYouTube() (*youtube.Service, error) {
	credentials, err := os.ReadFile(*credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %v", err)
	}

	config, err := google.ConfigFromJSON(credentials, youtube.YoutubeUploadScope)
	if err != nil {
		return nil, fmt.Errorf("parse credentials: %v", err)
	}

	token, err := tokenFromFile(*tokenFile)
	if err != nil {
		return nil, fmt.Errorf("read token: %v (run youtube-reset to authorize first)", err)
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

// HTTP handler for status checks
func startStatusServer() {
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"service":          "nolo-recorder",
			"segment_duration": segmentDuration.String(),
			"upload_enabled":   *uploadEnabled,
		})
	})
	http.ListenAndServe("127.0.0.1:8082", nil)
}
