package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

// Config holds the application configuration
type Config struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Privacy     string `json:"privacy"` // public, unlisted, private
	StreamID    string `json:"stream_id"`
}

func main() {
	// Command line flags
	configFile := flag.String("config", "config.json", "Path to config file")
	credentialsFile := flag.String("credentials", "client_secret.json", "Path to OAuth credentials file")
	tokenFile := flag.String("token", "token.json", "Path to token file")
	createStream := flag.Bool("create-stream", false, "Create a new reusable stream")
	listStreams := flag.Bool("list-streams", false, "List existing streams")
	resetBroadcast := flag.Bool("reset", false, "Reset/create a new broadcast")
	endBroadcast := flag.Bool("end", false, "End the current broadcast")
	status := flag.Bool("status", false, "Show current broadcast status")
	flag.Parse()

	ctx := context.Background()

	// Load OAuth credentials
	credentials, err := os.ReadFile(*credentialsFile)
	if err != nil {
		log.Fatalf("Unable to read credentials file: %v\nDownload from Google Cloud Console -> APIs & Services -> Credentials", err)
	}

	config, err := google.ConfigFromJSON(credentials, youtube.YoutubeScope, youtube.YoutubeForceSslScope)
	if err != nil {
		log.Fatalf("Unable to parse credentials: %v", err)
	}

	// Get OAuth token
	client := getClient(ctx, config, *tokenFile)

	// Create YouTube service
	service, err := youtube.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Error creating YouTube service: %v", err)
	}

	// Load app config
	appConfig := loadConfig(*configFile)

	switch {
	case *listStreams:
		listAllStreams(service)
	case *createStream:
		createReusableStream(service, appConfig)
	case *resetBroadcast:
		resetLiveBroadcast(service, appConfig)
	case *endBroadcast:
		endLiveBroadcast(service)
	case *status:
		showStatus(service)
	default:
		flag.Usage()
		fmt.Println("\nExamples:")
		fmt.Println("  First time setup:")
		fmt.Println("    ./youtube-reset -create-stream")
		fmt.Println("")
		fmt.Println("  Before starting broadcast:")
		fmt.Println("    ./youtube-reset -reset")
		fmt.Println("")
		fmt.Println("  Check status:")
		fmt.Println("    ./youtube-reset -status")
	}
}

func loadConfig(path string) *Config {
	config := &Config{
		Title:       "Miami River Camera",
		Description: "AI-powered PTZ camera tracking on the Miami River",
		Privacy:     "public",
	}

	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("No config file found at %s, using defaults", path)
		return config
	}

	if err := json.Unmarshal(data, config); err != nil {
		log.Printf("Error parsing config: %v, using defaults", err)
	}

	return config
}

func saveConfig(path string, config *Config) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// getClient retrieves a token, saves it, and returns the generated client
func getClient(ctx context.Context, config *oauth2.Config, tokenFile string) *http.Client {
	token, err := tokenFromFile(tokenFile)
	if err != nil {
		token = getTokenFromWeb(config)
		saveToken(tokenFile, token)
	}
	return config.Client(ctx, token)
}

// tokenFromFile retrieves a token from a local file
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

// getTokenFromWeb uses OAuth2 to get a token
func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	// Use a random port for the callback
	config.RedirectURL = "http://localhost:8090/callback"

	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Open this URL in your browser:\n\n%s\n\n", authURL)

	// Start local server to receive callback
	codeChan := make(chan string)
	server := &http.Server{Addr: ":8090"}

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		fmt.Fprintf(w, "Authorization successful! You can close this window.")
		codeChan <- code
	})

	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	code := <-codeChan
	server.Shutdown(context.Background())

	token, err := config.Exchange(context.Background(), code)
	if err != nil {
		log.Fatalf("Unable to exchange code for token: %v", err)
	}

	return token
}

