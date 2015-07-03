package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"github.com/xthexder/rawstreamer"

	"code.google.com/p/portaudio-go/portaudio"
)

var Endianness binary.ByteOrder
var Stream *portaudio.Stream
var Buffers []chan float32

func processAudio(out [][]float32) {
	for i := range out[0] {
		out[0][i] = <-Buffers[0]
		out[1][i] = <-Buffers[1]
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage:", os.Args[0], "hostname:port")
		os.Exit(1)
		return
	}
	addr := os.Args[1]
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		fmt.Println("Error connecting to server:", err)
		return
	}
	defer conn.Close()

	portaudio.Initialize()
	defer portaudio.Terminate()

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

	sampleRate := Endianness.Uint32(buf[4:])
	fmt.Printf("Streaming info: %dHz, ", sampleRate)
	fmt.Printf("32bit float, ")
	fmt.Printf("%s\n", Endianness.String())

	Buffers = []chan float32{
		make(chan float32, 1024), // TODO: Calculate this
		make(chan float32, 1024),
	}

	Stream, err = portaudio.OpenDefaultStream(0, 2, float64(sampleRate), 0, processAudio)
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

	var bits uint32
	for {
		_, err = io.ReadFull(conn, buf)
		if err != nil {
			fmt.Println("Error reading stream:", err)
			return
		}

		bits = Endianness.Uint32(buf)
		Buffers[0] <- math.Float32frombits(bits)
		bits = Endianness.Uint32(buf[4:])
		Buffers[1] <- math.Float32frombits(bits)
	}
}
