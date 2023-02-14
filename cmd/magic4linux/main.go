package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-vgo/robotgo"

	"github.com/mafredri/magic4linux/m4p"
)

const (
	broadcastPort    = 42830
	subscriptionPort = 42831
)

func main() {
	ctx,cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	run(ctx)
}

func run(ctx context.Context) error {

    dev := m4p.DeviceInfo{IPAddr: "192.168.5.101", Port: 42831}
	return connect(ctx, dev)
}

func connect(ctx context.Context, dev m4p.DeviceInfo) error {
	addr := fmt.Sprintf("%s:%d", dev.IPAddr, dev.Port)
	log.Printf("connect: connecting to: %s", addr)

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
			log.Printf("connect: got %s: %v", m.Type, m.Input)

			key := m.Input.Parameters.KeyCode
			pressed := m.Input.Parameters.IsDown
			log.Printf("Key: %i ressed: %b", key, pressed)
			if key == 406 {
				robotgo.Click("right", pressed)
			}
			
		case m4p.RemoteUpdateMessage:
			// log.Printf("connect: got %s: %s", m.Type, hex.EncodeToString(m.RemoteUpdate.Payload))

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

			x := coordinate[0]
			y := coordinate[1]
			//fmt.Println("Move mouse", x, y)
			robotgo.Move(int(x),int(y))
			
			// log.Printf("connect: %d %d %#v %#v %#v %#v", returnValue, deviceID, coordinate, gyroscope, acceleration, quaternion)

		case m4p.MouseMessage:
		    l//og.Printf("Type: %s", m.Mouse.Type)
			switch m.Mouse.Type {
			case "mousedown":
			    robotgo.Click("left", true)
			case "mouseup":
			    robotgo.Click("left", false)
			}

		case m4p.WheelMessage:
		   // log.Printf("WHEEEEEEEL %i", m.Wheel.Delta)
			robotgo.Scroll(0,int(m.Wheel.Delta/60))
		default:
		}
	}
}
