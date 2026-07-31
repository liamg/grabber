package git

import (
	"os"
	"testing"
)

// TestMain isolates these tests from the developer's own git configuration.
// The fixtures create tags via go-git, which fails if the ambient config has
// tag.gpgSign enabled ("cannot auto-sign tag"). Pointing the global and system
// config at the null device (portable via os.DevNull) makes the suite pass
// regardless of the machine it runs on. GIT_CONFIG_NOSYSTEM is belt-and-braces
// for go-git versions that consult it rather than the path override.
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
}
