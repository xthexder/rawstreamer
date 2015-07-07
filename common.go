package rawstreamer

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
