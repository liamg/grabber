package git

import (
	"context"
	"net"
	nethttp "net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v6/plumbing/transport/http"
	cryptossh "golang.org/x/crypto/ssh"

	"github.com/liamg/grabber/settings"
	"github.com/liamg/grabber/ssrf"
)

// newGitHTTPServerWithAuth serves bareRepo over the git smart-HTTP protocol,
// accepting only the given basic-auth credential and answering everything else
// with a 401. It returns the server and a function reporting the usernames it
// was offered, in order.
func newGitHTTPServerWithAuth(t *testing.T, bareRepo, wantUser, wantPass string) (*httptest.Server, func() []string) {
	t.Helper()

	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not found; skipping smart-HTTP server test")
	}

	root := t.TempDir()
	if err := os.Rename(bareRepo, filepath.Join(root, "repo.git")); err != nil {
		t.Fatalf("moving bare repo: %v", err)
	}

	backend := &cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}

	var mu sync.Mutex
	var offered []string

	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		user, pass, ok := r.BasicAuth()
		mu.Lock()
		// Only the first request of each attempt names a new credential; git
		// makes several per clone, so record transitions rather than every hit.
		if len(offered) == 0 || offered[len(offered)-1] != user {
			offered = append(offered, user)
		}
		mu.Unlock()

		if !ok || user != wantUser || pass != wantPass {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			nethttp.Error(w, "HTTP Basic: Access denied", nethttp.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), offered...)
	}
}

// TestDownload_MultipleCredentialsForOneHost covers FIX-574: a host with
// several configured credentials, only one of which the remote still accepts.
// Either ordering must clone — picking the last match, or stopping at the first
// rejection, fails every module fetch for the whole host.
func TestDownload_MultipleCredentialsForOneHost(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	const (
		goodUser = "oauth2"
		goodPass = "valid-token"
	)

	dead := func(host, name string) settings.HTTPSCredential {
		return settings.HTTPSCredential{Host: host, Username: name, Password: "revoked"}
	}

	tests := []struct {
		name string
		// order builds the credential list for the server's host.
		order func(host string) []settings.HTTPSCredential
	}{
		{
			name: "working credential first, dead ones after",
			order: func(host string) []settings.HTTPSCredential {
				return []settings.HTTPSCredential{
					{Host: host, Username: goodUser, Password: goodPass},
					dead(host, "sa-one"),
					dead(host, "sa-two"),
				}
			},
		},
		{
			name: "dead credentials first, working one after",
			order: func(host string) []settings.HTTPSCredential {
				return []settings.HTTPSCredential{
					dead(host, "sa-one"),
					dead(host, "sa-two"),
					{Host: host, Username: goodUser, Password: goodPass},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newGitHTTPServerWithAuth(t, createBareRepo(t), goodUser, goodPass)
			u, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatalf("parsing server URL: %v", err)
			}

			s := settings.Settings{
				SSRFLevel:        ssrf.None,
				NoSystemFallback: true, // the developer's own helpers must not decide this
				HTTPSCredentials: tt.order(u.Hostname()),
			}

			dst := t.TempDir()
			d := &Downloader{repoURL: srv.URL + "/repo.git"}
			if _, err := d.Download(context.Background(), dst, s); err != nil {
				t.Fatalf("clone with a valid credential configured: %v", err)
			}
			assertFileContains(t, filepath.Join(dst, "file.txt"), "hello")
		})
	}

	t.Run("every credential rejected reports how many were tried", func(t *testing.T) {
		srv, offered := newGitHTTPServerWithAuth(t, createBareRepo(t), goodUser, goodPass)
		u, _ := url.Parse(srv.URL)

		s := settings.Settings{
			SSRFLevel:        ssrf.None,
			NoSystemFallback: true,
			HTTPSCredentials: []settings.HTTPSCredential{
				dead(u.Hostname(), "sa-one"),
				dead(u.Hostname(), "sa-two"),
			},
		}

		d := &Downloader{repoURL: srv.URL + "/repo.git"}
		if _, err := d.Download(context.Background(), t.TempDir(), s); err == nil {
			t.Fatal("expected the clone to fail when no credential is accepted")
		}
		got := offered()
		if len(got) != 2 || got[0] != "sa-one" || got[1] != "sa-two" {
			t.Errorf("credentials offered = %v, want both in configuration order", got)
		}
	})
}

// TestAuthCandidates_LazyResolution proves the chain does not consult a source
// until the ones before it have been tried, so the system git credential helper
// is not shelled out to whenever a configured credential exists.
func TestAuthCandidates_LazyResolution(t *testing.T) {
	called := stubCredentialFill(t, &http.BasicAuth{Username: "helper", Password: "h"})

	s := settings.Settings{
		HTTPSCredentials: []settings.HTTPSCredential{
			{Host: "git.example.com", Username: "first", Password: "one"},
			{Host: "git.example.com", Username: "second", Password: "two"},
		},
	}
	d := &Downloader{repoURL: "https://git.example.com/org/repo.git"}

	candidates := d.authCandidates(context.Background(), s)
	if *called {
		t.Error("building the chain must not consult the system credential helper")
	}
	// Two configured credentials, the dynamic function, and the system helper.
	if len(candidates) != 4 {
		t.Fatalf("got %d candidates, want 4", len(candidates))
	}

	var users []string
	for _, c := range candidates[:2] {
		auth, err := c.resolve()
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		users = append(users, auth.(*http.BasicAuth).Username)
	}
	if users[0] != "first" || users[1] != "second" {
		t.Errorf("configured credentials offered as %v, want configuration order", users)
	}
	if *called {
		t.Error("the helper must not be consulted while configured credentials remain untried")
	}
}

