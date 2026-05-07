package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/netham45/magic4pc_altclient/m4p"
)

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
			log.Printf("Key: %i pressed: %b", key, pressed)
			switch key {
			case 415: // play
				ydoKey("XF86AudioPlay", state == "down")
			case 413: // stop
				ydoKey("XF86AudioStop", state == "down")
			case 0x13: // pause
				ydoKey("XF86AudioPause", state == "down")
			case 461: // back
				ydoClick("x1", state == "down")
			case 403: // red
				ydoKey("super", state == "down")
			case 404: // green
				ydoKey("Escape", state == "down")
			case 405: // yellow
				ydoKey("c", state == "down")
			case 406: // blue
				ydoClick("x2", state == "down")
			case 13: // Enter
				ydoKey("Return", state == "down")
			case 458: // GUIDE
				ydoClick("right", state == "down")
			default:
				if key < 1000 {
					ydoKey(fmt.Sprintf("%d", key), state == "down")
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
			fixedx := float64(coordinate[0]) * float64(1.00313479624) // 1080p
			fixedy := float64(coordinate[1]) * float64(1.00558659218)
			if coordinate[0] != 0 || coordinate[1] != 0 {
				ydoMove(int(fixedx), int(fixedy))
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
