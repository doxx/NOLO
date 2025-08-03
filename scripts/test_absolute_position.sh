#!/bin/bash

# Camera details
CAMERA_IP="192.168.0.59"
USERNAME="admin"
PASSWORD="password1"

echo "=== Testing Absolute Position Response Time ==="
echo "=== Current time: $(date '+%H:%M:%S') ==="

# Get current position first
echo "=== Getting initial camera position ==="
INITIAL_RESPONSE=$(curl -s --digest -u "$USERNAME:$PASSWORD" \
  "http://$CAMERA_IP/ISAPI/PTZCtrl/channels/1/status")

if [ $? -eq 0 ]; then
  INITIAL_PAN=$(echo "$INITIAL_RESPONSE" | grep -o '<azimuth>[^<]*</azimuth>' | sed 's/<[^>]*>//g')
  INITIAL_TILT=$(echo "$INITIAL_RESPONSE" | grep -o '<elevation>[^<]*</elevation>' | sed 's/<[^>]*>//g')
  INITIAL_ZOOM=$(echo "$INITIAL_RESPONSE" | grep -o '<absoluteZoom>[^<]*</absoluteZoom>' | sed 's/<[^>]*>//g')
  echo "Initial Position - Pan: $INITIAL_PAN, Tilt: $INITIAL_TILT, Zoom: $INITIAL_ZOOM"
else
  echo "Failed to get initial position"
  exit 1
fi

# Calculate target position (move right by 20 units)
TARGET_PAN=$((INITIAL_PAN + 20))
TARGET_TILT=$INITIAL_TILT
TARGET_ZOOM=$INITIAL_ZOOM

echo ""
echo "=== Sending ABSOLUTE position command ==="
echo "Target Position - Pan: $TARGET_PAN, Tilt: $TARGET_TILT, Zoom: $TARGET_ZOOM"

START_TIME=$(date +%s.%3N)
echo "Command start time: $(date '+%H:%M:%S')"

# Send absolute position command (this is what your tracking system uses)
time curl -s --digest -u "$USERNAME:$PASSWORD" \
  -X PUT \
  -H "Content-Type: application/xml" \
  -d "<?xml version=\"1.0\" encoding=\"UTF-8\"?>
<PTZData>
  <AbsoluteHigh>
    <azimuth>$TARGET_PAN</azimuth>
    <elevation>$TARGET_TILT</elevation>
    <absoluteZoom>$TARGET_ZOOM</absoluteZoom>
  </AbsoluteHigh>
</PTZData>" \
  "http://$CAMERA_IP/ISAPI/PTZCtrl/channels/1/absolute" \
  && echo "Absolute position command sent successfully" || echo "Command failed"

echo ""
echo "=== Polling camera position every 100ms until target reached ==="

# Poll position until we reach target or timeout
TIMEOUT=50  # 5 seconds
TOLERANCE=2  # Within 2 units is considered "arrived"

for i in $(seq 1 $TIMEOUT); do
  CURRENT_TIME=$(date +%s.%3N)
  ELAPSED=$(echo "$CURRENT_TIME - $START_TIME" | bc -l)
  
  echo -n "[$i] Time: +${ELAPSED}s - "
  
  POSITION=$(curl -s --digest -u "$USERNAME:$PASSWORD" \
    "http://$CAMERA_IP/ISAPI/PTZCtrl/channels/1/status" 2>/dev/null)
  
  if [ $? -eq 0 ]; then
    CURRENT_PAN=$(echo "$POSITION" | grep -o '<azimuth>[^<]*</azimuth>' | sed 's/<[^>]*>//g')
    CURRENT_TILT=$(echo "$POSITION" | grep -o '<elevation>[^<]*</elevation>' | sed 's/<[^>]*>//g')
    CURRENT_ZOOM=$(echo "$POSITION" | grep -o '<absoluteZoom>[^<]*</absoluteZoom>' | sed 's/<[^>]*>//g')
    
    # Calculate distance from target
    PAN_DIFF=$((TARGET_PAN - CURRENT_PAN))
    PAN_DIFF=${PAN_DIFF#-}  # Absolute value
    
    echo "Pan: $CURRENT_PAN (target: $TARGET_PAN, diff: $PAN_DIFF), Tilt: $CURRENT_TILT, Zoom: $CURRENT_ZOOM"
    
    # Check if we've arrived at target
    if [ $PAN_DIFF -le $TOLERANCE ]; then
      echo ""
      echo "🎯 ARRIVED! Camera reached target position in ${ELAPSED}s"
      echo "Final Position - Pan: $CURRENT_PAN, Tilt: $CURRENT_TILT, Zoom: $CURRENT_ZOOM"
      break
    fi
  else
    echo "Failed to get position"
  fi
  
  sleep 0.1
done

# Check if we timed out
if [ $i -eq $TIMEOUT ]; then
  echo ""
  echo "⏰ TIMEOUT! Camera did not reach target position within 5 seconds"
  echo "This indicates a camera response problem!"
fi

echo ""
echo "=== Test Summary ==="
echo "Initial Pan: $INITIAL_PAN → Target Pan: $TARGET_PAN"
echo "Movement requested: +20 units"
echo "Total test time: $(echo "$(date +%s.%3N) - $START_TIME" | bc -l)s"
echo "=== Test completed at: $(date '+%H:%M:%S') ===" 