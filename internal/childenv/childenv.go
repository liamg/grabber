// Package childenv builds the environment for child processes we spawn (git,
// hg). A library call must never block waiting for a human, so the environment
// disables every interactive credential prompt we know of.
package childenv

import (
	"os"
	"slices"
	"strings"
)

// blocked names environment variables removed from the child environment. Both
// point at an askpass helper; a GUI helper blocks the child until someone
// answers the dialog, and in a runner nobody ever does.
var blocked = []string{
	"GIT_ASKPASS",
	"SSH_ASKPASS",
}

// forced names environment variables set on the child environment, overriding
// any inherited value.
var forced = []string{
	// git never prompts on a terminal.
	"GIT_TERMINAL_PROMPT=0",
	// Git Credential Manager fails instead of showing any prompt.
	"GCM_INTERACTIVE=false",
	// Belt and braces: no GCM dialog even if interactive is re-enabled.
	"GCM_GUI_PROMPT=false",
	// No GUI askpass for ssh.
	"SSH_ASKPASS_REQUIRE=never",
}

// NonInteractive returns the current environment with all interactive
// credential prompting disabled. Configured credential helpers (keychain,
// manager-core, ...) are still consulted; what this suppresses is the fallback
// to asking a human.
func NonInteractive() []string {
	parent := os.Environ()
	env := make([]string, 0, len(parent)+len(forced))
	for _, kv := range parent {
		if name, _, ok := strings.Cut(kv, "="); ok && slices.Contains(blocked, name) {
			continue
		}
		env = append(env, kv)
	}
	return append(env, forced...)
}
