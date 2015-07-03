package rawstreamer

const (
	EncodingSignedInt     = 1
	EncodingUnsignedInt   = 1 << 1
	EncodingFloatingPoint = 1 << 2

	EncodingLittleEndian = 1 << 6
	EncodingBigEndian    = 1 << 7
)
