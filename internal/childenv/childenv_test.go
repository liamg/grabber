package childenv

import (
	"strings"
	"testing"
)

func TestNonInteractive(t *testing.T) {
	t.Setenv("GIT_ASKPASS", "/usr/bin/some-gui-askpass")
	t.Setenv("SSH_ASKPASS", "/usr/bin/some-gui-askpass")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GRABBER_KEEP_ME", "kept")

	env := NonInteractive()

	vals := map[string]string{}
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("malformed entry %q", kv)
		}
		vals[name] = value // later entries win, as they do for the child process
	}

	for _, name := range blocked {
		if _, ok := vals[name]; ok {
			t.Errorf("%s should have been removed from the child env", name)
		}
	}

	want := map[string]string{
		"GIT_TERMINAL_PROMPT": "0",
		"GCM_INTERACTIVE":     "false",
		"GCM_GUI_PROMPT":      "false",
		"SSH_ASKPASS_REQUIRE": "never",
	}
	for name, value := range want {
		if vals[name] != value {
			t.Errorf("%s = %q, want %q", name, vals[name], value)
		}
	}

	if vals["GRABBER_KEEP_ME"] != "kept" {
		t.Error("unrelated environment variables should be passed through")
	}
}
