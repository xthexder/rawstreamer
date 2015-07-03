package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"net"
	"os"
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
	for n, bufp := range Buffers {
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
				fmt.Println("Blocking on connection:", n)
				break
			}
		}
	}
	return 0
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
		buf[i] = make(chan jack.AudioSample, 1024) // TODO: calculate this number
	}

	defer atomic.StorePointer(&Buffers[bufi], nil)

	bytes := make([]byte, 8)
	conn.Write([]byte{'R', 1, rawstreamer.EncodingFloatingPoint | rawstreamer.EncodingLittleEndian, 32})
	binary.LittleEndian.PutUint32(bytes, Client.GetSampleRate())
	conn.Write(bytes[0:4])
	// TODO: Support different bitrates
	for {
		lsample := <-buf[0]
		rsample := <-buf[1]
		bits := math.Float32bits(float32(lsample))
		binary.LittleEndian.PutUint32(bytes, bits)
		bits = math.Float32bits(float32(rsample))
		binary.LittleEndian.PutUint32(bytes[4:], bits)
		_, err := conn.Write(bytes)
		if err != nil {
			return
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

var addr string
var maxConns int

func init() {
	flag.StringVar(&addr, "addr", ":5253", "Listen address")
	flag.IntVar(&maxConns, "max-conn", 128, "Maximum number of connected clients")
	flag.Parse()
}
