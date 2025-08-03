# PTZ Calibration Results

## Pan Calibration Results

### Zoom Level 10
| Movement | Pixel Shift | Units | Pixels/Unit | Notes |
|----------|-------------|-------|-------------|--------|
| Base → +1 unit | 8px | 1 | 8.00 | Single unit measurement |
| +1 → +10 units | 40px | 9 | 4.44 | Multi-unit measurement |
| +10 → +50 units | 185px | 40 | 4.63 | Large movement measurement |

**Analysis:**
- Single unit: 8.00 px/unit
- Multi-unit average: (4.44 + 4.63) / 2 = 4.54 px/unit
- **Possible non-linearity or backlash in camera movement**
- **Recommended value: ~4.5-5.0 px/unit** (based on larger movements)

### Zoom Level 60
| Movement | Pixel Shift | Units | Pixels/Unit | Notes |
|----------|-------------|-------|-------------|--------|
| Base → +1 unit | 15px | 1 | 15.0 | Single unit measurement |
| +1 → +10 units | 75px | 9 | 8.33 | Multi-unit measurement |
| +10 → +50 units | 932px | 40 | 23.3 | Large movement measurement |

**Analysis:**
- Single unit: 15.0 px/unit
- Multi-unit average: (8.33 + 23.3) / 2 = 15.82 px/unit
- **SEVERE backlash - huge spread in measurements!**
- **Recommended value: ~16.0 px/unit** (average, but unreliable)
- **3.6x more sensitive than zoom 10** (16.0 vs 4.5) - matches tilt scaling!

### Zoom Level 120
| Movement | Pixel Shift | Units | Pixels/Unit | Notes |
|----------|-------------|-------|-------------|--------|
| Base → +1 unit | 30px | 1 | 30.0 | Single unit measurement |
| +1 → +10 units | 150px | 9 | 16.7 | Multi-unit measurement |
| +10 → +50 units | 1300px | 40 | 32.5 | Large movement measurement |

**Analysis:**
- Single unit: 30.0 px/unit
- Multi-unit average: (16.7 + 32.5) / 2 = 24.6 px/unit
- **EXTREME backlash - 30.0 vs 16.7 px/unit!**
- **Recommended value: ~25.0 px/unit** (average, but very unreliable)
- **1.6x more sensitive than zoom 60** (25.0 vs 16.0) - lower scaling than expected

## Tilt Calibration Results

### Zoom Level 10
| Movement | Pixel Shift | Units | Pixels/Unit | Notes |
|----------|-------------|-------|-------------|--------|
| Base → +1 unit | 5px | 1 | 5.00 | Single unit measurement |
| +1 → +10 units | 45px | 9 | 5.00 | Multi-unit measurement |
| +10 → +50 units | 195px | 40 | 4.88 | Large movement measurement |

**Analysis:**
- Single unit: 5.00 px/unit
- Multi-unit average: (5.00 + 4.88) / 2 = 4.94 px/unit
- **Much more consistent than pan measurements!**
- **Recommended value: ~5.0 px/unit** (very consistent across all ranges)

### Zoom Level 60
| Movement | Pixel Shift | Units | Pixels/Unit | Notes |
|----------|-------------|-------|-------------|--------|
| Base → +1 unit | 10px | 1 | 10.0 | Single unit measurement |
| +1 → +10 units | 162px | 9 | 18.0 | Multi-unit measurement |
| +10 → +50 units | 740px | 40 | 18.5 | Large movement measurement |

**Analysis:**
- Single unit: 10.0 px/unit
- Multi-unit average: (18.0 + 18.5) / 2 = 18.25 px/unit
- **Some backlash but much less than pan motor**
- **Recommended value: ~18.0 px/unit** (based on multi-unit average)
- **3.6x more sensitive than zoom 10** (18.0 vs 5.0)

### Zoom Level 120
| Movement | Pixel Shift | Units | Pixels/Unit | Notes |
|----------|-------------|-------|-------------|--------|
| Base → +1 unit | 20px | 1 | 20.0 | Single unit measurement |
| +1 → +10 units | 317px | 9 | 35.2 | Multi-unit measurement |
| +10 → +50 units | 1300px | 40 | 32.5 | Large movement measurement |

