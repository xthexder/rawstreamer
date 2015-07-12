package rawstreamer

import (
	"encoding/binary"
	"math"
)

const (
	EncodingSignedInt = 1 + iota
	EncodingUnsignedInt
	EncodingFloatingPoint

	EncodingLittleEndian = 1 << 6
	EncodingBigEndian    = 1 << 7
)

const EncodingMask byte = ^byte(EncodingLittleEndian | EncodingBigEndian)

var EncodingString = map[byte]string{
	EncodingSignedInt:     "int",
	EncodingUnsignedInt:   "uint",
	EncodingFloatingPoint: "float",
}

func ReadFloat32(buf []byte, format byte, endianness binary.ByteOrder) float32 {
	encoding := format & EncodingMask

	if encoding == EncodingFloatingPoint {
		return math.Float32frombits(endianness.Uint32(buf))
	} else {
		offset := 0
		if endianness == binary.LittleEndian {
			offset = len(buf) - 1
		}
		var neg byte = 0
		if encoding == EncodingSignedInt && buf[offset]&(1<<7) != 0 {
			neg = 0xFF
		}
		tmp := []byte{neg, neg, neg, neg}

		if endianness == binary.BigEndian {
			copy(tmp[4-len(buf):], buf)
		} else {
			copy(tmp, buf)
		}

		sample := endianness.Uint32(tmp)

		div := math.Pow(2, float64(len(buf)*8-1))
		if encoding == EncodingSignedInt {
			return float32(float64(int32(sample)) / div)
		} else {
			return float32(float64(sample)/div - 1.0)
		}
	}
}

func WriteFloat32(buf []byte, sample float32, format byte, endianness binary.ByteOrder) {
	encoding := format & EncodingMask

	if encoding == EncodingFloatingPoint {
		endianness.PutUint32(buf, math.Float32bits(sample))
	} else {
		mult := math.Pow(2, float64(len(buf)*8-1))
		var offset float64 = -0.5 // Round instead of floor
		if encoding == EncodingUnsignedInt {
			offset = mult - 1.5
		}

		bits := uint32(float64(sample)*mult + offset)

		tmp := make([]byte, 4)
		endianness.PutUint32(tmp, bits)

		if endianness == binary.BigEndian {
			copy(buf, tmp[4-len(buf):4])
		} else {
			copy(buf, tmp)
		}
	}
}
