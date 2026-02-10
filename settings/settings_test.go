package settings

import "testing"

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
