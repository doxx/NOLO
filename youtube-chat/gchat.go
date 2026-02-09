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
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

// Command represents a parsed chat command
type Command struct {
	Type      string // up, down, left, right, zoomin, zoomout, zoomfull, zoommid, zoom, pause, help
	Value     int    // For zoom levels
	User      string // Display name
	UserID    string // Channel ID
	MessageID string // For potential replies
	Timestamp time.Time
}

// RateLimiter tracks command rate limiting
type RateLimiter struct {
	mu              sync.Mutex
	lastGlobalCmd   time.Time
	userCommands    map[string][]time.Time // userID -> list of command times
	globalCooldown  time.Duration          // 10 seconds between any commands
	userCooldown    time.Duration          // 30 second window
	userMaxCommands int                    // 2 commands per window
}

// CommandHandler processes validated commands
type CommandHandler struct {
	rateLimiter *RateLimiter
	commandChan chan Command
	apiEndpoint string // Future: NOLO API endpoint
	service     *youtube.Service
	liveChatID  string
	camera      *CameraController
}

// ChatBot manages the YouTube chat connection
type ChatBot struct {
	service        *youtube.Service
	liveChatID     string
	handler        *CommandHandler
	pollInterval   time.Duration
	nextPageToken  string
	commandPattern *regexp.Regexp
}

var (
	validCommands = map[string]bool{
		"up":       true,
		"down":     true,
		"left":     true,
		"right":    true,
		"zoomin":   true,
		"zoomout":  true,
		"zoomfull": true,
		"zoommid":  true,
		"zoom":     true, // #zoom or #zoom5
		"pause":    true,
		"commands": true,
		"auto":     true,
		"bridge1":  true,
		"bridge2":  true,
		"bridge3":  true,
		"river":    true,
	}

	helpText = `Camera Commands:
#up #down #left #right - Move camera
#zoomin #zoomout - Adjust zoom
#zoomfull #zoommid - Zoom presets
#zoom1-#zoom10 - Set zoom level
#bridge1 #bridge2 #bridge3 - Bridge views
#river - River view
#auto - Resume AI tracking
#pause - Pause tracking (30s)
#commands - Show this list
Rate limit: 2 commands per 30s per user`
)

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		lastGlobalCmd:   time.Time{},
		userCommands:    make(map[string][]time.Time),
		globalCooldown:  10 * time.Second,
		userCooldown:    30 * time.Second,
		userMaxCommands: 2,
	}
}

// CanExecute checks if a command can be executed based on rate limits
func (rl *RateLimiter) CanExecute(userID string) (bool, string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	// Check global cooldown
	if now.Sub(rl.lastGlobalCmd) < rl.globalCooldown {
		remaining := rl.globalCooldown - now.Sub(rl.lastGlobalCmd)
		return false, fmt.Sprintf("Global cooldown: wait %.0fs", remaining.Seconds())
	}

	// Clean old user commands
	if cmds, ok := rl.userCommands[userID]; ok {
		var recent []time.Time
		for _, t := range cmds {
			if now.Sub(t) < rl.userCooldown {
				recent = append(recent, t)
			}
		}
		rl.userCommands[userID] = recent
	}

	// Check user rate limit
	if len(rl.userCommands[userID]) >= rl.userMaxCommands {
		oldest := rl.userCommands[userID][0]
		remaining := rl.userCooldown - now.Sub(oldest)
		return false, fmt.Sprintf("User rate limit: wait %.0fs", remaining.Seconds())
	}

	return true, ""
}

// RecordCommand records that a command was executed
func (rl *RateLimiter) RecordCommand(userID string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	rl.lastGlobalCmd = now
	rl.userCommands[userID] = append(rl.userCommands[userID], now)
}

func NewCommandHandler() *CommandHandler {
	return &CommandHandler{
		rateLimiter: NewRateLimiter(),
		commandChan: make(chan Command, 100),
		apiEndpoint: "http://localhost:8080/api/ptz", // Future
	}
}

