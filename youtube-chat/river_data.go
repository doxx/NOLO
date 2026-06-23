package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// NWS Weather - Miami International Airport
	nwsStationURL = "https://api.weather.gov/stations/KMIA/observations/latest"

	// NOAA Tides - Virginia Key, Miami
	noaaTideURL     = "https://api.tidesandcurrents.noaa.gov/api/prod/datagetter"
	noaaTideStation = "8723214"

	// AISStream
	aisStreamWSS = "wss://stream.aisstream.io/v0/stream"

	// Camera location: 200 Biscayne Blvd Way, Miami, FL 33131
	cameraLat = 25.7695
	cameraLon = -80.1890

	// Detection radius in nautical miles
	detectionRadius = 0.5 // 0.5nm = ~926 meters = ~3,038 feet
)

// SegmentEvent is a timestamped event for video description enrichment
type SegmentEvent struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`    // vessel, weather, tide, bridge, chat_topic
	Message string    `json:"message"` // No usernames - content only
}

// RiverData manages all environmental data feeds
type RiverData struct {
	mu sync.RWMutex

	// Cached weather
	weatherMsg  string
	weatherTime time.Time

	// Cached tides
	tideMsg  string
	tideTime time.Time

	// Active vessels
	vessels    map[int]*Vessel
	vesselMu   sync.RWMutex
	announced  map[int]time.Time // MMSI -> last announcement time
	sendChatFn func(string)      // Function to send chat messages

	// AIS connection
	aisConn   *websocket.Conn
	aisAPIKey string

	// Segment event log for video descriptions
	segmentEvents []SegmentEvent
	segmentMu     sync.Mutex
}

// Vessel represents a tracked vessel
type Vessel struct {
	Name     string
	MMSI     int
	Lat      float64
	Lon      float64
	Distance float64 // nautical miles from camera
	Speed    float64 // knots
	COG      float64 // course over ground in degrees
	LastSeen time.Time
}

// describePosition returns a human-friendly description of where the vessel is
// relative to the Brickell Ave Bridge
func describePosition(vesselLat, vesselLon, cog float64) string {
	// How far east/west of the bridge
	lonDiff := vesselLon - cameraLon // positive = east, negative = west
	latDiff := vesselLat - cameraLat // positive = north, negative = south

	// Very close to bridge
	distFt := haversineNM(cameraLat, cameraLon, vesselLat, vesselLon) * 6076
	if distFt < 500 {
		return "at Brickell Bridge"
	}

	// Determine location relative to bridge
	location := ""
	if lonDiff < -0.002 { // ~600ft west
		location = "upriver"
	} else if lonDiff > 0.002 { // ~600ft east
		location = "near Biscayne Bay"
	} else if latDiff > 0.001 {
		location = "north of bridge"
	} else {
		location = "south of bridge"
	}

	// Determine if approaching or leaving based on COG
	// COG 0-360: 0=north, 90=east, 180=south, 270=west
	if cog >= 0 {
		heading := ""
		if cog >= 45 && cog < 135 {
			heading = "heading east"
		} else if cog >= 135 && cog < 225 {
			heading = "heading south"
		} else if cog >= 225 && cog < 315 {
			heading = "heading west"
		} else {
			heading = "heading north"
		}

		// Is it approaching the bridge?
		if location == "upriver" && (cog >= 45 && cog < 135) {
			return "approaching Brickell Bridge from upriver"
		}
		if location == "near Biscayne Bay" && (cog >= 225 && cog < 315) {
			return "approaching Brickell Bridge from the bay"
		}

		return fmt.Sprintf("%s, %s", location, heading)
	}

	return location
}

func NewRiverData(sendChat func(string), aisKey string) *RiverData {
	return &RiverData{
		vessels:    make(map[int]*Vessel),
		announced:  make(map[int]time.Time),
		sendChatFn: sendChat,
		aisAPIKey:  aisKey,
	}
}

// Start begins all data feeds
func (rd *RiverData) Start() {
	go rd.weatherLoop()
	go rd.tideLoop()
	if rd.aisAPIKey != "" {
		go rd.aisLoop()
		go rd.vesselCleanup()
		log.Println("[AIS] Vessel tracking enabled")
	} else {
		log.Println("[AIS] No API key - vessel tracking disabled")
	}
	go rd.hourlyAnnounce()
	go rd.segmentEventSaver()
	log.Println("[RIVER_DATA] All data feeds started")
}

// segmentEventSaver periodically saves segment events to disk for the recorder
func (rd *RiverData) segmentEventSaver() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		rd.segmentMu.Lock()
		count := len(rd.segmentEvents)
		rd.segmentMu.Unlock()
		if count > 0 {
			path := fmt.Sprintf("/home/blyon/NOLO/recordings/events_%s.json",
				time.Now().Format("20060102_1504"))
			if err := rd.SaveSegmentEvents(path); err != nil {
				log.Printf("[EVENTS_ERROR] Failed to save: %v", err)
			} else {
				log.Printf("[EVENTS] Saved %d events to %s", count, path)
			}
		}
	}
}

// GetWeather returns cached weather string
func (rd *RiverData) GetWeather() string {
	rd.mu.RLock()
	defer rd.mu.RUnlock()
	if rd.weatherMsg == "" {
		return "Weather data loading..."
	}
	return rd.weatherMsg
}

// GetTide returns cached tide string
func (rd *RiverData) GetTide() string {
	rd.mu.RLock()
	defer rd.mu.RUnlock()
	if rd.tideMsg == "" {
		return "Tide data loading..."
	}
	return rd.tideMsg
}

// GetBoats returns current vessel info string
func (rd *RiverData) GetBoats() string {
	rd.vesselMu.RLock()
	defer rd.vesselMu.RUnlock()

	if len(rd.vessels) == 0 {
		return "No vessels detected near the bridge right now"
	}

	var parts []string
	for _, v := range rd.vessels {
		if time.Since(v.LastSeen) > 5*time.Minute {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%.0fft)", v.Name, v.Distance*6076))
	}

	if len(parts) == 0 {
		return "No active vessels near the bridge"
	}

	msg := fmt.Sprintf("%d vessels nearby: %s", len(parts), strings.Join(parts, ", "))
	if len(msg) > 200 {
		msg = msg[:197] + "..."
	}
	return msg
}

// weatherLoop fetches weather every 15 minutes
func (rd *RiverData) weatherLoop() {
	rd.fetchWeather() // Initial fetch
	ticker := time.NewTicker(15 * time.Minute)
	for range ticker.C {
		rd.fetchWeather()
	}
}

func (rd *RiverData) fetchWeather() {
	req, err := http.NewRequest("GET", nwsStationURL, nil)
	if err != nil {
		log.Printf("[WEATHER_ERROR] %v", err)
		return
	}
	req.Header.Set("User-Agent", "NOLOCam/1.0 (github.com/doxx/NOLO)")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[WEATHER_ERROR] %v", err)
		return
	}
	defer resp.Body.Close()

	var obs struct {
		Properties struct {
			Temperature struct {
				Value *float64 `json:"value"`
			} `json:"temperature"`
			RelativeHumidity struct {
				Value *float64 `json:"value"`
			} `json:"relativeHumidity"`
			WindSpeed struct {
				Value *float64 `json:"value"`
			} `json:"windSpeed"`
			TextDescription string `json:"textDescription"`
		} `json:"properties"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&obs); err != nil {
		log.Printf("[WEATHER_ERROR] decode: %v", err)
		return
	}

	p := obs.Properties
	if p.Temperature.Value == nil {
		log.Printf("[WEATHER_ERROR] no temperature data")
		return
	}

	tempC := *p.Temperature.Value
	tempF := tempC*9/5 + 32
	humidity := 0.0
	if p.RelativeHumidity.Value != nil {
		humidity = *p.RelativeHumidity.Value
	}
	windMPH := 0.0
	if p.WindSpeed.Value != nil {
		windMPH = *p.WindSpeed.Value * 0.621371
	}

	msg := fmt.Sprintf("Miami: %.0f°F/%.0f°C, %s, %.0f%% humidity, wind %.0f mph",
		tempF, tempC, p.TextDescription, humidity, windMPH)

	rd.mu.Lock()
	rd.weatherMsg = msg
	rd.weatherTime = time.Now()
	rd.mu.Unlock()

	log.Printf("[WEATHER] %s", msg)
	rd.LogSegmentEvent("weather", msg)
}

