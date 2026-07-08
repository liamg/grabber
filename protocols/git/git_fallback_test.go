package git

import (
	"testing"

	"github.com/liamg/grabber/settings"
)

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