// SendChatMessage posts a message to the YouTube live chat
func (ch *CommandHandler) SendChatMessage(text string) {
	if ch.service == nil || ch.liveChatID == "" {
		log.Printf("[CHAT_SEND] No service/chatID configured, skipping: %s", text)
		return
	}
	log.Printf("[CHAT_SEND] Sending to chatID: %s", ch.liveChatID)
	msg := &youtube.LiveChatMessage{
		Snippet: &youtube.LiveChatMessageSnippet{
			LiveChatId: ch.liveChatID,
			Type:       "textMessageEvent",
			TextMessageDetails: &youtube.LiveChatTextMessageDetails{
				MessageText: text,
			},
		},
	}
	// Force send type field
	msg.Snippet.ForceSendFields = []string{"LiveChatId", "Type", "TextMessageDetails"}
	resp, err := ch.service.LiveChatMessages.Insert([]string{"snippet"}, msg).Do()
	if err != nil {
		log.Printf("[CHAT_SEND_ERROR] Failed: %v", err)
		// Try raw JSON debug
		jsonBytes, _ := json.MarshalIndent(msg, "", "  ")
		log.Printf("[CHAT_SEND_DEBUG] Request body: %s", string(jsonBytes))
		return
	}
	log.Printf("[CHAT_SENT] ID=%s: %s", resp.Id, text)
}

// ProcessCommand validates and queues a command
func (ch *CommandHandler) ProcessCommand(cmd Command) {
	// Check rate limits
	canExecute, reason := ch.rateLimiter.CanExecute(cmd.UserID)
	if !canExecute {
		log.Printf("[RATE_LIMIT] %s (%s): %s - %s", cmd.User, cmd.Type, reason, cmd.UserID[:8])
		return
	}

	// Record the command
	ch.rateLimiter.RecordCommand(cmd.UserID)

	// Log the command (future: send to NOLO API)
	log.Printf("[COMMAND] %s from %s: #%s", formatCommand(cmd), cmd.User, cmd.Type)

	// Queue for processing
	select {
	case ch.commandChan <- cmd:
	default:
		log.Printf("[QUEUE_FULL] Dropped command from %s", cmd.User)
	}
}

func formatCommand(cmd Command) string {
	switch cmd.Type {
	case "zoom":
		return fmt.Sprintf("#zoom%d", cmd.Value)
	default:
		return "#" + cmd.Type
	}
}

// CommandProcessor runs in background to process commands
func (ch *CommandHandler) CommandProcessor(ctx context.Context) {
	log.Println("[PROCESSOR] Command processor started")
	for {
		select {
		case <-ctx.Done():
			log.Println("[PROCESSOR] Command processor stopped")
			return
		case cmd := <-ch.commandChan:
			ch.executeCommand(cmd)
		}
	}
}

