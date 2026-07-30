package oci

import (
	"testing"
)

func TestOCIRepoPath(t *testing.T) {
	tests := []struct {
		ref, registry, want string
	}{
		{"ghcr.io/org/repo:v1.0.0", "ghcr.io", "org/repo"},
		{"ghcr.io/org/repo@sha256:abc", "ghcr.io", "org/repo"},
		{"ghcr.io/org/repo", "ghcr.io", "org/repo"},
		{"reg:5000/repo:latest", "reg:5000", "repo"},
	}
	for _, tt := range tests {
		if got := ociRepoPath(tt.ref, tt.registry); got != tt.want {
			t.Errorf("ociRepoPath(%q, %q) = %q, want %q", tt.ref, tt.registry, got, tt.want)
		}
	}
}

func TestDetect(t *testing.T) {
	p := New()

	tests := []struct {
		name  string
		url   string
		match bool
	}{
		{"oci scheme", "oci://registry.example.com/repo:latest", true},
		{"oci scheme with digest", "oci://registry.example.com/repo@sha256:abc123", true},
		{"oci scheme no tag", "oci://registry.example.com/repo", true},
		{"oci scheme nested repo", "oci://ghcr.io/user/repo:v1.0.0", true},
		{"https not oci", "https://registry.example.com/repo", false},
		{"no scheme", "registry.example.com/repo", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := p.Detect(tt.url)
			if ok != tt.match {
				t.Errorf("Detect(%q) = %v, want %v", tt.url, ok, tt.match)
			}
		})
	}
}

func TestParseOCIURL(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		wantRef      string
		wantRegistry string
		wantErr      bool
	}{
		{
			name:         "basic with tag",
			url:          "oci://registry.example.com/myrepo:v1.0.0",
			wantRef:      "registry.example.com/myrepo:v1.0.0",
			wantRegistry: "registry.example.com",
		},
		{
			name:         "with digest",
			url:          "oci://registry.example.com/myrepo@sha256:abc123",
			wantRef:      "registry.example.com/myrepo@sha256:abc123",
			wantRegistry: "registry.example.com",
		},
		{
			name:         "no tag defaults to latest",
			url:          "oci://registry.example.com/myrepo",
			wantRef:      "registry.example.com/myrepo",
			wantRegistry: "registry.example.com",
		},
		{
			name:         "nested repo path",
			url:          "oci://ghcr.io/user/charts/mychart:1.0.0",
			wantRef:      "ghcr.io/user/charts/mychart:1.0.0",
			wantRegistry: "ghcr.io",
		},
		{
			name:         "docker hub",
			url:          "oci://docker.io/library/alpine:3.18",
			wantRef:      "docker.io/library/alpine:3.18",
			wantRegistry: "docker.io",
		},
		{
			name:         "with port",
			url:          "oci://localhost:5000/myrepo:latest",
			wantRef:      "localhost:5000/myrepo:latest",
			wantRegistry: "localhost:5000",
		},
		{
			name:    "not oci scheme",
			url:     "https://registry.example.com/repo",
			wantErr: true,
		},
		{
			name:    "no host",
			url:     "oci:///repo",
			wantErr: true,
		},
		{
			name:    "no repo",
			url:     "oci://registry.example.com",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := parseOCIURL(tt.url, false)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseOCIURL(%q) expected error", tt.url)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOCIURL(%q) unexpected error: %v", tt.url, err)
			}
			if d.ref != tt.wantRef {
				t.Errorf("ref = %q, want %q", d.ref, tt.wantRef)
			}
			if d.registry != tt.wantRegistry {
				t.Errorf("registry = %q, want %q", d.registry, tt.wantRegistry)
			}
		})
	}
}

// TestDetectForced covers a reference that does not repeat the scheme, which
// auto-detection has to reject because the scheme is all it has to go on.
func TestDetectForced(t *testing.T) {
	p := New()

	const ref = "registry.example.com/repo:tag"
	if _, ok := p.Detect(ref); ok {
		t.Error("Detect accepted a reference with no scheme; it has nothing else to go on")
	}
	d, ok := p.DetectForced(ref)
	if !ok {
		t.Fatal("DetectForced rejected a scheme-less reference")
	}
	dl := d.(*Downloader)
	if dl.ref != ref || dl.registry != "registry.example.com" {
		t.Errorf("parsed %+v, want the reference and registry from the URL", dl)
	}

	// The scheme is optional, not forbidden.
	if _, ok := p.DetectForced("oci://" + ref); !ok {
		t.Error("DetectForced rejected a reference that did carry the scheme")
	}

	// Another scheme is not an OCI reference, whatever the prefix claims.
	for _, url := range []string{"https://registry.example.com/repo", "registry.example.com"} {
		if _, ok := p.DetectForced(url); ok {
			t.Errorf("DetectForced accepted %q", url)
		}
	}
}

func TestPrefix(t *testing.T) {
	p := New()
	if p.Prefix() != "oci" {
		t.Errorf("Prefix() = %q, want %q", p.Prefix(), "oci")
	}
}

func TestPriority(t *testing.T) {
	p := New()
	if p.Priority() != 50 {
		t.Errorf("Priority() = %d, want %d", p.Priority(), 50)
	}
}
