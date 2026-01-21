# NOLO Server Deployment Guide

This guide covers the complete deployment of NOLO on a dedicated GPU server with NVIDIA hardware acceleration. This setup was tested on Debian 12 (Bookworm) with an NVIDIA RTX 5050 GPU.

## Table of Contents

1. [System Requirements](#system-requirements)
2. [NVIDIA Driver Installation](#nvidia-driver-installation)
3. [CUDA Toolkit Installation](#cuda-toolkit-installation)
4. [cuDNN Installation](#cudnn-installation)
5. [OpenCV with CUDA Build](#opencv-with-cuda-build)
6. [FFmpeg with NVENC Build](#ffmpeg-with-nvenc-build)
7. [Go and GoCV Installation](#go-and-gocv-installation)
8. [SRS RTMP Server Setup](#srs-rtmp-server-setup)
9. [NOLO Installation](#nolo-installation)
10. [Broadcast Setup](#broadcast-setup)
11. [Systemd Services](#systemd-services)
12. [Troubleshooting](#troubleshooting)

---

## System Requirements

### Hardware
- **GPU**: NVIDIA GPU with CUDA support (tested with RTX 5050, compute capability 12.0)
- **RAM**: 8GB+ recommended
- **Storage**: 50GB+ for build files and models
- **Network**: Stable connection for RTSP input and RTMP output

### Software
- **OS**: Debian 12 (Bookworm), Ubuntu 22.04+, or similar
- **Kernel**: Linux 6.x
- **Build tools**: gcc, g++, make, cmake, pkg-config

### Initial System Setup

```bash
# Update system
sudo apt update && sudo apt upgrade -y

# Install essential build tools
sudo apt install -y build-essential cmake pkg-config git wget curl
sudo apt install -y libgtk-3-dev libavcodec-dev libavformat-dev libswscale-dev
sudo apt install -y libv4l-dev libxvidcore-dev libx264-dev libjpeg-dev libpng-dev
sudo apt install -y libtiff-dev libatlas-base-dev gfortran python3-dev
sudo apt install -y libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev
sudo apt install -y libtbb2 libtbb-dev libdc1394-dev libopenblas-dev
sudo apt install -y unzip tclsh automake yasm nasm
```

---

## NVIDIA Driver Installation

### Add NVIDIA Repository

```bash
# Install prerequisites
sudo apt install -y software-properties-common dirmngr ca-certificates

# Add NVIDIA CUDA repository
wget https://developer.download.nvidia.com/compute/cuda/repos/debian12/x86_64/cuda-keyring_1.1-1_all.deb
sudo dpkg -i cuda-keyring_1.1-1_all.deb
sudo apt update
```

### Install NVIDIA Driver

```bash
# Install CUDA (includes driver)
sudo apt install -y cuda
```

### Disable Secure Boot (if enabled)

NVIDIA drivers require Secure Boot to be disabled:

```bash
# Check Secure Boot status
mokutil --sb-state

# If enabled, disable it in BIOS/UEFI settings
# The driver will fail with "Key was rejected by service" error otherwise
```

### Verify Driver Installation

```bash
# Reboot required after driver installation
sudo reboot

# Verify driver is loaded
nvidia-smi
```

Expected output:
```
+-----------------------------------------------------------------------------+
| NVIDIA-SMI 590.xx.xx    Driver Version: 590.xx.xx    CUDA Version: 13.x    |
|-------------------------------+----------------------+----------------------+
| GPU  Name        Persistence-M| Bus-Id        Disp.A | Volatile Uncorr. ECC |
| Fan  Temp  Perf  Pwr:Usage/Cap|         Memory-Usage | GPU-Util  Compute M. |
|===============================+======================+======================|
|   0  NVIDIA GeForce ...  Off  | 00000000:06:00.0 Off |                  N/A |
| N/A   45C    P8     5W /  75W |      0MiB /  8192MiB |      0%      Default |
+-------------------------------+----------------------+----------------------+
```

---

## CUDA Toolkit Installation

### Set Up Environment

```bash
# Create CUDA environment file
cat << 'EOF' | sudo tee /etc/profile.d/cuda.sh
export PATH=/usr/local/cuda/bin:$PATH
export LD_LIBRARY_PATH=/usr/local/cuda/lib64:$LD_LIBRARY_PATH
EOF

# Apply to current session
source /etc/profile.d/cuda.sh
```

### Verify CUDA Installation

```bash
nvcc --version
# Should show CUDA version (e.g., 13.1)
```

---

## cuDNN Installation

### Download and Install cuDNN

```bash
# Download cuDNN from NVIDIA (requires developer account)
# https://developer.nvidia.com/cudnn

# Install from .deb packages
sudo dpkg -i cudnn-local-repo-*.deb
sudo cp /var/cudnn-local-repo-*/cudnn-*-keyring.gpg /usr/share/keyrings/
sudo apt update
sudo apt install -y libcudnn9-cuda-12 libcudnn9-dev-cuda-12
```

### Verify cuDNN

```bash
cat /usr/include/cudnn_version.h | grep CUDNN_MAJOR -A 2
```

---

## OpenCV with CUDA Build

Building OpenCV from source with CUDA support is required for GPU-accelerated inference.

### Create Build Directory

```bash
mkdir -p ~/build && cd ~/build
```

### Download OpenCV and Contrib Modules

```bash
# Download OpenCV 4.11.0
wget -O opencv.zip https://github.com/opencv/opencv/archive/4.11.0.zip
wget -O opencv_contrib.zip https://github.com/opencv/opencv_contrib/archive/4.11.0.zip
unzip opencv.zip
unzip opencv_contrib.zip
```

### Determine CUDA Architecture

```bash
# Find your GPU's compute capability
nvidia-smi --query-gpu=compute_cap --format=csv,noheader
# Example output: 12.0 (for RTX 5050)
# Use this value for CUDA_ARCH_BIN below
```

### Configure and Build OpenCV

```bash
cd ~/build/opencv-4.11.0
mkdir build && cd build

# Configure with CUDA support
# Replace CUDA_ARCH_BIN with your GPU's compute capability
cmake -D CMAKE_BUILD_TYPE=RELEASE \
      -D CMAKE_INSTALL_PREFIX=/usr/local \
      -D OPENCV_EXTRA_MODULES_PATH=~/build/opencv_contrib-4.11.0/modules \
      -D WITH_CUDA=ON \
      -D WITH_CUDNN=ON \
      -D OPENCV_DNN_CUDA=ON \
      -D ENABLE_FAST_MATH=1 \
      -D CUDA_FAST_MATH=1 \
      -D CUDA_ARCH_BIN=12.0 \
      -D WITH_CUBLAS=1 \
      -D WITH_TBB=ON \
      -D WITH_V4L=ON \
      -D WITH_FFMPEG=ON \
      -D WITH_GSTREAMER=ON \
      -D BUILD_opencv_python3=OFF \
      -D BUILD_TESTS=OFF \
      -D BUILD_PERF_TESTS=OFF \
      -D BUILD_EXAMPLES=OFF \
      ..

# Build (adjust -j flag to your CPU cores)
make -j$(nproc)
sudo make install
sudo ldconfig
```

### Create pkg-config File

```bash
# Create opencv4.pc for GoCV
cat << 'EOF' | sudo tee /usr/local/lib/pkgconfig/opencv4.pc
prefix=/usr/local
exec_prefix=${prefix}
libdir=${exec_prefix}/lib
includedir=${prefix}/include/opencv4

Name: OpenCV
Description: Open Source Computer Vision Library
Version: 4.11.0
Libs: -L${libdir} -lopencv_gapi -lopencv_stitching -lopencv_aruco -lopencv_bgsegm -lopencv_bioinspired -lopencv_ccalib -lopencv_cudabgsegm -lopencv_cudafeatures2d -lopencv_cudaobjdetect -lopencv_cudastereo -lopencv_cudaoptflow -lopencv_dnn_objdetect -lopencv_dnn_superres -lopencv_dpm -lopencv_face -lopencv_freetype -lopencv_fuzzy -lopencv_hfs -lopencv_img_hash -lopencv_intensity_transform -lopencv_line_descriptor -lopencv_mcc -lopencv_quality -lopencv_rapid -lopencv_reg -lopencv_rgbd -lopencv_saliency -lopencv_stereo -lopencv_structured_light -lopencv_phase_unwrapping -lopencv_superres -lopencv_optflow -lopencv_surface_matching -lopencv_tracking -lopencv_highgui -lopencv_datasets -lopencv_text -lopencv_plot -lopencv_videostab -lopencv_videoio -lopencv_wechat_qrcode -lopencv_xfeatures2d -lopencv_shape -lopencv_ml -lopencv_ximgproc -lopencv_video -lopencv_xobjdetect -lopencv_objdetect -lopencv_calib3d -lopencv_imgcodecs -lopencv_features2d -lopencv_dnn -lopencv_flann -lopencv_xphoto -lopencv_photo -lopencv_cudawarping -lopencv_cudaimgproc -lopencv_cudafilters -lopencv_cudaarithm -lopencv_cudacodec -lopencv_imgproc -lopencv_core -lopencv_cudev
Cflags: -I${includedir}
EOF

# Update pkg-config path
export PKG_CONFIG_PATH=/usr/local/lib/pkgconfig:$PKG_CONFIG_PATH
```

### Verify OpenCV Installation

```bash
pkg-config --modversion opencv4
# Should output: 4.11.0

pkg-config --libs opencv4 | grep cuda
# Should show CUDA libraries
```

---

## FFmpeg with NVENC Build

Building FFmpeg from source with NVIDIA hardware encoding support.

### Install FFmpeg Dependencies

```bash
sudo apt install -y libx264-dev libx265-dev libvpx-dev libfdk-aac-dev \
    libmp3lame-dev libopus-dev libass-dev libfreetype6-dev
```

### Install NVIDIA Headers

```bash
cd ~/build
git clone https://git.videolan.org/git/ffmpeg/nv-codec-headers.git
cd nv-codec-headers
make
sudo make install
```

### Download and Build FFmpeg

```bash
cd ~/build
wget https://ffmpeg.org/releases/ffmpeg-7.1.1.tar.xz
tar xf ffmpeg-7.1.1.tar.xz
cd ffmpeg-7.1.1

# Ensure CUDA is in PATH
export PATH=/usr/local/cuda/bin:$PATH

# Configure with NVENC support
# Replace compute_120 with your GPU's compute capability (e.g., compute_86 for RTX 3090)
./configure --prefix=/usr/local \
    --enable-gpl \
    --enable-nonfree \
    --enable-cuda-nvcc \
    --enable-cuvid \
    --enable-nvdec \
    --enable-nvenc \
    --enable-libnpp \
    --extra-cflags=-I/usr/local/cuda/include \
    --extra-ldflags=-L/usr/local/cuda/lib64 \
    --nvccflags="-gencode arch=compute_120,code=sm_120" \
    --enable-libx264 \
    --enable-libx265 \
    --enable-libvpx \
    --enable-libfdk-aac \
    --enable-libmp3lame \
    --enable-libopus \
    --enable-libass \
    --enable-libfreetype

# Build (must have PATH set for nvcc)
make -j$(nproc)
sudo make install
```

### Verify FFmpeg NVENC Support

```bash
ffmpeg -encoders 2>/dev/null | grep nvenc
# Should show: h264_nvenc, hevc_nvenc, etc.
```

---

## Go and GoCV Installation

### Install Go

```bash
# Download Go 1.23+
cd ~/build
wget https://go.dev/dl/go1.23.5.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.23.5.linux-amd64.tar.gz

# Add Go to PATH
cat << 'EOF' | sudo tee /etc/profile.d/go.sh
export PATH=/usr/local/go/bin:$PATH
export GOPATH=$HOME/go
export PATH=$PATH:$GOPATH/bin
EOF

source /etc/profile.d/go.sh
```

### Install GoCV

```bash
# Set CGO flags for OpenCV
export CGO_CFLAGS="$(pkg-config --cflags opencv4)"
export CGO_LDFLAGS="$(pkg-config --libs opencv4)"
export CGO_CXXFLAGS="-std=c++11"
export PKG_CONFIG_PATH=/usr/local/lib/pkgconfig:$PKG_CONFIG_PATH

# Install GoCV
go install gocv.io/x/gocv@latest
```

### Verify GoCV Installation

```bash
# Create test program
mkdir -p ~/test_gocv && cd ~/test_gocv
cat << 'EOF' > main.go
package main

import (
    "fmt"
    "gocv.io/x/gocv"
)

func main() {
    fmt.Println("OpenCV Version:", gocv.Version())
    fmt.Println("GoCV working!")
}
EOF

go mod init test_gocv
go mod tidy
go run main.go
# Should output OpenCV version
```

---

## SRS RTMP Server Setup

SRS (Simple Realtime Server) provides local RTMP relay for the broadcast pipeline.

### Build and Install SRS

```bash
cd ~/build
git clone --depth 1 https://github.com/ossrs/srs.git
cd srs/trunk
./configure
make -j$(nproc)
sudo cp objs/srs /usr/local/bin/
```

### Configure SRS

```bash
sudo mkdir -p /etc/srs /var/run/srs

cat << 'EOF' | sudo tee /etc/srs/srs.conf
listen              1935;
max_connections     500;
daemon              off;
srs_log_tank        console;
pid                 /var/run/srs/srs.pid;

vhost __defaultVhost__ {
    tcp_nodelay     on;
}
EOF
```

### Create SRS Systemd Service

```bash
cat << 'EOF' | sudo tee /etc/systemd/system/srs.service
[Unit]
Description=SRS RTMP Server
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/srs -c /etc/srs/srs.conf
WorkingDirectory=/usr/local
User=root
Restart=always
RestartSec=5
LimitNOFILE=100000

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable srs
sudo systemctl start srs
```

---

## NOLO Installation

### Clone Repository

```bash
cd ~
git clone https://github.com/doxx/NOLO.git
cd NOLO
```

### Download YOLO Models

```bash
mkdir -p models

# Download YOLOv8n ONNX model
wget -O models/yolov8n.onnx https://github.com/ultralytics/assets/releases/download/v8.3.0/yolov8n.onnx

# Download YOLOv3-tiny (alternative)
wget -O yolov3-tiny.weights https://pjreddie.com/media/files/yolov3-tiny.weights
wget -O yolov3-tiny.cfg https://raw.githubusercontent.com/pjreddie/darknet/master/cfg/yolov3-tiny.cfg
```

### Build NOLO

```bash
# Set environment
export CGO_CFLAGS="$(pkg-config --cflags opencv4)"
export CGO_LDFLAGS="$(pkg-config --libs opencv4)"
export CGO_CXXFLAGS="-std=c++11"
export LD_LIBRARY_PATH=/usr/local/lib:/usr/local/cuda/lib64:$LD_LIBRARY_PATH

# Build
go mod tidy
go build -o NOLO .
```

### Create FFmpeg Symlink (if needed)

Some builds have hardcoded FFmpeg paths:

```bash
mkdir -p ~/FFmpeg-n7.1.1
ln -sf /usr/local/bin/ffmpeg ~/FFmpeg-n7.1.1/ffmpeg
```

### Configure Scanning Pattern

Edit `scanning.json` with your camera positions:

```json
{
  "name": "My Camera",
  "description": "Camera scanning pattern",
  "positions": [
    {
      "id": 1,
      "name": "Position 1",
      "position": {
        "Pan": 1800,
        "Tilt": 200,
        "Zoom": 10
      },
      "dwell_time_seconds": 15
    },
    {
      "id": 2,
      "name": "Position 2",
      "position": {
        "Pan": 2200,
        "Tilt": 300,
        "Zoom": 15
      },
      "dwell_time_seconds": 20
    }
  ]
}
```

---

## Broadcast Setup

The broadcast system takes the NOLO output and pushes it to YouTube with audio mixing.

### Build Broadcast Component

```bash
cd ~/NOLO/broadcast
go build -o broadcast broadcast.go
```

### Configure Broadcast

Edit `broadcast_config_nvidia_nodrawtext.json`:

```json
{
  "max_restarts": 999999,
  "health_timeout_seconds": 15,
  "restart_delay_seconds": 1,
  "log_file": "broadcast.log",
  
  "enable_local_recording": false,
  "recording_path": "./recordings",
  "segment_duration_seconds": 3600,
  "max_recording_days": 7,
  
  "ffmpeg_args": [
    "-re",
    "-thread_queue_size", "1024",
    "-i", "rtmp://localhost/live/stream",
    "-thread_queue_size", "512",
    "-i", "rtsp://USER:PASS@CAMERA_IP:554/Streaming/Channels/202",
    "-stream_loop", "-1",
    "-i", "track.aac",
    "-filter_complex", "[1:a]volume=0.5[a1];[2:a]volume=0.09[a2];[a1][a2]amix=inputs=2:duration=first:normalize=0[aout]",
    "-map", "0:v:0", "-map", "[aout]",
    "-fflags", "+genpts+discardcorrupt+flush_packets",
    "-r", "30", "-g", "60", "-keyint_min", "60",
    "-vf", "scale=2560:1440",
    "-c:v", "h264_nvenc", "-preset", "p7", "-profile:v", "high",
    "-b:v", "16000k", "-maxrate", "18000k", "-bufsize", "2G",
    "-rc", "vbr", "-cq", "23", "-spatial_aq", "1", "-temporal_aq", "1",
    "-pix_fmt", "yuv420p",
    "-c:a", "aac", "-b:a", "128k", "-ac", "2", "-ar", "48000",
    "-f", "flv", "rtmp://a.rtmp.youtube.com/live2/YOUR_STREAM_KEY"
  ]
}
```

**Note:** Replace `USER`, `PASS`, `CAMERA_IP`, and `YOUR_STREAM_KEY` with your actual values.

---

## Systemd Services

### NOLO Service

```bash
cat << 'EOF' | sudo tee /etc/systemd/system/nolo.service
[Unit]
Description=NOLO Camera Tracking System
After=network.target srs.service

[Service]
Type=simple
Environment="LD_LIBRARY_PATH=/usr/local/lib:/usr/local/cuda/lib64"
Environment="PATH=/usr/local/bin:/usr/local/go/bin:/usr/local/cuda/bin:/usr/bin:/bin"
WorkingDirectory=/home/YOUR_USER/NOLO
ExecStart=/home/YOUR_USER/NOLO/NOLO -input "rtsp://USER:PASS@CAMERA_IP:554/Streaming/Channels/201" -ptzinput "http://USER:PASS@CAMERA_IP:80/" -max-pan=2550 -min-pan=900 -max-zoom=120 -min-zoom=10 -debug
User=YOUR_USER
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
```

### Broadcast Service

```bash
cat << 'EOF' | sudo tee /etc/systemd/system/nolo-broadcast.service
[Unit]
Description=NOLO YouTube Broadcast
After=network.target srs.service nolo.service

[Service]
Type=simple
Environment="LD_LIBRARY_PATH=/usr/local/lib:/usr/local/cuda/lib64"
Environment="PATH=/usr/local/bin:/usr/local/go/bin:/usr/local/cuda/bin:/usr/bin:/bin"
WorkingDirectory=/home/YOUR_USER/NOLO/broadcast
ExecStart=/home/YOUR_USER/NOLO/broadcast/broadcast -c broadcast_config_nvidia_nodrawtext.json
User=YOUR_USER
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
```

### Enable and Start Services

```bash
sudo systemctl daemon-reload
sudo systemctl enable srs nolo nolo-broadcast
sudo systemctl start srs nolo nolo-broadcast
```

### Service Management Commands

```bash
# Check status
sudo systemctl status srs nolo nolo-broadcast

# View logs
journalctl -u nolo -f
journalctl -u nolo-broadcast -f

# Restart services
sudo systemctl restart nolo
sudo systemctl restart nolo-broadcast

# Stop everything
sudo systemctl stop nolo-broadcast nolo srs
```

---

## Troubleshooting

### GPU Not Detected

```bash
# Check if GPU is visible
lspci | grep -i nvidia

# Check driver status
nvidia-smi

# If "Key was rejected by service" error, disable Secure Boot
mokutil --sb-state
```

### FFmpeg NVENC Errors

```bash
# Verify NVENC is available
ffmpeg -encoders 2>/dev/null | grep nvenc

# Check CUDA architecture
nvidia-smi --query-gpu=compute_cap --format=csv,noheader

# Common error: "Unsupported gpu architecture"
# Solution: Rebuild FFmpeg with correct --nvccflags for your GPU
```

### OpenCV CUDA Not Working

```bash
# Check if OpenCV was built with CUDA
pkg-config --libs opencv4 | grep cuda

# Verify CUDA libraries are found
ldd /usr/local/lib/libopencv_cudaarithm.so

# If missing libraries, run:
sudo ldconfig
```

### GoCV Build Errors

```bash
# Ensure pkg-config finds opencv4
pkg-config --cflags opencv4
pkg-config --libs opencv4

# Clear Go build cache
go clean -cache

# Rebuild with explicit flags
export CGO_CFLAGS="$(pkg-config --cflags opencv4)"
export CGO_LDFLAGS="$(pkg-config --libs opencv4)"
go build .
```

### SRS Not Starting

```bash
# Check if port 1935 is in use
sudo netstat -tlnp | grep 1935

# Check SRS logs
journalctl -u srs -n 50

# Common issue: file descriptor limits
# Solution: LimitNOFILE=100000 in systemd service
```

### RTSP Connection Issues

```bash
# Test RTSP stream directly
ffprobe "rtsp://USER:PASS@CAMERA_IP:554/Streaming/Channels/201"

# Check network connectivity
ping CAMERA_IP

# Verify camera credentials via web interface
```

---

## Quick Reference

### Environment Setup Script

```bash
# Save as /etc/profile.d/nolo-env.sh
export PATH=/usr/local/go/bin:/usr/local/cuda/bin:/usr/local/bin:$PATH
export LD_LIBRARY_PATH=/usr/local/lib:/usr/local/cuda/lib64:$LD_LIBRARY_PATH
export PKG_CONFIG_PATH=/usr/local/lib/pkgconfig:$PKG_CONFIG_PATH
export CGO_CFLAGS="$(pkg-config --cflags opencv4)"
export CGO_LDFLAGS="$(pkg-config --libs opencv4)"
export CGO_CXXFLAGS="-std=c++11"
```

### Architecture Overview

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│   PTZ Camera    │────▶│      NOLO       │────▶│   SRS Server    │
│   (RTSP/PTZ)    │     │  (AI Tracking)  │     │  (RTMP Relay)   │
└─────────────────┘     └─────────────────┘     └────────┬────────┘
                                                         │
                        ┌─────────────────┐              │
                        │    Broadcast    │◀─────────────┘
                        │  (Audio Mix +   │
                        │   Re-encode)    │
                        └────────┬────────┘
                                 │
                        ┌────────▼────────┐
                        │    YouTube      │
                        │   Live Stream   │
                        └─────────────────┘
```

### GPU Compute Capabilities

| GPU | Compute Capability |
|-----|-------------------|
| RTX 4090/4080 | 8.9 |
| RTX 3090/3080 | 8.6 |
| RTX 3070/3060 | 8.6 |
| RTX 5050 | 12.0 |
| Tesla T4 | 7.5 |

Use the appropriate value for `CUDA_ARCH_BIN` and `--nvccflags`.
