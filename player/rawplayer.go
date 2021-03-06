package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cheggaaa/pb"
	"github.com/gordonklaus/portaudio"
	"github.com/xthexder/rawstreamer"
)

var Endianness binary.ByteOrder
var Stream *portaudio.Stream
var Buffers []chan float32
var Buffering int32
var BufferingSync sync.Mutex
var LastLeft, LastRight float32

func processAudio(out [][]float32) {
	bar.Set(len(Buffers[0]))
	if atomic.LoadInt32(&Buffering) <= 0 {
		for i := range out[0] {
			select {
			case out[0][i] = <-Buffers[0]:
				out[1][i] = <-Buffers[1]
				LastLeft = out[0][i]
				LastRight = out[1][i]
			default:
				if LastLeft != 0 || LastRight != 0 {
					fmt.Println("Buffer underflow!")
				}
				go func() {
					BufferingSync.Lock()
					defer BufferingSync.Unlock()
					atomic.StoreInt32(&Buffering, int32(cap(Buffers[0])-len(Buffers[0])))
				}()
				for ; i < len(out[0]); i++ {
					out[0][i] = LastLeft
					out[1][i] = LastRight
				}
				return
			}
		}
	} else {
		for i := range out[0] {
			out[0][i] = LastLeft
			out[1][i] = LastRight
		}
	}
}

func getChannelBufferSize(bufferLen time.Duration, bufferSize, sampleRate uint32) int {
	return int(bufferLen*time.Duration(sampleRate)/time.Second) + 1
}

var bar *pb.ProgressBar

func printStatus() {
	count := cap(Buffers[0])
	bar = pb.New(count)

	bar.SetRefreshRate(100 * time.Millisecond)
	bar.ShowCounters = true
	bar.ShowTimeLeft = false

	bar.Start()
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

	Buffering = int32(getChannelBufferSize(bufferLen, bufferSize, sampleRate))
	LastLeft = 0
	LastRight = 0

	printStatus()

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

	var lastData time.Time
	remainder := 0
	for {
		n := 0
		conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		n, err = io.ReadAtLeast(conn, buf[remainder:], numBytes*2-remainder)
		if err != nil {
			fmt.Println("Error reading stream:", err)
			return
		}
		n += remainder
		remainder = n % (numBytes * 2)

		for i := 0; i < (n - remainder); i += numBytes * 2 {
			BufferingSync.Lock()

			left := rawstreamer.ReadFloat32(buf[i:i+numBytes], flags, Endianness)
			right := rawstreamer.ReadFloat32(buf[i+numBytes:i+numBytes*2], flags, Endianness)
			if left != 0 || right != 0 {
				lastData = time.Now()
				Buffers[0] <- left
				Buffers[1] <- right
			} else if time.Since(lastData) < 1*time.Minute {
				Buffers[0] <- left
				Buffers[1] <- right
			}
			if atomic.LoadInt32(&Buffering) > 0 && time.Since(lastData) < 1*time.Minute {
				atomic.AddInt32(&Buffering, -1)
			}
			BufferingSync.Unlock()
		}

		copy(buf, buf[n-remainder:n])
	}
}
