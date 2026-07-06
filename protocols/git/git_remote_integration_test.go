package git

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/liamg/grabber/settings"
	"github.com/liamg/grabber/ssrf"
)

func startGitHTTPServer(t *testing.T) (repoURL string, cleanup func()) {
	t.Helper()
	ctx := context.Background()

	// Use debian/git image which includes git-http-backend
	setupScript := `#!/bin/bash
set -e
apt-get update -qq && apt-get install -y -qq git apache2 >/dev/null 2>&1

git config --global init.defaultBranch main
git config --global safe.directory '*'

# Create bare repo with content
mkdir -p /srv/git/repo.git
cd /srv/git/repo.git
git init --bare

cd /tmp
git clone /srv/git/repo.git working
cd working
git config user.email "test@test.com"
git config user.name "Test"
echo "hello from http" > file.txt
mkdir sub
echo "nested via http" > sub/nested.txt
git add .
git commit -m "first commit"
git tag v1.0.0
echo "second file" > sub/extra.txt
git add .
git commit -m "second commit"
git push origin HEAD:main --tags
cd /
rm -rf /tmp/working

# Make repo accessible
chown -R www-data:www-data /srv/git
cd /srv/git/repo.git
git update-server-info

# Enable required Apache modules
a2enmod cgi alias env >/dev/null 2>&1

# Configure Apache for git-http-backend
cat > /etc/apache2/sites-available/git.conf << 'APACHECONF'
<VirtualHost *:80>
    SetEnv GIT_PROJECT_ROOT /srv/git
    SetEnv GIT_HTTP_EXPORT_ALL
    ScriptAlias /git/ /usr/lib/git-core/git-http-backend/
    <Directory "/usr/lib/git-core">
        Options +ExecCGI
        Require all granted
    </Directory>
</VirtualHost>
APACHECONF

a2dissite 000-default >/dev/null 2>&1
a2ensite git >/dev/null 2>&1

# Start apache in foreground
apachectl -D FOREGROUND
`

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "setup.sh")
	if err := os.WriteFile(scriptPath, []byte(setupScript), 0o755); err != nil {
		t.Fatalf("writing setup script: %v", err)
	}

	req := testcontainers.ContainerRequest{
		Image:        "debian:bookworm-slim",
		ExposedPorts: []string{"80/tcp"},
		Cmd:          []string{"/bin/bash", "/tmp/setup.sh"},
		Files: []testcontainers.ContainerFile{
			{
				HostFilePath:      scriptPath,
				ContainerFilePath: "/tmp/setup.sh",
				FileMode:          0o755,
			},
		},
		WaitingFor: wait.ForListeningPort("80/tcp").
			WithStartupTimeout(90 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("starting git http container: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("getting host: %v", err)
	}

	port, err := container.MappedPort(ctx, "80")
	if err != nil {
		t.Fatalf("getting port: %v", err)
	}

	repoURL = fmt.Sprintf("http://%s:%s/git/repo.git", host, port.Port())

	// Wait for the HTTP endpoint to actually serve git content.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(repoURL + "/info/refs?service=git-upload-pack")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	return repoURL, func() {
		container.Terminate(ctx)
	}
}

func TestDownload_GitOverHTTP_BasicClone(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repoURL, cleanup := startGitHTTPServer(t)
	defer cleanup()

	dst := t.TempDir()
	d := &Downloader{repoURL: repoURL}

	_, err := d.Download(context.Background(), dst, settings.Settings{SSRFLevel: ssrf.None})
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	assertFileContains(t, filepath.Join(dst, "file.txt"), "hello from http\n")
	assertFileContains(t, filepath.Join(dst, "sub/nested.txt"), "nested via http\n")
	assertFileContains(t, filepath.Join(dst, "sub/extra.txt"), "second file\n")
}

func TestDownload_GitOverHTTP_WithRef(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repoURL, cleanup := startGitHTTPServer(t)
	defer cleanup()

	dst := t.TempDir()
	d := &Downloader{repoURL: repoURL, ref: "v1.0.0"}

	_, err := d.Download(context.Background(), dst, settings.Settings{SSRFLevel: ssrf.None})
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	assertFileContains(t, filepath.Join(dst, "file.txt"), "hello from http\n")
	assertFileContains(t, filepath.Join(dst, "sub/nested.txt"), "nested via http\n")
	assertFileNotExists(t, filepath.Join(dst, "sub/extra.txt"))
}

func TestDownload_GitOverHTTP_WithSubdir(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repoURL, cleanup := startGitHTTPServer(t)
	defer cleanup()

	dst := t.TempDir()
	d := &Downloader{repoURL: repoURL, subdir: "sub"}

	_, err := d.Download(context.Background(), dst, settings.Settings{SSRFLevel: ssrf.None})
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	assertFileContains(t, filepath.Join(dst, "nested.txt"), "nested via http\n")
	assertFileContains(t, filepath.Join(dst, "extra.txt"), "second file\n")
	assertFileNotExists(t, filepath.Join(dst, "file.txt"))
}

func TestDownload_GitOverHTTP_WithDepth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	repoURL, cleanup := startGitHTTPServer(t)
	defer cleanup()

	dst := t.TempDir()
	d := &Downloader{repoURL: repoURL, depth: 1}

	_, err := d.Download(context.Background(), dst, settings.Settings{SSRFLevel: ssrf.None})
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	assertFileContains(t, filepath.Join(dst, "file.txt"), "hello from http\n")
	assertFileContains(t, filepath.Join(dst, "sub/extra.txt"), "second file\n")
}
