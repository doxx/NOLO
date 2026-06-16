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
	noloAPI     string // NOLO HTTP API base URL
	service     *youtube.Service
	liveChatID  string
	schedule    *ScheduleManager
	riverData   *RiverData
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
		// Movement
		"up": true, "down": true, "left": true, "right": true,
		// Zoom
		"zoomin": true, "zoomout": true, "zoomfull": true, "zoommid": true, "zoom": true,
		// Presets
		"bridge1": true, "bridge2": true, "bridge3": true, "river": true,
		// Override control
		"stay": true, "linger": true, "auto": true, "pause": true,
		// Overlays
		"show": true, "hide": true,
		// Info
		"boats": true, "weather": true, "tide": true, "schedule": true,
		// Help
		"commands": true,
	}

	// show/hide sub-commands
	validOverlays = map[string]bool{
		"target": true, "console": true, "pip": true, "status": true, "overlay": true,
	}

	helpText = `Camera Commands:
#up #down #left #right - Move camera
#zoomin #zoomout #zoomfull #zoommid #zoom1-#zoom10 - Zoom
#bridge1 #bridge2 #bridge3 #river - Preset views
#stay #linger - Hold camera position longer
#auto - Release to AI tracking
#show.target #show.pip #show.console - Toggle overlays
#commands - This list | 2 cmds/30s per user`
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

func NewCommandHandler(noloAPI string) *CommandHandler {
	return &CommandHandler{
		rateLimiter: NewRateLimiter(),
		commandChan: make(chan Command, 100),
		noloAPI:     noloAPI,
	}
}

