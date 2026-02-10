package gcs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/api/option"
	"google.golang.org/api/storage/v1"

	"github.com/liamg/grabber/settings"
)

func startFakeGCS(t *testing.T) (endpoint string, cleanup func()) {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "fsouza/fake-gcs-server:latest",
		ExposedPorts: []string{"4443/tcp"},
		Cmd:          []string{"-scheme", "http", "-port", "4443"},
		WaitingFor: wait.ForHTTP("/storage/v1/b").
			WithPort("4443/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("starting fake-gcs-server: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("getting host: %v", err)
	}

	port, err := container.MappedPort(ctx, "4443")
	if err != nil {
		t.Fatalf("getting port: %v", err)
	}

	endpoint = fmt.Sprintf("http://%s:%s", host, port.Port())

	// Update the external URL so fake-gcs-server generates correct download URLs.
	updateBody := fmt.Sprintf(`{"externalUrl": %q}`, endpoint)
	updateReq, _ := http.NewRequest(http.MethodPut, endpoint+"/_internal/config", bytes.NewBufferString(updateBody))
	updateReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(updateReq)
	if err != nil {
		container.Terminate(ctx)
		t.Fatalf("updating fake-gcs-server config: %v", err)
	}
	resp.Body.Close()

	return endpoint, func() {
		container.Terminate(ctx)
	}
}

// newAdminService creates a GCS service for test setup (creating buckets, uploading objects).
func newAdminService(t *testing.T, endpoint string) *storage.Service {
	t.Helper()
	ctx := context.Background()

	svc, err := storage.NewService(ctx,
		option.WithEndpoint(endpoint+"/storage/v1/"),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("creating admin GCS service: %v", err)
	}

	return svc
}

func createGCSBucket(t *testing.T, svc *storage.Service, bucket string) {
	t.Helper()
	_, err := svc.Buckets.Insert("test-project", &storage.Bucket{
		Name: bucket,
	}).Do()
	if err != nil {
		t.Fatalf("creating bucket: %v", err)
	}
}

func putGCSObject(t *testing.T, svc *storage.Service, bucket, key, content string) {
	t.Helper()
	obj := &storage.Object{Name: key}
	_, err := svc.Objects.Insert(bucket, obj).
		Media(io.NopCloser(bytes.NewReader([]byte(content)))).
		Do()
	if err != nil {
		t.Fatalf("putting object gs://%s/%s: %v", bucket, key, err)
	}
}

func testSettings(endpoint string) settings.Settings {
	s := settings.Defaults
	s.GCPCredentials.Endpoint = endpoint + "/storage/v1/"
	return s
}

func TestGCSIntegration_SingleFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	endpoint, cleanup := startFakeGCS(t)
	defer cleanup()

	admin := newAdminService(t, endpoint)
	createGCSBucket(t, admin, "test-bucket")
	putGCSObject(t, admin, "test-bucket", "hello.txt", "hello from gcs")

	d := &Downloader{
		bucket: "test-bucket",
		key:    "hello.txt",
	}

	dst := t.TempDir()
	isFile, err := d.Download(context.Background(), dst, testSettings(endpoint))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !isFile {
		t.Error("expected isFile=true for single file download")
	}

	assertFileContent(t, filepath.Join(dst, "hello.txt"), "hello from gcs")
}

func TestGCSIntegration_Directory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	endpoint, cleanup := startFakeGCS(t)
	defer cleanup()

	admin := newAdminService(t, endpoint)
	createGCSBucket(t, admin, "test-bucket")
	putGCSObject(t, admin, "test-bucket", "mydir/file1.txt", "content1")
	putGCSObject(t, admin, "test-bucket", "mydir/file2.txt", "content2")
	putGCSObject(t, admin, "test-bucket", "mydir/sub/file3.txt", "content3")

	d := &Downloader{
		bucket: "test-bucket",
		key:    "mydir/",
	}

	dst := t.TempDir()
	isFile, err := d.Download(context.Background(), dst, testSettings(endpoint))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if isFile {
		t.Error("expected isFile=false for directory download")
	}

	assertFileContent(t, filepath.Join(dst, "file1.txt"), "content1")
	assertFileContent(t, filepath.Join(dst, "file2.txt"), "content2")
	assertFileContent(t, filepath.Join(dst, "sub/file3.txt"), "content3")
}

func TestGCSIntegration_EmptyBucket(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	endpoint, cleanup := startFakeGCS(t)
	defer cleanup()

	admin := newAdminService(t, endpoint)
	createGCSBucket(t, admin, "empty-bucket")

	d := &Downloader{
		bucket: "empty-bucket",
		key:    "prefix/",
	}

	dst := t.TempDir()
	_, err := d.Download(context.Background(), dst, testSettings(endpoint))
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty directory, got %d entries", len(entries))
	}
}

func TestGCSIntegration_SparseCheckoutError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	d := &Downloader{
		bucket: "test-bucket",
		key:    "file.txt",
	}

	s := settings.Defaults
	s.EnableSparseCheckout = true
	_, err := d.Download(context.Background(), t.TempDir(), s)
	if err == nil {
		t.Fatal("expected error for sparse checkout on GCS")
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
