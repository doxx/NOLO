package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ScheduleEvent represents a single vessel movement
type ScheduleEvent struct {
	Direction string // "ARRIVAL" or "DEPARTURE"
	Time      time.Time
	Status    string // "SCHEDULED", "CONFIRMED", "IN PROGRESS"
	Vessel    string // Vessel name
	Type      string // "CRUISE", "CARGO", "YACHT", "RIVER", "TUG-BARGE"
	Location  string // Port location
	Announced bool   // Whether we've already announced this event
}

// ScheduleManager scrapes and manages the BBPilots schedule
type ScheduleManager struct {
	mu          sync.RWMutex
	events      []ScheduleEvent
	lastFetch   time.Time
	fetchPeriod time.Duration
}

func NewScheduleManager() *ScheduleManager {
	return &ScheduleManager{
		fetchPeriod: 6 * time.Hour, // Refresh schedule every 6 hours
	}
}

// FetchSchedule scrapes bbpilots.com and parses the schedule
func (sm *ScheduleManager) FetchSchedule() error {
	resp, err := http.Get("https://bbpilots.com")
	if err != nil {
		return fmt.Errorf("failed to fetch schedule: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read schedule: %v", err)
	}

	events := parseScheduleHTML(string(body))
	sm.mu.Lock()
	sm.events = events
	sm.lastFetch = time.Now()
	sm.mu.Unlock()

	log.Printf("[SCHEDULE] Fetched %d events from bbpilots.com", len(events))
	return nil
}

// parseScheduleHTML extracts events from the bbpilots.com HTML table
func parseScheduleHTML(html string) []ScheduleEvent {
	var events []ScheduleEvent
	loc, _ := time.LoadLocation("America/New_York")

	// Collapse whitespace for easier regex matching
	rawText := strings.ReplaceAll(html, "\n", " ")
	rawText = regexp.MustCompile(`\s+`).ReplaceAllString(rawText, " ")

	// Find day headers: <th class="day-title" colspan="5">Sun, Feb 15, 2026</th>
	dayPattern := regexp.MustCompile(`day-title[^>]*>(?:Sun|Mon|Tue|Wed|Thu|Fri|Sat),\s+(\w+)\s+(\d+),\s+(\d{4})</th>`)

	// Find each service row — extract direction, time, vessel name, vessel type
	// Direction: <div>ARRIVAL</div> or <div>DEPARTURE</div>
	// Time: <div class="time-in-badge">HH:MM</div>
	// Status: bbp-service-badge-bottom contains SCHEDULED/CONFIRMED/IN PROGRESS
	// Vessel: <div class="text-truncate font-weight-bold ...">VESSEL NAME</div>
	// Type: <div class="vessel-type">CRUISE</div>
	rowPattern := regexp.MustCompile(`service-row.*?<div>(ARRIVAL|DEPARTURE)</div>.*?time-in-badge"?>(\d{2}:\d{2})</div>.*?badge-bottom[^>]*>\s*(SCHEDULED|CONFIRMED|IN PROGRESS)\s*</div>.*?font-weight-bold[^>]*>([^<]+)</div>\s*<div class="vessel-type">([^<]+)</div>`)

	// Build date position map
	dayMatches := dayPattern.FindAllStringSubmatchIndex(rawText, -1)
	dayValues := dayPattern.FindAllStringSubmatch(rawText, -1)

	type datePos struct {
		pos  int
		date time.Time
	}
	var dates []datePos
	for i, dv := range dayValues {
		dateStr := fmt.Sprintf("%s %s, %s", dv[1], dv[2], dv[3])
		parsed, err := time.Parse("Jan 2, 2006", dateStr)
		if err == nil {
			dates = append(dates, datePos{dayMatches[i][0], parsed})
		}
	}

	// Find all vessel entries
	rowMatches := rowPattern.FindAllStringSubmatch(rawText, -1)
	rowPositions := rowPattern.FindAllStringSubmatchIndex(rawText, -1)

	for i, match := range rowMatches {
		direction := match[1]
		timeStr := match[2]
		status := match[3]
		vessel := strings.TrimSpace(match[4])
		vesselType := strings.TrimSpace(match[5])

		// HTML entity decode
		vessel = strings.ReplaceAll(vessel, "&#x27;", "'")
		vessel = strings.ReplaceAll(vessel, "&amp;", "&")

		// Find which date this row belongs to
		var entryDate time.Time
		if len(rowPositions) > i {
			entryPos := rowPositions[i][0]
			for _, dp := range dates {
				if dp.pos <= entryPos {
					entryDate = dp.date
				}
			}
		}
		if entryDate.IsZero() {
			entryDate = time.Now()
		}

		// Parse time
		var hour, min int
		fmt.Sscanf(timeStr, "%d:%d", &hour, &min)
		eventTime := time.Date(entryDate.Year(), entryDate.Month(), entryDate.Day(),
			hour, min, 0, 0, loc)

		// Clean vessel name — title case
		vessel = strings.TrimSuffix(vessel, " (PPX)")
		vessel = toTitleCase(vessel)

		events = append(events, ScheduleEvent{
			Direction: direction,
			Time:      eventTime,
			Status:    status,
			Vessel:    vessel,
			Type:      vesselType,
			Announced: false,
		})
	}

	return events
}

