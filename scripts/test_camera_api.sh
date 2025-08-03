#!/bin/bash

# Quick test of Hikvision API connectivity
CAMERA_IP="192.168.0.59"
USERNAME="admin"
PASSWORD="password1"

echo "Testing Hikvision API connectivity..."

# Test 1: Get current PTZ position
echo "Getting current PTZ position..."
curl --digest -u "$USERNAME:$PASSWORD" \
    "http://$CAMERA_IP/ISAPI/PTZCtrl/channels/1/status" \
    --silent | head -20

echo -e "\n"

# Test 2: Move to calibration position and capture test image
echo "Moving to calibration position (Pan:2050, Tilt:220, Zoom:60)..."
curl --digest -u "$USERNAME:$PASSWORD" \
    "http://$CAMERA_IP/ISAPI/PTZCtrl/channels/1/absolute" \
    -X PUT -H "Content-Type: application/xml" \
    -d "<PTZData><AbsoluteHigh><azimuth>2050</azimuth><elevation>220</elevation><absoluteZoom>60</absoluteZoom></AbsoluteHigh></PTZData>" \
    --silent

sleep 3

echo "Capturing test image..."
curl --digest -u "$USERNAME:$PASSWORD" \
    "http://$CAMERA_IP/ISAPI/Streaming/channels/1/picture" \
    -o "test_capture.jpg" \
    --silent

if [ -f "test_capture.jpg" ]; then
    echo "✓ Test image captured successfully: test_capture.jpg"
    ls -la test_capture.jpg
else
    echo "✗ Failed to capture test image"
    exit 1
fi

echo -e "\n✓ Camera API test completed successfully!"
echo "Ready to run: ./calibration_capture.sh" 