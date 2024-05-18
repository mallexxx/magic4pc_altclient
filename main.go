package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/getlantern/systray"
	"github.com/go-vgo/robotgo"
	"github.com/netham45/magic4pc_altclient/m4p"
)

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	// Add a menu item with label "Exit" and action to exit the application
	exitMenuItem := systray.AddMenuItem("Exit", "Exit the application")

	// Set an action for the "Exit" menu item
	go func() {
		<-exitMenuItem.ClickedCh
		fmt.Println("Exiting...")
		systray.Quit()
	}()

	// Get the icon data
	iconData, err := getIconData("icon.ico")
	if err != nil {
		fmt.Println("Error loading icon data:", err)
		return
	}

	// Set the icon for the systray
	systray.SetIcon(iconData)

	// Set the tooltip text for the systray
	systray.SetTooltip("magic4pc")

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
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		select {
		case <-ctx.Done():
			fmt.Println("Exiting...")
			return
		default:

			if err := connect(ctx, dev); err != nil {
				fmt.Println("Failed to connect, retrying in 5 seconds...")
			}

			time.Sleep(5 * time.Second)
		}
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
				robotgo.KeyToggle("audio_play", state)
			case 0x13: // pause
				robotgo.KeyToggle("audio_pause", state)

			case 461: // back
				robotgo.Toggle("x1", state) // XBUTTON1 == Go Back

			case 403: // red
				robotgo.KeyToggle("cmd", state)

			case 404: // green
				robotgo.KeyToggle("escape", state)

			case 405: // yellow
				break

			case 406: // blue
				robotgo.Toggle("x2", state) // XBUTTON2 == Go Forward

			case 458: // GUIDE
				robotgo.Toggle("right", state) // Right click

			default:
				if key < 1000 {
					robotgo.KeyToggles(key, state)
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

			// fixedx := float64(coordinate[0]) * float64(1.00313479624) // Mouse only ranges from 0-1914, 0-1074, adjust to 0-1920, 0-1080
			// fixedy := float64(coordinate[1]) * float64(1.00558659218)
			fixedx := float64(coordinate[0]) * float64(0.668756530825496) // Mouse only ranges from 0-1914, 0-1074, adjust to 0-3840, 0-2160
			fixedy := float64(coordinate[1]) * float64(0.670391061452514)

			//log.Printf("%d x %d : fixedx: %f x %f", coordinate[0], coordinate[1], fixedx, fixedy)

			robotgo.Move(int(fixedx), int(fixedy))

		case m4p.MouseMessage:

			// fmt.Println("Mouse Message %s", m.Mouse.Type)
			switch m.Mouse.Type {
			case "mousedown":
				robotgo.Toggle("left", "down")
			case "mouseup":
				robotgo.Toggle("left", "up")
			}

		case m4p.WheelMessage:
			robotgo.Scroll(0, int(m.Wheel.Delta/60))
		default:
		}
	}
}

func onExit() {
	// Cleanup on exit, if needed
}

func getIconData(filename string) ([]byte, error) {
	// Open the PNG file
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Read the contents of the file
	iconData, err := ioutil.ReadAll(file)
	if err != nil {
		return nil, err
	}

	return iconData, nil
}
