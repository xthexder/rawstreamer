package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/xthexder/go-jack"
	"github.com/xthexder/rawstreamer"
)

var Client *jack.Client
var Ports []*jack.Port
var Buffers []unsafe.Pointer

func process(nframes uint32) int {
	lsamples := Ports[0].GetBuffer(nframes)
	rsamples := Ports[1].GetBuffer(nframes)
	for _, bufp := range Buffers {
		tmp := (*[]chan jack.AudioSample)(atomic.LoadPointer(&bufp))
		if tmp == nil {
			continue
		}
		buf := *tmp

		for i := 0; i < int(nframes); i++ {
			select {
			case buf[0] <- lsamples[i]:
				buf[1] <- rsamples[i]
			default:
				// fmt.Println("Blocking on connection:", n)
				break
			}
		}
		// fmt.Println("Chan size:", len(buf[0]))
	}
	return 0
}

func initPortMirror() {
	ports := Client.GetPorts("^"+mirror, Ports[0].GetType(), jack.PortIsInput)
	for i, name := range ports {
		if i < len(Ports) {
			port := Client.GetPortByName(name)
			connections := port.GetConnections()
			for _, conn := range connections {
				Client.Connect(conn, Ports[i].GetName())
			}
		}
	}
}

func updateProcs() {
	ports := Client.GetPorts("^alsa-jack\\.jackP\\.", Ports[0].GetType(), jack.PortIsOutput)
	procs := make(map[string]int)
	for _, name := range ports {
		clientName := strings.SplitN(name, ":", 2)[0]
		pidStr := strings.SplitN(clientName[16:], ".", 2)[0]
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}
		procs[clientName] = pid
	}
	for clientName, pid := range procs {
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
		if err != nil {
			fmt.Println("Error finding process:", err)
			return
		}
		proc := string(out)
		if strings.Contains(proc, procName) {
			ports = Client.GetPorts("^"+clientName+":", Ports[0].GetType(), jack.PortIsOutput)
			for i, name := range ports {
				if i < len(Ports) {
					Client.Connect(name, Ports[i].GetName())
				}
			}
		}
	}
}

func portRegistered(portId jack.PortId, registered bool) {
	if registered && !Client.IsPortMine(Client.GetPortById(portId)) {
		go updateProcs()
	}
}

func portConnect(portAId, portBId jack.PortId, connected bool) {
	portA := Client.GetPortById(portAId)
	portB := Client.GetPortById(portBId)
	if Client.IsPortMine(portB) {
		return
	}

	if len(mirror) > 0 && strings.HasPrefix(portB.GetName(), mirror) {
		clientName := portB.GetClientName()
		go func() {
			ports := Client.GetPorts("^"+clientName+":", Ports[0].GetType(), jack.PortIsInput)
			for i, name := range ports {
				if name == portB.GetName() && i < len(Ports) {
					if connected {
						Client.ConnectPorts(portA, Ports[i])
					} else {
						Client.DisconnectPorts(portA, Ports[i])
					}
					break
				}
			}
		}()
	}
}

func shutdown() {
	fmt.Println("Shutting down")
	os.Exit(1)
}

func streamConnection(conn *net.TCPConn) {
	defer conn.Close()
	bufi, buf := getBuffer()
	if buf == nil {
		conn.Write([]byte{'R', 0})
		conn.Close()
		return
	}

	for i := 0; i < len(buf); i++ {
		buf[i] = make(chan jack.AudioSample, 4096) // TODO: calculate this number
	}

	defer atomic.StorePointer(&Buffers[bufi], nil)

	numBytes := bits / 8
	conn.Write([]byte{'R', 1, formatFlag, byte(numBytes)})
	bytes := make([]byte, 4)
	endianness.PutUint32(bytes, Client.GetSampleRate())
	conn.Write(bytes[:4])
	bytes = make([]byte, bits/4)
	align := 8 % len(bytes)
	if align > 0 {
		// Add extra padding to make the header an even number of samples in length
		conn.Write(bytes[:len(bytes)-align])
	}

	if formatFlag&rawstreamer.EncodingMask == rawstreamer.EncodingFloatingPoint {
		for {
			lsample := <-buf[0]
			rsample := <-buf[1]

			bits := math.Float32bits(float32(lsample))
			endianness.PutUint32(bytes, bits)
			bits = math.Float32bits(float32(rsample))
			endianness.PutUint32(bytes[numBytes:], bits)
			_, err := conn.Write(bytes)
			if err != nil {
				return
			}
		}
	} else {
		mult := float32(math.Pow(2, float64(bits-1)))
		var offset float32 = -0.5 // Round float instead of floor
		if formatFlag&rawstreamer.EncodingMask == rawstreamer.EncodingUnsignedInt {
			offset = mult - 1.5
		}

		tmp := make([]byte, 8)
		for {
			lsample := uint32(float32(<-buf[0])*mult + offset)
			rsample := uint32(float32(<-buf[1])*mult + offset)

			endianness.PutUint32(tmp, lsample)
			endianness.PutUint32(tmp[4:], rsample)

			if endianness == binary.BigEndian {
				copy(bytes, tmp[4-numBytes:4])
				copy(bytes[numBytes:], tmp[8-numBytes:8])
			} else {
				copy(bytes, tmp[:numBytes])
				copy(bytes[numBytes:], tmp[4:4+numBytes])
			}
			_, err := conn.Write(bytes)
			if err != nil {
				return
			}
		}
	}
}