func (ch *CommandHandler) executeCommand(cmd Command) {
	var err error

	switch cmd.Type {
	case "commands":
		log.Printf("[EXECUTE] Sending commands list to %s", cmd.User)
		ch.SendChatMessage("I'm an AI camera you can control! Move: #up #down #left #right | Zoom: #zoomin #zoomout #zoomfull #zoommid | Presets: #bridge1 #bridge2 #bridge3 #river")
		time.Sleep(1 * time.Second)
		ch.SendChatMessage("#zoom1-#zoom10 set zoom level | #auto resume AI tracking | #pause stop 30s | #commands this list | Limit: 2 cmds per 30s")
		return // Don't send camera command for help

	case "pause":
		log.Printf("[EXECUTE] Pause requested by %s (AI will resume automatically)", cmd.User)
		return
	case "auto":
		log.Printf("[EXECUTE] Auto requested by %s (AI tracking is always active)", cmd.User)
		return

	case "up":
		if ch.camera != nil {
			err = ch.camera.MoveRelative(0, -ch.camera.tiltStep, 0) // Negative tilt = up
		}
	case "down":
		if ch.camera != nil {
			err = ch.camera.MoveRelative(0, ch.camera.tiltStep, 0) // Positive tilt = down
		}
	case "left":
		if ch.camera != nil {
			err = ch.camera.MoveRelative(-ch.camera.panStep, 0, 0) // Negative pan = left
		}
	case "right":
		if ch.camera != nil {
			err = ch.camera.MoveRelative(ch.camera.panStep, 0, 0) // Positive pan = right
		}

	case "zoomin":
		if ch.camera != nil {
			err = ch.camera.MoveRelative(0, 0, 10) // +10 zoom units
		}
	case "zoomout":
		if ch.camera != nil {
			err = ch.camera.MoveRelative(0, 0, -10) // -10 zoom units
		}
	case "zoomfull":
		if ch.camera != nil {
			pos, gerr := ch.camera.GetPosition()
			if gerr == nil {
				err = ch.camera.SendAbsolute(pos.Pan, pos.Tilt, 120)
			} else {
				err = gerr
			}
		}
	case "zoommid":
		if ch.camera != nil {
			pos, gerr := ch.camera.GetPosition()
			if gerr == nil {
				err = ch.camera.SendAbsolute(pos.Pan, pos.Tilt, 60)
			} else {
				err = gerr
			}
		}
	case "zoom":
		if ch.camera != nil {
			err = ch.camera.SetZoomLevel(cmd.Value)
		}

	case "bridge1":
		if ch.camera != nil {
			err = ch.camera.GoToPreset("bridge1")
		}
	case "bridge2":
		if ch.camera != nil {
			err = ch.camera.GoToPreset("bridge2")
		}
	case "bridge3":
		if ch.camera != nil {
			err = ch.camera.GoToPreset("bridge3")
		}
	case "river":
		if ch.camera != nil {
			// Go to first river preset
			err = ch.camera.GoToPreset("river1")
		}

	default:
		log.Printf("[EXECUTE] Unknown command: %s", cmd.Type)
		return
	}

	if err != nil {
		log.Printf("[EXECUTE_ERROR] %s failed: %v", cmd.Type, err)
	} else if ch.camera != nil {
		log.Printf("[EXECUTE] %s from %s - OK", cmd.Type, cmd.User)
	} else {
		log.Printf("[EXECUTE] %s from %s (no camera configured)", cmd.Type, cmd.User)
	}
}

func NewChatBot(service *youtube.Service, liveChatID string, handler *CommandHandler) *ChatBot {
	// Pattern matches #command or #command123 (letters then optional digits)
	pattern := regexp.MustCompile(`#([a-zA-Z]+)(\d*)`)
	return &ChatBot{
		service:        service,
		liveChatID:     liveChatID,
		handler:        handler,
		pollInterval:   3 * time.Second,
		commandPattern: pattern,
	}
}

// ParseCommand extracts a command from a chat message
func (cb *ChatBot) ParseCommand(message string) (string, int, bool) {
	matches := cb.commandPattern.FindStringSubmatch(strings.ToLower(message))
	if matches == nil {
		return "", 0, false
	}

	cmdBase := matches[1]   // letters only
	numSuffix := matches[2] // digits only
	value := 0

	// Parse numeric suffix if present
	if numSuffix != "" {
		fmt.Sscanf(numSuffix, "%d", &value)
	}

	// Handle #zoom5 style commands
	if cmdBase == "zoom" && value >= 1 && value <= 10 {
		return "zoom", value, true
	}

	// Handle #bridge1, #bridge2, #bridge3
	if cmdBase == "bridge" && value >= 1 && value <= 3 {
		return fmt.Sprintf("bridge%d", value), 0, true
	}

	// Standard commands (ignore any numeric suffix)
	if validCommands[cmdBase] {
		return cmdBase, 0, true
	}

	return "", 0, false
}

// PollMessages continuously polls for new chat messages
func (cb *ChatBot) PollMessages(ctx context.Context) {
	log.Printf("[CHAT] Starting chat poll (interval: %v)", cb.pollInterval)
	ticker := time.NewTicker(cb.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[CHAT] Chat polling stopped")
			return
		case <-ticker.C:
			cb.fetchMessages()
		}
	}
}

