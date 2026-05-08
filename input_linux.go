//go:build linux

package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// scaleX/scaleY: actual screen size / TV coordinate space (1920×1080).
// Updated after each xdotool start via getdisplaygeometry.
var scaleX atomic.Value // float64
var scaleY atomic.Value // float64

func init() {
	scaleX.Store(1.0)
	scaleY.Store(1.0)
}

// cmdCh carries all xdotool commands (keys, clicks).
// moveCh carries mouse moves — buffered 1, drops stale coords.
var moveCh = make(chan [2]int, 1)
var cmdCh = make(chan string, 64)

// inputInit starts the persistent xdotool worker goroutine.
func inputInit() {
	initXdoWorker()
}

// inputMove scales TV coords to actual screen size and sends to xdotool.
func inputMove(x, y int) {
	sx := scaleX.Load().(float64)
	sy := scaleY.Load().(float64)
	fx := int(math.Round(float64(x) * sx))
	fy := int(math.Round(float64(y) * sy))
	if fx == 0 && fy == 0 {
		return
	}
	select {
	case moveCh <- [2]int{fx, fy}:
	default:
		select {
		case <-moveCh:
		default:
		}
		moveCh <- [2]int{fx, fy}
	}
}

// inputKey sends a key down or up event via xdotool.
func inputKey(key string, down bool) {
	if down {
		ydoCmd("keydown", key)
	} else {
		ydoCmd("keyup", key)
	}
}

// inputClick sends a mouse button down or up event via xdotool.
func inputClick(button string, down bool) {
	var btn string
	switch button {
	case "left":
		btn = "1"
	case "right":
		btn = "3"
	case "x1":
		btn = "8"
	case "x2":
		btn = "9"
	default:
		return
	}
	if down {
		ydoCmd("mousedown", btn)
	} else {
		ydoCmd("mouseup", btn)
	}
}

// inputScroll sends a scroll event via xdotool.
func inputScroll(delta int) {
	if delta > 0 {
		ydoCmd("click", "4")
	} else {
		ydoCmd("click", "5")
	}
}

// inputBackKey: Esc in gamescope (Steam Big Picture), x1 mouse button in KDE.
func inputBackKey(pressed bool) {
	if isGamescopeSession() {
		inputKey("Escape", pressed)
	} else {
		inputClick("x1", pressed)
	}
}

// inputRedKey: Ctrl+1 (Steam menu) in gamescope, Super in KDE.
func inputRedKey(pressed bool) {
	if pressed {
		if isGamescopeSession() {
			go sendSteamMenu()
		} else {
			inputKey("super", true)
		}
	} else {
		if !isGamescopeSession() {
			inputKey("super", false)
		}
	}
}

// inputYellowKey: Ctrl+2 (Steam QAM) in gamescope, middle click in KDE.
func inputYellowKey(pressed bool) {
	if pressed {
		if isGamescopeSession() {
			go func() {
				cmd := exec.Command("/usr/bin/ydotool", "key", "29:1", "3:1", "3:0", "29:0")
				cmd.Env = append(os.Environ(), "YDOTOOL_SOCKET=/run/user/1000/.ydotool_socket")
				if err := cmd.Run(); err != nil {
					log.Printf("sendQAM: %v", err)
				}
			}()
		} else {
			ydoCmd("mousedown 2")
		}
	} else {
		if !isGamescopeSession() {
			ydoCmd("mouseup 2")
		}
	}
}

// --- xdotool internals ---

func ydoCmd(args ...string) {
	cmdCh <- strings.Join(args, " ")
}

// isGamescopeSession returns true when kwin_wayland is not running (i.e. we're in gamescope).
func isGamescopeSession() bool {
	matches, _ := filepath.Glob("/proc/*/cmdline")
	for _, f := range matches {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if bytes.Contains(data, []byte("kwin_wayland")) {
			return false
		}
	}
	return true
}

// sendSteamMenu sends Ctrl+1 via ydotool — opens/closes Steam menu in gamescope.
func sendSteamMenu() {
	cmd := exec.Command("/usr/bin/ydotool", "key", "29:1", "2:1", "2:0", "29:0")
	cmd.Env = append(os.Environ(), "YDOTOOL_SOCKET=/run/user/1000/.ydotool_socket")
	if err := cmd.Run(); err != nil {
		log.Printf("sendSteamMenu: %v", err)
	}
}

