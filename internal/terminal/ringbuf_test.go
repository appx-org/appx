package terminal

import (
	"bytes"
	"testing"
)

// TestRingBuffer_WriteRead verifies that data written to the ring buffer can
// be read back correctly when the buffer has not wrapped around.
func TestRingBuffer_WriteRead(t *testing.T) {
	rb := NewRingBuffer(64)
	rb.Write([]byte("hello"))
	got := rb.Bytes()
	if !bytes.Equal(got, []byte("hello")) {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

// TestRingBuffer_Wraparound verifies that the ring buffer correctly wraps
// around and returns only the most recent data when more bytes are written
// than the buffer can hold.
func TestRingBuffer_Wraparound(t *testing.T) {
	rb := NewRingBuffer(8)
	rb.Write([]byte("abcdefgh"))  // fill exactly
	rb.Write([]byte("ij"))        // overwrite first 2 bytes
	got := rb.Bytes()
	if !bytes.Equal(got, []byte("cdefghij")) {
		t.Errorf("got %q, want %q", got, "cdefghij")
	}
}

// TestRingBuffer_SizeRespected verifies that the buffer never returns more
// bytes than its configured size, even after many writes.
func TestRingBuffer_SizeRespected(t *testing.T) {
	rb := NewRingBuffer(4)
	rb.Write([]byte("aabbccdd"))
	got := rb.Bytes()
	if len(got) != 4 {
		t.Errorf("got len %d, want 4", len(got))
	}
	if !bytes.Equal(got, []byte("ccdd")) {
		t.Errorf("got %q, want %q", got, "ccdd")
	}
}

// TestRingBuffer_MultipleSmallWrites verifies that multiple small writes
// accumulate correctly.
func TestRingBuffer_MultipleSmallWrites(t *testing.T) {
	rb := NewRingBuffer(16)
	rb.Write([]byte("abc"))
	rb.Write([]byte("def"))
	rb.Write([]byte("ghi"))
	got := rb.Bytes()
	if !bytes.Equal(got, []byte("abcdefghi")) {
		t.Errorf("got %q, want %q", got, "abcdefghi")
	}
}

// TestRingBuffer_EmptyRead verifies that reading from a fresh buffer returns
// an empty slice.
func TestRingBuffer_EmptyRead(t *testing.T) {
	rb := NewRingBuffer(64)
	got := rb.Bytes()
	if len(got) != 0 {
		t.Errorf("expected empty, got %d bytes", len(got))
	}
}

// TestRingBuffer_LargeWrite verifies that a single write larger than the
// buffer size keeps only the tail.
func TestRingBuffer_LargeWrite(t *testing.T) {
	rb := NewRingBuffer(4)
	rb.Write([]byte("abcdefghij"))
	got := rb.Bytes()
	if !bytes.Equal(got, []byte("ghij")) {
		t.Errorf("got %q, want %q", got, "ghij")
	}
}
