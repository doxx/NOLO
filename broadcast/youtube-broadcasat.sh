#!/bin/bash

while true ; do {

	go run broadcast.go -c broadcast_config_nvidia_nodrawtext.json 
#-record ./recordings/ 

	echo crashed 
	sleep 1s

} ; done
