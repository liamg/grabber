package git

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh"

	"github.com/liamg/grabber/settings"
)

// generateSSHKeyPair generates an ed25519 SSH key pair and returns
// the private key in PEM format and the public key in authorized_keys format.
func generateSSHKeyPair(t *testing.T) (privateKeyPEM []byte, authorizedKey []byte) {
	t.Helper()

	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	privBytes, err := ssh.MarshalPrivateKey(privKey, "")
	if err != nil {
		t.Fatalf("marshaling private key: %v", err)
	}
	privateKeyPEM = pem.EncodeToMemory(privBytes)

	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("creating ssh public key: %v", err)
	}
	authorizedKey = ssh.MarshalAuthorizedKey(sshPubKey)

	return privateKeyPEM, authorizedKey
}

func TestDownload_GitOverSSH(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	privateKey, authorizedKey := generateSSHKeyPair(t)

	// Create a temp directory with the authorized key and a bare git repo.
	tmpDir := t.TempDir()

	// Write authorized_keys file.
	sshDir := filepath.Join(tmpDir, "ssh")
	os.MkdirAll(sshDir, 0o700)
	if err := os.WriteFile(filepath.Join(sshDir, "authorized_keys"), authorizedKey, 0o600); err != nil {
		t.Fatalf("writing authorized_keys: %v", err)
	}

	// Create a bare git repo with test content using a script.
	repoSetupScript := `#!/bin/sh
set -e
apk add --no-cache git openssh >/dev/null 2>&1

git config --global init.defaultBranch main
git config --global safe.directory '*'

# Create git user with /bin/sh as shell (git-shell may not be in expected path)
adduser -D -s /bin/sh git 2>/dev/null || true
passwd -u git 2>/dev/null || true

mkdir -p /home/git/.ssh
cp /tmp/ssh/authorized_keys /home/git/.ssh/authorized_keys
chown -R git:git /home/git
chmod 700 /home/git/.ssh
chmod 600 /home/git/.ssh/authorized_keys

# Generate host keys
ssh-keygen -A

# Configure sshd to allow pubkey auth
echo "PubkeyAuthentication yes" >> /etc/ssh/sshd_config
echo "AuthorizedKeysFile .ssh/authorized_keys" >> /etc/ssh/sshd_config
echo "PasswordAuthentication no" >> /etc/ssh/sshd_config

# Create bare repo
mkdir -p /home/git/repo.git
cd /home/git/repo.git
git init --bare
chown -R git:git /home/git/repo.git

# Create a temporary clone to add content
cd /tmp
git clone /home/git/repo.git working
cd working
git config user.email "test@test.com"
git config user.name "Test"
echo "hello from ssh" > file.txt
mkdir sub
echo "nested via ssh" > sub/nested.txt
git add .
git commit -m "initial commit"
git push origin HEAD:main
cd /
rm -rf /tmp/working

# Start sshd in foreground
/usr/sbin/sshd -D -e
`

	scriptPath := filepath.Join(tmpDir, "setup.sh")
	if err := os.WriteFile(scriptPath, []byte(repoSetupScript), 0o755); err != nil {
		t.Fatalf("writing setup script: %v", err)
	}

	req := testcontainers.ContainerRequest{
		Image:        "alpine:latest",
		ExposedPorts: []string{"22/tcp"},
		Cmd:          []string{"/bin/sh", "/tmp/setup.sh"},
		Files: []testcontainers.ContainerFile{
			{
				HostFilePath:      scriptPath,
				ContainerFilePath: "/tmp/setup.sh",
				FileMode:          0o755,
			},
			{
				HostFilePath:      filepath.Join(sshDir, "authorized_keys"),
				ContainerFilePath: "/tmp/ssh/authorized_keys",
				FileMode:          0o600,
			},
		},
		WaitingFor: wait.ForListeningPort("22/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("starting ssh container: %v", err)
	}
	defer container.Terminate(ctx)

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("getting host: %v", err)
	}

	port, err := container.MappedPort(ctx, "22")
	if err != nil {
		t.Fatalf("getting port: %v", err)
	}

	// Build SSH URL.
	sshURL := fmt.Sprintf("ssh://git@%s:%s/home/git/repo.git", host, port.Port())

	dst := t.TempDir()

	d := &Downloader{repoURL: sshURL}
	s := settings.Settings{
		Git: settings.GitConfig{
			SSHKeys:                   []settings.SSHCredential{{Key: privateKey}},
			InsecureSkipHostKeyVerify: true,
		},
	}

	_, err = d.Download(ctx, dst, s)
	if err != nil {
		t.Fatalf("download via ssh: %v", err)
	}

	assertFileContains(t, filepath.Join(dst, "file.txt"), "hello from ssh\n")
	assertFileContains(t, filepath.Join(dst, "sub/nested.txt"), "nested via ssh\n")
}

