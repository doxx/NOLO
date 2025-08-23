<div align="center">
  <img src="img/NOLO.png" alt="NOLO Logo" width="400"/>
</div>

# NOLOcam - Never Only Look Once 

**Advanced AI-powered PTZ camera tracking system with flexible object priorities**

NOLOcam is a sophisticated Go-based system for tracking and monitoring objects using PTZ cameras with YOLO object detection. Unlike YOLO's "You Only Look Once" approach, NOLOcam continuously tracks objects with spatial awareness and predictive capabilities - hence "Never Only Look Once".

TLDR: Dude hooked up AI to a PTZ camera and it works. 

See it in action here: https://www.youtube.com/@MiamiRiverCamera/streams

## The Philosophy Behind AI-Integrated Systems

Why build this software? While everyone uses AI to enable code, I'm using code to *enable* AI. This project began as an experiment to test a fundamental thesis: **AI integration isn't just coming to everything—it's inevitable, and I think the most powerful implementations will be hybrid systems that blend local and cloud intelligence.**

### The Ubiquitous AI Future

This camera system represents a microcosm of where I believe all technology is heading. We're transitioning from AI as a separate tool to AI as an integrated nervous system within traditional hardware. Consider the implications:

**Today**: A security camera records video that humans review later, maybe in some conditions has movement or basic object detection on cameras that do not move. Other cases uses offload to a cloud service to do any sort of AI processing.   
**Tomorrow**: Devices that *think*, track, analyze, and make decisions in real-time while seamlessly integrating with cloud-based reasoning systems. It's going to happen everywhere, children's toys, cars, and even the kitchen. 

But this extends far beyond cameras. Imagine:
- **Smart kitchens** where sensor-aware cookware communicates thermal dynamics, moisture levels, and chemical changes to AI models embedded in ovens and ranges, automatically adjusting cooking parameters in real-time
- **Intelligent manufacturing** where every tool, sensor, and component participates in a distributed AI network, predicting failures, optimizing processes, and self-correcting
- **Responsive infrastructure** where bridges, roads, and buildings continuously monitor their structural health and environmental conditions, making micro-adjustments and predictive maintenance decisions
- **Chidrend's Toys** Imagine speaking to your childhood best friend Teddy Ruxpin and learning math from a talking fish? It's going to happen. 

### The Local vs. Cloud Intelligence 

Pure cloud-based AI processing is fundamentally flawed for real-time applications:

**The Math Doesn't Work**: Processing 30fps video at broadcast quality would require constant cloud API calls. Even with perfect connectivity, you're looking at:
- Latency that kills real-time responsiveness instnatly. Dead in the water.
- API costs that could easily reach $$$$$$$$$ for continuous operation

The same math issue exists in cyber security detections and monitoring or really any sort of real-time streaming dataset. 

**The Local Reality**: A modest gaming laptop with an NVIDIA GeForce RTX 3070 sits 80% idle while effortlessly processing this high-bandwidth video stream. The computational power already exists at the edge. However, I am not running any fully advanced model on this machine as of now.

### The Hybrid Architecture 

This led to the system's hybrid design: local AI handles the high-frequency, low-latency tasks (object detection, tracking, movement control), while cloud AI provides deep reasoning and analysis for complex decision-making. This isn't just more efficient—it's more intelligent.

**Local AI handles**: Frame-by-frame analysis, real-time tracking, immediate responses  
**Cloud AI handles**: Complex scene interpretation, behavioral analysis, strategic decision-making

This hybrid model represents the future across domains:
- **Cybersecurity**: Local models detecting anomalies in microseconds, cloud models providing sophisticated threat analysis and summarization. 
- **Autonomous vehicles**: Local processing for immediate hazard response, cloud intelligence for route optimization and traffic pattern learning  
- **Medical devices**: Local monitoring for critical vital signs, cloud analysis for diagnostic insights and treatment recommendations

### The Shift to AI Architecture

We're witnessing a fundamental transformation in how intelligence is distributed through our technological ecosystem. Rather than centralizing AI in distant data centers, we're creating a web of interconnected intelligence that spans from the smallest embedded processors to the largest cloud infrastructures and overlapping APIs.

This camera system is proof of concept for a world where every device isn't just connected—it's *intelligent*. Where the boundary between hardware and software dissolves into responsive, adaptive systems that learn, predict, and evolve.

We're going to wire up AI to things that have controls and it's going to be very interesting to watch "things" become more than just "things". 

The question isn't whether AI will be embedded in everything. The question is whether we'll build these systems thoughtfully, with proper attention to latency, cost, privacy, and resilience. This project suggests we can—and we must. 


## Personal Story

I haven't set up a webcam in decades, and I decided to set up something basic. All these Chinese branded cameras advertise AI and auto tracking, so after buying and returning about 5 of them I realized that was all made up marketing scammy stuff. These little cameras do not have the processing power to do any of that. Then, you have to offload your images or video to a cloud service, and that is not free, and it's really bad, and not secure. So, I thought, hey all of the parts and pieces are here. I have a GPU on a laptop, Linux, and I did a little more research and found a PTZ camera that I could get status messages from, which is very important, to have absolute values and to know where the camera is positioned.

Publishing was to YouTube which also has its own quirks. I started with an AI narrator that was hooked into the stream via ChatGPT gpt-4o model and could narrate what the camera sees at the same time as the AI tracking is running. This system is included in the packages and is very useful because it could also be used for many many other deep analysis tasks such as object classification, animal descriptions (what type of panther or boat is this, etc). All in all it is a challenging process.

I'm pretty sure this software can be flexible enough to monitor less complicated areas such as the Miami River. I'm sure it can run well for a lot of applications, so I am interested to see what people come up with and how they use the software.


## Cameras Tested

I tried to use the cheapest off-the-shelf PTZ camera I could find, and it was a disaster. 

Most of them support RTSP in different ways that don't make sense. Their APIs are flaky and not reliable. 

Major issues: Zoom speed, digital zoom only via apps, very bad auto focus, RTSP relaiblity, API crasehs, etc etc

I finally settled on a Hikvision ColorVu AcuSense DS-2DE7A412MCG-EB 4MP Outdoor PTZ camera. The API is pretty good, the RTSP stream is rock solid, quality is good, and the PTZ control provides absolute position status. All these features allow it be wired into AI pretty smoothly.  

Failures: 
- Fosscam PTZ (Almost worked but slow focus and lack of API position data killed it)
- SUNBA PTZ (Bad for this type of thing)
- Reolink PTZ (Very bad for this type of project)

## Important Ethical Considerations Regarding Hikvision

While this project uses Hikvision cameras for technical demonstration purposes, it's essential to acknowledge the serious ethical concerns surrounding this manufacturer. Hangzhou Hikvision Digital Technology Co., Ltd. is a Chinese state-owned surveillance equipment company that has been [extensively documented](https://uhrp.org/report/hikvisions-links-to-human-rights-abuses-in-east-turkistan/) as being directly complicit in human rights violations.