func (cb *ChatBot) fetchMessages() {
	call := cb.service.LiveChatMessages.List(cb.liveChatID, []string{"snippet", "authorDetails"})
	if cb.nextPageToken != "" {
		call.PageToken(cb.nextPageToken)
	}

	response, err := call.Do()
	if err != nil {
		log.Printf("[CHAT_ERROR] Failed to fetch messages: %v", err)
		return
	}

	cb.nextPageToken = response.NextPageToken

	if len(response.Items) > 0 {
		log.Printf("[CHAT] Received %d messages", len(response.Items))
	}

	for _, msg := range response.Items {
		text := msg.Snippet.DisplayMessage
		publishedAt, _ := time.Parse(time.RFC3339, msg.Snippet.PublishedAt)
		age := time.Since(publishedAt)

		// Log all messages for debugging
		log.Printf("[MSG] %s: %s (age: %.0fs)", msg.AuthorDetails.DisplayName, text, age.Seconds())

		// Skip messages older than 60 seconds
		if age > 60*time.Second {
			continue
		}

		cmdType, value, found := cb.ParseCommand(text)
		if !found {
			continue
		}

		cmd := Command{
			Type:      cmdType,
			Value:     value,
			User:      msg.AuthorDetails.DisplayName,
			UserID:    msg.AuthorDetails.ChannelId,
			MessageID: msg.Id,
			Timestamp: publishedAt,
		}

		cb.handler.ProcessCommand(cmd)
	}

	// Adjust poll interval based on response
	if response.PollingIntervalMillis > 0 {
		newInterval := time.Duration(response.PollingIntervalMillis) * time.Millisecond
		if newInterval != cb.pollInterval {
			cb.pollInterval = newInterval
			log.Printf("[CHAT] Adjusted poll interval to %v", cb.pollInterval)
		}
	}
}

// GetLiveChatIDByVideo finds the live chat ID for a specific video
func GetLiveChatIDByVideo(service *youtube.Service, videoID string) (string, error) {
	call := service.Videos.List([]string{"liveStreamingDetails", "snippet"}).Id(videoID)
	response, err := call.Do()
	if err != nil {
		return "", fmt.Errorf("failed to get video %s: %v", videoID, err)
	}
	if len(response.Items) == 0 {
		return "", fmt.Errorf("video %s not found", videoID)
	}
	video := response.Items[0]
	log.Printf("[VIDEO] Found: %s (ID: %s)", video.Snippet.Title, video.Id)
	if video.LiveStreamingDetails == nil {
		return "", fmt.Errorf("video %s is not a live stream", videoID)
	}
	chatID := video.LiveStreamingDetails.ActiveLiveChatId
	if chatID == "" {
		return "", fmt.Errorf("video %s has no active live chat", videoID)
	}
	return chatID, nil
}

// GetLiveChatID finds the live chat ID for the current broadcast
func GetLiveChatID(service *youtube.Service) (string, error) {
	// List all of my broadcasts (don't filter by status to avoid API error)
	call := service.LiveBroadcasts.List([]string{"snippet", "status"}).Mine(true)
	response, err := call.Do()
	if err != nil {
		return "", fmt.Errorf("failed to list broadcasts: %v", err)
	}

	// Find an active/live broadcast
	for _, broadcast := range response.Items {
		status := broadcast.Status.LifeCycleStatus
		log.Printf("[BROADCAST] Found: %s (ID: %s, status: %s)", broadcast.Snippet.Title, broadcast.Id, status)
		if status == "live" || status == "ready" || status == "testing" {
			chatID := broadcast.Snippet.LiveChatId
			if chatID != "" {
				log.Printf("[BROADCAST] Using broadcast: %s (status: %s, chatID: %s)", broadcast.Snippet.Title, status, chatID)
				return chatID, nil
			}
		}
	}

	return "", fmt.Errorf("no active/live broadcasts found (checked %d broadcasts)", len(response.Items))
}

