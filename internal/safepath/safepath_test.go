package safepath

import (
	"path/filepath"
	"testing"
)

func TestJoin(t *testing.T) {
	root := filepath.Clean("/tmp/dest")

	t.Run("plain relative path stays inside", func(t *testing.T) {
		got, err := Join(root, "a/b/c.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join(root, "a/b/c.txt"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("internal .. that stays inside is allowed", func(t *testing.T) {
		if _, err := Join(root, "a/../b.txt"); err != nil {
			t.Errorf("expected an internal .. to be allowed, got %v", err)
		}
	})

	t.Run("escaping .. is rejected", func(t *testing.T) {
		for _, rel := range []string{
			"../evil.txt",
			"../../etc/passwd",
			"a/../../evil",
			"/etc/passwd/../../../../evil",
		} {
			if _, err := Join(root, rel); err == nil {
				t.Errorf("expected %q to be rejected", rel)
			}
		}
	})

	t.Run("a sibling with the same prefix is rejected", func(t *testing.T) {
		// "/tmp/dest-evil" shares the string prefix "/tmp/dest" but is not inside.
		if _, err := Join(root, "../dest-evil/x"); err == nil {
			t.Error("expected a same-prefix sibling to be rejected")
		}
	})
}
