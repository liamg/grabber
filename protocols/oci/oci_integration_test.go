package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/liamg/grabber/settings"
)

func startRegistry(t *testing.T) (host string, cleanup func()) {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "registry:2",
		ExposedPorts: []string{"5000/tcp"},
		WaitingFor: wait.ForHTTP("/v2/").
			WithPort("5000/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("starting registry: %v", err)
	}

	containerHost, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("getting host: %v", err)
	}

	port, err := container.MappedPort(ctx, "5000")
	if err != nil {
		t.Fatalf("getting port: %v", err)
	}

	host = fmt.Sprintf("%s:%s", containerHost, port.Port())

	return host, func() {
		container.Terminate(ctx)
	}
}

// pushTestArtifact pushes a simple artifact with a single file layer to the registry.
func pushTestArtifact(t *testing.T, registryHost, repoName, tag string, fileName string, fileContent []byte) {
	t.Helper()
	ctx := context.Background()

	store := memory.New()

	// Push the file content as a blob.
	blobDesc, err := oras.PushBytes(ctx, store, "application/octet-stream", fileContent)
	if err != nil {
		t.Fatalf("pushing blob: %v", err)
	}

	// Annotate with the file name so oras file store can extract it.
	blobDesc.Annotations = map[string]string{
		ocispec.AnnotationTitle: fileName,
	}

	// Pack a manifest with the blob as a layer.
	packOpts := oras.PackManifestOptions{
		Layers: []ocispec.Descriptor{blobDesc},
	}
	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, "application/vnd.test.artifact", packOpts)
	if err != nil {
		t.Fatalf("packing manifest: %v", err)
	}

	// Tag the manifest.
	if err := store.Tag(ctx, manifestDesc, tag); err != nil {
		t.Fatalf("tagging: %v", err)
	}

	// Push to the remote registry.
	ref := fmt.Sprintf("%s/%s", registryHost, repoName)
	repo, err := remote.NewRepository(ref)
	if err != nil {
		t.Fatalf("creating remote repo: %v", err)
	}
	repo.PlainHTTP = true

	_, err = oras.Copy(ctx, store, tag, repo, tag, oras.DefaultCopyOptions)
	if err != nil {
		t.Fatalf("pushing to registry: %v", err)
	}
}

// pushTestArtifactMultiLayer pushes an artifact with multiple file layers.
func pushTestArtifactMultiLayer(t *testing.T, registryHost, repoName, tag string, files map[string][]byte) {
	t.Helper()
	ctx := context.Background()

	store := memory.New()

	var layers []ocispec.Descriptor
	for name, content := range files {
		desc, err := oras.PushBytes(ctx, store, "application/octet-stream", content)
		if err != nil {
			t.Fatalf("pushing blob %s: %v", name, err)
		}
		desc.Annotations = map[string]string{
			ocispec.AnnotationTitle: name,
		}
		layers = append(layers, desc)
	}

	packOpts := oras.PackManifestOptions{
		Layers: layers,
	}
	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, "application/vnd.test.artifact", packOpts)
	if err != nil {
		t.Fatalf("packing manifest: %v", err)
	}

	if err := store.Tag(ctx, manifestDesc, tag); err != nil {
		t.Fatalf("tagging: %v", err)
	}

	ref := fmt.Sprintf("%s/%s", registryHost, repoName)
	repo, err := remote.NewRepository(ref)
	if err != nil {
		t.Fatalf("creating remote repo: %v", err)
	}
	repo.PlainHTTP = true

	_, err = oras.Copy(ctx, store, tag, repo, tag, oras.DefaultCopyOptions)
	if err != nil {
		t.Fatalf("pushing to registry: %v", err)
	}
}

func TestOCIIntegration_SingleFileArtifact(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	registryHost, cleanup := startRegistry(t)
	defer cleanup()

	pushTestArtifact(t, registryHost, "test/artifact", "v1.0.0", "hello.txt", []byte("hello from oci"))

	d := &Downloader{
		ref:      fmt.Sprintf("%s/test/artifact:v1.0.0", registryHost),
		registry: registryHost,
	}

	s := settings.Defaults
	s.OCIPlainHTTP = true

	dst := t.TempDir()
	_, err := d.Download(context.Background(), dst, s)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	assertFileContent(t, filepath.Join(dst, "hello.txt"), "hello from oci")
}

// TestOCIIntegration_WithCredentials exercises the auth-client construction
// path (credentials configured, no custom transport). The local registry
// ignores the Authorization header, but the pull must still succeed through
// the credentialed client.
func TestOCIIntegration_WithCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	registryHost, cleanup := startRegistry(t)
	defer cleanup()

	pushTestArtifact(t, registryHost, "test/authed", "v1.0.0", "hello.txt", []byte("authed content"))

	d := &Downloader{
		ref:      fmt.Sprintf("%s/test/authed:v1.0.0", registryHost),
		registry: registryHost,
	}

	s := settings.Defaults
	s.OCIPlainHTTP = true
	s.OCICredentials = []settings.OCICredential{
		{Registry: registryHost, Username: "user", Password: "pass"},
	}

	dst := t.TempDir()
	if _, err := d.Download(context.Background(), dst, s); err != nil {
		t.Fatalf("download with credentials: %v", err)
	}

	assertFileContent(t, filepath.Join(dst, "hello.txt"), "authed content")
}

func TestOCIIntegration_MultiLayerArtifact(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	registryHost, cleanup := startRegistry(t)
	defer cleanup()

	files := map[string][]byte{
		"file1.txt": []byte("content1"),
		"file2.txt": []byte("content2"),
	}
	pushTestArtifactMultiLayer(t, registryHost, "test/multi", "latest", files)

	d := &Downloader{
		ref:      fmt.Sprintf("%s/test/multi:latest", registryHost),
		registry: registryHost,
	}

	s := settings.Defaults
	s.OCIPlainHTTP = true

	dst := t.TempDir()
	_, err := d.Download(context.Background(), dst, s)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	assertFileContent(t, filepath.Join(dst, "file1.txt"), "content1")
	assertFileContent(t, filepath.Join(dst, "file2.txt"), "content2")
}

func TestOCIIntegration_LatestTag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	registryHost, cleanup := startRegistry(t)
	defer cleanup()

	pushTestArtifact(t, registryHost, "test/latest", "latest", "data.txt", []byte("latest data"))

	d := &Downloader{
		ref:      fmt.Sprintf("%s/test/latest:latest", registryHost),
		registry: registryHost,
	}

	s := settings.Defaults
	s.OCIPlainHTTP = true

	dst := t.TempDir()
	_, err := d.Download(context.Background(), dst, s)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	assertFileContent(t, filepath.Join(dst, "data.txt"), "latest data")
}

func TestOCIIntegration_NonexistentTag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	registryHost, cleanup := startRegistry(t)
	defer cleanup()

	d := &Downloader{
		ref:      fmt.Sprintf("%s/test/noexist:v999", registryHost),
		registry: registryHost,
	}

	s := settings.Defaults
	s.OCIPlainHTTP = true

	_, err := d.Download(context.Background(), t.TempDir(), s)
	if err == nil {
		t.Fatal("expected error for nonexistent tag")
	}
}

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(data) != expected {
		t.Errorf("%s: got %q, want %q", path, string(data), expected)
	}
}

// Unused but ensures json import is valid for future use.
var _ = json.Marshal
