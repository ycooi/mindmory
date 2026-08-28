package vectorstore

import (
	"encoding/binary"
	"fmt"
	"math"
)

var magic = [8]byte{'M', 'M', 'V', 'E', 'C', '0', '0', '1'}

type Header struct {
	Version        uint16
	DType          DType
	Normalized     bool
	Dimensions     uint32
	CommittedCount uint64
	RecordSize     uint64
}

func NewHeader(dimensions int) (Header, error) {
	if dimensions <= 0 || dimensions > 1<<20 {
		return Header{}, fmt.Errorf("invalid vector dimensions %d", dimensions)
	}
	return Header{Version: FormatVersion, DType: DTypeFloat32LE, Normalized: true,
		Dimensions: uint32(dimensions), RecordSize: uint64(dimensions) * 4}, nil
}

func (h Header) Validate() error {
	if h.Version != FormatVersion {
		return fmt.Errorf("unsupported vector format %d", h.Version)
	}
	if h.DType != DTypeFloat32LE {
		return fmt.Errorf("unsupported vector dtype %d", h.DType)
	}
	if !h.Normalized || h.Dimensions == 0 || h.RecordSize != uint64(h.Dimensions)*4 {
		return fmt.Errorf("inconsistent vector header")
	}
	return nil
}

func (h Header) Encode() ([]byte, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	b := make([]byte, HeaderSize)
	copy(b[:8], magic[:])
	binary.LittleEndian.PutUint16(b[8:10], h.Version)
	b[10], b[11] = byte(h.DType), 1
	binary.LittleEndian.PutUint32(b[12:16], h.Dimensions)
	binary.LittleEndian.PutUint64(b[16:24], h.CommittedCount)
	binary.LittleEndian.PutUint64(b[24:32], h.RecordSize)
	return b, nil
}

func DecodeHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize || string(b[:8]) != string(magic[:]) {
		return Header{}, fmt.Errorf("invalid vector magic")
	}
	h := Header{Version: binary.LittleEndian.Uint16(b[8:10]), DType: DType(b[10]), Normalized: b[11] == 1,
		Dimensions: binary.LittleEndian.Uint32(b[12:16]), CommittedCount: binary.LittleEndian.Uint64(b[16:24]),
		RecordSize: binary.LittleEndian.Uint64(b[24:32])}
	return h, h.Validate()
}

func VectorOffset(h Header, ordinal uint64) (int64, error) {
	if err := h.Validate(); err != nil {
		return 0, err
	}
	if ordinal > (math.MaxInt64-HeaderSize)/h.RecordSize {
		return 0, fmt.Errorf("vector offset overflow")
	}
	return HeaderSize + int64(ordinal*h.RecordSize), nil
}

func normalizeEncode(vector []float32, dimensions int) ([]byte, error) {
	if len(vector) != dimensions {
		return nil, fmt.Errorf("vector dimensions: got %d want %d", len(vector), dimensions)
	}
	var sum float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("invalid vector value")
		}
		sum += float64(value) * float64(value)
	}
	if sum == 0 {
		return nil, fmt.Errorf("zero-norm vector")
	}
	norm := float32(math.Sqrt(sum))
	b := make([]byte, dimensions*4)
	for i, value := range vector {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(value/norm))
	}
	return b, nil
}