// tideLoop fetches tide data every 30 minutes
func (rd *RiverData) tideLoop() {
	rd.fetchTides() // Initial fetch
	ticker := time.NewTicker(30 * time.Minute)
	for range ticker.C {
		rd.fetchTides()
	}
}

func (rd *RiverData) fetchTides() {
	client := &http.Client{Timeout: 10 * time.Second}

	// Current water level
	levelURL := fmt.Sprintf("%s?date=latest&station=%s&product=water_level&datum=MLLW&time_zone=lst_ldt&units=english&format=json",
		noaaTideURL, noaaTideStation)
	resp, err := client.Get(levelURL)
	if err != nil {
		log.Printf("[TIDE_ERROR] %v", err)
		return
	}
	defer resp.Body.Close()

	var levelData struct {
		Data []struct {
			V string `json:"v"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&levelData)

	currentLevel := "N/A"
	if len(levelData.Data) > 0 {
		currentLevel = levelData.Data[0].V + "ft"
	}

	// High/low predictions
	hiloURL := fmt.Sprintf("%s?date=today&station=%s&product=predictions&datum=MLLW&time_zone=lst_ldt&units=english&format=json&interval=hilo",
		noaaTideURL, noaaTideStation)
	resp2, err := client.Get(hiloURL)
	if err != nil {
		log.Printf("[TIDE_ERROR] predictions: %v", err)
		return
	}
	defer resp2.Body.Close()

	var hiloData struct {
		Predictions []struct {
			T    string `json:"t"`
			V    string `json:"v"`
			Type string `json:"type"`
		} `json:"predictions"`
	}
	json.NewDecoder(resp2.Body).Decode(&hiloData)

	now := time.Now()
	nextTide := ""
	for _, p := range hiloData.Predictions {
		t, err := time.ParseInLocation("2006-01-02 15:04", p.T, now.Location())
		if err != nil {
			continue
		}
		if t.After(now) && nextTide == "" {
			tideType := "high"
			if p.Type == "L" {
				tideType = "low"
			}
			nextTide = fmt.Sprintf("next %s %sft at %s", tideType, p.V, t.Format("3:04 PM"))
		}
	}

	msg := fmt.Sprintf("Tide: %s, %s", currentLevel, nextTide)

	rd.mu.Lock()
	rd.tideMsg = msg
	rd.tideTime = time.Now()
	rd.mu.Unlock()

	log.Printf("[TIDE] %s", msg)
	rd.LogSegmentEvent("tide", msg)
}

// aisLoop maintains AIS websocket connection
func (rd *RiverData) aisLoop() {
	for {
		rd.connectAIS()
		log.Println("[AIS] Connection lost, reconnecting in 10s...")
		time.Sleep(10 * time.Second)
	}
}

func (rd *RiverData) connectAIS() {
	latMin, lonMin, latMax, lonMax := boundingBoxNM(cameraLat, cameraLon, detectionRadius)
	log.Printf("[AIS] Connecting (%.2fnm radius around camera)...", detectionRadius)

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(aisStreamWSS, nil)
	if err != nil {
		log.Printf("[AIS_ERROR] dial: %v", err)
		return
	}
	defer conn.Close()
	rd.aisConn = conn

	// Subscribe to our area
	sub := map[string]interface{}{
		"APIKey":             rd.aisAPIKey,
		"BoundingBoxes":      [][][2]float64{{{latMin, lonMin}, {latMax, lonMax}}},
		"FilterMessageTypes": []string{"PositionReport", "StandardClassBPositionReport"},
	}
	if err := conn.WriteJSON(sub); err != nil {
		log.Printf("[AIS_ERROR] subscribe: %v", err)
		return
	}

	log.Printf("[AIS] Subscribed to area [%.4f,%.4f]-[%.4f,%.4f]", latMin, lonMin, latMax, lonMax)

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[AIS_ERROR] read: %v", err)
			return
		}

		// Check for error
		var errResp map[string]string
		if json.Unmarshal(msg, &errResp) == nil {
			if e, ok := errResp["error"]; ok {
				log.Printf("[AIS_ERROR] %s", e)
				return
			}
		}

		var ais struct {
			MessageType string `json:"MessageType"`
			MetaData    struct {
				MMSI      int     `json:"MMSI"`
				ShipName  string  `json:"ShipName"`
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"MetaData"`
			Message json.RawMessage `json:"Message"`
		}
		if err := json.Unmarshal(msg, &ais); err != nil {
			continue
		}

		if ais.MetaData.Latitude == 0 && ais.MetaData.Longitude == 0 {
			continue
		}

		dist := haversineNM(cameraLat, cameraLon, ais.MetaData.Latitude, ais.MetaData.Longitude)
		if dist > detectionRadius {
			continue // Outside our zone
		}

		name := formatVesselName(ais.MetaData.ShipName)
		if name == "" {
			continue // Skip vessels without names
		}

		// Parse speed and course from position report
		speed := 0.0
		cog := -1.0
		var posReport struct {
			PositionReport struct {
				Sog float64 `json:"Sog"`
				Cog float64 `json:"Cog"`
			} `json:"PositionReport"`
			StandardClassBPositionReport struct {
				Sog float64 `json:"Sog"`
				Cog float64 `json:"Cog"`
			} `json:"StandardClassBPositionReport"`
		}
		json.Unmarshal(ais.Message, &posReport)
		if posReport.PositionReport.Sog > 0 {
			speed = posReport.PositionReport.Sog
			cog = posReport.PositionReport.Cog
		} else if posReport.StandardClassBPositionReport.Sog > 0 {
			speed = posReport.StandardClassBPositionReport.Sog
			cog = posReport.StandardClassBPositionReport.Cog
		}

		rd.vesselMu.Lock()
		_, existed := rd.vessels[ais.MetaData.MMSI]
		rd.vessels[ais.MetaData.MMSI] = &Vessel{
			Name:     name,
			MMSI:     ais.MetaData.MMSI,
			Lat:      ais.MetaData.Latitude,
			Lon:      ais.MetaData.Longitude,
			Distance: dist,
			Speed:    speed / 10.0, // AIS speed is in 1/10 knot
			COG:      cog,
			LastSeen: time.Now(),
		}

		// Auto-announce new vessels
		// Skip docked/stationary vessels (speed < 0.3 knots) - they spam AIS from marinas
		actualSpeed := speed / 10.0 // AIS speed is in 1/10 knot
		lastAnnounce, wasAnnounced := rd.announced[ais.MetaData.MMSI]
		shouldAnnounce := !existed && actualSpeed >= 0.3 && (!wasAnnounced || time.Since(lastAnnounce) > 10*time.Minute)

		if shouldAnnounce {
			rd.announced[ais.MetaData.MMSI] = time.Now()
			position := describePosition(ais.MetaData.Latitude, ais.MetaData.Longitude, cog)
			chatMsg := fmt.Sprintf("Incoming: %s %s", name, position)
			if len(chatMsg) > 200 {
				chatMsg = chatMsg[:197] + "..."
			}
			log.Printf("[AIS_ANNOUNCE] %s", chatMsg)
			if rd.sendChatFn != nil {
				go rd.sendChatFn(chatMsg)
			}
			// Log for video description
			rd.LogSegmentEvent("vessel", fmt.Sprintf("%s %s", name, position))
		}
		rd.vesselMu.Unlock()

		if !existed {
			position := describePosition(ais.MetaData.Latitude, ais.MetaData.Longitude, cog)
			log.Printf("[AIS] New: %s at %.2fnm - %s", name, dist, position)
		}
	}
}

