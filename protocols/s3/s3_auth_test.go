package s3

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liamg/grabber/settings"
)

// fetchAuth downloads an object from a fake S3 endpoint and returns the
// Authorization header the request carried.
func fetchAuth(t *testing.T, s settings.Settings) string {
	t.Helper()

	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("module bytes"))
	}))
	defer srv.Close()

	d, ok := New().DetectForced(srv.URL + "/bucket/main.tf")
	if !ok {
		t.Fatal("DetectForced did not match")
	}
	s.MaxBytes = settings.DefaultMaxBytes
	if _, err := d.Download(context.Background(), t.TempDir(), s); err != nil {
		t.Fatalf("Download: %v", err)
	}
	return auth
}

func setAmbientAWSCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAAMBIENT")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
}

func TestDownloadUsesAmbientCredentialsByDefault(t *testing.T) {
	setAmbientAWSCredentials(t)

	if auth := fetchAuth(t, settings.Settings{}); !containsKey(auth, "AKIAAMBIENT") {
		t.Errorf("Authorization = %q, want it signed with the ambient key", auth)
	}
}

func TestDownloadWithoutAmbientCredentialsIsUnsigned(t *testing.T) {
	setAmbientAWSCredentials(t)

	if auth := fetchAuth(t, settings.Settings{NoAmbientCredentials: true}); auth != "" {
		t.Errorf("Authorization = %q, want no credentials to be sent", auth)
	}
}

func TestDownloadWithoutAmbientCredentialsUsesExplicitCredentials(t *testing.T) {
	setAmbientAWSCredentials(t)

	auth := fetchAuth(t, settings.Settings{
		NoAmbientCredentials: true,
		AWSCredentials: settings.AWSCredentials{
			AccessKeyID:     "AKIAEXPLICIT",
			SecretAccessKey: "explicit-secret",
		},
	})
	if !containsKey(auth, "AKIAEXPLICIT") {
		t.Errorf("Authorization = %q, want it signed with the explicit key", auth)
	}
}

func containsKey(auth, key string) bool {
	return strings.Contains(auth, "Credential="+key+"/")
}
