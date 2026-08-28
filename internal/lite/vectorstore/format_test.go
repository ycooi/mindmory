package vectorstore

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestHeaderRoundTripAndValidation(t *testing.T) {
	h, err := NewHeader(3)
	if err != nil {
		t.Fatal(err)
	}
	h.CommittedCount = 7
	b, err := h.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeHeader(b)
	if err != nil || got != h {
		t.Fatalf("round trip: %#v err=%v", got, err)
	}
	bad := append([]byte(nil), b...)
	bad[0] = 'X'
	if _, err := DecodeHeader(bad); err == nil {
		t.Fatal("wrong magic accepted")
	}
	bad = append([]byte(nil), b...)
	binary.LittleEndian.PutUint16(bad[8:10], 99)
	if _, err := DecodeHeader(bad); err == nil {
		t.Fatal("version accepted")
	}
	bad = append([]byte(nil), b...)
	bad[10] = byte(DTypeFloat16LE)
	if _, err := DecodeHeader(bad); err == nil {
		t.Fatal("dtype accepted")
	}
	bad = append([]byte(nil), b...)
	binary.LittleEndian.PutUint32(bad[12:16], 0)
	if _, err := DecodeHeader(bad); err == nil {
		t.Fatal("zero dimensions accepted")
	}
	bad = append([]byte(nil), b...)
	binary.LittleEndian.PutUint64(bad[24:32], 3)
	if _, err := DecodeHeader(bad); err == nil {
		t.Fatal("record size accepted")
	}
}

func TestVectorValidationAndOffset(t *testing.T) {
	if _, err := normalizeEncode([]float32{1}, 2); err == nil {
		t.Fatal("dimension mismatch accepted")
	}
	if _, err := normalizeEncode([]float32{0, 0}, 2); err == nil {
		t.Fatal("zero norm accepted")
	}
	if _, err := normalizeEncode([]float32{float32(math.NaN()), 1}, 2); err == nil {
		t.Fatal("NaN accepted")
	}
	if _, err := normalizeEncode([]float32{float32(math.Inf(1)), 1}, 2); err == nil {
		t.Fatal("Inf accepted")
	}
	h, _ := NewHeader(1)
	if _, err := VectorOffset(h, math.MaxUint64); err == nil {
		t.Fatal("offset overflow accepted")
	}
}