// getXDisplay returns the active Xwayland display and xauth path by inspecting /proc.
// Prefers kwin_wayland args, falls back to first /tmp/.X11-unix socket.
func getXDisplay() (display string, xauth string) {
	matches, _ := filepath.Glob("/proc/*/cmdline")
	for _, f := range matches {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		args := bytes.Split(data, []byte{0})
		isKwin := false
		for _, a := range args {
			if strings.Contains(string(a), "kwin_wayland") {
				isKwin = true
				break
			}
		}
		if !isKwin {
			continue
		}
		for i, a := range args {
			switch string(a) {
			case "--xwayland-display":
				if i+1 < len(args) {
					display = string(args[i+1])
				}
			case "--xwayland-xauthority":
				if i+1 < len(args) {
					xauth = string(args[i+1])
				}
			}
		}
		if display != "" {
			return display, xauth
		}
	}

	// Fallback: first X socket
	sockets, _ := filepath.Glob("/tmp/.X11-unix/X*")
	if len(sockets) > 0 {
		num := strings.TrimPrefix(filepath.Base(sockets[0]), "X")
		display = ":" + num
	}

	// Fallback xauth
	for _, f := range matches {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		args := bytes.Split(data, []byte{0})
		for i, a := range args {
			if string(a) == "--xwayland-xauthority" && i+1 < len(args) {
				xauth = string(args[i+1])
				return display, xauth
			}
		}
	}
	return display, xauth
}

// getDisplaySize queries actual screen dimensions via xdotool getdisplaygeometry.
func getDisplaySize(disp, xauth string) (w, h int) {
	cmd := exec.Command("/usr/bin/xdotool", "getdisplaygeometry")
	cmd.Env = append(os.Environ(), "DISPLAY="+disp, "XAUTHORITY="+xauth)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("getdisplaygeometry failed: %v, using default scale", err)
		return int(tvWidth), int(tvHeight)
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) != 2 {
		log.Printf("getdisplaygeometry unexpected output: %q", out)
		return int(tvWidth), int(tvHeight)
	}
	w, _ = strconv.Atoi(parts[0])
	h, _ = strconv.Atoi(parts[1])
	if w == 0 || h == 0 {
		return int(tvWidth), int(tvHeight)
	}
	return w, h
}

func updateScale(disp, xauth string) {
	w, h := getDisplaySize(disp, xauth)
	sx := float64(w) / tvWidth
	sy := float64(h) / tvHeight
	scaleX.Store(sx)
	scaleY.Store(sy)
	log.Printf("screen size: %dx%d → scale %.4f×%.4f", w, h, sx, sy)
}

func initXdoWorker() {
	go func() {
		var (
			mu    sync.Mutex
			stdin io.WriteCloser
			cmd   *exec.Cmd
		)

		startXdotool := func(disp, xauth string) {
			mu.Lock()
			defer mu.Unlock()
			if stdin != nil {
				stdin.Close()
				stdin = nil
			}
			if cmd != nil {
				cmd.Wait()
				cmd = nil
			}
			c := exec.Command("/usr/bin/xdotool", "-")
			c.Env = append(os.Environ(), "DISPLAY="+disp, "XAUTHORITY="+xauth)
			s, err := c.StdinPipe()
			if err != nil {
				log.Printf("xdotool StdinPipe: %v", err)
				return
			}
			if err := c.Start(); err != nil {
				log.Printf("xdotool Start: %v", err)
				return
			}
			cmd = c
			stdin = s
			log.Printf("xdotool started on %s", disp)
			go updateScale(disp, xauth)
		}

		write := func(line string) {
			for attempts := 0; attempts < 3; attempts++ {
				mu.Lock()
				s := stdin
				mu.Unlock()
				if s == nil {
					time.Sleep(time.Duration(500+attempts*500) * time.Millisecond)
					disp, xauth := getXDisplay()
					if disp == "" {
						disp = ":0"
					}
					startXdotool(disp, xauth)
					continue
				}
				if _, err := fmt.Fprintf(s, "%s\n", line); err == nil {
					return
				}
				log.Printf("xdotool write error, retrying in 1s...")
				mu.Lock()
				if stdin != nil {
					stdin.Close()
					stdin = nil
				}
				if cmd != nil {
					cmd.Wait()
					cmd = nil
				}
				mu.Unlock()
				time.Sleep(time.Second)
			}
		}

		// Initial start
		disp, xauth := getXDisplay()
		if disp == "" {
			disp = ":0"
		}
		startXdotool(disp, xauth)

		for {
			select {
			case pos := <-moveCh:
				write(fmt.Sprintf("mousemove %d %d", pos[0], pos[1]))
			case line := <-cmdCh:
				write(line)
			}
		}
	}()
}
