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
    "time"
    "github.com/go-vgo/robotgo"
    "github.com/netham45/magic4pc_altclient/m4p"
)

func main() {
    dev := m4p.DeviceInfo{IPAddr: "192.168.5.101", Port: 42831}
    for {
        ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
        defer cancel()
		run(ctx)
		time.Sleep(2 * time.Second)
        select {
        case <-ctx.Done():
            fmt.Println("Exiting...")
            return
		default:
		    connect(ctx, dev)
		    time.Sleep(2 * time.Second)
        }
    }
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
            key := m.Input.Parameters.KeyCode
            pressed := m.Input.Parameters.IsDown
            state := "up"
            if pressed {
                state = "down"
            }
            log.Printf("Key: %i pressed: %b", key, pressed)
            if key == 406 {
                robotgo.Toggle("right", state)
            }
            if key == 405 {
                robotgo.Toggle("center", state)
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

            fixedx := float64(coordinate[0]) * float64(1.00313479624)  // Mouse only ranges from 0-1914, 0-1074, adjust to 0-1920, 0-1080
            fixedy := float64(coordinate[1]) * float64(1.00558659218)         
            robotgo.Move(int(fixedx * 2),int(fixedy * 2))

        case m4p.MouseMessage:
            switch m.Mouse.Type {
            case "mousedown":
                robotgo.Toggle("left", "down")
            case "mouseup":
                robotgo.Toggle("left", "up")
            }

        case m4p.WheelMessage:
            robotgo.Scroll(0,int(m.Wheel.Delta/60))
        default:
        }
    }
}
