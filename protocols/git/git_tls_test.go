package git

import (
	"net/url"
	"testing"

	gogit "github.com/go-git/go-git/v5"

	"github.com/liamg/grabber/settings"
)

func TestApplyTLSAndProxy(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.example.com:8080")

	full := settings.Settings{
		TLSCACerts: [][]byte{[]byte("ca-one"), []byte("ca-two")},
		ClientCertificates: []settings.ClientCertificate{
			{Cert: []byte("default-cert"), Key: []byte("default-key")},
			{Host: "github.example.com", Cert: []byte("gh-cert"), Key: []byte("gh-key")},
		},
		Proxies: []settings.ProxyConfig{
			{URL: proxyURL, Username: "u", Password: "p"},
		},
	}

	t.Run("https clone gets CA bundle, host cert, and proxy", func(t *testing.T) {
		opts := &gogit.CloneOptions{}
		applyTLSAndProxy(opts, "https://github.example.com/org/repo.git", full)

		if string(opts.CABundle) != "ca-one\nca-two" {
			t.Errorf("CABundle = %q", opts.CABundle)
		}
		if string(opts.ClientCert) != "gh-cert" || string(opts.ClientKey) != "gh-key" {
			t.Errorf("client cert = %q/%q, want host-scoped pair", opts.ClientCert, opts.ClientKey)
		}
		if opts.ProxyOptions.URL != "http://proxy.example.com:8080" {
			t.Errorf("proxy URL = %q", opts.ProxyOptions.URL)
		}
		if opts.ProxyOptions.Username != "u" || opts.ProxyOptions.Password != "p" {
			t.Errorf("proxy creds = %q/%q", opts.ProxyOptions.Username, opts.ProxyOptions.Password)
		}
	})

	t.Run("unmatched host falls back to default cert", func(t *testing.T) {
		opts := &gogit.CloneOptions{}
		applyTLSAndProxy(opts, "https://gitlab.example.com/org/repo.git", full)
		if string(opts.ClientCert) != "default-cert" {
			t.Errorf("client cert = %q, want default", opts.ClientCert)
		}
	})

	t.Run("ssh URL is a no-op", func(t *testing.T) {
		opts := &gogit.CloneOptions{}
		applyTLSAndProxy(opts, "ssh://git@github.example.com/org/repo.git", full)
		if opts.CABundle != nil || opts.ClientCert != nil || opts.ProxyOptions.URL != "" {
			t.Errorf("expected no-op for ssh URL, got %+v", opts)
		}
	})

	t.Run("scp URL is a no-op", func(t *testing.T) {
		opts := &gogit.CloneOptions{}
		applyTLSAndProxy(opts, "git@github.example.com:org/repo.git", full)
		if opts.CABundle != nil || opts.ClientCert != nil || opts.ProxyOptions.URL != "" {
			t.Errorf("expected no-op for scp URL, got %+v", opts)
		}
	})

	t.Run("no configuration is a no-op", func(t *testing.T) {
		opts := &gogit.CloneOptions{}
		applyTLSAndProxy(opts, "https://github.example.com/org/repo.git", settings.Settings{})
		if opts.CABundle != nil || opts.ClientCert != nil || opts.ProxyOptions.URL != "" {
			t.Errorf("expected no-op with empty settings, got %+v", opts)
		}
	})
}
