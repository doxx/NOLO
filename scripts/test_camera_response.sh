#!/bin/bash

# Camera details
CAMERA_IP="192.168.0.59"
USERNAME="admin"
PASSWORD="password1"

# Get current position first
echo "=== Getting initial camera position ==="
time curl -s --digest -u "$USERNAME:$PASSWORD" \
  "http://$CAMERA_IP/ISAPI/PTZCtrl/channels/1/status" \
  | grep -E "(pan|tilt|zoom)" || echo "Failed to get initial position"

echo ""
echo "=== Current time: $(date '+%H:%M:%S.%3N') ==="

# Send PTZ command - move right by a small amount
echo "=== Sending PTZ command: Move Right ==="
START_TIME=$(date +%s.%3N)
time curl -s --digest -u "$USERNAME:$PASSWORD" \
  -X PUT \
  -H "Content-Type: application/xml" \
  -d '<?xml version="1.0" encoding="UTF-8"?>
<PTZData>
  <pan>10</pan>
  <tilt>0</tilt>
  <zoom>0</zoom>
</PTZData>' \
  "http://$CAMERA_IP/ISAPI/PTZCtrl/channels/1/continuous" \
  && echo "Command sent successfully" || echo "Command failed"

echo ""
echo "=== Command sent at: $(date '+%H:%M:%S.%3N') ==="

# Stop the movement after a short duration
sleep 0.5
echo "=== Stopping movement ==="
curl -s --digest -u "$USERNAME:$PASSWORD" \
  -X PUT \
  -H "Content-Type: application/xml" \
  -d '<?xml version="1.0" encoding="UTF-8"?>
<PTZData>
  <pan>0</pan>
  <tilt>0</tilt>
  <zoom>0</zoom>
</PTZData>' \
  "http://$CAMERA_IP/ISAPI/PTZCtrl/channels/1/continuous" > /dev/null

echo ""
echo "=== Polling camera position every 100ms ==="

# Poll position for 5 seconds
for i in {1..50}; do
  CURRENT_TIME=$(date +%s.%3N)
  ELAPSED=$(echo "$CURRENT_TIME - $START_TIME" | bc)
  
  echo -n "[$i] Time: +${ELAPSED}s - "
  
  POSITION=$(curl -s --digest -u "$USERNAME:$PASSWORD" \
    "http://$CAMERA_IP/ISAPI/PTZCtrl/channels/1/status" 2>/dev/null)
  
  if [ $? -eq 0 ]; then
    PAN=$(echo "$POSITION" | grep -o '<pan>[^<]*</pan>' | sed 's/<[^>]*>//g')
    TILT=$(echo "$POSITION" | grep -o '<tilt>[^<]*</tilt>' | sed 's/<[^>]*>//g')
    ZOOM=$(echo "$POSITION" | grep -o '<zoom>[^<]*</zoom>' | sed 's/<[^>]*>//g')
    echo "Pan: $PAN, Tilt: $TILT, Zoom: $ZOOM"
  else
    echo "Failed to get position"
  fi
  
  sleep 0.1
done

echo ""
echo "=== Final position check ==="
time curl -s --digest -u "$USERNAME:$PASSWORD" \
  "http://$CAMERA_IP/ISAPI/PTZCtrl/channels/1/status" \
  | grep -E "(pan|tilt|zoom)" || echo "Failed to get final position"

echo ""
echo "=== Test completed at: $(date '+%H:%M:%S.%3N') ===" 