// TestDownload_SSHWithoutKnownHostsFile is the end-to-end shape of the FIX-574
// SSH defect: with no known_hosts file anywhere, go-git rejected the clone
// before it ever dialled, whatever host-key policy was configured. The clone
// here cannot succeed (nothing is listening), but it must fail on the
// connection rather than on a known_hosts file it was told not to consult.
func TestDownload_SSHWithoutKnownHostsFile(t *testing.T) {
	t.Setenv("SSH_KNOWN_HOSTS", filepath.Join(t.TempDir(), "absent_known_hosts"))
	t.Setenv("SSH_AUTH_SOCK", "")

	// A port nothing is listening on, so the attempt reaches the dial and stops.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	key, _ := generateSSHKeyPair(t)
	s := settings.Settings{
		SSRFLevel: ssrf.None,
		Git: settings.GitConfig{
			SSHKeys:                   []settings.SSHCredential{{Key: key}},
			InsecureSkipHostKeyVerify: true,
		},
	}

	d := &Downloader{repoURL: "ssh://git@" + addr + "/org/repo.git"}
	_, err = d.Download(context.Background(), t.TempDir(), s)
	if err == nil {
		t.Fatal("expected the clone to fail against a closed port")
	}
	if strings.Contains(err.Error(), "known_hosts") {
		t.Fatalf("host key policy did not reach go-git: %v", err)
	}
}

// TestSSHAuth_NamesHostKeyAlgorithms covers the second half of FIX-574. go-git
// falls back to known_hosts to choose the host key algorithms whenever the
// client config leaves them unset, and treats a missing file as fatal — even
// with verification switched off, where the file is never read. Without a
// known_hosts file (the norm in a container) that turned every SSH clone into
// "unable to find any valid known_hosts file", and the configured key was never
// offered.
func TestSSHAuth_NamesHostKeyAlgorithms(t *testing.T) {
	// Point the lookup at a file that does not exist, so any attempt to read
	// known_hosts fails rather than silently picking up the developer's own.
	t.Setenv("SSH_KNOWN_HOSTS", filepath.Join(t.TempDir(), "absent_known_hosts"))
	t.Setenv("SSH_AUTH_SOCK", "")

	key, _ := generateSSHKeyPair(t)
	d := &Downloader{repoURL: "ssh://git@example.com/org/repo.git"}

	for _, tc := range []struct {
		name string
		git  settings.GitConfig
	}{
		{
			name: "host key verification skipped",
			git: settings.GitConfig{
				SSHKeys:                   []settings.SSHCredential{{Key: key}},
				InsecureSkipHostKeyVerify: true,
			},
		},
		{
			name: "known hosts supplied in memory",
			git: func() settings.GitConfig {
				_, hostAuth := newTestHostKey(t)
				return settings.GitConfig{
					SSHKeys:    []settings.SSHCredential{{Key: key}},
					KnownHosts: knownHostsLine("example.com", hostAuth),
				}
			}(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, err := d.resolveSSHAuth(settings.Settings{Git: tc.git})
			if err != nil {
				t.Fatalf("resolveSSHAuth: %v", err)
			}
			cfg, err := newSSHAuth(auth).ClientConfig(context.Background(), nil)
			if err != nil {
				t.Fatalf("ClientConfig without a known_hosts file: %v", err)
			}
			if len(cfg.HostKeyAlgorithms) == 0 {
				t.Fatal("host key algorithms must be named so go-git does not read known_hosts")
			}
			// The defaults x/crypto/ssh would negotiate on its own, so the
			// handshake is unchanged apart from skipping the known_hosts read.
			if want := cryptossh.SupportedAlgorithms().HostKeys; len(cfg.HostKeyAlgorithms) != len(want) {
				t.Errorf("host key algorithms = %v, want the x/crypto defaults %v", cfg.HostKeyAlgorithms, want)
			}
			if cfg.HostKeyCallback == nil {
				t.Error("expected the configured host key policy to reach go-git")
			}
		})
	}

	// The other half of the contract: when the user's own known_hosts is what
	// verifies the host, go-git derives the algorithms recorded for it. That is
	// the stricter choice, so it is left alone.
	t.Run("system known_hosts keeps go-git's own algorithm selection", func(t *testing.T) {
		_, hostAuth := newTestHostKey(t)
		knownHosts := filepath.Join(t.TempDir(), "known_hosts")
		if err := os.WriteFile(knownHosts, knownHostsLine("example.com", hostAuth), 0o600); err != nil {
			t.Fatalf("writing known_hosts: %v", err)
		}
		t.Setenv("SSH_KNOWN_HOSTS", knownHosts)

		auth, err := d.resolveSSHAuth(settings.Settings{
			Git: settings.GitConfig{SSHKeys: []settings.SSHCredential{{Key: key}}},
		})
		if err != nil {
			t.Fatalf("resolveSSHAuth: %v", err)
		}
		if auth.HostKeyCallback != nil {
			t.Fatal("expected go-git to be left to its known_hosts default")
		}
		cfg, err := newSSHAuth(auth).ClientConfig(context.Background(), nil)
		if err != nil {
			t.Fatalf("ClientConfig: %v", err)
		}
		if len(cfg.HostKeyAlgorithms) != 0 {
			t.Errorf("host key algorithms = %v, want go-git's known_hosts selection left untouched",
				cfg.HostKeyAlgorithms)
		}
	})
}
