package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"time"
	"github.com/xthexder/rawstreamer"

	"code.google.com/p/portaudio-go/portaudio"
)

var Endianness binary.ByteOrder
var Stream *portaudio.Stream
var Buffers []chan float32
var Started chan struct{}

func processAudio(out [][]float32) {
	select {
	case <-Started:
		for i := range out[0] {
			out[0][i] = <-Buffers[0]
			out[1][i] = <-Buffers[1]
		}
	default:
		for i := range out[0] {
			out[0][i] = 0
			out[1][i] = 0
		}
	}
}

func getChannelBufferSize(bufferLen time.Duration, bufferSize, sampleRate uint32) int {
	return int(bufferLen*time.Duration(sampleRate)/time.Second) + 1
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage:", os.Args[0], "hostname:port [buffer-duration]")
		os.Exit(1)
		return
	}

	portaudio.Initialize()
	defer portaudio.Terminate()

	addr := os.Args[1]
	bufferStr := "100ms"
	if len(os.Args) > 2 {
		bufferStr = os.Args[2]
	}
	bufferLen, err := time.ParseDuration(bufferStr)
	if err != nil {
		fmt.Printf("Invalid buffer length: %v\n", err)
		return
	}

	Started = make(chan struct{})

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		fmt.Println("Error connecting to server:", err)
		return
	}
	defer conn.Close()

	buf := make([]byte, 8)
	_, err = io.ReadFull(conn, buf)
	if err != nil {
		fmt.Println("Error reading headers:", err)
		return
	}

	if buf[0] != 'R' {
		fmt.Printf("Invalid header start: %x\n", buf[0])
		return
	} else if buf[1] == 0 {
		fmt.Println("Server is full")
		return
	} else if buf[1] != 1 {
		fmt.Printf("Unsupported protocol version: %d\n", buf[1])
		return
	}

	flags := buf[2]
	if flags&rawstreamer.EncodingLittleEndian != 0 {
		Endianness = binary.LittleEndian
	} else if flags&rawstreamer.EncodingBigEndian != 0 {
		Endianness = binary.BigEndian
	} else {
		fmt.Println("Encoding endianness not specified!")
		return
	}

	encoding := flags & rawstreamer.EncodingMask

	numBytes := int(buf[3])
	bits := numBytes * 8
	if numBytes < 1 || numBytes > 4 {
		fmt.Printf("Unsupported number of bits: %d\n", bits)
		return
	}
	if encoding == rawstreamer.EncodingFloatingPoint {
		numBytes = 4
		bits = 32
	}

	sampleRate := Endianness.Uint32(buf[4:])
	fmt.Printf("Streaming info: %dHz, ", sampleRate)
	fmt.Printf("%dbit %s, ", bits, rawstreamer.EncodingString[encoding])
	fmt.Printf("%s, %v buffer\n", Endianness.String(), bufferLen)

	bufferSize := sampleRate / 50 // 20ms buffer size

	Buffers = []chan float32{
		make(chan float32, getChannelBufferSize(bufferLen, bufferSize, sampleRate)),
		make(chan float32, getChannelBufferSize(bufferLen, bufferSize, sampleRate)),
	}

	Stream, err = portaudio.OpenDefaultStream(0, 2, float64(sampleRate), int(bufferSize), processAudio)
	defer Stream.Close()
	err = Stream.Start()
	if err != nil {
		fmt.Println("Failed to start stream:", err)
		return
	}
	defer func() {
		Stream.Stop()
		close(Buffers[0])
		close(Buffers[1])
	}()

	buf = make([]byte, int(bufferSize)*numBytes*2)
	align := 8 % (numBytes * 2)
	if align > 0 {
		// Read in the extra padding
		_, err = io.ReadFull(conn, buf[:(numBytes*2)-align])
		if err != nil {
			fmt.Println("Error reading headers:", err)
			return
		}
	}

	remainder := 0
	bufferFill := getChannelBufferSize(bufferLen, bufferSize, sampleRate)
	for {
		n, err := io.ReadAtLeast(conn, buf[remainder:], numBytes*2-remainder)
		if err != nil {
			fmt.Println("Error reading stream:", err)
			return
		}
		remainder = n % (numBytes * 2)

		for i := 0; i < (n - remainder); i += numBytes * 2 {
			if bufferFill > 0 {
				bufferFill--
				if bufferFill == 0 {
					close(Started)
				}
			}
			Buffers[0] <- rawstreamer.ReadFloat32(buf[i:i+numBytes], flags, Endianness)
			Buffers[1] <- rawstreamer.ReadFloat32(buf[i+numBytes:i+numBytes*2], flags, Endianness)
		}

		copy(buf, buf[n-remainder:n])
	}
}