// saveToken saves a token to a file
func saveToken(path string, token *oauth2.Token) {
	dir := filepath.Dir(path)
	if dir != "." {
		os.MkdirAll(dir, 0755)
	}

	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("Unable to save token: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
	fmt.Printf("Token saved to %s\n", path)
}

// listAllStreams lists all live streams on the channel
func listAllStreams(service *youtube.Service) {
	call := service.LiveStreams.List([]string{"id", "snippet", "cdn", "status"}).Mine(true)
	response, err := call.Do()
	if err != nil {
		log.Fatalf("Error listing streams: %v", err)
	}

	if len(response.Items) == 0 {
		fmt.Println("No streams found. Create one with -create-stream")
		return
	}

	fmt.Println("Existing Streams:")
	fmt.Println("-----------------")
	for _, stream := range response.Items {
		fmt.Printf("ID: %s\n", stream.Id)
		fmt.Printf("  Title: %s\n", stream.Snippet.Title)
		fmt.Printf("  Status: %s\n", stream.Status.StreamStatus)
		if stream.Cdn != nil && stream.Cdn.IngestionInfo != nil {
			fmt.Printf("  RTMP URL: %s\n", stream.Cdn.IngestionInfo.IngestionAddress)
			fmt.Printf("  Stream Key: %s\n", stream.Cdn.IngestionInfo.StreamName)
		}
		fmt.Println()
	}
}

// createReusableStream creates a new reusable stream
func createReusableStream(service *youtube.Service, config *Config) {
	stream := &youtube.LiveStream{
		Snippet: &youtube.LiveStreamSnippet{
			Title:       config.Title + " Stream",
			Description: "Reusable stream for " + config.Title,
		},
		Cdn: &youtube.CdnSettings{
			FrameRate:     "variable",
			IngestionType: "rtmp",
			Resolution:    "variable",
		},
		ContentDetails: &youtube.LiveStreamContentDetails{
			IsReusable: true,
		},
	}

	call := service.LiveStreams.Insert([]string{"snippet", "cdn", "contentDetails", "status"}, stream)
	response, err := call.Do()
	if err != nil {
		log.Fatalf("Error creating stream: %v", err)
	}

	fmt.Println("Stream created successfully!")
	fmt.Printf("Stream ID: %s\n", response.Id)
	fmt.Printf("RTMP URL: %s\n", response.Cdn.IngestionInfo.IngestionAddress)
	fmt.Printf("Stream Key: %s\n", response.Cdn.IngestionInfo.StreamName)
	fmt.Println("\nAdd this stream ID to your config.json:")
	fmt.Printf("  \"stream_id\": \"%s\"\n", response.Id)

	// Save stream ID to config
	config.StreamID = response.Id
	saveConfig("config.json", config)
}

// resetLiveBroadcast creates a new broadcast and binds it to the stream
func resetLiveBroadcast(service *youtube.Service, config *Config) {
	if config.StreamID == "" {
		log.Fatal("No stream_id in config. Run -create-stream first or add stream_id to config.json")
	}

	// First, end any active broadcasts
	endActiveBroadcasts(service)

	// Create new broadcast
	scheduledStart := time.Now().Add(1 * time.Minute).Format(time.RFC3339)

	broadcast := &youtube.LiveBroadcast{
		Snippet: &youtube.LiveBroadcastSnippet{
			Title:              config.Title,
			Description:        config.Description,
			ScheduledStartTime: scheduledStart,
		},
		Status: &youtube.LiveBroadcastStatus{
			PrivacyStatus:           config.Privacy,
			SelfDeclaredMadeForKids: false,
		},
		ContentDetails: &youtube.LiveBroadcastContentDetails{
			EnableAutoStart:  true,  // Auto-transition to live when video flows
			EnableAutoStop:   true,  // Auto-end when stream stops
			EnableDvr:        true,  // Allow DVR/rewind
			RecordFromStart:  true,  // Record the stream
			EnableClosedCaptions: false,
			LatencyPreference: "normal", // normal, low, ultraLow
		},
	}

	call := service.LiveBroadcasts.Insert([]string{"snippet", "status", "contentDetails"}, broadcast)
	response, err := call.Do()
	if err != nil {
		log.Fatalf("Error creating broadcast: %v", err)
	}

	fmt.Printf("Broadcast created: %s\n", response.Id)
	fmt.Printf("Title: %s\n", response.Snippet.Title)
	fmt.Printf("Privacy: %s\n", response.Status.PrivacyStatus)

	// Bind broadcast to stream
	bindCall := service.LiveBroadcasts.Bind(response.Id, []string{"id", "snippet", "contentDetails", "status"})
	bindCall.StreamId(config.StreamID)
	_, err = bindCall.Do()
	if err != nil {
		log.Fatalf("Error binding broadcast to stream: %v", err)
	}

	fmt.Println("Broadcast bound to stream successfully!")
	fmt.Println("\nBroadcast is ready. Start streaming and it will auto-go-live.")
	fmt.Printf("Watch at: https://youtube.com/watch?v=%s\n", response.Id)
}

// endActiveBroadcasts ends any currently active broadcasts
func endActiveBroadcasts(service *youtube.Service) {
	call := service.LiveBroadcasts.List([]string{"id", "status"}).Mine(true).BroadcastStatus("active")
	response, err := call.Do()
	if err != nil {
		log.Printf("Error checking active broadcasts: %v", err)
		return
	}

	for _, broadcast := range response.Items {
		fmt.Printf("Ending active broadcast: %s\n", broadcast.Id)
		transitionCall := service.LiveBroadcasts.Transition("complete", broadcast.Id, []string{"id", "status"})
		_, err := transitionCall.Do()
		if err != nil {
			log.Printf("Error ending broadcast %s: %v", broadcast.Id, err)
		}
	}

	// Also check for "live" status broadcasts
	call = service.LiveBroadcasts.List([]string{"id", "status"}).Mine(true).BroadcastStatus("live")
	response, err = call.Do()
	if err != nil {
		return
	}

	for _, broadcast := range response.Items {
		fmt.Printf("Ending live broadcast: %s\n", broadcast.Id)
		transitionCall := service.LiveBroadcasts.Transition("complete", broadcast.Id, []string{"id", "status"})
		_, err := transitionCall.Do()
		if err != nil {
			log.Printf("Error ending broadcast %s: %v", broadcast.Id, err)
		}
	}
}

// endLiveBroadcast ends the current broadcast
func endLiveBroadcast(service *youtube.Service) {
	endActiveBroadcasts(service)
	fmt.Println("All active broadcasts ended.")
}

// showStatus shows the current broadcast status
func showStatus(service *youtube.Service) {
	fmt.Println("=== Stream Status ===")
	
	// Check streams
	streamCall := service.LiveStreams.List([]string{"id", "snippet", "status"}).Mine(true)
	streamResp, err := streamCall.Do()
	if err != nil {
		log.Printf("Error getting streams: %v", err)
	} else {
		for _, stream := range streamResp.Items {
			fmt.Printf("Stream: %s\n", stream.Snippet.Title)
			fmt.Printf("  Status: %s\n", stream.Status.StreamStatus)
			fmt.Printf("  Health: %s\n", stream.Status.HealthStatus.Status)
		}
	}

	fmt.Println("\n=== Broadcast Status ===")

	// Check for any broadcasts in various states
	statuses := []string{"active", "live", "upcoming", "created"}
	for _, status := range statuses {
		call := service.LiveBroadcasts.List([]string{"id", "snippet", "status"}).Mine(true).BroadcastStatus(status)
		response, err := call.Do()
		if err != nil {
			continue
		}

		for _, broadcast := range response.Items {
			fmt.Printf("Broadcast: %s\n", broadcast.Snippet.Title)
			fmt.Printf("  ID: %s\n", broadcast.Id)
			fmt.Printf("  Status: %s\n", broadcast.Status.LifeCycleStatus)
			fmt.Printf("  URL: https://youtube.com/watch?v=%s\n", broadcast.Id)
			fmt.Println()
		}
	}
}