func TestDownload_GitOverSSH_WithSubdir(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	privateKey, authorizedKey := generateSSHKeyPair(t)

	tmpDir := t.TempDir()
	sshDir := filepath.Join(tmpDir, "ssh")
	os.MkdirAll(sshDir, 0o700)
	if err := os.WriteFile(filepath.Join(sshDir, "authorized_keys"), authorizedKey, 0o600); err != nil {
		t.Fatalf("writing authorized_keys: %v", err)
	}

	repoSetupScript := `#!/bin/sh
set -e
apk add --no-cache git openssh >/dev/null 2>&1

git config --global init.defaultBranch main
git config --global safe.directory '*'

adduser -D -s /bin/sh git 2>/dev/null || true
passwd -u git 2>/dev/null || true

mkdir -p /home/git/.ssh
cp /tmp/ssh/authorized_keys /home/git/.ssh/authorized_keys
chown -R git:git /home/git
chmod 700 /home/git/.ssh
chmod 600 /home/git/.ssh/authorized_keys
ssh-keygen -A

echo "PubkeyAuthentication yes" >> /etc/ssh/sshd_config
echo "AuthorizedKeysFile .ssh/authorized_keys" >> /etc/ssh/sshd_config
echo "PasswordAuthentication no" >> /etc/ssh/sshd_config

mkdir -p /home/git/repo.git
cd /home/git/repo.git
git init --bare
chown -R git:git /home/git/repo.git

cd /tmp
git clone /home/git/repo.git working
cd working
git config user.email "test@test.com"
git config user.name "Test"
echo "root file" > root.txt
mkdir -p modules/vpc
echo "vpc main.tf" > modules/vpc/main.tf
echo "vpc vars.tf" > modules/vpc/variables.tf
git add .
git commit -m "initial commit"
git push origin HEAD:main
cd /
rm -rf /tmp/working

/usr/sbin/sshd -D -e
`

	scriptPath := filepath.Join(tmpDir, "setup.sh")
	os.WriteFile(scriptPath, []byte(repoSetupScript), 0o755)

	req := testcontainers.ContainerRequest{
		Image:        "alpine:latest",
		ExposedPorts: []string{"22/tcp"},
		Cmd:          []string{"/bin/sh", "/tmp/setup.sh"},
		Files: []testcontainers.ContainerFile{
			{
				HostFilePath:      scriptPath,
				ContainerFilePath: "/tmp/setup.sh",
				FileMode:          0o755,
			},
			{
				HostFilePath:      filepath.Join(sshDir, "authorized_keys"),
				ContainerFilePath: "/tmp/ssh/authorized_keys",
				FileMode:          0o600,
			},
		},
		WaitingFor: wait.ForListeningPort("22/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("starting ssh container: %v", err)
	}
	defer container.Terminate(ctx)

	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "22")

	sshURL := fmt.Sprintf("ssh://git@%s:%s/home/git/repo.git", host, port.Port())

	dst := t.TempDir()

	d := &Downloader{repoURL: sshURL, subdir: "modules/vpc"}
	s := settings.Settings{
		Git: settings.GitConfig{
			SSHKeys:                   []settings.SSHCredential{{Key: privateKey}},
			InsecureSkipHostKeyVerify: true,
		},
	}

	_, err = d.Download(ctx, dst, s)
	if err != nil {
		t.Fatalf("download via ssh with subdir: %v", err)
	}

	assertFileContains(t, filepath.Join(dst, "main.tf"), "vpc main.tf\n")
	assertFileContains(t, filepath.Join(dst, "variables.tf"), "vpc vars.tf\n")

	// Root file should not be present.
	assertFileNotExists(t, filepath.Join(dst, "root.txt"))
}
