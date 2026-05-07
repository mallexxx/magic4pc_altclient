package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/netham45/magic4pc_altclient/m4p"
)

// TV always sends coords in 1920×1080 space.
const tvWidth = 1920.0
const tvHeight = 1080.0

// scaleX/scaleY are updated after each xdotool start (actual screen size / TV space).
var scaleX atomic.Value // float64
var scaleY atomic.Value // float64

func init() {
	scaleX.Store(1.0)
	scaleY.Store(1.0)
}

func main() {
	initXdoWorker()
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		os.Exit(1)
	}()

	go startUDPListener()

	ipAddr := "192.168.1.75"
	port := 42831

	if len(os.Args) > 1 {
		ipAddr = os.Args[1]
		if len(os.Args) > 2 {
			var err error
			port, err = strconv.Atoi(os.Args[2])
			if err != nil {
				log.Fatalf("invalid port: %v", err)
			}
		}
	}

	dev := m4p.DeviceInfo{IPAddr: ipAddr, Port: port}
	for {
		if err := connect(context.Background(), dev); err != nil {
			if err == context.Canceled {
				fmt.Println("Exiting 2...")
			}
			fmt.Println("Failed to connect,", err, "retrying in 2 seconds...")
		}
		time.Sleep(2 * time.Second)
	}
}

const (
	listenAddr = "0.0.0.0:9105"
)

func startUDPListener() {
	udpAddr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		fmt.Println("Error resolving UDP address:", err)
		os.Exit(1)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		fmt.Println("Error listening:", err)
		os.Exit(1)
	}
	defer conn.Close()
	fmt.Println("UDP server started. Listening on", listenAddr)
	buffer := make([]byte, 1024)
	for {
		n, addr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			fmt.Println("Error reading from UDP:", err)
			continue
		}
		fmt.Printf("Received UDP packet from %s: %s\n", addr.String(), string(buffer[:n]))
	}
}

