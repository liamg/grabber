package s3

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/liamg/grabber/settings"
)

func startLocalStack(t *testing.T) (endpoint string, cleanup func()) {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "localstack/localstack:3.8.1",
		ExposedPorts: []string{"4566/tcp"},
		Env: map[string]string{
			"SERVICES": "s3",
		},
		WaitingFor: wait.ForHTTP("/_localstack/health").
			WithPort("4566/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("starting localstack: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("getting host: %v", err)
	}

	port, err := container.MappedPort(ctx, "4566")
	if err != nil {
		t.Fatalf("getting port: %v", err)
	}

	endpoint = fmt.Sprintf("http://%s:%s", host, port.Port())

	return endpoint, func() {
		container.Terminate(ctx)
	}
}

func newLocalStackS3Client(t *testing.T, endpoint string) *awss3.Client {
	t.Helper()
	ctx := context.Background()

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", ""),
		),
	)
	if err != nil {
		t.Fatalf("loading aws config: %v", err)
	}

	return awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}

func createBucket(t *testing.T, client *awss3.Client, bucket string) {
	t.Helper()
	_, err := client.CreateBucket(context.Background(), &awss3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		t.Fatalf("creating bucket: %v", err)
	}
}

func putObject(t *testing.T, client *awss3.Client, bucket, key, content string) {
	t.Helper()
	_, err := client.PutObject(context.Background(), &awss3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader([]byte(content)),
	})
	if err != nil {
		t.Fatalf("putting object: %v", err)
	}
}

func TestS3Integration_SingleFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	endpoint, cleanup := startLocalStack(t)
	defer cleanup()

	client := newLocalStackS3Client(t, endpoint)
	createBucket(t, client, "test-bucket")
	putObject(t, client, "test-bucket", "hello.txt", "hello world")

	dst := t.TempDir()

	// The S3 downloader talks to real S3 endpoints by default.
	// We need to override the client for localstack. To do this,
	// we'll test the downloader directly with a custom client.
	d := &Downloader{
		bucket: "test-bucket",
		key:    "hello.txt",
	}

	lsClient := newLocalStackS3Client(t, endpoint)
	fileDst := filepath.Join(dst, "hello.txt")
	err := d.downloadFile(context.Background(), lsClient, "hello.txt", fileDst)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	data, err := os.ReadFile(fileDst)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("got %q, want %q", string(data), "hello world")
	}
}

func TestS3Integration_Directory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	endpoint, cleanup := startLocalStack(t)
	defer cleanup()

	client := newLocalStackS3Client(t, endpoint)
	createBucket(t, client, "test-bucket")
	putObject(t, client, "test-bucket", "mydir/file1.txt", "content1")
	putObject(t, client, "test-bucket", "mydir/file2.txt", "content2")
	putObject(t, client, "test-bucket", "mydir/sub/file3.txt", "content3")

	dst := t.TempDir()

	d := &Downloader{
		bucket: "test-bucket",
		key:    "mydir/",
	}

	lsClient := newLocalStackS3Client(t, endpoint)
	err := d.downloadDir(context.Background(), lsClient, dst)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	assertFileContent(t, filepath.Join(dst, "file1.txt"), "content1")
	assertFileContent(t, filepath.Join(dst, "file2.txt"), "content2")
	assertFileContent(t, filepath.Join(dst, "sub/file3.txt"), "content3")
}

func TestS3Integration_WithCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	endpoint, cleanup := startLocalStack(t)
	defer cleanup()

	client := newLocalStackS3Client(t, endpoint)
	createBucket(t, client, "cred-bucket")
	putObject(t, client, "cred-bucket", "secret.txt", "secret data")

	// Test that credentials from settings are used.
	// LocalStack doesn't actually enforce credentials, but we verify
	// the credential resolution path works without error.
	s := settings.Settings{
		AWSCredentials: settings.AWSCredentials{
			AccessKeyID:     "test",
			SecretAccessKey: "test",
			Region:          "us-east-1",
		},
	}

	d := &Downloader{
		bucket: "cred-bucket",
		key:    "secret.txt",
	}

	// Verify the credential resolution works without error.
	ctx := context.Background()
	_, err := d.newClient(ctx, s)
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}

	// Create a client pointed at localstack for the actual download.
	s3Client := awss3.NewFromConfig(func() aws.Config {
		cfg, _ := awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithRegion("us-east-1"),
			awsconfig.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider("test", "test", ""),
			),
		)
		return cfg
	}(), func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	dst := filepath.Join(t.TempDir(), "secret.txt")
	err = d.downloadFile(ctx, s3Client, "secret.txt", dst)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	assertFileContent(t, dst, "secret data")
}

func TestS3Integration_EmptyBucket(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	endpoint, cleanup := startLocalStack(t)
	defer cleanup()

	client := newLocalStackS3Client(t, endpoint)
	createBucket(t, client, "empty-bucket")

	dst := t.TempDir()

	d := &Downloader{
		bucket: "empty-bucket",
		key:    "prefix/",
	}

	lsClient := newLocalStackS3Client(t, endpoint)
	err := d.downloadDir(context.Background(), lsClient, dst)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	// Should succeed but dst should be empty.
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty directory, got %d entries", len(entries))
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