**Analysis:**
- Single unit: 20.0 px/unit
- Multi-unit average: (35.2 + 32.5) / 2 = 33.85 px/unit
- **Moderate backlash** - similar pattern to zoom 60
- **Recommended value: ~34.0 px/unit** (based on multi-unit average)
- **1.9x more sensitive than zoom 60** (34.0 vs 18.0)

## Observations

### Pan Movement (Zoom 10)
- **Single unit movement** shows higher sensitivity (8 px/unit)
- **Multi-unit movements** show lower sensitivity (4.4-4.6 px/unit)
- This suggests possible **mechanical backlash** or **non-linear movement**
- For tracking system, should probably use the **multi-unit average (~4.5 px/unit)**

### Tilt Movement (Zoom 10) ✅
- **Excellent consistency:** 5.0, 5.0, 4.88 px/unit
- **Minimal backlash** - single unit matches multi-unit measurements
- **Tilt motor appears more precise than pan motor**
- For tracking system, can confidently use **5.0 px/unit**

### Complete Calibration Results
| Zoom | Axis | Single Unit | Multi-Unit Avg | Backlash | Recommended |
|------|------|-------------|----------------|----------|-------------|
| **10** | **Pan** | 8.0 px/unit | 4.5 px/unit | High | ~4.5 px/unit |
| **10** | **Tilt** | 5.0 px/unit | 4.9 px/unit | Minimal | ~5.0 px/unit |
| **60** | **Pan** | 15.0 px/unit | 15.8 px/unit | **SEVERE** | ~16.0 px/unit ⚠️ |
| **60** | **Tilt** | 10.0 px/unit | 18.3 px/unit | Moderate | ~18.0 px/unit |
| **120** | **Pan** | 30.0 px/unit | 24.6 px/unit | **EXTREME** | ~25.0 px/unit ⚠️⚠️ |
| **120** | **Tilt** | 20.0 px/unit | 33.9 px/unit | Moderate | ~34.0 px/unit |

### Zoom Sensitivity Pattern (NON-LINEAR!)

**Tilt (Reliable Motor):**
- **Zoom 10:** ~5.0 px/unit (baseline)
- **Zoom 60:** ~18.0 px/unit (3.6x increase for 6x zoom)
- **Zoom 120:** ~34.0 px/unit (1.9x increase for 2x zoom)
- **Overall:** 6.8x sensitivity increase for 12x zoom (10→120)

**Pan (Problematic Motor):**
- **Zoom 10:** ~4.5 px/unit (baseline)
- **Zoom 60:** ~16.0 px/unit (3.6x increase for 6x zoom) ⚠️
- **Zoom 120:** ~25.0 px/unit (1.6x increase for 2x zoom) ⚠️⚠️
- **Overall:** 5.6x sensitivity increase for 12x zoom (10→120)

### Motor Quality Assessment
- **Tilt Motor:** Consistent measurements, predictable scaling ✅
- **Pan Motor:** Severe backlash, unreliable measurements ⚠️
- **Zoom Scaling:** Both axes show identical 3.6x scaling factor (10→60)

### Zoom Scaling Analysis

**Both Axes Show Same Non-Linear Pattern:**
- **10→60:** 6x zoom = 3.6x sensitivity (0.6x efficiency)
- **60→120:** 2x zoom = 1.9x sensitivity (0.95x efficiency) [tilt], 1.6x sensitivity (0.8x efficiency) [pan]
- **Pattern:** Zoom sensitivity increases are **non-linear** - more dramatic at lower zoom levels

**Complete Scaling Factors:**
- **Tilt:** 10→60→120 = 3.6x→1.9x = 6.8x total
- **Pan:** 10→60→120 = 3.6x→1.6x = 5.6x total

### Calibration Status
1. ✅ Pan zoom 10 measurements complete
2. ✅ Tilt zoom 10 measurements complete  
3. ✅ Tilt zoom 60 complete
4. ✅ Tilt zoom 120 complete
5. ✅ Pan zoom 60 complete
6. ✅ **Pan zoom 120 complete - ALL MEASUREMENTS DONE!**

## Critical Findings & Recommendations

### 🎯 **For Tracking System Implementation:**