func connect(ctx context.Context, dev m4p.DeviceInfo) error {
	addr := fmt.Sprintf("%s:%d", dev.IPAddr, dev.Port)
	log.Printf("hola! connecting to: %s", addr)

	client, err := m4p.Dial(ctx, addr)
	if err != nil {
		return err
	}
	defer client.Close()

	for {
		m, err := client.Recv(ctx)
		if err != nil {
			return err
		}

		switch m.Type {
		case m4p.InputMessage:
			key := m.Input.Parameters.KeyCode
			pressed := m.Input.Parameters.IsDown
			state := "up"
			if pressed {
				state = "down"
			}
			log.Printf("Key: %d pressed: %v", key, pressed)
			switch key {
			case 37: // Left
				ydoKey("Left", state == "down")
			case 38: // Up
				ydoKey("Up", state == "down")
			case 39: // Right
				ydoKey("Right", state == "down")
			case 40: // Down
				ydoKey("Down", state == "down")
			case 415: // play
				ydoKey("XF86AudioPlay", state == "down")
			case 413: // stop
				ydoKey("XF86AudioStop", state == "down")
			case 0x13: // pause
				ydoKey("XF86AudioPause", state == "down")
			case 461: // back
				ydoClick("x1", state == "down")
			case 403: // red → Steam menu in gamescope, Super in KDE
				if state == "down" {
					if isGamescopeSession() {
						go sendSteamMenu()
					} else {
						ydoKey("super", true)
					}
				} else {
					if !isGamescopeSession() {
						ydoKey("super", false)
					}
				}
			case 404: // green
				ydoKey("Escape", state == "down")
			case 33: // Ch Up
				ydoKey("Prior", state == "down")
			case 34: // Ch Down
				ydoKey("Next", state == "down")
			case 405: // yellow → Ctrl+2 (Steam QAM) in gamescope, middle click in KDE
				if state == "down" {
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
			case 406: // blue → right click
				ydoClick("right", state == "down")
			case 13: // Enter
				ydoKey("Return", state == "down")
			case 458: // GUIDE
				ydoClick("right", state == "down")
			default:
				if key >= 32 && key < 127 {
					// ASCII range — send as character, not raw keycode
					ydoKey(string(rune(key)), state == "down")
				}
			}

		case m4p.RemoteUpdateMessage:
			r := bytes.NewReader(m.RemoteUpdate.Payload)
			var returnValue, deviceID uint8
			var coordinate [2]int32
			var gyroscope, acceleration [3]float32
			var quaternion [4]float32
			for _, fn := range []func() error{
				func() error { return binary.Read(r, binary.LittleEndian, &returnValue) },
				func() error { return binary.Read(r, binary.LittleEndian, &deviceID) },
				func() error { return binary.Read(r, binary.LittleEndian, coordinate[:]) },
				func() error { return binary.Read(r, binary.LittleEndian, gyroscope[:]) },
				func() error { return binary.Read(r, binary.LittleEndian, acceleration[:]) },
				func() error { return binary.Read(r, binary.LittleEndian, quaternion[:]) },
			} {
				if err := fn(); err != nil {
					log.Printf("connect: %s decode failed: %v", m.Type, err)
					break
				}
			}
			if coordinate[0] != 0 || coordinate[1] != 0 {
				sx := scaleX.Load().(float64)
				sy := scaleY.Load().(float64)
				ydoMove(int(math.Round(float64(coordinate[0])*sx)), int(math.Round(float64(coordinate[1])*sy)))
			}

		case m4p.MouseMessage:
			switch m.Mouse.Type {
			case "mousedown":
				ydoClick("left", true)
			case "mouseup":
				ydoClick("left", false)
			}

		case m4p.WheelMessage:
			ydoScroll(int(m.Wheel.Delta))

		default:
		}
	}
}

// --- Single persistent xdotool process ---

// cmdCh carries all xdotool commands (mouse moves + buttons/keys).
// Buffered: moves drop old coords, cmds have headroom.
var moveCh = make(chan [2]int, 1)
var cmdCh = make(chan string, 64)

// getXDisplay returns the current active X display number and xauth path.
// Prefers KDE/kwin_wayland, falls back to first available socket.
func getXDisplay() (display string, xauth string) {
	// Try to find kwin_wayland and its --xwayland-display / --xwayland-xauthority args
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

	// Fallback xauth: kwin xauthority arg only
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

// getDisplaySize queries the actual screen dimensions via xdotool.
// Falls back to tvWidth×tvHeight (scale=1) on error.
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
		log.Printf("getdisplaygeometry unexpected output: %q, using default scale", out)
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


// isGamescopeSession returns true if we're running in gamescope (no kwin_wayland).
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
// Only fires on press (not release) to avoid double-toggle.
func sendSteamMenu() {
	cmd := exec.Command("/usr/bin/ydotool", "key", "29:1", "2:1", "2:0", "29:0")
	cmd.Env = append(os.Environ(), "YDOTOOL_SOCKET=/run/user/1000/.ydotool_socket")
	if err := cmd.Run(); err != nil {
		log.Printf("sendSteamMenu: %v", err)
	}
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
					// xdotool not running — try to start
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
				// broken pipe — close and retry after delay
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

func ydoMove(x, y int) {
	if x == 0 && y == 0 {
		return
	}
	select {
	case moveCh <- [2]int{x, y}:
	default:
		select {
		case <-moveCh:
		default:
		}
		moveCh <- [2]int{x, y}
	}
}

// ydoCmd sends to the single persistent xdotool — never spawns a new process.
func ydoCmd(args ...string) {
	cmdCh <- strings.Join(args, " ")
}

func ydoKey(key string, down bool) {
	if down {
		ydoCmd("keydown", key)
	} else {
		ydoCmd("keyup", key)
	}
}

func ydoClick(button string, down bool) {
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

func ydoScroll(delta int) {
	if delta > 0 {
		ydoCmd("click", "4")
	} else {
		ydoCmd("click", "5")
	}
}
