package settings

import (
	"bytes"
	"testing"
)

func TestMatchHTTPSCredential(t *testing.T) {
	tests := []struct {
		name         string
		credentials  []HTTPSCredential
		url          string
		wantUsername string
		wantNil      bool
	}{
		{
			name: "host match",
			credentials: []HTTPSCredential{
				{Host: "github.com", Username: "user1", Password: "pass1"},
			},
			url:          "https://github.com/user/repo.git",
			wantUsername: "user1",
		},
		{
			name: "host case insensitive",
			credentials: []HTTPSCredential{
				{Host: "GitHub.COM", Username: "user1", Password: "pass1"},
			},
			url:          "https://github.com/user/repo.git",
			wantUsername: "user1",
		},
		{
			name: "no match",
			credentials: []HTTPSCredential{
				{Host: "gitlab.com", Username: "user1", Password: "pass1"},
			},
			url:     "https://github.com/user/repo.git",
			wantNil: true,
		},
		{
			name: "path prefix match",
			credentials: []HTTPSCredential{
				{Host: "github.com", Username: "user1", Password: "pass1"},
				{Host: "github.com", Path: "/org", Username: "user2", Password: "pass2"},
			},
			url:          "https://github.com/org/repo.git",
			wantUsername: "user2",
		},
		{
			name: "path prefix no match falls back to host",
			credentials: []HTTPSCredential{
				{Host: "github.com", Username: "user1", Password: "pass1"},
				{Host: "github.com", Path: "/other-org", Username: "user2", Password: "pass2"},
			},
			url:          "https://github.com/my-org/repo.git",
			wantUsername: "user1",
		},
		{
			name: "most specific path wins",
			credentials: []HTTPSCredential{
				{Host: "github.com", Username: "user1", Password: "pass1"},
				{Host: "github.com", Path: "/org", Username: "user2", Password: "pass2"},
				{Host: "github.com", Path: "/org/specific-repo", Username: "user3", Password: "pass3"},
			},
			url:          "https://github.com/org/specific-repo.git",
			wantUsername: "user3",
		},
		{
			name: "trailing slash on credential path",
			credentials: []HTTPSCredential{
				{Host: "github.com", Path: "/org/", Username: "user1", Password: "pass1"},
			},
			url:          "https://github.com/org/repo",
			wantUsername: "user1",
		},
		{
			name:    "empty credentials",
			url:     "https://github.com/user/repo",
			wantNil: true,
		},
		{
			name: "invalid url",
			credentials: []HTTPSCredential{
				{Host: "github.com", Username: "user1", Password: "pass1"},
			},
			url:     "://not-a-url",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Settings{
				HTTPSCredentials: tt.credentials,
			}
			cred := s.MatchHTTPSCredential(tt.url)
			if tt.wantNil {
				if cred != nil {
					t.Fatalf("expected nil, got %+v", cred)
				}
				return
			}
			if cred == nil {
				t.Fatal("expected credential, got nil")
			}
			if cred.Username != tt.wantUsername {
				t.Errorf("username = %q, want %q", cred.Username, tt.wantUsername)
			}
		})
	}
}

func TestMatchSSHKey(t *testing.T) {
	tests := []struct {
		name string
		keys []SSHCredential
		host string
		want string // "" means expect nil
	}{
		{
			name: "host match",
			keys: []SSHCredential{{Host: "github.com", Key: []byte("gh")}},
			host: "github.com",
			want: "gh",
		},
		{
			name: "host case insensitive",
			keys: []SSHCredential{{Host: "GitHub.com", Key: []byte("gh")}},
			host: "github.com",
			want: "gh",
		},
		{
			name: "host-specific wins over default",
			keys: []SSHCredential{{Key: []byte("default")}, {Host: "github.com", Key: []byte("gh")}},
			host: "github.com",
			want: "gh",
		},
		{
			name: "falls back to default for unmatched host",
			keys: []SSHCredential{{Key: []byte("default")}, {Host: "github.com", Key: []byte("gh")}},
			host: "gitlab.com",
			want: "default",
		},
		{
			name: "no match and no default",
			keys: []SSHCredential{{Host: "github.com", Key: []byte("gh")}},
			host: "gitlab.com",
			want: "",
		},
		{
			name: "empty",
			host: "github.com",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Settings{Git: GitConfig{SSHKeys: tt.keys}}
			got := s.MatchSSHKey(tt.host)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("expected nil, got %q", got)
				}
				return
			}
			if !bytes.Equal(got, []byte(tt.want)) {
				t.Errorf("key = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMatchOCICredential(t *testing.T) {
	tests := []struct {
		name         string
		credentials  []OCICredential
		registry     string
		wantUsername string
		wantNil      bool
	}{
		{
			name:         "registry match",
			credentials:  []OCICredential{{Registry: "ghcr.io", Username: "gh"}},
			registry:     "ghcr.io",
			wantUsername: "gh",
		},
		{
			name:         "registry case insensitive",
			credentials:  []OCICredential{{Registry: "GHCR.io", Username: "gh"}},
			registry:     "ghcr.io",
			wantUsername: "gh",
		},
		{
			name:         "registry-specific wins over default",
			credentials:  []OCICredential{{Username: "default"}, {Registry: "ghcr.io", Username: "gh"}},
			registry:     "ghcr.io",
			wantUsername: "gh",
		},
		{
			name:         "falls back to default for unmatched registry",
			credentials:  []OCICredential{{Username: "default"}, {Registry: "ghcr.io", Username: "gh"}},
			registry:     "registry.example.com",
			wantUsername: "default",
		},
		{
			name:        "no match and no default",
			credentials: []OCICredential{{Registry: "ghcr.io", Username: "gh"}},
			registry:    "registry.example.com",
			wantNil:     true,
		},
		{
			name:     "empty",
			registry: "ghcr.io",
			wantNil:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Settings{OCICredentials: tt.credentials}
			cred := s.MatchOCICredential(tt.registry)
			if tt.wantNil {
				if cred != nil {
					t.Fatalf("expected nil, got %+v", cred)
				}
				return
			}
			if cred == nil {
				t.Fatal("expected credential, got nil")
			}
			if cred.Username != tt.wantUsername {
				t.Errorf("username = %q, want %q", cred.Username, tt.wantUsername)
			}
		})
	}
}