// hourlyAnnounce posts weather + tide summary every 4 hours
func (rd *RiverData) hourlyAnnounce() {
	// Wait for next 4-hour mark (don't announce on startup)
	now := time.Now()
	// Next 4-hour boundary: 0, 4, 8, 12, 16, 20
	currentHour := now.Hour()
	nextSlot := ((currentHour / 4) + 1) * 4
	nextAnnounce := time.Date(now.Year(), now.Month(), now.Day(), nextSlot%24, 0, 0, 0, now.Location())
	if nextSlot >= 24 {
		nextAnnounce = nextAnnounce.AddDate(0, 0, 1)
	}
	log.Printf("[ANNOUNCE] Next weather/tide announcement at %s", nextAnnounce.Format("3:04 PM"))
	time.Sleep(time.Until(nextAnnounce))

	rd.doHourlyAnnounce()

	ticker := time.NewTicker(4 * time.Hour)
	for range ticker.C {
		rd.doHourlyAnnounce()
	}
}

func (rd *RiverData) doHourlyAnnounce() {
	if rd.sendChatFn == nil {
		return
	}

	weather := rd.GetWeather()
	tide := rd.GetTide()

	// Combine weather + tide into one message (under 200 chars)
	msg := fmt.Sprintf("%s | %s", weather, tide)
	if len(msg) > 200 {
		msg = weather // Just weather if combined is too long
	}

	log.Printf("[HOURLY] %s", msg)
	rd.sendChatFn(msg)
}