// toTitleCase converts "CARNIVAL CELEBRATION" to "Carnival Celebration"
func toTitleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
		// Keep common abbreviations uppercase
		upper := strings.ToUpper(w)
		if upper == "M/Y" || upper == "MSC" || upper == "CMA" || upper == "CGM" {
			words[i] = upper
		}
	}
	return strings.Join(words, " ")
}

// GetUpcomingEvents returns events happening in the next N minutes
func (sm *ScheduleManager) GetUpcomingEvents(withinMinutes int) []ScheduleEvent {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	now := time.Now()
	cutoff := now.Add(time.Duration(withinMinutes) * time.Minute)

	var upcoming []ScheduleEvent
	for _, e := range sm.events {
		if e.Time.After(now) && e.Time.Before(cutoff) && !e.Announced {
			upcoming = append(upcoming, e)
		}
	}
	return upcoming
}

// GetTodaysSummary returns a summary of today's interesting events
func (sm *ScheduleManager) GetTodaysSummary() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	tomorrow := today.Add(24 * time.Hour)

	var cruises, yachts, cargo int
	for _, e := range sm.events {
		if e.Time.After(today) && e.Time.Before(tomorrow) {
			switch e.Type {
			case "CRUISE":
				cruises++
			case "YACHT":
				yachts++
			case "CARGO":
				cargo++
			}
		}
	}

	if cruises == 0 && yachts == 0 && cargo == 0 {
		return ""
	}

	parts := []string{}
	if cruises > 0 {
		parts = append(parts, fmt.Sprintf("%d cruise ships", cruises))
	}
	if yachts > 0 {
		parts = append(parts, fmt.Sprintf("%d yachts", yachts))
	}
	if cargo > 0 {
		parts = append(parts, fmt.Sprintf("%d cargo vessels", cargo))
	}

	return fmt.Sprintf("Today's port schedule: %s expected. Source: Biscayne Bay Pilots", strings.Join(parts, ", "))
}

// MarkAnnounced marks an event as announced so we don't repeat it
func (sm *ScheduleManager) MarkAnnounced(vessel string, eventTime time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for i, e := range sm.events {
		if e.Vessel == vessel && e.Time.Equal(eventTime) {
			sm.events[i].Announced = true
		}
	}
}

// FormatEventAnnouncement creates a chat-friendly announcement for an event
func FormatEventAnnouncement(e ScheduleEvent) string {
	timeStr := e.Time.Format("3:04 PM")
	dir := "arriving"
	if e.Direction == "DEPARTURE" {
		dir = "departing"
	}

	switch e.Type {
	case "CRUISE":
		return fmt.Sprintf("Cruise ship %s %s at %s! Keep watching!", e.Vessel, dir, timeStr)
	case "YACHT":
		return fmt.Sprintf("Yacht %s %s at %s", e.Vessel, dir, timeStr)
	case "CARGO":
		return fmt.Sprintf("Cargo vessel %s %s at %s", e.Vessel, dir, timeStr)
	default:
		return fmt.Sprintf("%s %s at %s", e.Vessel, dir, timeStr)
	}
}

// NeedsRefresh checks if the schedule should be re-fetched
func (sm *ScheduleManager) NeedsRefresh() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return time.Since(sm.lastFetch) > sm.fetchPeriod
}
