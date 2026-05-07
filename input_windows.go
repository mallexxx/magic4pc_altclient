//go:build windows

package main

import (
	"log"
	"math"

	"github.com/go-vgo/robotgo"
)

// inputInit — no-op on Windows (robotgo needs no daemon).
func inputInit() {}

// inputMove scales TV coords to actual screen size and moves the mouse.
func inputMove(x, y int) {
	sw, sh := robotgo.GetScreenSize()
	sx := float64(sw) / tvWidth
	sy := float64(sh) / tvHeight
	fx := int(math.Round(float64(x) * sx))
	fy := int(math.Round(float64(y) * sy))
	robotgo.Move(fx, fy)
}

// inputKey sends a key down or up event via robotgo.
func inputKey(key string, down bool) {
	state := "up"
	if down {
		state = "down"
	}
	if err := robotgo.Toggle(key, state); err != nil {
		log.Printf("inputKey %s %s: %v", key, state, err)
	}
}

// inputClick sends a mouse button down or up event via robotgo.
func inputClick(button string, down bool) {
	state := "up"
	if down {
		state = "down"
	}
	switch button {
	case "left":
		robotgo.Toggle("left", state)
	case "right":
		robotgo.Toggle("right", state)
	case "x1":
		robotgo.Toggle("center", state) // back button → middle on Windows (original mapping)
	case "x2":
		robotgo.Toggle("right", state)
	}
}

// inputScroll sends a scroll event via robotgo.
func inputScroll(delta int) {
	if delta > 0 {
		robotgo.Scroll(0, -1)
	} else {
		robotgo.Scroll(0, 1)
	}
}

// inputRedKey: Super (Win key) on Windows.
func inputRedKey(pressed bool) {
	state := "up"
	if pressed {
		state = "down"
	}
	robotgo.Toggle("super", state)
}

// inputYellowKey: middle click on Windows (original mapping).
func inputYellowKey(pressed bool) {
	state := "up"
	if pressed {
		state = "down"
	}
	robotgo.Toggle("center", state)
}