var bufSync sync.Mutex

func getBuffer() (int, []chan jack.AudioSample) {
	bufSync.Lock()
	defer bufSync.Unlock()

	for i, buf := range Buffers {
		if buf == nil {
			buf2 := make([]chan jack.AudioSample, len(Ports))
			atomic.StorePointer(&Buffers[i], unsafe.Pointer(&buf2))
			return i, buf2
		}
	}
	return -1, nil
}

func main() {
	if bits < 8 || bits > 32 || bits%8 != 0 {
		fmt.Println("Audio bits must be one of: 8, 16, 24, 32")
		return
	}
	formatFlag = 0
	for flag, str := range rawstreamer.EncodingString {
		if format == str {
			formatFlag = flag
			break
		}
	}
	if formatFlag == 0 {
		fmt.Printf("Unsupported stream format: %s\n", format)
		return
	}
	if formatFlag == rawstreamer.EncodingFloatingPoint {
		bits = 32
	}

	if bigEndian {
		endianness = binary.BigEndian
		formatFlag |= rawstreamer.EncodingBigEndian
	} else {
		endianness = binary.LittleEndian
		formatFlag |= rawstreamer.EncodingLittleEndian
	}

	var status int
	Client, status = jack.ClientOpen("Raw Streamer", jack.NoStartServer)
	if status != 0 {
		fmt.Println("Status:", status)
		return
	}
	defer Client.Close()

	if code := Client.SetProcessCallback(process); code != 0 {
		fmt.Printf("Failed to set process callback: %d\n", code)
		return
	}
	if code := Client.SetPortRegistrationCallback(portRegistered); code != 0 {
		fmt.Printf("Failed to set port registration callback: %d\n", code)
		return
	}
	if code := Client.SetPortConnectCallback(portConnect); code != 0 {
		fmt.Printf("Failed to set port connect callback: %d\n", code)
		return
	}
	Client.OnShutdown(shutdown)

	if code := Client.Activate(); code != 0 {
		fmt.Printf("Failed to activate client: %d\n", code)
		return
	}

	for i := 0; i < 2; i++ {
		port := Client.PortRegister(fmt.Sprintf("in_%d", i), jack.DEFAULT_AUDIO_TYPE, jack.PortIsInput, 0)
		Ports = append(Ports, port)
	}
	Buffers = make([]unsafe.Pointer, maxConns)

	if len(mirror) > 0 {
		initPortMirror()
	}
	if len(procName) > 0 {
		updateProcs()
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Printf("Error listening on address '%s': %v\n", addr, err)
		return
	} else {
		fmt.Printf("Listening on address: %s\n", ln.Addr().String())
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Printf("Error accepting connection: %v\n", err)
			continue
		}
		go streamConnection(conn.(*net.TCPConn))
	}
}

var formatFlag byte
var endianness binary.ByteOrder

var addr string
var maxConns int

var bits int
var format string
var bigEndian bool
var littleEndian bool

var mirror string
var procName string

func init() {
	flag.StringVar(&addr, "addr", ":5253", "Listen address")
	flag.IntVar(&maxConns, "max-conn", 128, "Maximum number of connected clients")

	flag.IntVar(&bits, "bits", 24, "Stream audio bit")
	flag.StringVar(&format, "format", "int", "Stream format (int, uint, float)")
	flag.BoolVar(&bigEndian, "big-endian", false, "Big-endian stream encoding")
	flag.BoolVar(&littleEndian, "little-endian", true, "Little-endian stream encoding (default)")

	flag.StringVar(&mirror, "mirror", "", "The name of a port to mirror (prefix matched)")
	flag.StringVar(&procName, "proc-name", "", "An alsa process to auto-connect to (substring matched)")
	flag.Parse()
}
