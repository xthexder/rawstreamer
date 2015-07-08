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

	buf = make([]byte, numBytes*2)
	align := 8 % len(buf)
	if align > 0 {
		// Read in the extra padding
		_, err = io.ReadFull(conn, buf[:len(buf)-align])
		if err != nil {
			fmt.Println("Error reading headers:", err)
			return
		}
	}

	tmp := make([]byte, 8)
	for {
		_, err := io.ReadFull(conn, buf)
		if err != nil {
			fmt.Println("Error reading stream:", err)
			return
		}

		if encoding == rawstreamer.EncodingFloatingPoint {
			lsample := Endianness.Uint32(buf)
			rsample := Endianness.Uint32(buf[numBytes:])
			Buffers[0] <- math.Float32frombits(lsample)
			Buffers[1] <- math.Float32frombits(rsample)
		} else {
			offset := 0
			if Endianness == binary.LittleEndian {
				offset = numBytes - 1
			}
			var neg byte = 0
			if encoding == rawstreamer.EncodingSignedInt && buf[offset]&(1<<7) != 0 {
				neg = 0xFF
			}
			tmp[0] = neg
			tmp[1] = neg
			tmp[2] = neg
			tmp[3] = neg

			neg = 0
			if encoding == rawstreamer.EncodingSignedInt && buf[numBytes+offset]&(1<<7) != 0 {
				neg = 0xFF
			}
			tmp[4] = neg
			tmp[5] = neg
			tmp[6] = neg
			tmp[7] = neg

			if Endianness == binary.BigEndian {
				copy(tmp[4-numBytes:4], buf)
				copy(tmp[8-numBytes:8], buf[numBytes:])
			} else {
				copy(tmp[:numBytes], buf)
				copy(tmp[4:4+numBytes], buf[numBytes:])
			}

			lsample := Endianness.Uint32(tmp)
			rsample := Endianness.Uint32(tmp[4:])

			div := float32(math.Pow(2, float64(bits-1)))
			if encoding == rawstreamer.EncodingSignedInt {
				Buffers[0] <- float32(int32(lsample)) / div
				Buffers[1] <- float32(int32(rsample)) / div
			} else {
				Buffers[0] <- float32(lsample)/div - 1.0
				Buffers[1] <- float32(rsample)/div - 1.0
			}
		}
	}
}