// callNOLO sends a command to the NOLO HTTP API
func (ch *CommandHandler) callNOLO(path string) error {
	url := ch.noloAPI + path
	resp, err := http.Post(url, "", nil)
	if err != nil {
		return fmt.Errorf("NOLO API error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("NOLO API %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	log.Printf("[NOLO_API] %s -> %v", path, result)
	return nil
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
	// Rate limiting disabled - let all commands through

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
	var apiPath string
	var confirmMsg string

	switch cmd.Type {
	case "commands":
		log.Printf("[EXECUTE] Sending commands list to %s", cmd.User)
		ch.SendChatMessage("I'm an AI camera you can control! Move: #up #down #left #right | Zoom: #zoomin #zoomout #zoomfull #zoommid | Presets: #bridge1 #bridge2 #bridge3 #river")
		time.Sleep(1 * time.Second)
		ch.SendChatMessage("#boats #weather #tide #schedule for info | #stay hold position | #auto release to AI | #show.overlay all AI vision | #show.target #show.pip individual overlays | 2 cmds/30s")
		return

	case "weather":
		if ch.riverData != nil {
			ch.SendChatMessage(ch.riverData.GetWeather())
		}
		return
	case "tide":
		if ch.riverData != nil {
			ch.SendChatMessage(ch.riverData.GetTide())
		}
		return
	case "boats":
		if ch.riverData != nil {
			ch.SendChatMessage(ch.riverData.GetBoats())
		}
		return
	case "schedule":
		if ch.schedule != nil {
			upcoming := ch.schedule.GetUpcomingEvents(120) // Next 2 hours
			if len(upcoming) == 0 {
				ch.SendChatMessage("No vessels scheduled in the next 2 hours. Source: Biscayne Bay Pilots")
			} else {
				msgs := []string{}
				for _, e := range upcoming {
					if len(msgs) >= 3 {
						break // Max 3 events per request
					}
					msgs = append(msgs, FormatEventAnnouncement(e))
				}
				ch.SendChatMessage("Upcoming: " + strings.Join(msgs, " | "))
			}
		}
		return

	// Movement
	case "up":
		apiPath = "/ptz/up"
		confirmMsg = "Moving up (stream has ~30s delay)"
	case "down":
		apiPath = "/ptz/down"
		confirmMsg = "Moving down (stream has ~30s delay)"
	case "left":
		apiPath = "/ptz/left"
		confirmMsg = "Moving left (stream has ~30s delay)"
	case "right":
		apiPath = "/ptz/right"
		confirmMsg = "Moving right (stream has ~30s delay)"

	// Zoom
	case "zoomin":
		apiPath = "/ptz/zoomin"
		confirmMsg = "Zooming in (stream has ~30s delay)"
	case "zoomout":
		apiPath = "/ptz/zoomout"
		confirmMsg = "Zooming out (stream has ~30s delay)"
	case "zoomfull":
		apiPath = "/ptz/zoomfull"
		confirmMsg = "Max zoom (stream has ~30s delay)"
	case "zoommid":
		apiPath = "/ptz/zoommid"
		confirmMsg = "Mid zoom (stream has ~30s delay)"
	case "zoom":
		apiPath = fmt.Sprintf("/ptz/zoom/%d", cmd.Value)
		confirmMsg = fmt.Sprintf("Zoom set to %d (stream has ~30s delay)", cmd.Value)

	// Presets
	case "bridge1", "bridge2", "bridge3":
		apiPath = "/ptz/preset/" + cmd.Type
		confirmMsg = "Going to " + cmd.Type + " (stream has ~30s delay)"
	case "river":
		apiPath = "/ptz/preset/river"
		confirmMsg = "Going to river view (stream has ~30s delay)"

	// Override control
	case "stay", "linger":
		apiPath = "/ptz/stay"
		confirmMsg = "Holding position"
	case "auto":
		apiPath = "/ptz/release"
		confirmMsg = "Released to AI tracking"
	case "pause":
		apiPath = "/ptz/stay"
		confirmMsg = "Camera paused"

	case "show.overlay":
		for _, o := range []string{"target", "pip", "status", "console"} {
			ch.callNOLO("/show/" + o)
		}
		ch.SendChatMessage("All overlays turned ON")
		log.Printf("[EXECUTE] All overlays enabled")
		return
	case "hide.overlay":
		for _, o := range []string{"target", "pip", "status", "console"} {
			ch.callNOLO("/hide/" + o)
		}
		ch.SendChatMessage("All overlays turned OFF")
		log.Printf("[EXECUTE] All overlays disabled")
		return
	// Overlay toggles (#show.target, #hide.pip, etc.)
	default:
		if strings.HasPrefix(cmd.Type, "show.") {
			overlay := strings.TrimPrefix(cmd.Type, "show.")
			apiPath = "/show/" + overlay
			confirmMsg = overlay + " overlay ON"
		} else if strings.HasPrefix(cmd.Type, "hide.") {
			overlay := strings.TrimPrefix(cmd.Type, "hide.")
			apiPath = "/hide/" + overlay
			confirmMsg = overlay + " overlay OFF"
		} else {
			log.Printf("[EXECUTE] Unknown command: %s", cmd.Type)
			return
		}
	}

	err = ch.callNOLO(apiPath)
	if err != nil {
		log.Printf("[EXECUTE_ERROR] %s failed: %v", cmd.Type, err)
	} else {
		log.Printf("[EXECUTE] %s from %s - OK", cmd.Type, cmd.User)
		if confirmMsg != "" {
			ch.SendChatMessage(confirmMsg)
		}
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
	lower := strings.ToLower(message)

	// Handle #show.target, #show.pip, #hide.console style commands
	dotPattern := regexp.MustCompile(`#(show|hide)\.(\w+)`)
	if dotMatches := dotPattern.FindStringSubmatch(lower); dotMatches != nil {
		action := dotMatches[1]  // show or hide
		overlay := dotMatches[2] // target, console, pip, status
		if validOverlays[overlay] {
			return action + "." + overlay, 0, true
		}
		return "", 0, false
	}

	matches := cb.commandPattern.FindStringSubmatch(lower)
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
			cb.fetchMessages(ticker)
		}
	}
}

func (cb *ChatBot) fetchMessages(ticker *time.Ticker) {
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
			ticker.Reset(newInterval)
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

// ScrapeChatBot reads chat via YouTube's internal endpoint (zero API quota)
type ScrapeChatBot struct {
	reader         *ChatReader
	handler        *CommandHandler
	commandPattern *regexp.Regexp
}

func NewScrapeChatBot(videoID string, handler *CommandHandler) (*ScrapeChatBot, error) {
	reader, err := NewChatReader(videoID)
	if err != nil {
		return nil, err
	}

	return &ScrapeChatBot{
		reader:         reader,
		handler:        handler,
		commandPattern: regexp.MustCompile(`#([a-zA-Z]+)(\d*)`),
	}, nil
}

// PollMessages continuously reads chat via the internal endpoint
func (sb *ScrapeChatBot) PollMessages(ctx context.Context) {
	log.Println("[SCRAPE] Starting chat poll (zero API quota)")

	// First fetch grabs history - skip it
	firstFetch := true

	for {
		select {
		case <-ctx.Done():
			log.Println("[SCRAPE] Stopped")
			return
		default:
		}

		messages, err := sb.reader.Fetch()
		if err != nil {
			log.Printf("[SCRAPE_ERROR] %v", err)
			time.Sleep(10 * time.Second)
			continue
		}

		if firstFetch {
			log.Printf("[SCRAPE] Skipped %d history messages", len(messages))
			firstFetch = false
			continue
		}

		for _, msg := range messages {
			age := time.Since(msg.Timestamp)

			if age > 60*time.Second {
				continue
			}

			cmdType, value, found := sb.parseCommand(msg.Message)
			if !found {
				continue
			}

			log.Printf("[SCRAPE_CMD] %s: %s (age: %.0fs)", msg.Author, msg.Message, age.Seconds())

			cmd := Command{
				Type:      cmdType,
				Value:     value,
				User:      msg.Author,
				UserID:    msg.AuthorID,
				Timestamp: msg.Timestamp,
			}

			// Fall back to author name if no channel ID
			if cmd.UserID == "" {
				cmd.UserID = msg.Author
			}

			sb.handler.ProcessCommand(cmd)
		}
	}
}

func (sb *ScrapeChatBot) parseCommand(message string) (string, int, bool) {
	lower := strings.ToLower(message)

	// Handle #show.target, #hide.pip style
	dotPattern := regexp.MustCompile(`#(show|hide)\.(\w+)`)
	if dotMatches := dotPattern.FindStringSubmatch(lower); dotMatches != nil {
		action := dotMatches[1]
		overlay := dotMatches[2]
		if validOverlays[overlay] {
			return action + "." + overlay, 0, true
		}
		return "", 0, false
	}

	matches := sb.commandPattern.FindStringSubmatch(lower)
	if matches == nil {
		return "", 0, false
	}

	cmdBase := matches[1]
	numSuffix := matches[2]
	value := 0

	if numSuffix != "" {
		fmt.Sscanf(numSuffix, "%d", &value)
	}

	if cmdBase == "zoom" && value >= 1 && value <= 10 {
		return "zoom", value, true
	}
	if cmdBase == "bridge" && value >= 1 && value <= 3 {
		return fmt.Sprintf("bridge%d", value), 0, true
	}
	if validCommands[cmdBase] {
		return cmdBase, 0, true
	}

	return "", 0, false
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
	videoID := flag.String("video", "", "Video ID to monitor chat for (e.g. 7wHgYc_kN98)")
	chatID := flag.String("chat-id", "", "Direct live chat ID (skip video lookup, saves API quota)")
	scrapeMode := flag.Bool("scrape", false, "Use internal YouTube endpoint for reading chat (zero API quota for reads)")
	noloAPIAddr := flag.String("nolo-api", "http://127.0.0.1:8080", "NOLO HTTP API address")
	aisKey := flag.String("ais-key", "", "AISStream.io API key for vessel tracking")
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Println("========================================")
	log.Println("  NOLO YouTube Chat Controller")
	log.Println("========================================")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := NewCommandHandler(*noloAPIAddr)
	log.Printf("[API] NOLO API endpoint: %s", *noloAPIAddr)

	// Start river data feeds (weather, tides, AIS vessels)
	// sendChat function will be set once we have YouTube API access
	handler.riverData = NewRiverData(nil, *aisKey)

	// Start command processor
	go handler.CommandProcessor(ctx)

	// Scrape mode: read chat via internal endpoint (no API quota for reads)
	if *scrapeMode {
		if *videoID == "" {
			log.Fatal("--scrape requires --video VIDEO_ID")
		}

		scrapeBot, err := NewScrapeChatBot(*videoID, handler)
		if err != nil {
			log.Fatalf("Failed to start scrape chat bot: %v", err)
		}

		// Optionally set up official API for replies only (if credentials exist)
		if _, err := os.Stat(*credentialsFile); err == nil {
			credentials, err := os.ReadFile(*credentialsFile)
			if err == nil {
				config, err := google.ConfigFromJSON(credentials, youtube.YoutubeScope, youtube.YoutubeForceSslScope)
				if err == nil {
					client := getClient(ctx, config, *tokenFile)
					service, err := youtube.NewService(ctx, option.WithHTTPClient(client))
					if err == nil {
						// Get chat ID for sending replies
						liveChatID, err := GetLiveChatIDByVideo(service, *videoID)
						if err == nil {
							handler.service = service
							handler.liveChatID = liveChatID
							log.Printf("[API] Official API enabled for replies (chat ID: %s)", liveChatID[:20]+"...")
						} else {
							log.Printf("[API] Warning: Could not get chat ID for replies: %v", err)
							log.Printf("[API] Chat replies disabled, read-only mode")
						}
					}
				}
			}
		} else {
			log.Printf("[SCRAPE] No credentials file found - running read-only (no chat replies)")
		}

		// Start river data feeds with chat send function
		handler.riverData.sendChatFn = func(msg string) { handler.SendChatMessage(msg) }
		handler.riverData.Start()

		// Start port schedule announcements (bbpilots.com) — scrape mode
		schedule := NewScheduleManager()
		handler.schedule = schedule
		go func() {
			if err := schedule.FetchSchedule(); err != nil {
				log.Printf("[SCHEDULE] Initial fetch failed: %v", err)
			} else {
				log.Printf("[SCHEDULE] Loaded schedule")
				if summary := schedule.GetTodaysSummary(); summary != "" {
					handler.SendChatMessage(summary)
				}
			}

			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			sunriseAnnounced := false
			sunriseEndAnnounced := false
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if schedule.NeedsRefresh() {
						if err := schedule.FetchSchedule(); err != nil {
							log.Printf("[SCHEDULE] Refresh failed: %v", err)
						}
					}

					// Sunrise park announcement
					loc, _ := time.LoadLocation("America/New_York")
					now := time.Now().In(loc)
					minuteOfDay := now.Hour()*60 + now.Minute()
					inSunrise := minuteOfDay >= 4*60+30 && minuteOfDay < 7*60+30

					if inSunrise && !sunriseAnnounced {
						handler.SendChatMessage("Parking camera for sunrise viewing until 7:30 AM. Enjoy the view!")
						sunriseAnnounced = true
						sunriseEndAnnounced = false
					} else if !inSunrise && sunriseAnnounced && !sunriseEndAnnounced {
						handler.SendChatMessage("Sunrise viewing complete. AI boat tracking resumed!")
						sunriseEndAnnounced = true
						sunriseAnnounced = false
					}

					upcoming := schedule.GetUpcomingEvents(30)
					for _, event := range upcoming {
						if event.Type == "YACHT" {
							msg := FormatEventAnnouncement(event)
							handler.SendChatMessage(msg)
							schedule.MarkAnnounced(event.Vessel, event.Time)
							log.Printf("[SCHEDULE] Announced: %s", msg)
							time.Sleep(2 * time.Second)
						}
					}
				}
			}
		}()

		scrapeBot.PollMessages(ctx)
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

	// Start river data feeds with chat send function
	handler.riverData.sendChatFn = func(msg string) { handler.SendChatMessage(msg) }
	handler.riverData.Start()

	// Start port schedule announcements (bbpilots.com)
	schedule := NewScheduleManager()
	handler.schedule = schedule
	go func() {
		// Initial fetch
		if err := schedule.FetchSchedule(); err != nil {
			log.Printf("[SCHEDULE] Initial fetch failed: %v", err)
		} else {
			// Post today's summary on startup
			if summary := schedule.GetTodaysSummary(); summary != "" {
				handler.SendChatMessage(summary)
			}
		}

		// Check every 5 minutes for upcoming events
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		sunriseAnnounced := false
		sunriseEndAnnounced := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Refresh schedule if stale
				if schedule.NeedsRefresh() {
					if err := schedule.FetchSchedule(); err != nil {
						log.Printf("[SCHEDULE] Refresh failed: %v", err)
					}
				}

				// Sunrise park announcement
				loc, _ := time.LoadLocation("America/New_York")
				now := time.Now().In(loc)
				minuteOfDay := now.Hour()*60 + now.Minute()
				inSunrise := minuteOfDay >= 4*60+30 && minuteOfDay < 7*60+30

				if inSunrise && !sunriseAnnounced {
					handler.SendChatMessage("Parking camera for sunrise viewing until 7:30 AM. Enjoy the view!")
					sunriseAnnounced = true
					sunriseEndAnnounced = false
				} else if !inSunrise && sunriseAnnounced && !sunriseEndAnnounced {
					handler.SendChatMessage("Sunrise viewing complete. AI boat tracking resumed!")
					sunriseEndAnnounced = true
					sunriseAnnounced = false
				}

				// Announce events coming up in next 30 minutes
				upcoming := schedule.GetUpcomingEvents(30)
				for _, event := range upcoming {
					// Only announce yachts (cruise ships are too frequent and spammy)
					if event.Type == "YACHT" {
						msg := FormatEventAnnouncement(event)
						handler.SendChatMessage(msg)
						schedule.MarkAnnounced(event.Vessel, event.Time)
						log.Printf("[SCHEDULE] Announced: %s", msg)
						time.Sleep(2 * time.Second)
					}
				}
			}
		}
	}()

	// Create and start chat bot
	bot := NewChatBot(service, liveChatID, handler)
	bot.PollMessages(ctx)
}
