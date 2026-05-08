package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/netham45/magic4pc_altclient/m4p"
)

// TV always sends coords in 1920×1080 space.
const tvWidth = 1920.0
const tvHeight = 1080.0

func main() {
	inputInit()
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
			log.Printf("Key: %d pressed: %v", key, pressed)
			switch key {
			case 37: // Left
				inputKey("Left", pressed)
			case 38: // Up
				inputKey("Up", pressed)
			case 39: // Right
				inputKey("Right", pressed)
			case 40: // Down
				inputKey("Down", pressed)
			case 415: // play
				inputKey("XF86AudioPlay", pressed)
			case 413: // stop
				inputKey("XF86AudioStop", pressed)
			case 0x13: // pause
				inputKey("XF86AudioPause", pressed)
			case 461: // back
				inputBackKey(pressed)
			case 403: // red — platform-specific (Steam menu / Super)
				inputRedKey(pressed)
			case 404: // green
				inputKey("Escape", pressed)
			case 33: // Ch Up
				inputKey("Prior", pressed)
			case 34: // Ch Down
				inputKey("Next", pressed)
			case 405: // yellow — platform-specific (Steam QAM / middle click)
				inputYellowKey(pressed)
			case 406: // blue → right click
				inputClick("right", pressed)
			case 13: // Enter
				inputKey("Return", pressed)
			case 458: // GUIDE
				inputClick("right", pressed)
			default:
				if key >= 32 && key < 127 {
					// ASCII range — send as character
					inputKey(strings.ToLower(string(rune(key))), pressed)
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
				inputMove(int(coordinate[0]), int(coordinate[1]))
			}

		case m4p.MouseMessage:
			switch m.Mouse.Type {
			case "mousedown":
				inputClick("left", true)
			case "mouseup":
				inputClick("left", false)
			}

		case m4p.WheelMessage:
			inputScroll(int(m.Wheel.Delta))

		default:
		}
	}
}
