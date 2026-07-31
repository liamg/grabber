package limitio

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCopy(t *testing.T) {
	t.Run("under the limit succeeds", func(t *testing.T) {
		var dst bytes.Buffer
		n, err := Copy(&dst, strings.NewReader("hello"), 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 5 || dst.String() != "hello" {
			t.Errorf("got n=%d body=%q", n, dst.String())
		}
	})

	t.Run("exactly on the limit succeeds", func(t *testing.T) {
		var dst bytes.Buffer
		if _, err := Copy(&dst, strings.NewReader("hello"), 5); err != nil {
			t.Errorf("expected exactly-on-limit to pass, got %v", err)
		}
	})

	t.Run("over the limit is rejected", func(t *testing.T) {
		var dst bytes.Buffer
		_, err := Copy(&dst, strings.NewReader("hello world"), 5)
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("expected ErrLimitExceeded, got %v", err)
		}
	})

	t.Run("zero limit means unlimited", func(t *testing.T) {
		var dst bytes.Buffer
		if _, err := Copy(&dst, strings.NewReader("hello world"), 0); err != nil {
			t.Errorf("expected zero limit to be unlimited, got %v", err)
		}
	})
}