// OAuth helper functions (same as youtube-reset)
func getClient(ctx context.Context, config *oauth2.Config, tokenFile string) *http.Client {
	token, err := tokenFromFile(tokenFile)
	if err != nil {
		token = getTokenFromWeb(config)
		saveToken(tokenFile, token)
	}
	return config.Client(ctx, token)
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

func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	config.RedirectURL = "http://localhost:8091/callback"
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	fmt.Printf("Open this URL in your browser:\n\n%s\n\n", authURL)

	codeChan := make(chan string)
	server := &http.Server{Addr: ":8091"}

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

func main() {
	credentialsFile := flag.String("credentials", "client_secret.json", "Path to OAuth credentials file")
	tokenFile := flag.String("token", "token.json", "Path to token file")
	videoID := flag.String("video", "", "Video ID to monitor chat for (e.g. zQGzrbwXabo)")
	chatID := flag.String("chat-id", "", "Direct live chat ID (skip video lookup, saves API quota)")
	cameraIP := flag.String("camera-ip", "192.168.0.59", "Camera IP address")
	cameraPort := flag.String("camera-port", "80", "Camera HTTP port")
	cameraUser := flag.String("camera-user", "admin", "Camera username")
	cameraPass := flag.String("camera-pass", "password1", "Camera password")
	presetsFile := flag.String("presets", "", "Path to scanning.json for preset positions")
	testMode := flag.Bool("test", false, "Test mode - simulate commands without YouTube")
	quickTest := flag.Bool("quicktest", false, "Quick test - fast simulation with 1s cooldowns")
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Println("========================================")
	log.Println("  NOLO YouTube Chat Controller")
	log.Println("========================================")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := NewCommandHandler()

	// Initialize camera controller
	if *cameraIP != "" {
		camera := NewCameraController(*cameraIP, *cameraPort, *cameraUser, *cameraPass)

		// Load presets
		presetPath := *presetsFile
		if presetPath == "" {
			// Try default location
			presetPath = "../scanning.json"
		}
		if err := camera.LoadPresets(presetPath); err != nil {
			log.Printf("[PTZ] Warning: Could not load presets: %v", err)
		}

		// Test camera connection
		pos, err := camera.GetPosition()
		if err != nil {
			log.Printf("[PTZ] Warning: Could not connect to camera: %v", err)
			log.Printf("[PTZ] Camera control disabled - commands will be logged only")
		} else {
			log.Printf("[PTZ] Camera connected! Current position: Pan=%.0f Tilt=%.0f Zoom=%.0f", pos.Pan, pos.Tilt, pos.Zoom)
			handler.camera = camera
		}
	}

	// Start command processor
	go handler.CommandProcessor(ctx)

	if *testMode || *quickTest {
		if *quickTest {
			log.Println("[TEST] Running quick test mode (1s cooldowns)")
			handler.rateLimiter.globalCooldown = 1 * time.Second
			handler.rateLimiter.userCooldown = 3 * time.Second
		} else {
			log.Println("[TEST] Running test mode (production cooldowns: 10s global, 30s user)")
		}
		testCommands(handler, *quickTest)
		return
	}

	// Load OAuth credentials
	credentials, err := os.ReadFile(*credentialsFile)
	if err != nil {
		log.Fatalf("Unable to read credentials file: %v", err)
	}

	config, err := google.ConfigFromJSON(credentials, youtube.YoutubeScope, youtube.YoutubeForceSslScope)
	if err != nil {
		log.Fatalf("Unable to parse credentials: %v", err)
	}

	client := getClient(ctx, config, *tokenFile)

	service, err := youtube.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Error creating YouTube service: %v", err)
	}

	// Get live chat ID
	var liveChatID string
	if *chatID != "" {
		liveChatID = *chatID
		log.Printf("[CHAT] Using provided chat ID (no API lookup)")
	} else if *videoID != "" {
		liveChatID, err = GetLiveChatIDByVideo(service, *videoID)
		if err != nil {
			log.Fatalf("Failed to get live chat ID: %v", err)
		}
	} else {
		liveChatID, err = GetLiveChatID(service)
		if err != nil {
			log.Fatalf("Failed to get live chat ID: %v", err)
		}
	}
	log.Printf("[CHAT] Connected to live chat: %s", liveChatID)

	// Give handler access to chat for replies
	handler.service = service
	handler.liveChatID = liveChatID

	// Create and start chat bot
	bot := NewChatBot(service, liveChatID, handler)
	bot.PollMessages(ctx)
}

