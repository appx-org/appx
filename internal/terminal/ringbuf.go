package terminal

// RingBuffer is a fixed-size circular byte buffer that stores the most recent
// N bytes written to it. Older bytes are silently discarded when the buffer is
// full. Used to hold terminal output for replay on WebSocket reconnect.
type RingBuffer struct {
	buf  []byte
	size int
	w    int  // next write position
	full bool // true once the buffer has wrapped at least once
}

// NewRingBuffer creates a ring buffer that holds at most size bytes.
func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{
		buf:  make([]byte, size),
		size: size,
	}
}

// Write appends p to the ring buffer. If len(p) exceeds the buffer size, only
// the last size bytes are kept. Write is not safe for concurrent use — the
// caller must hold the session mutex.
func (rb *RingBuffer) Write(p []byte) {
	// If the incoming data is larger than the buffer, only keep the tail.
	if len(p) >= rb.size {
		p = p[len(p)-rb.size:]
		copy(rb.buf, p)
		rb.w = 0
		rb.full = true
		return
	}

	// Write in up to two segments (before and after wrap).
	n := copy(rb.buf[rb.w:], p)
	if n < len(p) {
		copy(rb.buf, p[n:])
		rb.w = len(p) - n
		rb.full = true
	} else {
		rb.w += n
		if rb.w == rb.size {
			rb.w = 0
			rb.full = true
		}
	}
}

// Bytes returns a copy of the buffered data in chronological order (oldest
// first). Returns an empty slice if nothing has been written.
func (rb *RingBuffer) Bytes() []byte {
	if !rb.full {
		// Haven't wrapped yet — data is [0, w).
		out := make([]byte, rb.w)
		copy(out, rb.buf[:rb.w])
		return out
	}
	// Wrapped — data is [w, size) + [0, w).
	out := make([]byte, rb.size)
	n := copy(out, rb.buf[rb.w:])
	copy(out[n:], rb.buf[:rb.w])
	return out
}