The [Uyghur Human Rights Project](https://uhrp.org/statement/hikvision-and-dahua-facilitating-genocidal-crimes-in-east-turkistan/) has documented that Hikvision has secured lucrative contracts with Chinese authorities to supply, develop, and directly operate mass surveillance systems specifically targeting Uyghur and other ethnic minorities in East Turkistan (Xinjiang). These systems have been installed in and around internment camps, mosques, and schools across the region. After working with the camera and AI models, I can see how a specificlly traned model could be used to target just about anything you want. So, tehnically this is possible.

Investigations by [IPVM](https://ipvm.com/reports/hikvision-uyghur-nvidia) have revealed that Hikvision developed facial recognition technology specifically designed to identify ethnic minorities, including what internal documents described as "Uyghur detection" capabilities. The company's technology is integral to China's Integrated Joint Operations Platform (IJOP), a predictive policing system used for mass surveillance and targeting of ethnic minorities.

The international community has responded with comprehensive sanctions. The United States has placed Hikvision on multiple sanctions lists, including the [Entity List](https://www.opensanctions.org/entities/NK-HYQpJVFfjnrATqKwpZpit7/) (2019), [Chinese Military Companies list](https://sanctionssearch.ofac.treas.gov/Details.aspx?id=30946) (2021), and banned their equipment from federal facilities under the [National Defense Authorization Act](https://www.congress.gov/115/plaws/publ232/PLAW-115publ232.pdf). The UK, Australia, and several European countries have also implemented bans or restrictions on Hikvision equipment in government facilities.

While Hikvision cameras were selected for this project purely for their technical capabilities and API compatibility, I want to be transparent about these ethical concerns. The robustness and reliability that makes these cameras effective for this wildlife monitoring project are the same qualities that unfortunately make them effective tools for authoritarian surveillance. As this project evolves, I'm actively seeking partnerships with ethical camera manufacturers like Sony, Panasonic, or Bosch to migrate away from Hikvision hardware. If you represent an alternative camera manufacturer and would like to collaborate on integrating your APIs with this system, please reach out.

## Performance Notes

The camera pipeline is astoundingly fast, capture is 33ms which at 30fps is 33ms, so it is not a bottleneck. >1 frame per millisecond.

```
Capture: 30.1 fps (Read: 33.312905ms)
```

Process application can run estimated at 60fps with 12-13ms in YOLO and the tracking code and overlay is sub millisecond.

```
Process: 60.0 fps (YOLO: 12.728951ms, Track: 15.102µs)
```

Write is 1ms which is 1000fps, so it is not a bottleneck.

```
Write:   30.0 fps (Write: 1.067839ms)
```

I had to create some buffer systems to handle the pipeline, and it seems to be working great. There was a lot of tricky nature to memory leaks and frame ordering that had to be dealt with. Kind of drove me crazy for a bit.

```
Buffers: FrameChan 1/120 (0.8%) | WriteQueue 0/120 (0.0%) | PendingFrames 1/120 (0.8%)
```

## Technical Challenges Overcome

### Letterboxing Confusion

The most challenging bug was a subtle aspect ratio issue that took forever to debug. The camera produces 2688×1520 frames (1.768:1 aspect ratio), but YOLO expects square 832×832 input (1:1 aspect ratio). This mismatch caused detection boxes to appear in completely wrong locations.

**The Initial Confusion: "Crop" vs "Letterbox"**

I initially thought OpenCV's `BlobFromImage` crop parameter would handle this properly:

```go
// What we THOUGHT would work (it doesn't):
blob := gocv.BlobFromImage(frame, 1.0/255.0, image.Pt(832, 832), 
    gocv.NewScalar(0, 0, 0, 0), true, false)  // crop=false
```

The `crop=false` parameter was supposed to add letterbox padding, but it actually just **stretches** the image to fit 832×832, destroying the aspect ratio. YOLO would see a distorted, horizontally-compressed image and return bounding boxes for the stretched coordinate space.

**The Solution: Manual Letterboxing**

I implemented proper letterboxing by hand:

```go
// STEP 1: Calculate letterbox dimensions
originalWidth := 2688.0   // Camera resolution
originalHeight := 1520.0  // Camera resolution  
aspectRatio := 1.768      // 2688/1520
contentHeight := 470      // 832/1.768 (preserves aspect ratio)
yOffset := 181           // (832-470)/2 (black bars on top/bottom)

// STEP 2: Create 832×832 black canvas
letterboxed := gocv.NewMatWithSize(832, 832, gocv.MatTypeCV8UC3)
letterboxed.SetTo(gocv.NewScalar(0, 0, 0, 0)) // Fill black

// STEP 3: Resize frame to 832×470 (preserves aspect ratio)  
gocv.Resize(frame, &resized, image.Pt(832, 470), 0, 0, gocv.InterpolationLinear)

// STEP 4: Copy resized content to center of canvas (Y offset = 181px)
contentROI := letterboxed.Region(image.Rect(0, 181, 832, 651))
resized.CopyTo(&contentROI)

// STEP 5: Now we can safely use crop=true since image is pre-letterboxed
blob := gocv.BlobFromImage(letterboxed, 1.0/255.0, image.Pt(832, 832), 
    gocv.NewScalar(0, 0, 0, 0), true, true)  // crop=true is safe now
```

**Coordinate Transformation Magic**

The real complexity is transforming YOLO's detected bounding boxes back to original camera coordinates:

```
Camera Frame:    2688×1520 (landscape)
      ┌─────────────────────────┐
      │                         │
      │      Camera View        │ 1520px
      │                         │  
      └─────────────────────────┘
             2688px

YOLO Input:      832×832 (square with letterbox)
      ┌─────────────────────────┐
      │     BLACK BAR (181px)   │ ← yOffset
      ├─────────────────────────┤
      │                         │
      │   Content Area (470px)  │ ← contentHeight  
      │                         │
      ├─────────────────────────┤
      │     BLACK BAR (181px)   │ ← yOffset
      └─────────────────────────┘
             832px

Detection Transform:
1. YOLO returns: (xNorm, yNorm, wNorm, hNorm) in 0.0-1.0 range
2. Convert to 832×832 space: yPixel832 = yNorm * 832  
3. Remove letterbox offset: yContentPixel = yPixel832 - 181
4. Scale to camera coords: finalY = yContentPixel * (1520/470)
```

<div align="center">
  <img src="img/yolo_example.jpg" alt="YOLO Example width="400"/>

</div>

This coordinate transformation ensures that a boat detected at the bottom of the letterboxed YOLO input correctly maps to the bottom of the original camera frame, not somewhere in the middle due to the black bars.

**Why This Was So Confusing**

The `crop` parameter in OpenCV's `BlobFromImage` is misleadingly named. We thought `crop=false` meant "add letterbox padding," but it actually means "stretch to fit." The GPU calls weren't creating letterboxing - they were creating stretched, distorted images that made YOLO's coordinate system completely wrong.


### FFmpeg Build Challenges

FFmpeg also was a bit of a pain in the ass due to the apt and brew versions being so old and out of date, I had to build it from source. Which I still have yet to get drawtext working in 7.x. So there went the narrator AI feed, but I was not sure if that added a lot of value or not.

### Streaming Infrastructure Migration

Another issue was using nginx to publish the stream to YouTube, I had to use the -async 1 option to get it to work, but it was not working well, and I had to use the -vsync passthrough option to get it to work. At the end I decided nginx was terrible at RTMP and I switched the program over to SRS which is much better at handling this type of stuff and feed management.

I still have an issue with VLC attaching to the RTMP feed and not getting some sort of ordering issue, I suspect someplace in my pipeline there's a frame reordering issue, where frame 10 goes before frame 9 or something like that to trigger a reordering issue that makes VLC freak out. I'm not sure if this is a problem with VLC or my pipeline.

### GPU Performance Advantages
Moving to the GPU off the CPU was a huge win, however that came with its own issues with NVIDIA and Apple Silicon. It's clear that NVIDIA is so much more supported on Linux than open source tools like ffmpeg being supported on Apple Silicon even on native Apple macOS. For example, on "slower" encode (which means the best) on Apple Silicon we could never really reach 1x 1:1 real-time speed, where on NVIDIA after some of its own internal workings does reach 1:1 frame rate at real-time. Which is important for publishing to YouTube and to keep the stream looking good.

I also did not take the time to use on/off movements which are much smoother than the absolute ones, and I really don't have the time to do that right now, also I could not get the Hikvision camera to manage speed dynamically because of the API doing some strange shit. If anyone at Hikvision can help, please shoot me a note.

## 🏗️ NOLO Pipeline Architecture

### Main Processing Pipeline

```
┌─────────────────┐    ┌──────────────┐    ┌─────────────────┐    ┌──────────────┐
│ RTSP Camera     │───▶│ captureFrames│───▶│   frameChan     │───▶│ writeFrames  │
│ (30fps 1440p)   │    │  Goroutine   │    │  (120 buffer)   │    │  Goroutine   │
└─────────────────┘    └──────────────┘    └─────────────────┘    └──────────────┘
                              │                                           │
                              ▼                                           ▼
                        ┌─────────────┐                            ┌─────────────┐
                        │ Frame Valid │                            │ YOLO Detect │
                        │ 8UC3 Check  │                            │ & Process   │
                        └─────────────┘                            └─────────────┘
                                                                          │
                                                                          ▼
┌─────────────────┐    ┌─────────────┐    ┌─────────────────┐    ┌─────────────┐
│ FFmpeg Output   │◀───│ Overlay     │◀───│ Spatial Track   │◀───│ P1/P2 Filter│
│ (RTMP Stream)   │    │ Rendering   │    │ Integration     │    │ & Classify  │
└─────────────────┘    └─────────────┘    └─────────────────┘    └─────────────┘
```

### Detailed Frame Processing Flow

```
┌─────────────────┐
│ Raw Frame (Mat) │
└─────────┬───────┘
          │
          ▼
┌─────────────────┐    NO   ┌─────────────┐
│ Frame Valid?    │────────▶│ Drop Frame  │
│ (8UC3, 3-chan)  │         └─────────────┘
└─────────┬───────┘
          │ YES
          ▼                        ┌─────────────────┐
┌─────────────────┐                │ Pre-Overlay     │◀─── -pre-overlay-jpg
│ Clone for       │                │ JPEG Save       │     (LOCK state only)
│ Processing      │                │ timestamp_pre-  │
└─────────┬───────┘                │ overlay_det_N   │
          │                        └─────────────────┘
          ▼
┌─────────────────┐
│ Status Overlay  │◀──── -status-overlay flag
│ (FPS, Mode,     │
│  Time, etc.)    │
└─────────┬───────┘
          │
          ▼
┌─────────────────┐        ┌─────────────────┐
│ YOLO Blob       │        │ 4:3 Aspect      │
│ Creation        │───────▶│ Ratio Fix       │
│ (640x640)       │        │ (Black Borders) │
└─────────────────┘        └─────────────────┘
          │
          ▼
┌─────────────────┐        ┌─────────────────┐
│ net.SetInput()  │        │ CUDA/CPU        │
│ net.Forward()   │───────▶│ Acceleration    │
│ (12-13ms)       │        │ Auto-Detect     │
└─────────┬───────┘        └─────────────────┘
          │
          ▼
┌─────────────────┐        ┌─────────────────┐
│ P1/P2 Class     │  P1    │ Always Valid    │
│ Filtering       │───────▶│ (boat, kayak)   │
│ (isP1/isP2Obj)  │        └─────────────────┘
└─────────┬───────┘
          │ P2              ┌─────────────────┐
          └────────────────▶│ Valid Only in   │
                            │ Tracking Mode   │
                            │ (person, etc.)  │
                            └─────────────────┘
          │
          ▼
┌─────────────────┐
│ Confidence      │
│ Threshold       │
│ (>0.3 P1,       │
│  >0.5 P2)       │
└─────────┬───────┘
          │
          ▼
┌─────────────────────────────────────────────────┐
│            SPATIAL INTEGRATION                  │
│  ┌─────────────┐  ┌──────────────────────────┐  │
│  │updateAll    │  │detectP2ObjectsInP1Targets│  │
│  │Boats()      │  │(Enhancement Detection)   │  │
│  └─────────────┘  └──────────────────────────┘  │
│           │                   │                 │
│           ▼                   ▼                 │
│  ┌─────────────┐  ┌──────────────────────────┐  │
│  │cleanupLost  │  │feedLockedBoatsWithCluster│  │
│  │Boats()      │  │Detections()              │  │
│  └─────────────┘  └──────────────────────────┘  │
│           │                   │                 │
│           └──────┬────────────┘                 │
│                  ▼                              │
│          ┌─────────────┐                        │
│          │selectTarget │                        │ 
│          │Boat()       │                        │
│          └─────────────┘                        │
└─────────────────┬───────────────────────────────┘
                  │
                  ▼
┌─────────────────────────────────────────────────┐
│              PTZ CONTROL                        │
│  ┌─────────────┐  ┌──────────────────────────┐  │
│  │Camera State │  │Rate Limiting             │  │
│  │Manager      │  │(Command Timing)          │  │
│  └─────────────┘  └──────────────────────────┘  │
│           │                   │                 │
│           ▼                   ▼                 │
│  ┌─────────────┐  ┌──────────────────────────┐  │
│  │Position     │  │Hikvision HTTP API        │  │
│  │Validation   │  │(XML Commands)            │  │
│  │& Clamping   │  │                          │  │
│  └─────────────┘  └──────────────────────────┘  │
└─────────────────┬───────────────────────────────┘
                  │
                  ▼
┌──────────────────────────────────────────────────┐
│              OVERLAY RENDERING                   │
│  ┌─────────────┐  ┌──────────────────────────┐   │
│  │Target Box   │  │P2 Enhancement Indicators │   │
│  │Drawing      │  │(Person icons, etc.)      │   │
│  └─────────────┘  └──────────────────────────┘   │
│           │                   │                  │
│           ▼                   ▼                  │
│  ┌─────────────┐  ┌──────────────────────────┐   │
│  │PIP Zoom     │  │Direction Arrows          │   │
│  │Display      │  │(Predictive Tracking)     │   │
│  └─────────────┘  └──────────────────────────┘   │
└─────────────────┬────────────────────────────────┘
                  │
                  ▼                      ┌─────────────────┐
┌─────────────────┐                     │ Post-Overlay     │◀─── -post-overlay-jpg
│ Final Frame     │────────────────────▶│ JPEG Save        │     (LOCK state only)
│ (with overlays) │                     │ timestamp_post-  │     to organized dirs
└─────────┬───────┘                     │ overlay_det_N    │     /path/YYYY-MM-DD_HHPM/
          │                             └──────────────────┘
          ▼
┌─────────────────┐
│ FFmpeg Write    │
│ (1ms latency)   │
└─────────┬───────┘
          │
          ▼
┌─────────────────┐
│ RTMP Stream     │
│ (YouTube, SRS)  │
└─────────────────┘
```

### Priority Tracking System (P1/P2)

```
P1 OBJECTS (Primary Tracking Targets)
┌─────────────────────────────────────────────────────────────┐
│ boat, kayak, surfboard, etc. (-p1-track="boat,kayak")       │
└──────────────────────┬──────────────────────────────────────┘
                       │
                       ▼
              ┌─────────────────┐    ┌─────────────────┐
              │ Always Tracked  │───▶│ Can Achieve     │
              │ (conf > 0.3)    │    │ LOCK Status     │
              └─────────────────┘    └─────────────────┘
                       │                       │
                       ▼                       ▼
              ┌─────────────────┐    ┌─────────────────┐
              │ Builds History  │    │ Camera Follows  │
              │ (2+ = LOCK)     │    │ (PTZ Control)   │
              │ (24+ = SUPER)   │    │                 │
              └─────────────────┘    └─────────────────┘

P2 OBJECTS (Enhancement Objects)
┌─────────────────────────────────────────────────────────────┐
│ person, backpack, bottle, etc. (-p2-track="person,all")     │
└──────────────────────┬──────────────────────────────────────┘
                       │
                       ▼
              ┌─────────────────┐    ┌─────────────────┐
              │ Only During     │───▶│ Only INSIDE     │
              │ Tracking Mode   │    │ Locked P1 Boxes │
              │ (conf > 0.5)    │    │                 │
              └─────────────────┘    └─────────────────┘
                       │                       │
                       ▼                       ▼
              ┌─────────────────┐    ┌─────────────────┐
              │ Priority Bonus  │    │ Enhanced        │
              │ (+0.7 score)    │    │ Targeting       │
              │                 │    │ (Centroid)      │
              └─────────────────┘    └─────────────────┘

LOCK PROGRESSION
┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│ BUILDING    │───▶│    LOCK     │───▶│ SUPER LOCK  │
│ (0-1 det)   │    │ (2-23 det)  │    │ (24+ det)   │
│             │    │             │    │             │
│ No PTZ      │    │ PTZ Follow  │    │ Enhanced    │
│ No PIP      │    │ PIP Enabled │    │ PIP Zoom    │
│             │    │             │    │ Predictive  │
└─────────────┘    └─────────────┘    └─────────────┘
```

### Memory Management & Threading

```
┌─────────────────────────────────────────────────────────────┐
│                    GOROUTINE STRUCTURE                      │
└─────────────────────────────────────────────────────────────┘

Main Thread
    │
    ├─── captureFrames() Goroutine
    │    │
    │    ├─── webcam.Read() ────┐
    │    ├─── Frame Validation  │
    │    └─── frameChan <- data ─┼─── 120 Buffer Channel
    │                           │
    │                           │
    └─── writeFrames() Goroutine◀┘
         │
         ├─── YOLO Processing (12-13ms)
         ├─── Spatial Tracking (<1ms)
         ├─── Overlay Rendering (<1ms)
         └─── FFmpeg Write (1ms)

┌─────────────────────────────────────────────────────────────┐
│                   MEMORY TRACKING                           │
└─────────────────────────────────────────────────────────────┘

Mat Allocation Points:
├─── trackMatAlloc("capture")    - Frame capture
├─── trackMatAlloc("buffer")     - Frame cloning
├─── trackMatAlloc("yolo")       - YOLO blob & output
└─── trackMatClose() calls       - Explicit cleanup

Buffer Management:
├─── frameChan: 120 frames max
├─── writeQueue: 120 frames max  
├─── pendingFrames: 120 max
└─── Drop policy: Oldest first
```

## 🎯 The Technical Challenge: Why PTZ AI is Exponentially Harder

### **Fixed Camera vs. PTZ: A Complexity Analysis**

Most computer vision projects you see online use **fixed cameras** - a single static view where YOLO detects objects in a consistent scene. This is relatively straightforward: train on one perspective, optimize for one lighting condition, done.

**This project tackles something exponentially more complex: real-time AI on a fully articulated PTZ camera system.**

### **The Mathematics of PTZ Complexity**

Our camera system operates across:
- **Pan**: 0-3599 positions (3,600 unique angles)
- **Tilt**: 0-900 positions (901 unique angles)  
- **Zoom**: 10-120 levels (111 unique magnifications)

**Total unique camera positions: 3,600 × 901 × 111 = 360,360,360**

At 2600×1426 resolution, this creates **~1.34 quadrillion unique pixel-position combinations** that the system must understand spatially.

### **Why This Matters: The Real Challenges**

**Fixed Camera Approach:**
- ✅ Single scene, consistent perspective
- ✅ Static object relationships  
- ✅ Simple "digital zoom" on existing pixels
- ✅ One-time calibration

**PTZ Camera Reality (This Project):**
- 🔥 **360+ million different scenes** based on camera position
- 🔥 **Dynamic spatial relationships** - same object appears different at each zoom/angle
- 🔥 **Real-time coordinate transformation** between pixel space and physical PTZ coordinates
- 🔥 **Continuous recalibration** as camera moves through 3D space
- 🔥 **Predictive tracking** across position transitions
- 🔥 **Multi-scale object detection** - boats look different at 1x vs 12x zoom

### **What Makes This Implementation Unique**

While most PTZ projects are simple "move camera toward detected object" systems, this project solves the **true spatial intelligence problem**:

1. **Spatial Coordinate System**: Translates between pixel coordinates and real-world PTZ positions with mathematical precision
2. **Multi-Scale Intelligence**: Maintains object identity across zoom levels (a boat at 1x zoom vs 12x zoom)
3. **Predictive Movement**: Anticipates where objects will be based on motion vectors
4. **Production-Grade Engineering**: Handles the 360+ million position combinations reliably

**Hopefully result**: A system that doesn't just "follow objects" but truly **understands 3D space** through a moving camera. It's really not working exactly how I want it but it's a foundation for some new survalance systems. 

---

Okay on to the software... 


## 🚀 Key Features

### **Flexible Tracking Priority System**
- **P1 Objects** (Primary): Main tracking targets that can achieve LOCK status
- **P2 Objects** (Enhancement): Objects detected inside locked P1 targets for enhanced tracking
- **Configurable at runtime** with `-p1-track` and `-p2-track` flags

### **Advanced Tracking Capabilities**
- **Spatial Tracking**: Real-world coordinate tracking with camera position awareness
- **Predictive Tracking**: Anticipates object movement when temporarily lost
- **LOCK/SUPER LOCK Modes**: Progressive tracking confidence levels (2+ → 24+ detections)
- **Picture-in-Picture (PIP)**: Automatic zoom on locked targets with P2 objects
- **Recovery Mode**: Smart re-acquisition of lost targets

### **PTZ Camera Control**
- **Smart Camera Movement**: Smooth tracking with velocity compensation
- **Scanning Patterns**: Customizable area scanning when no targets detected
- **Position Limits**: Safety boundaries to prevent mechanical damage
- **State Management**: Coordinated camera commands with latency compensation

### **Real-time Performance**
- **YOLO/ONNX Integration**: Local AI inference (no API calls)
- **GPU Acceleration**: CUDA support for enhanced performance
- **Multi-threaded Pipeline**: Capture → Process → Track → Control
- **Memory Management**: Efficient OpenCV Mat handling with leak detection

### **Debug & Analysis Features**
- **Verbose Debug Mode**: `-debug-verbose` for detailed calculations
- **JPEG Frame Saving**: Auto-organized by date/hour (`/path/2025-01-01_03PM/`)
- **Granular Overlay Control**: Enable/disable specific overlay elements
- **Performance Monitoring**: Real-time FPS and latency tracking

## 🏆 What Sets This Apart from Other AI Camera Projects

### **Beyond Typical Computer Vision Demos**

Most AI camera tracking projects online are **proof-of-concept Python scripts** that:
- Run basic YOLO detection on static cameras
- Use simple "move camera toward object" logic
- Lack production reliability and error handling
- Process 1-5 FPS with basic tracking

**This project is production-grade software** that delivers:

### **Enterprise-Level Architecture**
- **Go-based implementation**: Higher performance and concurrency than Python alternatives
- **Real-time processing**: 30 FPS with sub-100ms latency
- **Comprehensive error recovery**: Handles camera disconnects, network issues, and tracking failures
- **Production monitoring**: Memory leak detection, performance metrics, debug infrastructure

### **True Spatial Intelligence**
- **Coordinate transformation mathematics**: Pixel→PTZ position mapping with calibration
- **Multi-scale object persistence**: Maintains identity across 1x→12x zoom transitions  
- **Predictive tracking algorithms**: Kalman filters and spatial integration
- **State machine progression**: SEARCH→TRACK→LOCK→SUPER LOCK modes

### **Hybrid AI Architecture (The Future)**
- **Local real-time AI**: YOLO inference for immediate responses
- **Cloud reasoning integration**: Ready for GPT-4V scene analysis 
- **Cost-optimized**: Avoids $200k+ monthly API costs of pure cloud solutions
- **Edge-to-cloud intelligence**: Best of both worlds

### **Real-World Deployment Ready**
- **Runtime configuration**: No code changes needed for different environments
- **Safety systems**: Hardware position limits and emergency stops
- **Industrial reliability**: Designed for 24/7 operation
- **Scalable architecture**: Multi-camera network support foundation

**The bottom line**: While others build demos, this project solves the **real engineering challenges** of autonomous camera systems - the foundation for next-generation surveillance, security, and monitoring applications.

## 🎛️ Configuration Options

### **Complete Command-Line Reference**

Run `./NOLO -h` to see all available options:

```bash
Usage of ./NOLO:
  -YOLOdebug
        Save YOLO input blob images to /tmp/YOLOdebug/ for analysis
  -debug
        Enable debug mode with overlay and detailed tracking logs
  -debug-verbose
        Enable verbose debug output (includes detailed YOLO, calibration, and tracking calculations)
  -exit-on-first-track
        Exit after first successful target lock (useful for debugging single track sessions)
  -input string
        RTSP input stream URL (required)
                        Example: rtsp://admin:password@192.168.1.100:554/Streaming/Channels/201
  -jpg-path string
        Directory path for saving JPEG frames (required when using JPEG flags)
  -maskcolors string
        Comma-separated hex colors to mask out (e.g., 6d9755,243314)
  -masktolerance int
        Color tolerance for masking (0-255, default: 50) (default 50)
  -max-pan float
        Maximum pan position in camera units (omit flag for hardware maximum)
                        Example: -max-pan=3000 prevents panning right of position 3000 (default -1)
  -max-tilt float
        Maximum tilt position in camera units (omit flag for hardware maximum)
                        Example: -max-tilt=900 prevents tilting too high (default -1)
  -max-zoom float
        Maximum zoom level in camera units (omit flag for hardware maximum)
                        Example: -max-zoom=120 prevents zooming above 12x (default -1)
  -min-pan float
        Minimum pan position in camera units (omit flag for hardware minimum)
                        Example: -min-pan=1000 prevents panning left of position 1000 (default -1)
  -min-tilt float
        Minimum tilt position in camera units (omit flag for hardware minimum)
                        Example: -min-tilt=0 prevents tilting below horizon (default -1)
  -min-zoom float
        Minimum zoom level in camera units (omit flag for hardware minimum)
                        Example: -min-zoom=10 prevents zooming below 1x (default -1)
  -p1-track string
        Priority 1 tracking objects (comma-separated) - primary targets that can achieve LOCK
                        Example: -p1-track="boat,surfboard,kayak" (default "boat")
  -p2-track string
        Priority 2 tracking objects (comma-separated, or 'all') - enhancement objects detected inside locked P1 targets
                        Example: -p2-track="person,backpack" or -p2-track="all" (default "person")
  -pip-zoom
        Enable Picture-in-Picture zoom display of locked targets (default: true) (default true)
  -post-overlay-jpg
        Save frames after overlay processing (requires -jpg-path)
  -pre-overlay-jpg
        Save frames before overlay processing (requires -jpg-path)
  -ptzinput string
        PTZ camera HTTP URL (required)
                        Example: http://admin:pass@192.168.0.59:80/
  -status-overlay
        Show status information overlay (time, FPS, mode) in lower-left corner
  -target-overlay
        Show tracking and targeting overlays (bounding boxes, paths, object info)
  -terminal-overlay
        Show debug terminal overlay (real-time messages) in upper-left corner
```

### **Tracking Priorities**



```bash
# Default behavior (boats with people enhancement)
./NOLO -input rtsp://... -ptzinput http://...

# Track boats and kayaks, enhance with people and backpacks
./NOLO -input rtsp://... -ptzinput http://... \
  -p1-track="boat,kayak" \
  -p2-track="person,backpack"

# Track surfboards, enhance with ANY detected object
./NOLO -input rtsp://... -ptzinput http://... \
  -p1-track="surfboard" \
  -p2-track="all"

# Multiple P1 objects with specific P2 enhancement
./NOLO -input rtsp://... -ptzinput http://... \
  -p1-track="boat,surfboard,kayak" \
  -p2-track="person,bottle,backpack"
```

### **JPEG Frame Saving (Auto-organized by date/hour)**

```bash
# Save clean frames (before overlays)
./NOLO -input [URL] -ptzinput [URL] -jpg-path=/tmp/clean -pre-overlay-jpg

# Save processed frames (with all overlays)
./NOLO -input [URL] -ptzinput [URL] -jpg-path=/tmp/processed -post-overlay-jpg

# Save both types for complete analysis
./NOLO -input [URL] -ptzinput [URL] -jpg-path=/tmp/analysis -pre-overlay-jpg -post-overlay-jpg

# Debug mode with JPEG saving
./NOLO -input [URL] -ptzinput [URL] -debug -jpg-path=/tmp/debug -post-overlay-jpg

# Files are automatically organized into subdirectories: /path/2025-01-01_03PM/
```

### **Overlay Control**

```bash
# Clean view (no overlays)
./NOLO -input [URL] -ptzinput [URL]

# Status info only (lightweight monitoring)
./NOLO -input [URL] -ptzinput [URL] -status-overlay

# Target tracking visualization
./NOLO -input [URL] -ptzinput [URL] -target-overlay

# Terminal-style overlay display
./NOLO -input [URL] -ptzinput [URL] -terminal-overlay

# Picture-in-Picture zoom display
./NOLO -input [URL] -ptzinput [URL] -pip

# Full debug mode with all overlays
./NOLO -input [URL] -ptzinput [URL] -debug -status-overlay -target-overlay -terminal-overlay -pip
```

### **Advanced Debug Options**

```bash
# Debug mode (clean output)
./NOLO -input [URL] -ptzinput [URL] -debug

# Verbose debug mode (includes detailed YOLO, calibration, and tracking calculations)
./NOLO -input [URL] -ptzinput [URL] -debug -debug-verbose

# YOLO analysis (save detection inputs)
./NOLO -input [URL] -ptzinput [URL] -debug -YOLOdebug

# Single track debugging (exit after first lock)
./NOLO -input [URL] -ptzinput [URL] -debug -exit-on-first-track
```

### **Camera Control**

```bash
# PTZ movement limits (camera coordinate units)
-min-pan=1000 -max-pan=3000    # Pan boundaries
-min-tilt=0 -max-tilt=900      # Tilt boundaries  
-min-zoom=10 -max-zoom=120     # Zoom boundaries

# Color masking for water removal
-maskcolors="6d9755,243314"    # Mask water colors for better detection
-masktolerance=50              # Color tolerance (0-255)
```

## 📋 Prerequisites

- **Go 1.19+**
- **OpenCV 4.x** with Go bindings (`gocv`)
- **YOLO Model**: YOLOv8n ONNX format (`models/yolov8n.onnx`)
- **PTZ Camera**: Hikvision-compatible HTTP API
- **RTSP Stream**: Camera video feed
- **Optional**: CUDA for GPU acceleration

## 🤖 YOLO Object Detection Models & Capabilities

### **Supported YOLO Models**

This system supports multiple YOLO architectures for maximum flexibility:

| Model | Type | Performance | Use Case |
|-------|------|-------------|----------|
| **YOLOv3-tiny** | OpenCV .weights | Fast, lightweight | CPU-optimized real-time detection |
| **YOLOv8n** | ONNX | Balanced speed/accuracy | GPU acceleration, higher precision |

### **80-Class COCO Object Detection**

The system can detect and track **80 different object types** from the COCO dataset:

**🚢 Vehicles & Transportation:**
- boat, car, bicycle, motorbike, aeroplane, bus, train, truck

**👥 People & Accessories:**  
- person, backpack, umbrella, handbag, tie, suitcase

**🏠 Furniture & Indoor Objects:**
- chair, sofa, bed, diningtable, toilet, tvmonitor, laptop, mouse, remote, keyboard

**🍕 Food & Kitchen:**
- bottle, wine glass, cup, fork, knife, spoon, bowl, banana, apple, sandwich, orange, broccoli, carrot, hot dog, pizza, donut, cake, microwave, oven, toaster, sink, refrigerator

**🐕 Animals:**
- bird, cat, dog, horse, sheep, cow, elephant, bear, zebra, giraffe

**⚽ Sports & Recreation:**
- frisbee, skis, snowboard, sports ball, kite, baseball bat, baseball glove, skateboard, surfboard, tennis racket

**🌱 Plants & Environment:**
- pottedplant

**🛠️ Tools & Household:**
- scissors, hair drier, toothbrush, clock, vase, book, teddy bear

**🚦 Infrastructure:**
- traffic light, fire hydrant, stop sign, parking meter, bench

**📱 Electronics:**
- cell phone

### **Model Files & Links**

**Required Files:**
```
models/
├── yolov8n.onnx          # YOLOv8 nano model (12MB)
├── yolov3-tiny.weights   # YOLOv3-tiny weights (34MB)  
├── yolov3-tiny.cfg       # YOLOv3-tiny configuration
└── coco.names            # 80 object class names
```

**Download Links:**
- **YOLOv8 Models**: [Ultralytics YOLOv8 Releases](https://github.com/ultralytics/ultralytics/releases)
- **YOLOv3-tiny**: [Official YOLO Weights](https://pjreddie.com/darknet/yolo/)
- **COCO Names**: [COCO Class Labels](https://github.com/pjreddie/darknet/blob/master/data/coco.names)

### **Performance Characteristics**

**YOLOv3-tiny:**
- ✅ **CPU-optimized**: Runs efficiently on modest hardware
- ✅ **Low latency**: ~15-30ms inference time
- ✅ **Small model**: 34MB weights file
- ⚠️ **Trade-off**: Slightly lower accuracy than full models

**YOLOv8n (nano):**
- ✅ **GPU-accelerated**: Leverages CUDA when available  
- ✅ **Better accuracy**: Improved detection performance
- ✅ **ONNX format**: Cross-platform compatibility
- ⚠️ **Requires**: More computational resources

### **Detection Configuration**

The system processes video at **832×832 resolution** for YOLO inference with smart letterboxing to maintain aspect ratios. Detection confidence thresholds and Non-Maximum Suppression (NMS) are automatically optimized for river monitoring scenarios.

**Priority Object Configuration:**
```bash
# Default: Track boats, enhance with people
-p1-track="boat" -p2-track="person"

# Maritime focus: Multiple watercraft types  
-p1-track="boat,surfboard,kayak" -p2-track="person,backpack"

# Complete flexibility: Any object can be primary/secondary
-p1-track="car,truck,bus" -p2-track="person,bicycle"
```

## 🔧 Installation

1. **Clone the repository:**
```bash
git clone https://github.com/doxx/NOLOcam.git
cd NOLOcam
```

2. **Install dependencies:**
```bash
go mod tidy
go mod download
```

3. **Set up YOLO models:**
   - Download YOLOv8n: Place `yolov8n.onnx` in `models/` directory
   - Download YOLOv3-tiny: Place `yolov3-tiny.weights` and `yolov3-tiny.cfg` in root directory  
   - Ensure `coco.names` contains the 80 COCO class labels (see YOLO section above for links)

4. **Configure scanning pattern (required):**
   - Edit `scanning.json` with your camera's scan positions
   - Define areas of interest for systematic monitoring

5. **Build the application:**
```bash
go build .
```

## AI Commentator: Captain BlackEye's Untapped Potential

The AI commentator system represents one of the most underutilized but potentially powerful components of this project. While currently implemented as entertainment (a sarcastic pirate making fun of Miami yacht owners), it demonstrates sophisticated AI-camera integration that could revolutionize autonomous surveillance systems.

### What It Currently Does

The `ai_commentary` standalone application embodies "Captain BlackEye," a pirate trapped inside the camera who provides running commentary about what he sees on the Miami River:

```go
// The Captain BlackEye persona system prompt (excerpt):
You were once a pirate named Captain BlackEye, and somehow you got trapped inside of a camera.
```

**Technical Implementation:**
- **Direct camera capture** via Hikvision ISAPI with digest authentication
- **GPT-4 vision analysis** of captured JPEG images every 30 seconds (configurable)
- **Conversation memory** maintains context across 10 previous exchanges
- **Atomic file writes** to `/tmp/commentary.txt` for FFmpeg integration
- **Robust error handling** with network timeouts and retry logic

```bash
# Captain BlackEye in action:
./ai_commentary 

💬 Captain BlackEye says: "Ahoy! Another rich asshole in a yacht that's 
bigger than my entire pirate ship ever was. Look at 'em, probably doesn't 
even know port from starboard. Miami's finest maritime morons, I tell ya!"
```

### The Massive Untapped Potential

This system scratches the surface of what's possible when you combine computer vision with large language models. The current implementation could be transformed into a sophisticated AI director for autonomous camera operations:

#### **1. Intelligent Scene Analysis & Camera Direction**
Instead of just making jokes, the AI could analyze scenes and direct camera movement:

```go
// Potential enhancement:
type SceneAnalysis struct {
    InterestingObjects []DetectedObject
    RecommendedFocus   CameraPosition
    TrackingPriority   int
    SceneDescription   string
}

// AI could say: "I see a rare manatee at coordinates (1200, 800). 
// Suggest zooming to 8x and panning 15 degrees east for optimal tracking."
```

#### **2. Deep Object Analysis & Behavioral Prediction**
The AI could analyze object behavior patterns and predict movement:

```go
// Enhanced detection integration:
"I've been watching this boat for 3 minutes. Based on its trajectory 
and speed, it will reach the bridge in 90 seconds. Recommend switching 
to wide-angle view to capture the entire passage sequence."
```

#### **3. Dynamic Interest Scoring**
The AI could rate scenes in real-time and guide the tracking system to focus on the most interesting events:

```go
type InterestScore struct {
    WildlifeActivity   int  // Manatees, dolphins, pelicans
    BoatActivity      int  // Unusual vessels, accidents, interactions  
    EnvironmentalEvent int  // Weather changes, tidal effects
    OverallPriority   int  // Combined weighted score
}
```

#### **4. Multi-Modal Environmental Awareness**
Integration with weather APIs, tide data, and historical patterns:

```go
"Storm approaching from the southeast. Recommend lowering zoom to 2x 
and focusing on the marina - boats will start moving to shelter in 
approximately 20 minutes based on historical patterns."
```

### Current Architecture Limitations

The standalone design isolates the AI from the main detection pipeline, which limits its potential:

```go
// Current: Isolated system
ai_commentary -> Camera -> GPT-4 -> Text File -> FFmpeg

// Potential: Integrated intelligence
Main Pipeline -> Detection Results -> AI Analysis -> Camera Commands
             \-> Scene Understanding -> Tracking Priorities -> PTZ Control
```

### Future Integration Possibilities

**Real-Time Detection Enhancement:**
```go
// AI could enhance detection confidence:
"I see what YOLO detected as a 'boat' but based on shape and movement 
patterns, this appears to be a manatee. Adjusting classification and 
switching to wildlife tracking mode."
```

**Proactive Scene Management:**
```go
// AI could anticipate interesting events:
"Multiple boats converging near the sandbar. Historical data shows 
this often leads to interesting interactions. Recommend pre-positioning 
camera at coordinates (2400, 1100) with 6x zoom."
```

**Contextual Understanding:**
```go
// AI provides rich context beyond basic object detection:
"Large yacht 'Sea Demon' (identified by hull markings) is the same 
vessel that had mechanical issues here last month. Captain appears 
to be showing off again - watch for potential comedy gold."
```

### Why This Matters

This represents the future of AI-augmented surveillance - not just detecting objects, but understanding scenes, predicting behavior, and making intelligent decisions about what deserves attention. Captain BlackEye may be making jokes about yacht owners now, but the underlying technology could power everything from wildlife research to security systems that truly understand what they're watching.

The gap between "AI that detects boats" and "AI that understands maritime behavior" is exactly what this commentator system could bridge. It's not just computer vision anymore - it's computer comprehension. 


## Calibration System: Precision Pixel-to-Camera Mapping

### **The Calibration Challenge**

For accurate PTZ tracking, the system must understand the precise relationship between pixel movement on screen and actual camera motor movement. This relationship **changes dramatically with zoom level** - at 10x zoom, moving 100 pixels might require 20 camera units, but at 120x zoom, the same 100 pixels might only require 3 camera units.

### **🛠️ Three Calibration Methods Available**

#### **1. Manual Hand Calibration** _(Most Precise for Pan/Tilt)_

**Tool**: `calibration/hand_calibrator/`

**How it works:**
1. **Interactive guidance** through PTZ positioning
2. User manually positions camera to align objects at screen edges
3. Records precise camera coordinates for left→right (pan) and top→bottom (tilt) movements
4. Calculates **pixels-per-PTZ-unit** for each zoom level

**Process:**
```bash
cd calibration/hand_calibrator
go build -o hand_calibrator
./hand_calibrator
```

The tool guides you through:
- **Pan Calibration**: Align object to left edge → record position → pan to right edge → record position
- **Tilt Calibration**: Align object to top edge → record position → tilt to bottom edge → record position
- **Multi-Zoom**: Repeat process at different zoom levels (10x, 20x, 30x, etc.)

**Output**: Precise conversion ratios like `15.27 pixels per pan unit at 50x zoom`

#### **2. Scanning Pattern Recorder** _(For Automated Movement)_

**Tool**: `calibration/scanning_recorder/`

**Purpose**: Creates predefined camera positions for automated scanning mode

**How it works:**
1. **Manual positioning**: Move camera to desired scan positions
2. **Record coordinates**: Press ENTER to save each position with name and dwell time
3. **Pattern generation**: Creates `scanning.json` with complete movement sequence

**Process:**
```bash
cd calibration/scanning_recorder  
go build -o scanning_recorder
./scanning_recorder
```

**Interactive workflow:**
```
🎯 Position camera at "river_left" → ENTER → Set dwell time: 15 seconds
🎯 Position camera at "bridge_center" → ENTER → Set dwell time: 10 seconds  
🎯 Position camera at "dock_area" → ENTER → Set dwell time: 20 seconds
Type 'done' → Saves to scanning.json
```

#### **3. Pixel-to-Inches Calibration** _(For Real-World Measurements)_

**Tool**: `calibration/pixelinches/`

**Purpose**: Enables speed calculations, boat length measurements, and distance estimation

**How it works:**
1. **Reference distance**: Uses known 369-inch reference object in camera view
2. **Multi-zoom capture**: Takes photos at all zoom levels (10x-120x)
3. **Manual measurement**: User measures pixels for the 369-inch reference in each photo
4. **Calibration calculation**: Generates pixels-per-inch ratios for each zoom level

**Process:**
```bash
cd calibration/pixelinches
go run pixelinches.go
```

For each zoom level:
1. Camera automatically sets zoom level
2. Captures photo (`Z10.jpg`, `Z20.jpg`, etc.)
3. User opens photo, measures 369-inch reference in pixels
4. System calculates conversion: `pixels ÷ 369 inches = pixels-per-inch`

### **📊 Calibration Data Integration**

#### **Current Hardcoded Values** _(Will be replaced by calibration files)_
```go
PanPixelsPerUnit: map[int]float64{
    10:  4.87,   // 4.87 pixels per pan unit at 10x zoom
    20:  7.91,   // 7.91 pixels per pan unit at 20x zoom  
    30:  10.38,  // 10.38 pixels per pan unit at 30x zoom
    40:  12.74,  // 12.74 pixels per pan unit at 40x zoom
    50:  15.27,  // 15.27 pixels per pan unit at 50x zoom
    60:  18.41,  // 18.41 pixels per pan unit at 60x zoom
    70:  19.91,  // 19.91 pixels per pan unit at 70x zoom
    80:  21.68,  // 21.68 pixels per pan unit at 80x zoom
    90:  26.10,  // 26.10 pixels per pan unit at 90x zoom
    100: 27.71,  // 27.71 pixels per pan unit at 100x zoom
    110: 30.20,  // 30.20 pixels per pan unit at 110x zoom
    120: 36.32,  // 36.32 pixels per pan unit at 120x zoom
}
```

#### **How Spatial Tracking Uses Calibration:**
```go
// Convert pixel detection to camera movement
panPixelsPerUnit := st.InterpolatePanCalibration(currentZoom)
tiltPixelsPerUnit := st.InterpolateTiltCalibration(currentZoom)

// Calculate required camera adjustment
panAdjustment := float64(pixelOffsetX) / panPixelsPerUnit  
tiltAdjustment := float64(pixelOffsetY) / tiltPixelsPerUnit

// Result: Precise camera movement to center detected object
```

### **🎯 Calibration Accuracy Impact**

**Poor calibration** → Objects drift off-screen, tracking failures  
**Good calibration** → Smooth tracking, objects stay centered  
**Excellent calibration** → Rock-solid lock, imperceptible tracking adjustments

### **📁 Generated Files**

| Tool | Output File | Purpose |
|------|-------------|---------|
| **Hand Calibrator** | `PTZ-master-calibration.json` | Pixel↔PTZ unit conversion ratios |
| **Scanning Recorder** | `scanning.json` | Automated scanning positions |  
| **Pixel-Inches** | `pixels-inches-cal.json` | Real-world measurement conversions |

Each calibration method generates JSON files that the tracking system loads for precise spatial calculations.


## Broadcast System: Production-Grade Stream Management

### **The Broadcasting Challenge**

Professional live streaming requires **rock-solid reliability** - any interruption, freeze, or crash results in lost viewers and revenue. Traditional FFmpeg scripts often fail silently, hang indefinitely, or crash without recovery. The NOLO broadcast system solves this with enterprise-grade monitoring and automatic recovery.

### **🎬 How the Broadcast Monitor Works**

#### **Intelligent Stream Mixing**
The broadcast system combines **two video sources** into a single professional stream:

1. **Primary Video**: RTMP stream from NOLO tracking system (`rtmp://192.168.0.12/live/stream`)
2. **Audio Source**: Direct RTSP feed from camera with audio (`rtsp://admin:password@camera/audio`)
3. **AI Commentary**: Dynamic text overlay from `/tmp/commentary.txt` (live AI analysis)

#### **Multi-Configuration Support**
Different encoding configurations for various hardware setups:

| Config File | Purpose | Encoding | Use Case |
|------------|---------|----------|----------|
| `broadcast_config.json` | **CPU Encoding** | `libx264` | Standard servers, development |
| `broadcast_config_nvidia.json` | **GPU Encoding** | `h264_nvenc` | Production with NVIDIA GPU |
| `broadcast_config_nvidia_nodrawtext.json` | **GPU No Overlay** | `h264_nvenc` | Raw feed without AI commentary |
| `broadcast_config_darwin.json` | **macOS** | Platform-optimized | Apple development |

### **⚡ Key Features**

#### **🔍 Process Health Monitoring**
```go
// Monitors multiple health indicators
- FFmpeg output activity (15-second timeout)
- Frame progression tracking (detects stalls)
- Process status monitoring
- DTS/PTS timestamp error detection
- Memory and resource usage
```

#### **🔄 Automatic Recovery System**
- **Unlimited restarts** (configurable)
- **Smart restart delays** prevent resource exhaustion  
- **Graceful shutdown** handling (SIGTERM → SIGKILL if needed)
- **State preservation** across restarts

#### **📊 Advanced Stream Configuration**
```json
{
  "max_restarts": 999999,
  "health_timeout_seconds": 15,
  "restart_delay_seconds": 1,
  "ffmpeg_args": [
    "-re", "-thread_queue_size", "1024",
    "-i", "rtmp://192.168.0.12/live/stream",      // NOLO video  
    "-i", "rtsp://admin:pass@camera:554/audio",   // Camera audio
    "-map", "0:v:0", "-map", "1:a:0",            // Mix video + audio
    "-vf", "scale=2560:1440,drawtext=...",       // Scale + AI overlay
    "-c:v", "h264_nvenc", "-preset", "p7",       // GPU encoding
    "-b:v", "16000k", "-maxrate", "18000k",      // Bitrate control
    "-f", "flv", "rtmp://a.rtmp.youtube.com/live2/YOUR_KEY"
  ]
}
```

### **🛠️ Usage Guide** 

#### **Build & Run**
```bash
cd broadcast
./build.sh
./broadcast-monitor
```

#### **Configuration Options**
```bash
# Use different config file
./broadcast-monitor -c broadcast_config_nvidia.json

# Enable local recording  
./broadcast-monitor -record ./recordings

# Background operation
nohup ./broadcast-monitor > /dev/null 2>&1 &
```

### **🏗️ Architecture Overview**

#### **Stream Processing Pipeline**
```
NOLO AI Tracking → RTMP Stream
                      ↓
                 Broadcast Monitor
                      ↓
  [Video Mixing + Audio + AI Overlay]
                      ↓
    Professional YouTube Live Stream
```

### ** Configuration Examples**

#### **High-Quality GPU Streaming** _(Recommended for Production)_
```json
{
  "ffmpeg_args": [
    "-c:v", "h264_nvenc", "-preset", "p7", "-profile:v", "high",
    "-b:v", "16000k", "-maxrate", "18000k", "-bufsize", "32000k",
    "-rc", "vbr", "-cq", "23", "-spatial_aq", "1", "-temporal_aq", "1"
  ]
}
```

#### **CPU Fallback Configuration** _(For servers without GPU)_
```json
{
  "ffmpeg_args": [
    "-c:v", "libx264", "-preset", "fast", "-profile:v", "high",
    "-b:v", "16000k", "-maxrate", "18000k", "-bufsize", "48000k"
  ]
}
```

### **📊 Monitoring & Logging**

The broadcast monitor provides comprehensive visibility:
- **Frame processing statistics** (every 30 seconds)
- **Network connectivity status**
- **Encoding performance metrics** 
- **Error categorization and frequency**
- **Restart event logging**
- **Resource utilization tracking**

This production-grade approach ensures your AI-powered streams maintain professional quality and reliability 24/7. 



## 🔒 Security Considerations

### ⚠️ **Critical Security Warning**

**This system operates with multiple unencrypted communication channels that transmit credentials in plaintext:**

| Protocol | Port | Security Issue | Risk Level |
|----------|------|----------------|------------|
| **RTSP** | 554 | No encryption, credentials in URL | 🔴 **HIGH** |
| **HTTP Camera Control** | 80 | Plaintext Basic Auth | 🔴 **HIGH** |
| **RTMP** | 1935 | No encryption | 🟡 **MEDIUM** |

### 🚨 **Plaintext Credential Transmission**

**All camera communications send usernames and passwords in plaintext:**

```bash
# ❌ THESE CREDENTIALS ARE VISIBLE ON THE NETWORK:
rtsp://user:password123@192.168.1.100:554/stream
http://user:password123@192.168.1.100:80/
```

**Anyone monitoring network traffic can capture:**
- Camera login credentials
- Video streams  
- PTZ control commands
- Administrative access tokens

### ✅ **Appropriate Use Cases**

```bash
# ✅ Suitable for:
- Private property monitoring (isolated networks)
- Development and testing environments
- Proof-of-concept demonstrations
- Internal research projects with proper network controls
- Education and learning (controlled environments)
```

## 🎮 Usage Examples

### **Basic River Monitoring**
```bash
./NOLO \
  -input "rtsp://admin:password@192.168.1.100:554/Streaming/Channels/101" \
  -ptzinput "http://admin:password@192.168.1.100:80/" \
  -debug
```

### **Multi-Object Flexible Tracking**
```bash
# Track boats and kayaks, enhance with people and equipment
./NOLO \
  -input "rtsp://admin:password@192.168.1.100:554/Streaming/Channels/101" \
  -ptzinput "http://admin:password@192.168.1.100:80/" \
  -p1-track="boat,kayak" \
  -p2-track="person,backpack,bottle" \
  -pip \
  -debug
```

### **Wildlife Monitoring**
```bash
# Track any floating objects, enhance with birds or other wildlife
./NOLO \
  -input "rtsp://admin:password@192.168.1.100:554/Streaming/Channels/101" \
  -ptzinput "http://admin:password@192.168.1.100:80/" \
  -p1-track="boat,surfboard" \
  -p2-track="bird,person" \
  -min-zoom=20 -max-zoom=100
```

### **Development and Testing**
```bash
# Debug mode with YOLO analysis and single-track exit
./NOLO \
  -input "rtsp://admin:password@192.168.1.100:554/Streaming/Channels/101" \
  -ptzinput "http://admin:password@192.168.1.100:80/" \
  -debug \
  -debug-verbose \
  -YOLOdebug \
  -exit-on-first-track \
  -jpg-path=/tmp/debug \
  -pre-overlay-jpg \
  -post-overlay-jpg
```

## 📊 Understanding the Output

NOLO provides rich debugging information:

```
[TRACKING_CONFIG] P1 (Primary): [boat kayak]
[TRACKING_CONFIG] P2 (Enhancement): [person backpack]

📊 Frame 1250: YOLO detected 2 boats, 1 people, 0 others
🎯 Boat boat_123 gets +0.7 priority for having 1 P2 objects!
🔒 Target boat boat_123 LOCKED for camera tracking (mature target)!
👤📺 Using LOCK target with P2 objects (det:15, lost:0) for PIP
🎯👤 Using P2 centroid (1245,680) for tracking - 1 P2 objects, quality 0.85
```

## 🏷️ Supported Object Types

**Common P1 (Primary) Objects:**
- `boat` - Motor boats, sailboats, yachts
- `kayak` - Kayaks, canoes  
- `surfboard` - Surfboards, paddleboards
- Any COCO dataset object class

**Common P2 (Enhancement) Objects:**
- `person` - People on boats/boards
- `backpack` - Equipment, luggage
- `bottle` - Drinks, containers
- `umbrella` - Shade, equipment
- `chair` - Seating, furniture
- `all` - Any non-P1 object

## 📁 Configuration Files

- **`scanning.json`**: Camera scan pattern definitions
- **`pixels-inches-cal.json`**: Spatial calibration data
- **`coco.names`**: YOLO class labels
- **`models/yolov8n.onnx`**: YOLO detection model

## 🐛 Debug Features

- **Debug Overlay**: Real-time tracking visualization
- **Verbose Mode**: Detailed YOLO, calibration, and tracking calculations
- **Spatial Logs**: Coordinate transformation details  
- **YOLO Analysis**: Save detection inputs for model debugging
- **Performance Stats**: FPS, latency, memory usage
- **Session Logs**: Per-object tracking history
- **JPEG Capture**: Organized frame saving by date/hour

## 🤝 Contributing

Join us on Discord: https://discord.gg/Gr9rByrEzZ


## 📄 License

### **NOLO Ethical AI License (Based on MIT)**

**Copyright (c) 2025 Barrett Lyon**

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction for **non-commercial, ethical, and educational purposes**, subject to the following conditions:

### ✅ **Permitted Uses**
- ✅ **Personal projects and research**
- ✅ **Educational and academic purposes**  
- ✅ **Open source contributions and improvements**
- ✅ **Wildlife and environmental monitoring**
- ✅ **Property security (private, residential)**
- ✅ **Technology demonstrations and prototyping**
- ✅ **Non-profit organizational use**

### 🚫 **Strictly Prohibited Uses**

The following uses are **explicitly prohibited** under this license:

#### **Commercial Restrictions**
- 🚫 **Commercial deployment** without explicit written license from Barrett Lyon
- 🚫 **Revenue-generating activities** using this software
- 🚫 **Integration into commercial products** or services
- 🚫 **Corporate surveillance systems**
- 🚫 **Paid consulting services** using this codebase

#### **Ethical and Human Rights Restrictions**
- 🚫 **Facial recognition or biometric identification** of any kind
- 🚫 **Human tracking, profiling, or behavioral analysis**
- 🚫 **Surveillance that violates privacy rights** or reasonable expectation of privacy
- 🚫 **Discriminatory targeting** based on race, religion, gender, nationality, or other protected characteristics
- 🚫 **Mass surveillance programs** or bulk data collection
- 🚫 **Stalking, harassment, or intimidation** of individuals

#### **Military and Security Restrictions**
- 🚫 **Military applications** or defense contractor use
- 🚫 **Weapons systems integration** or targeting assistance
- 🚫 **Border security or immigration enforcement**
- 🚫 **Law enforcement surveillance** without proper judicial oversight
- 🚫 **Intelligence gathering operations**

#### **Harmful Applications**
- 🚫 **Any use that could cause physical or psychological harm** to individuals
- 🚫 **Tracking of vulnerable populations** (children, elderly, disabled, refugees)
- 🚫 **Social credit scoring** or behavioral modification systems

### 📝 **License Conditions**

1. **Attribution Required**: The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

2. **Commercial Licensing**: For any commercial use, contact Barrett Lyon for explicit written permission and commercial licensing terms.

3. **Ethical Compliance**: Users must certify their intended use complies with the ethical restrictions outlined above.

5. **Immediate Termination**: Any violation of the prohibited uses immediately terminates your rights under this license.

### ⚖️ **Legal Disclaimer**

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

### 📧 **Commercial Licensing Contact**

For commercial licensing inquiries, please contact:  
**Barrett Lyon** - blyon@blyon.com


## 🙏 Credits & Acknowledgments

This project stands on the shoulders of giants. NOLO would not be possible without the incredible work of these open source communities and organizations:

### 🤖 **AI & Computer Vision**

**[Ultralytics YOLOv8](https://github.com/ultralytics/ultralytics)**
- The foundation of our object detection system
- Thanks to Glenn Jocher and the Ultralytics team for advancing real-time object detection
- [YOLOv8 Models & Documentation](https://docs.ultralytics.com/)

**[YOLOv3/YOLOv4 (Joseph Redmon & Alexey Bochkovskiy)](https://github.com/AlexeyAB/darknet)**
- Original YOLO architecture that revolutionized computer vision
- Joseph Redmon's pioneering work on "You Only Look Once"
- [YOLO: Real-Time Object Detection](https://pjreddie.com/darknet/yolo/)

**[COCO Dataset](https://cocodataset.org/)**
- 80-class object detection dataset that enables flexible tracking
- Microsoft's contribution to computer vision research
- Essential for training robust object detection models

### 🎥 **Computer Vision & Media Processing**

**[OpenCV (Open Source Computer Vision Library)](https://opencv.org/)**
- The backbone of all image processing, from frame capture to overlay rendering
- 20+ years of computer vision innovation by the global OpenCV community
- [OpenCV GitHub Repository](https://github.com/opencv/opencv)

**[GoCV (Go Bindings for OpenCV)](https://gocv.io/)**
- Makes OpenCV accessible from Go with excellent performance
- Thanks to Ron Evans (@deadprogram) and the Hybridgroup team
- [GoCV GitHub Repository](https://github.com/hybridgroup/gocv)

**[FFmpeg](https://ffmpeg.org/)**
- **Absolutely essential** for real-time video streaming and encoding
- Powers our RTMP pipeline for YouTube and live streaming
- The Swiss Army knife of multimedia processing
- Incredible engineering by Fabrice Bellard and the FFmpeg team spanning decades
- Supports virtually every video format and codec imaginable
- [FFmpeg GitHub Repository](https://github.com/FFmpeg/FFmpeg)

### ⚡ **GPU Acceleration & Performance**

**[NVIDIA CUDA](https://developer.nvidia.com/cuda-zone)**
- **Game-changing GPU acceleration** that makes real-time AI possible
- NVIDIA's decades of parallel computing innovation
- [CUDA Toolkit Documentation](https://docs.nvidia.com/cuda/)

**[NVIDIA cuDNN](https://developer.nvidia.com/cudnn)**
- Deep learning acceleration primitives
- Optimized neural network inference that powers our YOLO processing

### 🌐 **Streaming & Network Technologies**

**[SRS (Simple Realtime Server)](https://github.com/ossrs/srs)**
- Much better RTMP handling than nginx for live streaming
- Reliable, efficient real-time media server

Built with ❤️ for watchign crazy boat stuff.
