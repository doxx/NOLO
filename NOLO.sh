#!/bin/bash

# Simple loop to restart the software if we die.
#
#
# Replace your -input with the PTZ camera username password and stream values.
# Example: rtsp://username:password@192.168.1.59:554/Streaming/Channels/201
#
# The -ptzinput is the CGI/API interface to the camera: -ptzinput "http://user:password@192.168.1.59:80/"

while true ; do {

	go run NOLO.go  -input "rtsp://user:password1@192.168.1.100:554/Streaming/Channels/201" \
			-ptzinput "http://user:password@192.168.1.100:80/" \
			-max-pan=2550 -min-pan=900 \
		 	-p1-track="boat,surfboard" \
			-p2-track="all"\
			-pip\
			-target-overlay\
			-target-display-tracked\
			-status-overlay\
			-terminal-overlay\

	# debug and exparmental stuff
	# -YOLOdebug -maskcolors=6d9755 -masktolerance=50

	echo crashed 
	sleep 3s

} ; done
