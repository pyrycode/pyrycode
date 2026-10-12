package apps

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

func (p *PreparedResponse) stage(in ResponseInput) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return err
	}
	if in.Size > in.Limit {
		return ErrResponseLimit
	}
	reserve := func(n int64) bool {
		if n <= 0 {
			return true
		}
		if !in.Reserve(n) {
			return false
		}
		p.held += n
		return true
	}
	if !reserve(in.Size) {
		return ErrResponseCapacity
	}
	emptyReads := 0
	readFailed := false
	reader := responseReadFunc(func(buf []byte) (int, error) {
		n, err := p.source.Read(buf)
		if n == 0 && err == nil {
			emptyReads++
			if emptyReads >= 100 {
				err = ErrResponseIO
			}
		} else {
			emptyReads = 0
		}
		if err != nil && err != io.EOF {
			readFailed = true // ReadFull discards errors when the returned bytes fill its buffer.
			err = ErrResponseIO
		}
		return n, err
	})
	hash := sha256.New()
	var total int64
	for {
		if err := p.checkLocked(); err != nil {
			return err
		}
		capacity := min(int64(32768), in.Limit-total)
		if capacity < 32768 {
			capacity++
		} // Reserve the byte that proves overflow or EOF.
		if in.Size >= 0 && total <= in.Size {
			capacity = min(capacity, max(int64(1), in.Size-total))
		}
		if !reserve(capacity - (p.held - total)) {
			return ErrResponseCapacity
		}
		chunk := make([]byte, int(capacity))
		filled, err := io.ReadFull(reader, chunk)
		if failure := p.checkLocked(); failure != nil {
			return failure
		}
		if int64(filled) > in.Limit-total {
			return ErrResponseLimit
		}
		if readFailed || (err != nil && err != io.EOF && err != io.ErrUnexpectedEOF) {
			return ErrResponseIO
		}
		total += int64(filled)
		if filled > 0 {
			p.chunks = append(p.chunks, chunk[:filled])
			_, _ = hash.Write(chunk[:filled])
		}
		if err != nil {
			if unused := p.held - total; unused > 0 {
				p.release(unused)
				p.held = total
			}
			p.metadata.Size = total
			p.metadata.SHA256 = hex.EncodeToString(hash.Sum(nil))
			return nil
		}
	}
}

type responseReadFunc func([]byte) (int, error)

func (f responseReadFunc) Read(buf []byte) (int, error) { return f(buf) }
