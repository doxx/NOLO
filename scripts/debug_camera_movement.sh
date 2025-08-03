#!/bin/bash

# Camera details
CAMERA_IP="192.168.0.59"
USERNAME="admin"
PASSWORD="password1"

echo "=== CAMERA MOVEMENT DEBUG TEST ==="
echo "=== $(date '+%H:%M:%S') ==="

# Get initial position
echo "1. Getting initial position..."
INITIAL=$(curl -s --digest -u "$USERNAME:$PASSWORD" \
  "http://$CAMERA_IP/ISAPI/PTZCtrl/channels/1/status")

if [ $? -eq 0 ]; then
  INITIAL_PAN=$(echo "$INITIAL" | grep -o '<azimuth>[^<]*</azimuth>' | sed 's/<[^>]*>//g')
  INITIAL_TILT=$(echo "$INITIAL" | grep -o '<elevation>[^<]*</elevation>' | sed 's/<[^>]*>//g')
  echo "   Initial: Pan=$INITIAL_PAN, Tilt=$INITIAL_TILT"
else
  echo "   ERROR: Failed to get initial position"
  exit 1
fi

# Test 1: Small pan movement RIGHT
echo ""
echo "2. Testing small pan movement RIGHT (+20 units)..."
TARGET_PAN=$((INITIAL_PAN + 20))

curl -s --digest -u "$USERNAME:$PASSWORD" \
  -X PUT \
  -H "Content-Type: application/xml" \
  -d "<?xml version=\"1.0\" encoding=\"UTF-8\"?>
<PTZData>
  <AbsoluteHigh>
    <azimuth>$TARGET_PAN</azimuth>
    <elevation>$INITIAL_TILT</elevation>
    <absoluteZoom>26</absoluteZoom>
  </AbsoluteHigh>
</PTZData>" \
  "http://$CAMERA_IP/ISAPI/PTZCtrl/channels/1/absolute"

echo "   Command sent: Pan=$TARGET_PAN"

# Wait and check position multiple times
for i in {1..5}; do
  sleep 1
  CURRENT=$(curl -s --digest -u "$USERNAME:$PASSWORD" \
    "http://$CAMERA_IP/ISAPI/PTZCtrl/channels/1/status")
  
  CURRENT_PAN=$(echo "$CURRENT" | grep -o '<azimuth>[^<]*</azimuth>' | sed 's/<[^>]*>//g')
  CURRENT_TILT=$(echo "$CURRENT" | grep -o '<elevation>[^<]*</elevation>' | sed 's/<[^>]*>//g')
  
  echo "   ${i}s: Pan=$CURRENT_PAN, Tilt=$CURRENT_TILT"
  
  # Check if we reached target
  PAN_DIFF=$((TARGET_PAN - CURRENT_PAN))
  if [ ${PAN_DIFF#-} -le 5 ]; then
    echo "   ✅ SUCCESS: Camera reached target (within 5 units)"
    break
  fi
done

# Final check
if [ ${PAN_DIFF#-} -gt 5 ]; then
  echo "   ❌ FAILED: Camera did not reach target after 5 seconds"
  echo "   Expected: $TARGET_PAN, Got: $CURRENT_PAN, Diff: $PAN_DIFF"
else
  echo "   ✅ Camera movement working correctly"
fi

echo ""
echo "=== Test completed at $(date '+%H:%M:%S') ===" 