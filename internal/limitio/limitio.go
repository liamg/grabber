// Package limitio provides size-bounded copying, so a download or archive
// extraction from an untrusted source cannot exhaust the disk (a "decompression
// bomb" or an unbounded response body).
package limitio

import (
	"errors"
	"fmt"
	"io"
)

// ErrLimitExceeded is returned (wrapped) when a copy would exceed its limit.
var ErrLimitExceeded = errors.New("size limit exceeded")

// Copy is io.Copy bounded to max bytes. A max of 0 or less means unlimited. If
// the source yields more than max bytes the copy stops and returns an error
// wrapping ErrLimitExceeded; up to max+1 bytes may have been written to dst by
// then (the caller is expected to discard the partial output).
func Copy(dst io.Writer, src io.Reader, max int64) (int64, error) {
	if max <= 0 {
		return io.Copy(dst, src)
	}
	// Read one extra byte so a source sitting exactly on the limit is accepted
	// but anything larger is detected.
	n, err := io.Copy(dst, io.LimitReader(src, max+1))
	if err != nil {
		return n, err
	}
	if n > max {
		return n, fmt.Errorf("%w: exceeds %d bytes", ErrLimitExceeded, max)
	}
	return n, nil
}