// vesselCleanup removes stale vessels every minute
func (rd *RiverData) vesselCleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	for range ticker.C {
		rd.vesselMu.Lock()
		for mmsi, v := range rd.vessels {
			if time.Since(v.LastSeen) > 5*time.Minute {
				log.Printf("[AIS] Removed stale vessel: %s", v.Name)
				delete(rd.vessels, mmsi)
			}
		}
		rd.vesselMu.Unlock()
	}
}

// Haversine distance in nautical miles
func haversineNM(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusNM = 3440.065
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusNM * c
}

// LogSegmentEvent records an event for video description enrichment
func (rd *RiverData) LogSegmentEvent(eventType, message string) {
	rd.segmentMu.Lock()
	defer rd.segmentMu.Unlock()
	rd.segmentEvents = append(rd.segmentEvents, SegmentEvent{
		Time:    time.Now(),
		Type:    eventType,
		Message: message,
	})
}

// FlushSegmentEvents returns all events and clears the log (called when a recording segment completes)
func (rd *RiverData) FlushSegmentEvents() []SegmentEvent {
	rd.segmentMu.Lock()
	defer rd.segmentMu.Unlock()
	events := rd.segmentEvents
	rd.segmentEvents = nil
	return events
}

// SaveSegmentEvents writes events to a JSON file for the recorder to pick up
func (rd *RiverData) SaveSegmentEvents(path string) error {
	events := rd.FlushSegmentEvents()
	if len(events) == 0 {
		return nil
	}
	data, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// formatVesselName converts AIS vessel names from "BEYOND BEYOND" to "Beyond Beyond"
func formatVesselName(raw string) string {
	name := strings.TrimSpace(raw)
	if name == "" {
		return ""
	}
	// AIS names are typically ALL CAPS - convert to title case
	words := strings.Fields(strings.ToLower(name))
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// boundingBoxNM returns a bounding box for a given radius in nautical miles
func boundingBoxNM(lat, lon, radiusNM float64) (latMin, lonMin, latMax, lonMax float64) {
	latDelta := radiusNM / 60.0
	lonDelta := radiusNM / (60.0 * math.Cos(lat*math.Pi/180))
	return lat - latDelta, lon - lonDelta, lat + latDelta, lon + lonDelta
}
