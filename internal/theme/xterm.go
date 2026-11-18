package theme

import "math"

// cubeLevels are the channel values of the xterm 6x6x6 color cube.
var cubeLevels = [6]uint8{0, 95, 135, 175, 215, 255}

// XtermRGB returns the RGB value of an xterm-256 index in 16–255: indexes
// 16–231 are the 6x6x6 cube and 232–255 the grey ramp. Indexes outside that
// range return black; the basic 16 are excluded because terminal themes remap
// them.
func XtermRGB(index int) (r, g, b uint8) {
	switch {
	case index >= 16 && index <= 231:
		i := index - 16
		return cubeLevels[i/36], cubeLevels[(i/6)%6], cubeLevels[i%6]
	case index >= 232 && index <= 255:
		v := uint8(8 + 10*(index-232))
		return v, v, v
	}
	return 0, 0, 0
}

// ContrastFG returns 16 (black) or 231 (white), whichever contrasts better
// with the given xterm-256 background index.
func ContrastFG(index int) int {
	r, g, b := XtermRGB(index)
	return contrastFor(r, g, b)
}

// contrastFor picks black or white text for a background. Black has the higher
// WCAG contrast ratio exactly when the background's relative luminance exceeds
// sqrt(1.05*0.05)-0.05, about 0.179.
func contrastFor(r, g, b uint8) int {
	if relativeLuminance(r, g, b) > 0.179 {
		return 16
	}
	return 231
}

// relativeLuminance is the sRGB relative luminance of a color, 0 to 1.
func relativeLuminance(r, g, b uint8) float64 {
	lin := func(c uint8) float64 {
		v := float64(c) / 255
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}
