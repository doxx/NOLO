package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	chatPageURL    = "https://www.youtube.com/live_chat?is_popout=1&v=%s"
	innertubeURL   = "https://www.youtube.com/youtubei/v1/live_chat/get_live_chat?key=%s"
	defaultTimeout = 10 * time.Second
	userAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// Regex patterns for parsing YouTube's HTML
var (
	apiKeyRegex       = regexp.MustCompile(`"INNERTUBE_API_KEY":"([^"]+)"`)
	clientVersionRegex = regexp.MustCompile(`"INNERTUBE_CLIENT_VERSION":"([^"]+)"`)
	continuationRegex  = regexp.MustCompile(`"continuation":"(0of[^"]+)"`)
)

// ChatReader reads YouTube live chat via the internal innertube endpoint
type ChatReader struct {
	videoID       string
	apiKey        string
	clientVersion string
	continuation  string
	client        *http.Client
}

// ChatMsg represents a single chat message
type ChatMsg struct {
	Author    string
	AuthorID  string // Channel ID if available
	Message   string
	Timestamp time.Time
}

// innertubeRequest is the JSON body for the innertube API
type innertubeRequest struct {
	Context      innertubeContext `json:"context"`
	Continuation string          `json:"continuation"`
}

type innertubeContext struct {
	Client innertubeClient `json:"client"`
}

type innertubeClient struct {
	ClientName    string `json:"clientName"`
	ClientVersion string `json:"clientVersion"`
}

// Response types for parsing innertube JSON
type innertubeResponse struct {
	ContinuationContents struct {
		LiveChatContinuation struct {
			Actions       []chatAction       `json:"actions"`
			Continuations []chatContinuation `json:"continuations"`
		} `json:"liveChatContinuation"`
	} `json:"continuationContents"`
}

type chatAction struct {
	AddChatItemAction *struct {
		Item struct {
			LiveChatTextMessageRenderer *chatMessageRenderer `json:"liveChatTextMessageRenderer"`
		} `json:"item"`
	} `json:"addChatItemAction"`
}

type chatMessageRenderer struct {
	Message struct {
		Runs []messageRun `json:"runs"`
	} `json:"message"`
	AuthorName struct {
		SimpleText string `json:"simpleText"`
	} `json:"authorName"`
	AuthorExternalChannelID string `json:"authorExternalChannelId"`
	TimestampUsec           string `json:"timestampUsec"`
}

type messageRun struct {
	Text  string `json:"text,omitempty"`
	Emoji *struct {
		EmojiID string `json:"emojiId"`
	} `json:"emoji,omitempty"`
}

type chatContinuation struct {
	TimedContinuationData *struct {
		Continuation string `json:"continuation"`
		TimeoutMs    int    `json:"timeoutMs"`
	} `json:"timedContinuationData"`
	InvalidationContinuationData *struct {
		Continuation string `json:"continuation"`
		TimeoutMs    int    `json:"timeoutMs"`
	} `json:"invalidationContinuationData"`
}

// NewChatReader creates a new chat reader for a video
func NewChatReader(videoID string) (*ChatReader, error) {
	cr := &ChatReader{
		videoID: videoID,
		client: &http.Client{
			Timeout: defaultTimeout,
		},
	}

	if err := cr.initialize(); err != nil {
		return nil, err
	}

	return cr, nil
}

// initialize fetches the chat page and extracts the API key, client version, and continuation token
func (cr *ChatReader) initialize() error {
	url := fmt.Sprintf(chatPageURL, cr.videoID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %v", err)
	}

	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.AddCookie(&http.Cookie{Name: "CONSENT", Value: "YES+1"})

	resp, err := cr.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch chat page: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("chat page returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %v", err)
	}

	html := string(body)

	// Extract API key
	if matches := apiKeyRegex.FindStringSubmatch(html); len(matches) > 1 {
		cr.apiKey = matches[1]
	} else {
		return fmt.Errorf("could not find INNERTUBE_API_KEY in page")
	}

	// Extract client version
	if matches := clientVersionRegex.FindStringSubmatch(html); len(matches) > 1 {
		cr.clientVersion = matches[1]
	} else {
		return fmt.Errorf("could not find INNERTUBE_CLIENT_VERSION in page")
	}

	// Extract continuation token (last one is usually the "Top chat" continuation)
	matches := continuationRegex.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return fmt.Errorf("could not find continuation token - stream may not be live")
	}
	cr.continuation = matches[len(matches)-1][1]

	log.Printf("[SCRAPE] Initialized: API key=%s... version=%s continuation=%s...",
		cr.apiKey[:10], cr.clientVersion, cr.continuation[:30])

	return nil
}

// Fetch retrieves the next batch of chat messages
func (cr *ChatReader) Fetch() ([]ChatMsg, error) {
	reqBody := innertubeRequest{
		Context: innertubeContext{
			Client: innertubeClient{
				ClientName:    "WEB",
				ClientVersion: cr.clientVersion,
			},
		},
		Continuation: cr.continuation,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %v", err)
	}

	url := fmt.Sprintf(innertubeURL, cr.apiKey)
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := cr.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch chat: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("innertube returned %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	var result innertubeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %v", err)
	}

	// Update continuation token
	continuations := result.ContinuationContents.LiveChatContinuation.Continuations
	if len(continuations) == 0 {
		return nil, fmt.Errorf("stream ended (no continuation)")
	}

	cont := continuations[0]
	sleepMs := 5000
	if cont.InvalidationContinuationData != nil {
		cr.continuation = cont.InvalidationContinuationData.Continuation
		sleepMs = cont.InvalidationContinuationData.TimeoutMs
	} else if cont.TimedContinuationData != nil {
		cr.continuation = cont.TimedContinuationData.Continuation
		sleepMs = cont.TimedContinuationData.TimeoutMs
	}

	// Sleep the amount YouTube tells us to (respects their pacing)
	if sleepMs > 0 {
		time.Sleep(time.Duration(sleepMs) * time.Millisecond)
	}

	// Parse messages
	var messages []ChatMsg
	for _, action := range result.ContinuationContents.LiveChatContinuation.Actions {
		if action.AddChatItemAction == nil {
			continue
		}
		renderer := action.AddChatItemAction.Item.LiveChatTextMessageRenderer
		if renderer == nil {
			continue
		}

		// Build message text from runs
		var textParts []string
		for _, run := range renderer.Message.Runs {
			if run.Text != "" {
				textParts = append(textParts, run.Text)
			} else if run.Emoji != nil {
				textParts = append(textParts, run.Emoji.EmojiID)
			}
		}

		// Parse timestamp
		var ts time.Time
		if renderer.TimestampUsec != "" {
			usec, _ := strconv.ParseInt(renderer.TimestampUsec, 10, 64)
			ts = time.Unix(usec/1000000, (usec%1000000)*1000)
		}

		messages = append(messages, ChatMsg{
			Author:    renderer.AuthorName.SimpleText,
			AuthorID:  renderer.AuthorExternalChannelID,
			Message:   strings.Join(textParts, ""),
			Timestamp: ts,
		})
	}

	return messages, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
