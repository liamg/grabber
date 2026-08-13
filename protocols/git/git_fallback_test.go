package git

import (
	"context"
	"errors"
	"fmt"
	"testing"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/transport"

	"github.com/liamg/grabber/settings"
)

func TestDefinitiveCloneError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		// Answers the scheme fallback cannot change.
		{"missing ref", fmt.Errorf("cloning repo: %w: refs/tags/master", gogit.ErrRemoteRefNotFound), true},
		{"empty repository", fmt.Errorf("cloning repo: %w", transport.ErrEmptyRemoteRepository), true},
		{"context canceled", fmt.Errorf("cloning repo: %w", context.Canceled), true},
		{"context deadline", fmt.Errorf("cloning repo: %w", context.DeadlineExceeded), true},
		// Failures the fallback exists for: hosts hide private repositories
		// behind auth/not-found, and the other scheme may authenticate.
		{"repository not found", fmt.Errorf("cloning repo: %w", transport.ErrRepositoryNotFound), false},
		{"authentication required", fmt.Errorf("cloning repo: %w", transport.ErrAuthenticationRequired), false},
		{"authorization failed", fmt.Errorf("cloning repo: %w", transport.ErrAuthorizationFailed), false},
		{"transport-specific failure", errors.New("unable to find any valid known_hosts file"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := definitiveCloneError(tt.err); got != tt.want {
				t.Errorf("definitiveCloneError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestCloneCandidates(t *testing.T) {
	withKey := settings.Settings{Git: settings.GitConfig{
		SSHKeys: []settings.SSHCredential{{Host: "github.com", Key: []byte("k")}},
	}}

	tests := []struct {
		name    string
		repoURL string
		s       settings.Settings
		want    []string
	}{
		{
			name:    "ssh falls back to https",
			repoURL: "ssh://git@github.com/org/repo.git",
			want:    []string{"ssh://git@github.com/org/repo.git", "https://github.com/org/repo.git"},
		},
		{
			name:    "scp falls back to https",
			repoURL: "git@github.com:org/repo.git",
			want:    []string{"git@github.com:org/repo.git", "https://github.com/org/repo.git"},
		},
		{
			name:    "https falls back to ssh when a key is configured for the host",
			repoURL: "https://github.com/org/repo.git",
			s:       withKey,
			want:    []string{"https://github.com/org/repo.git", "ssh://git@github.com/org/repo.git"},
		},
		{
			name:    "https does not fall back to ssh without a key",
			repoURL: "https://github.com/org/repo.git",
			want:    []string{"https://github.com/org/repo.git"},
		},
		{
			name:    "force-https converts up front with no ssh attempt",
			repoURL: "ssh://git@github.com/org/repo.git",
			s:       settings.Settings{Git: settings.GitConfig{SSHToHTTPS: true}},
			want:    []string{"https://github.com/org/repo.git"},
		},
		{
			name:    "local path has no fallback",
			repoURL: "/tmp/local/repo",
			want:    []string{"/tmp/local/repo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Downloader{repoURL: tt.repoURL}
			got := d.cloneCandidates(tt.s)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("candidate[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestHTTPSToSSH(t *testing.T) {
	tests := map[string]string{
		"https://github.com/org/repo.git":          "ssh://git@github.com/org/repo.git",
		"https://github.com/org/repo":              "ssh://git@github.com/org/repo.git",
		"https://dev.azure.com/org/proj/_git/repo": "ssh://git@ssh.dev.azure.com/v3/org/proj/repo",
		"https://dev.azure.com/org/repo":           "", // not the _git form
		"https://github.com":                       "", // no path
	}
	for in, want := range tests {
		if got := httpsToSSH(in); got != want {
			t.Errorf("httpsToSSH(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAzureDevOpsRoundTrip pins the two conversions as inverses of each other.
// Azure DevOps is the one host whose SSH and HTTPS remotes differ by more than
// a scheme, so the pair has to be kept in step by hand.
func TestAzureDevOpsRoundTrip(t *testing.T) {
	const https = "https://dev.azure.com/org/proj/_git/repo"

	ssh := httpsToSSH(https)
	if ssh != "ssh://git@ssh.dev.azure.com/v3/org/proj/repo" {
		t.Fatalf("httpsToSSH(%q) = %q", https, ssh)
	}
	if got := sshToHTTPS(ssh); got != https {
		t.Errorf("sshToHTTPS(%q) = %q, want the original %q", ssh, got, https)
	}
}

func TestGitHostPort(t *testing.T) {
	tests := []struct {
		url, host, port string
	}{
		{"ssh://git@github.com/org/repo.git", "github.com", "22"},
		{"ssh://git@github.com:2222/org/repo.git", "github.com", "2222"},
		{"git@github.com:org/repo.git", "github.com", "22"},
		{"https://github.com/org/repo.git", "github.com", "443"},
		{"https://github.com:8443/org/repo.git", "github.com", "8443"},
		{"http://example.com/repo", "example.com", "80"},
		{"/tmp/local/repo", "", ""},
	}
	for _, tt := range tests {
		host, port := gitHostPort(tt.url)
		if host != tt.host || port != tt.port {
			t.Errorf("gitHostPort(%q) = (%q,%q), want (%q,%q)", tt.url, host, port, tt.host, tt.port)
		}
	}
}

// TestMissingRef covers the wire protocol v2 ambiguity. go-git asks ls-refs for
// only the namespace it wants, so a reference that is not on the remote comes
// back as an empty ref list, which go-git reports as an empty repository. That
// has to be read as "this ref is missing" so the next spelling is tried — but
// only when a reference was actually named.
func TestMissingRef(t *testing.T) {
	named := cloneAttempt{refName: plumbing.NewBranchReferenceName("v1.0.0")}
	unnamed := cloneAttempt{refName: ""}

	tests := []struct {
		name    string
		attempt cloneAttempt
		err     error
		want    bool
	}{
		{"ref not found", named, gogit.ErrRemoteRefNotFound, true},
		{"ref not found, no ref named", unnamed, gogit.ErrRemoteRefNotFound, true},
		{
			name:    "empty repository for a named ref is a missing ref",
			attempt: named,
			err:     fmt.Errorf("cloning repo: %w", transport.ErrEmptyRemoteRepository),
			want:    true,
		},
		{
			name:    "empty repository with no ref named is a genuinely empty repository",
			attempt: unnamed,
			err:     transport.ErrEmptyRemoteRepository,
			want:    false,
		},
		{"any other failure", named, errors.New("auth required"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := missingRef(tt.attempt, tt.err); got != tt.want {
				t.Errorf("missingRef = %v, want %v", got, tt.want)
			}
		})
	}
}