// testCommands simulates chat commands for testing
func testCommands(handler *CommandHandler, quick bool) {
	log.Println("[TEST] Simulating chat commands...")

	// Wait times: production uses 10s, quick uses 1s
	waitTime := 10
	shortWait := 2
	if quick {
		waitTime = 1
		shortWait = 0
	}

	testCases := []struct {
		user     string
		userID   string
		message  string
		waitSecs int // Seconds to wait BEFORE this command
		note     string
	}{
		{"Alice", "UC_ALICE_001", "#up", 0, "First command - should work"},
		{"Bob", "UC_BOB_00002", "#down", shortWait, "Global cooldown not elapsed - blocked"},
		{"Charlie", "UC_CHARLIE", "#left", waitTime, "After global cooldown - works"},
		{"Alice", "UC_ALICE_001", "#right", waitTime, "Alice's 2nd cmd, after cooldown - works"},
		{"Alice", "UC_ALICE_001", "#zoomin", waitTime, "Alice's 3rd cmd, user limit hit - blocked"},
		{"Bob", "UC_BOB_00002", "#zoom5", waitTime, "Bob can still command - works"},
		{"Charlie", "UC_CHARLIE", "#zoomfull", waitTime, "Charlie's 2nd - works"},
		{"Charlie", "UC_CHARLIE", "#zoommid", waitTime, "Charlie's 3rd - blocked"},
		{"Dave", "UC_DAVE_004", "#help", waitTime, "Dave's first - works"},
		{"Dave", "UC_DAVE_004", "#bridge1", waitTime, "Dave's 2nd - works"},
		{"Dave", "UC_DAVE_004", "#bridge2", waitTime, "Dave's 3rd - blocked"},
		{"Eve", "UC_EVE_00005", "#invalidcmd", 0, "Invalid command - ignored"},
		{"Eve", "UC_EVE_00005", "#auto", waitTime, "Valid command - works"},
		{"Eve", "UC_EVE_00005", "#pause", waitTime, "Eve's 2nd - works"},
	}

	// Use same pattern as ChatBot
	pattern := regexp.MustCompile(`#([a-zA-Z]+)(\d*)`)

	for i, tc := range testCases {
		// Wait before processing
		if tc.waitSecs > 0 {
			log.Printf("[TEST %d] Waiting %ds... (%s)", i+1, tc.waitSecs, tc.note)
			time.Sleep(time.Duration(tc.waitSecs) * time.Second)
		}

		matches := pattern.FindStringSubmatch(strings.ToLower(tc.message))
		if matches == nil {
			log.Printf("[TEST %d] No command found in: %s", i+1, tc.message)
			continue
		}

		cmdBase := matches[1]
		numSuffix := matches[2]
		value := 0

		if numSuffix != "" {
			fmt.Sscanf(numSuffix, "%d", &value)
		}

		var cmdType string
		var valid bool

		// Apply same logic as ParseCommand
		if cmdBase == "zoom" && value >= 1 && value <= 10 {
			cmdType = "zoom"
			valid = true
		} else if cmdBase == "bridge" && value >= 1 && value <= 3 {
			cmdType = fmt.Sprintf("bridge%d", value)
			value = 0
			valid = true
		} else if validCommands[cmdBase] {
			cmdType = cmdBase
			valid = true
		}

		if !valid {
			log.Printf("[TEST %d] Invalid: %s (%s)", i+1, tc.message, tc.note)
			continue
		}

		cmd := Command{
			Type:      cmdType,
			Value:     value,
			User:      tc.user,
			UserID:    tc.userID,
			Timestamp: time.Now(),
		}

		log.Printf("[TEST %d] %s: %s from %s", i+1, tc.note, tc.message, tc.user)
		handler.ProcessCommand(cmd)
	}

	log.Println("[TEST] Test complete")
}
