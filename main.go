package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"
	"io"
	"os/exec"
	"path/filepath"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/netham45/magic4pc_altclient/m4p"
)

func main() {
	initMoveWorker()
	c := make(chan os.Signal)
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
	listenAddr = "0.0.0.0:9105" // sleep UDP listen address
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

		// If a sleep command is received, put the system to sleep
		if string(buffer[:n]) == "sleep" {
			fmt.Println("Received sleep command. Putting system to sleep...")

			// Execute a Windows shell command to put the system to sleep
			cmd := exec.Command("cmd", "/C", "rundll32.exe powrprof.dll,SetSuspendState 0,1,0")
			err := cmd.Run()
			if err != nil {
				fmt.Println("Error executing sleep command:", err)
				continue
			}
			fmt.Println("System is now in sleep mode.")
		}
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
				ydoClick("x1", state == "down") // XBUTTON1 == Go Back

			case 403: // red
				ydoKey("super", state == "down")

			case 404: // green
				ydoKey("Escape", state == "down")

			case 405: // yellow
				ydoKey("c", state == "down")

			case 406: // blue
				ydoClick("x2", state == "down") // XBUTTON2 == Go Forward

			case 13: // Enter
				ydoKey("Return", state == "down")

			case 458: // GUIDE
				ydoClick("right", state == "down") // Right click

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
			// fixedx := float64(coordinate[0]) * float64(0.668756530825496) // 4K // Mouse only ranges from 0-1914, 0-1074, adjust to 0-3840, 0-2160
			// fixedy := float64(coordinate[1]) * float64(0.670391061452514)

			//log.Printf("%d x %d : fixedx: %f x %f", coordinate[0], coordinate[1], fixedx, fixedy)

			if coordinate[0] != 0 || coordinate[1] != 0 { ydoMove(int(fixedx), int(fixedy)) }

		case m4p.MouseMessage:

			// fmt.Println("Mouse Message %s", m.Mouse.Type)
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

// moveCh — координаты для xdotool, буфер 1 (старые выбрасываем)
var moveCh = make(chan [2]int, 1)

// getXauth читает XAUTHORITY из cmdline kwin_wayland
func getXauth() string {
	matches, _ := filepath.Glob("/proc/*/cmdline")
	for _, f := range matches {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		args := bytes.Split(data, []byte{0})
		for i, a := range args {
			if string(a) == "--xwayland-xauthority" && i+1 < len(args) {
				return string(args[i+1])
			}
		}
	}
	return ""
}

func initMoveWorker() {
	go func() {
		var xdoStdin io.WriteCloser
		var xdoCmd *exec.Cmd

		startXdotool := func() {
			if xdoStdin != nil {
				xdoStdin.Close()
			}
			xauth := getXauth()
			cmd := exec.Command("/usr/bin/xdotool", "-")
			cmd.Env = append(os.Environ(), "DISPLAY=:0", "XAUTHORITY="+xauth)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				log.Printf("xdotool stdin pipe error: %v", err)
				return
			}
			if err := cmd.Start(); err != nil {
				log.Printf("xdotool start error: %v", err)
				return
			}
			xdoCmd = cmd
			xdoStdin = stdin
			log.Printf("xdotool started, XAUTHORITY=%s", xauth)
		}

		startXdotool()

		for pos := range moveCh {
			if xdoStdin == nil {
				startXdotool()
				if xdoStdin == nil {
					continue
				}
			}
			_, err := fmt.Fprintf(xdoStdin, "mousemove %d %d\n", pos[0], pos[1])
			if err != nil {
				log.Printf("xdotool write error: %v, restarting", err)
				if xdoCmd != nil {
					xdoCmd.Wait()
				}
				xdoStdin = nil
				startXdotool()
				if xdoStdin != nil {
					fmt.Fprintf(xdoStdin, "mousemove %d %d\n", pos[0], pos[1])
				}
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

func ydoCmd(args ...string) {
	cmd := exec.Command("/usr/bin/xdotool", args...)
	xauth := getXauth()
	cmd.Env = append(os.Environ(), "DISPLAY=:0", "XAUTHORITY="+xauth)
	if err := cmd.Run(); err != nil {
		log.Printf("xdotool %v error: %v", args, err)
	}
}

func ydoKey(key string, down bool) {
	if down {
		ydoCmd("keydown", key)
	} else {
		ydoCmd("keyup", key)
	}
}

func ydoClick(button string, down bool) {
	if button == "left" {
		if down {
			ydoCmd("mousedown", "1")
		} else {
			ydoCmd("mouseup", "1")
		}
	} else if button == "right" {
		if down {
			ydoCmd("mousedown", "3")
		} else {
			ydoCmd("mouseup", "3")
		}
	} else if button == "x1" {
		if down {
			ydoCmd("mousedown", "8")
		} else {
			ydoCmd("mouseup", "8")
		}
	} else if button == "x2" {
		if down {
			ydoCmd("mousedown", "9")
		} else {
			ydoCmd("mouseup", "9")
		}
	}
}

func ydoScroll(delta int) {
	if delta > 0 {
		ydoCmd("click", "4")
	} else {
		ydoCmd("click", "5")
	}
}
