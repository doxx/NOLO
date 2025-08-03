#!/bin/bash

echo "Building Broadcast Monitor..."

# Build the Go binary
go build -o broadcast-monitor main.go

if [ $? -eq 0 ]; then
    echo "Build successful! Binary created: broadcast-monitor"
    echo ""
    echo "To run the monitor:"
    echo "  ./broadcast-monitor"
    echo ""
    echo "To run in background:"
    echo "  nohup ./broadcast-monitor > /dev/null 2>&1 &"
    echo ""
    echo "To stop background process:"
    echo "  pkill -f broadcast-monitor"
else
    echo "Build failed!"
    exit 1
fi 