#### **1. Use Zoom-Specific Lookup Tables (REQUIRED)**
```go
// Pan sensitivity by zoom level
panLookupTable := map[int]float64{
    10:  4.5,
    60:  16.0,
    120: 25.0,
}

// Tilt sensitivity by zoom level  
tiltLookupTable := map[int]float64{
    10:  5.0,
    60:  18.0,
    120: 34.0,
}
```

#### **2. Pan Motor Reliability Issues**
- **Extreme backlash at all zoom levels** - especially zoom 120
- **Recommendation:** Use smaller, more frequent pan adjustments
- **Consider:** Applying deadband compensation for pan movements

#### **3. Tilt Motor Excellence**
- **Highly reliable at all zoom levels**
- **Recommendation:** Can use tilt for precise tracking with confidence
- **Minimal backlash** - measurements are consistent

### 🚨 **Critical Discovery: Non-Linear Zoom Scaling!**
- Zoom sensitivity does NOT scale linearly with zoom level
- Lower zoom changes (10→60) have much higher sensitivity scaling
- Higher zoom changes (60→120) have lower sensitivity scaling
- **LINEAR SCALING WOULD BE COMPLETELY WRONG!**

## Implementation for Tracking System

### **Immediate Actions Required:**

1. **Replace linear zoom scaling with lookup tables**
2. **Implement interpolation for intermediate zoom levels**
3. **Add pan motor deadband compensation** 
4. **Consider favoring tilt adjustments over pan when possible**

### **Code Example for Implementation:**
```go
func getPixelsPerPanUnit(zoomLevel float64) float64 {
    // Use lookup table with interpolation
    if zoomLevel <= 10 { return 4.5 }
    if zoomLevel <= 60 { 
        return 4.5 + (16.0-4.5) * (zoomLevel-10)/50.0 
    }
    if zoomLevel <= 120 { 
        return 16.0 + (25.0-16.0) * (zoomLevel-60)/60.0 
    }
    return 25.0 // Max zoom
}

func getPixelsPerTiltUnit(zoomLevel float64) float64 {
    // Tilt is more reliable - can use precise interpolation
    if zoomLevel <= 10 { return 5.0 }
    if zoomLevel <= 60 { 
        return 5.0 + (18.0-5.0) * (zoomLevel-10)/50.0 
    }
    if zoomLevel <= 120 { 
        return 18.0 + (34.0-18.0) * (zoomLevel-60)/60.0 
    }
    return 34.0 // Max zoom
}
```

### **Motor-Specific Strategies:**
- **Pan:** Use smaller movements (±5-10 units max) to minimize backlash
- **Tilt:** Can use larger movements (±20+ units) with confidence
- **Zoom-dependent:** Reduce movement sizes at higher zoom levels

### ✅ **Questions Resolved:**
1. ✅ **Tilt measurements are much more consistent than pan**
2. ✅ **Zoom patterns are non-linear and require lookup tables**
3. ✅ **Ratios change dramatically with zoom levels**
4. ✅ **Different strategies needed for pan vs tilt motors** 


HAND CALIBRATION










📊 GENERATING FINAL CALIBRATION RESULTS
=====================================
📋 FINAL CALIBRATION TABLE
┌────────┬─────────────────┬─────────────────┐
│  Zoom  │  Pan (px/unit)  │ Tilt (px/unit)  │
├────────┼─────────────────┼─────────────────┤
│     10 │          -4.870 │          -5.000 │
│     20 │          -7.906 │          -7.755 │
│     30 │         -10.378 │         -10.857 │
│     40 │         -12.739 │         -12.667 │
│     50 │         -15.273 │         -15.833 │
│     60 │         -18.411 │         -18.537 │
│     70 │         -19.911 │         -22.687 │
│     80 │         -21.677 │         -23.750 │
│     90 │         -26.097 │         -26.667 │
│    100 │         -27.711 │         -30.400 │
│    110 │         -30.202 │         -31.667 │
│    120 │         -36.324 │         -33.778 │
└────────┴─────────────────┴─────────────────┘

📈 SCALING ANALYSIS:
   Pan scaling: 7.46x from zoom 10 to 120
   Tilt scaling: 6.76x from zoom 10 to 120
