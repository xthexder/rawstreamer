package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/xthexder/go-jack"
	"github.com/xthexder/rawstreamer"
)

var Client *jack.Client
var Ports []*jack.Port
var Buffers []unsafe.Pointer
var BufferPool chan []jack.AudioSample
var Listener net.Listener
var ClientWaitGroup sync.WaitGroup
var ShuttingDown chan struct{}

func process(nframes uint32) int {
	lsamples := Ports[0].GetBuffer(nframes)
	rsamples := Ports[1].GetBuffer(nframes)

	for client, bufp := range Buffers {
		buf := (*chan []jack.AudioSample)(atomic.LoadPointer(&bufp))
		if buf == nil {
			continue
		}

		n := 0
		for n < len(lsamples) {
			buffer := <-BufferPool
			nl := copy(buffer[:len(buffer)/2], lsamples[n:])
			buffer = buffer[:nl*2]
			copy(buffer[nl:], rsamples[n:])
			n += nl

			select {
			case *buf <- buffer:
			default:
				fmt.Println("Channel full for client:", client)
				n = len(lsamples)
			}
		}
	}
	return 0
}

func initPortMirror() {
	if len(mirror) <= 0 {
		return
	}

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

var updateLock sync.Mutex

func updateProcs() {
	if len(procName) <= 0 {
		return
	}
	updateLock.Lock()
	defer updateLock.Unlock()

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

func updateSources() {
	if len(source) <= 0 {
		return
	}
	updateLock.Lock()
	defer updateLock.Unlock()

	ports := Client.GetPorts("^"+source, Ports[0].GetType(), jack.PortIsOutput)
	sources := make(map[string][]string)
	for _, name := range ports {
		clientName := strings.SplitN(name, ":", 2)[0]
		sources[clientName] = append(sources[clientName], name)
	}
	for _, names := range sources {
		if len(names) == 1 { // Mono source
			for _, port := range Ports {
				Client.Connect(names[0], port.GetName())
			}
		} else {
			for i, name := range names {
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
		go updateSources()
	}
}

func disconnectClients() {
	for _, bufp := range Buffers {
		buf := (*chan []jack.AudioSample)(atomic.LoadPointer(&bufp))
		if buf == nil {
			continue
		}
		close(*buf)
	}
}

func sampleRateChanged(sampleRate uint32) int {
	printStreamInfo()
	disconnectClients()
	return 0
}

func bufferSizeChanged(bufferSize uint32) int {
	atomic.StoreUint32(&jackBufferSize, bufferSize*2)
	return 0
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
	close(ShuttingDown)
	Listener.Close()
	disconnectClients()
}

var ErrWriteTimeout = fmt.Errorf("write timeout")

func writeAligned(conn *net.TCPConn, buf []byte, timeout time.Time) error {
	conn.SetWriteDeadline(timeout)
	n, err := conn.Write(buf)
	if err != nil {
		if err2, ok := err.(*net.OpError); ok && err2.Timeout() {
			// Add extra padding to make sure we're still aligned
			align := n % (bits / 4)
			if align > 0 {
				fmt.Printf("Realigning: %d + %d\n", n, (bits/4)-align)
				conn.SetWriteDeadline(time.Time{})
				_, err = conn.Write(make([]byte, (bits/4)-align))
				if err != nil {
					return err
				}
			}
			return ErrWriteTimeout
		}
	}
	return err
}

func streamConnection(conn *net.TCPConn) {
	defer conn.Close()
	defer ClientWaitGroup.Done()
	bufi, buf := getBuffer()
	if buf == nil {
		conn.Write([]byte{'R', 0})
		conn.Close()
		return
	}

	defer atomic.StorePointer(&Buffers[bufi], nil)

	numBytes := bits / 8
	_, err := conn.Write([]byte{'R', 1, formatFlag, byte(numBytes)})
	if err != nil {
		return
	}
	bytes := make([]byte, 4)
	sampleRate := Client.GetSampleRate()
	endianness.PutUint32(bytes, sampleRate)
	_, err = conn.Write(bytes[:4])
	if err != nil {
		return
	}

	sample := make([]byte, bits/4)
	align := 8 % len(sample)
	if align > 0 {
		// Add extra padding to make the header an even number of samples in length
		_, err = conn.Write(sample[:len(sample)-align])
		if err != nil {
			return
		}
	}

	var lastSignal time.Time
	bytes = make([]byte, 0, int(Client.GetBufferSize())*2)
	for {
		buffer := <-buf
		if buffer == nil {
			return
		}

		lsamples := buffer[:len(buffer)/2]
		rsamples := buffer[len(buffer)/2:]

		signal := false
		for i := range lsamples {
			rawstreamer.WriteFloat32(sample[:numBytes], float32(lsamples[i]), formatFlag, endianness)
			rawstreamer.WriteFloat32(sample[numBytes:], float32(rsamples[i]), formatFlag, endianness)

			signal = signal || lsamples[i] != 0 || rsamples[i] != 0

			bytes = append(bytes, sample...)
		}
		if signal {
			lastSignal = time.Now()
		} else if time.Since(lastSignal) > 1*time.Second {
			if time.Since(lastSignal) <= 1*time.Minute {
				returnToPool(buffer)
				bytes = bytes[:0]
				continue
			}
			// Send some data for keepalive
			lastSignal = time.Now()
		}

		err = writeAligned(conn, bytes, time.Now().Add(bufferLen))
		if err == ErrWriteTimeout {
			fmt.Println("Write timeout!")

			// Skip forward the length of the timeout
			for i := 0; i <= cap(buf)/2; i++ {
				returnToPool(buffer)
				buffer = <-buf
			}
		} else if err != nil {
			fmt.Printf("Sample write error: %v\n", err)
			returnToPool(buffer)
			return
		}

		returnToPool(buffer)

		bytes = bytes[:0]
	}
}

var bufSync sync.Mutex

func getBufferChanSize() int {
	bufferSize := time.Duration(atomic.LoadUint32(&jackBufferSize) / 2)
	sampleRate := time.Duration(Client.GetSampleRate())
	return int(bufferLen*sampleRate/bufferSize/time.Second)*2 + 1
}

func getBuffer() (int, chan []jack.AudioSample) {
	bufSync.Lock()
	defer bufSync.Unlock()

	for i, bufp := range Buffers {
		buf := (*chan []jack.AudioSample)(atomic.LoadPointer(&bufp))
		if buf != nil {
			continue
		}
		buf2 := make(chan []jack.AudioSample, getBufferChanSize())
		atomic.StorePointer(&Buffers[i], unsafe.Pointer(&buf2))
		return i, buf2
	}
	return -1, nil
}

var jackBufferSize uint32

func initPool() {
	atomic.StoreUint32(&jackBufferSize, Client.GetBufferSize()*2)
	bufferSize := int(atomic.LoadUint32(&jackBufferSize))
	BufferPool = make(chan []jack.AudioSample, maxConns)
	for i := 0; i < cap(BufferPool); i++ {
		BufferPool <- make([]jack.AudioSample, bufferSize)
	}
}

func returnToPool(buffer []jack.AudioSample) {
	bufferSize := int(atomic.LoadUint32(&jackBufferSize))
	if cap(buffer) < bufferSize {
		BufferPool <- make([]jack.AudioSample, bufferSize)
	} else {
		BufferPool <- buffer[:bufferSize]
	}
}

func printStreamInfo() {
	fmt.Printf("Stream info: %dHz, ", Client.GetSampleRate())
	fmt.Printf("%dbit %s, ", bits, rawstreamer.EncodingString[formatFlag&rawstreamer.EncodingMask])
	fmt.Printf("%s, %v max buffer\n", endianness.String(), bufferLen)
}

func main() {
	ShuttingDown = make(chan struct{})

	if bits < 8 || bits > 32 || bits%8 != 0 {
		fmt.Println("Bit-depth must be one of: 8, 16, 24, 32")
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

	var err error
	bufferLen, err = time.ParseDuration(bufferStr)
	if err != nil {
		fmt.Printf("Invalid buffer length: %v\n", err)
		return
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
	if code := Client.SetSampleRateCallback(sampleRateChanged); code != 0 {
		fmt.Printf("Failed to set sample rate callback: %d\n", code)
		return
	}
	if code := Client.SetBufferSizeCallback(bufferSizeChanged); code != 0 {
		fmt.Printf("Failed to set buffer size callback: %d\n", code)
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
	initPool()

	initPortMirror()
	updateProcs()
	updateSources()

	Listener, err = net.Listen("tcp", addr)
	if err != nil {
		fmt.Printf("Error listening on address '%s': %v\n", addr, err)
		return
	} else {
		fmt.Printf("Listening on address: %s\n", Listener.Addr().String())
	}
	for {
		conn, err := Listener.Accept()
		if err != nil {
			select {
			case <-ShuttingDown:
				ClientWaitGroup.Wait()
				return
			default:
				fmt.Printf("Error accepting connection: %v\n", err)
				continue
			}
		}
		ClientWaitGroup.Add(1)
		go streamConnection(conn.(*net.TCPConn))
	}
}

var formatFlag byte
var endianness binary.ByteOrder
var bufferLen time.Duration

var addr string
var maxConns int

var bits int
var format string
var bigEndian bool
var littleEndian bool

var bufferStr string

var mirror string
var procName string
var source string

func init() {
	flag.StringVar(&addr, "addr", ":5253", "Listen address")
	flag.IntVar(&maxConns, "max-conn", 128, "Maximum number of connected clients")

	flag.IntVar(&bits, "bits", 24, "Stream bit-depth")
	flag.StringVar(&format, "format", "int", "Stream format (int, uint, float)")
	flag.BoolVar(&bigEndian, "big-endian", false, "Big-endian stream encoding")
	flag.BoolVar(&littleEndian, "little-endian", true, "Little-endian stream encoding (default)")

	flag.StringVar(&bufferStr, "buffer", "100ms", "Max buffer length")

	flag.StringVar(&mirror, "mirror", "", "The name of a port to mirror (prefix matched)")
	flag.StringVar(&procName, "proc-name", "", "An alsa process to auto-connect (substring matched)")
	flag.StringVar(&source, "source", "", "The name of a port to auto-connect (prefix matched)")
	flag.Parse()
}
