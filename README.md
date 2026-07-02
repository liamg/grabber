# grabber

A Go library for downloading files and directories from various sources using a URL string as input.

Grabber is an alternative to [go-getter](https://github.com/hashicorp/go-getter) with a cleaner API and active development.

## Why grabber?

| | grabber | go-getter |
|---|---|---|
| Sparse checkout | ✅ | ❌ |
| Programmatic credential injection | ✅ | ❌ (env vars / URL params only) |
| HTTPS credential matching | ✅ (git-style host/path matching) | ❌ |
| Git credential helper support | ✅ (via system `git`) | ✅ (shells out to `git`) |
| SSH-to-HTTPS auto-transform | ✅ | ❌ |
| OCI registry support | ✅ | ❌ |
| Checksum verification | ✅ (URL param or explicit API) | ✅ (URL param only) |
| Pure Go | ✅ (`git` required only if using credential helpers; `hg` required for Mercurial) | ❌ (shells out to git, hg, etc.) |
| Zstandard / LZ4 archives | ✅ | ❌ |
| Actively maintained | ✅ | ❌ [Maintenance-only](https://github.com/hashicorp/go-getter/graphs/commit-activity) |

## Features

- Download files and directories from Git, Mercurial, S3, GCS, OCI, HTTP, and local filesystems
- **Sparse checkout** — only fetch the subdirectory you need from a Git repo
- **Programmatic credential injection** — pass SSH keys, AWS credentials, GCP service account keys, OCI registry credentials, and HTTPS credentials via the Go API
- **HTTPS credential matching** — configure HTTPS credentials with git-style host/path matching, used automatically for Git and HTTP protocols
- **SSH-to-HTTPS auto-transform** — automatically convert SSH/SCP Git URLs to HTTPS (useful in CI environments without SSH key access)
- **Pure Go** — no system `git` or other CLI tools required (except `hg` for Mercurial; system `git` is used for credential helper support if available)
- **Checksum verification** — verify downloaded file integrity via URL query param (`?checksum=sha256:abc...`) or the explicit `GrabWithSHA256Checksum()` API
- **Automatic archive extraction** — downloaded archives are detected and extracted by extension
- **Subdirectory support** — use `//` in URLs to extract a subdirectory (e.g. `github.com/user/repo//sub/dir`)
- **Protocol auto-detection** — URLs are automatically routed to the right protocol based on hostname, scheme, and path
- **Extensible** — bring your own protocol implementations via `WithProtocols()`

## Supported Protocols

| Protocol | Prefix | Status | Description |
|----------|--------|--------|-------------|
| Git | `git::` | Implemented | Clone Git repos over HTTPS, SSH, or git:// |
| Mercurial | `hg::` | Implemented | Clone Mercurial repos (requires `hg` CLI) |
| S3 | `s3::` | Implemented | Download files/directories from Amazon S3 |
| GCS | `gcs::` | Implemented | Download files/directories from Google Cloud Storage |
| OCI | `oci::` | Implemented | Pull artifacts from OCI-compatible registries |
| HTTP/HTTPS | `http::` | Implemented | Plain file downloads over HTTP/HTTPS |
| File | `file::` | Implemented | Copy from local filesystem paths |

Protocols are auto-detected from the URL.

### Git

**Supported URL formats:**

| Format | Example |
|--------|---------|
| HTTPS | `https://github.com/user/repo.git` |
| SSH | `ssh://git@github.com/user/repo.git` |
| SCP-style | `git@github.com:user/repo.git` |
| git:// | `git://github.com/user/repo.git` |

**Auto-detected when:**
- URL has `.git` suffix
- URL uses `ssh://` or `git://` scheme
- URL is SCP-style (`git@host:user/repo`)
- Host is a known Git provider: `github.com`, `gitlab.com`, `bitbucket.org`, `codeberg.org`, `dev.azure.com`, `sr.ht`

**Query parameters:**
- `ref` - branch, tag, or commit SHA to check out
- `depth` - shallow clone depth (e.g. `?depth=1`)

**Subdirectory support:**
Use `//` to specify a subdirectory: `github.com/user/repo//modules/vpc?ref=v1.0.0`

When sparse checkout is enabled, only the specified subdirectory is checked out. Otherwise the full repo is cloned and the subdirectory is extracted.

**Orphaned commit fallback:**
When `ref` is a commit SHA that the git protocol can't reach (e.g. an orphaned commit that is no longer reachable from any branch or tag), grabber falls back to downloading a tarball of that commit from the hosting platform's HTTP API. GitHub, GitLab, and Bitbucket are supported. Credentials are resolved from the same sources as clones (URL userinfo, configured HTTPS credentials, the git credential helper) and, failing those, from well-known API token environment variables: `GH_TOKEN`/`GITHUB_TOKEN`, `GITLAB_TOKEN`/`GL_TOKEN`, and `BITBUCKET_TOKEN`. The result is a plain source snapshot with no `.git` directory. SSH keys cannot be used for this HTTP fallback, so SSH-only setups need HTTP credentials configured for private repositories.

### Mercurial

> **Note:** Mercurial support requires the `hg` CLI to be installed on the system.

**Supported URL formats:**

| Format | Example |
|--------|---------|
| HTTPS | `https://bitbucket.org/user/repo` |

**Auto-detected when:**
- Host is a known Mercurial provider: `bitbucket.org`

Since Bitbucket also hosts Git repos (and Git has higher priority), use the `hg::` prefix to force Mercurial: `hg::https://bitbucket.org/user/repo`

**Query parameters:**
- `rev` — revision, tag, or branch to check out (e.g. `?rev=v1.0.0`)

**Subdirectory support:**
Use `//` to specify a subdirectory: `hg::bitbucket.org/user/repo//lib/core?rev=stable`

### S3

**Supported URL formats:**

| Format | Example |
|--------|---------|
| s3:// scheme | `s3://bucket/key` |
| Path-style | `s3.amazonaws.com/bucket/key` |
| Path-style regional | `s3.us-west-2.amazonaws.com/bucket/key` |
| Virtual-hosted | `bucket.s3.amazonaws.com/key` |
| Virtual-hosted regional | `bucket.s3.us-west-2.amazonaws.com/key` |

**Auto-detected when** (no `s3::` prefix needed):
- URL uses `s3://` scheme
- Hostname contains `s3` and `amazonaws.com`

Keys ending in `/` are treated as directory prefixes - all objects under that prefix are downloaded.

### GCS

**Supported URL formats:**

| Format | Example |
|--------|---------|
| Path-style googleapis | `storage.googleapis.com/bucket/key` |
| Path-style cloud.google.com | `storage.cloud.google.com/bucket/key` |
| Virtual-hosted | `bucket.storage.googleapis.com/key` |

**Auto-detected when:**
- Hostname is `storage.googleapis.com` or `storage.cloud.google.com`
- Hostname ends with `.storage.googleapis.com`

Keys ending in `/` are treated as directory prefixes - all objects under that prefix are downloaded.

### OCI

**Supported URL formats:**

| Format | Example |
|--------|---------|
| With tag | `oci://ghcr.io/user/repo:v1.0.0` |
| With digest | `oci://ghcr.io/user/repo@sha256:abc123...` |
| Latest (default) | `oci://ghcr.io/user/repo` |

**Auto-detected when:**
- URL uses `oci://` scheme

### HTTP/HTTPS

**Supported URL formats:**

| Format | Example |
|--------|---------|
| HTTPS | `https://example.com/path/to/file.tar.gz` |
| HTTP | `http://example.com/path/to/file.tar.gz` |
| No scheme (defaults to HTTPS) | `example.com/path/to/file.tar.gz` |

**Auto-detected when:**
- URL uses `http://` or `https://` scheme
- URL has no scheme (defaults to HTTPS)

HTTP is the lowest-priority protocol, so it acts as a fallback when no other protocol matches.

### File

**Supported URL formats:**

| Format | Example |
|--------|---------|
| file:// scheme | `file:///path/to/source` |
| Absolute path | `/path/to/source` |
| Relative path | `./relative/path` |

**Auto-detected when:**
- URL uses `file://` scheme
- URL is an absolute filesystem path
- URL starts with `./` or `../`

If the source is a directory, all contents are copied recursively. If it's a file, it's copied as a single file (and may be auto-extracted if it's an archive).

## Options

Options are passed to `grabber.New()`:

```go
g := grabber.New(
    grabber.WithGitSSHKey(privateKey),
    grabber.WithAWSCredentials(keyID, secret, token, region),
)
```

| Option | Description |
|--------|-------------|
| `WithSparseCheckout(bool)` | Enable sparse checkout for Git subdirectories (default: `false`) |
| `WithAutoExtract(bool)` | Enable automatic archive extraction (default: `true`) |
| `WithGitSSHKey([]byte)` | SSH private key for Git authentication |
| `WithGitDepth(int)` | Override shallow clone depth for Git (default: 1; 0 = full clone) |
| `WithGitInsecureSkipHostKeyVerify()` | Skip SSH host key verification |
| `WithAWSCredentials(keyID, secret, token, region)` | Static AWS credentials for S3 |
| `WithGCPCredentials(serviceAccountKey)` | GCP service account key for GCS |
| `WithOCICredentials(username, password)` | Registry credentials for OCI |
| `WithOCIPlainHTTP()` | Use HTTP instead of HTTPS for OCI registries |
| `WithHTTPSCredential(host, user, pass)` | Add an HTTPS credential matched by host |
| `WithHTTPSCredentialForPath(host, path, user, pass)` | Add an HTTPS credential matched by host and path prefix |
| `WithGitSSHToHTTPS()` | Auto-convert SSH/SCP Git URLs to HTTPS before cloning |
| `WithProtocols(...Protocol)` | Override the default set of protocols |

When AWS/GCP credentials are not provided, the respective SDK default credential chains are used (env vars, shared config, IAM roles, etc.).

Git clones default to `depth=1` (shallow) for performance, since go-git is slower than system `git` for full clones and full history is rarely needed. Commit hash refs (`?ref=abc1234`) automatically use a full clone so the commit is reachable. URL query parameters (`?depth=1`) override all defaults.

### HTTPS Credential Matching

HTTPS credentials are matched using git-style semantics: host must match (case-insensitive), and if a path is specified it must be a prefix of the URL path. The most specific match (longest path prefix) wins.

```go
g := grabber.New(
    // Matches any URL on github.com
    grabber.WithHTTPSCredential("github.com", "user", "token"),

    // Matches only URLs under github.com/my-org/... (takes priority over the above)
    grabber.WithHTTPSCredentialForPath("github.com", "/my-org", "org-user", "org-token"),
)
```

Credentials are applied automatically to both Git (HTTPS clones) and HTTP downloads. For Git, HTTPS credentials are checked after embedded URL credentials but before system `git credential fill`.

### SSH-to-HTTPS Auto-Transform

When `WithGitSSHToHTTPS()` is enabled, SSH and SCP-style Git URLs are automatically converted to HTTPS before cloning:

- `git@github.com:user/repo.git` → `https://github.com/user/repo.git`
- `ssh://git@github.com/user/repo.git` → `https://github.com/user/repo.git`

This is useful in CI environments where SSH keys are not available but HTTPS tokens are configured via `WithHTTPSCredential()`.

## Archive Extraction

When `WithAutoExtract()` is enabled (the default), downloaded files are automatically detected and extracted by extension:

| Format | Extensions |
|--------|------------|
| Tar | `.tar` |
| Tar + Gzip | `.tar.gz`, `.tgz` |
| Tar + Bzip2 | `.tar.bz2`, `.tbz2` |
| Tar + XZ | `.tar.xz`, `.txz` |
| Tar + Zstandard | `.tar.zst`, `.tzst` |
| Tar + LZ4 | `.tar.lz4` |
| Zip | `.zip` |
| Gzip | `.gz` |
| Bzip2 | `.bz2` |
| XZ | `.xz` |
| Zstandard | `.zst` |
| LZ4 | `.lz4` |

## Checksum Verification

Downloaded files can be verified against an expected checksum. This works for single-file downloads only (not directories).

**Via URL query parameter:**

```go
// With explicit algorithm
err := g.Grab(ctx, "https://example.com/file.tar.gz?checksum=sha256:e3b0c44...", "./output")

// Without algorithm prefix — defaults to SHA-256
err := g.Grab(ctx, "https://example.com/file.tar.gz?checksum=e3b0c44...", "./output")
```

**Via explicit API (recommended):**

```go
err := g.GrabWithSHA256Checksum(ctx, "https://example.com/file.tar.gz", "./output", "e3b0c44...")
```

When both a URL parameter and an explicit checksum are provided, the explicit one takes precedence.

The URL parameter supports other algorithms via the `algo:hex` format (e.g. `?checksum=sha512:cf83e13...`). Supported algorithms: `md5`, `sha1`, `sha256`, `sha512`.

Checksum verification runs on the raw downloaded file, before archive extraction.

## Usage

```go
package main

import (
    "context"
    "log"

    "github.com/liamg/grabber"
)

func main() {
    g := grabber.New(
        grabber.WithGitSSHKey(privateKeyBytes),
    )

    // Clone a Git repo subdirectory
    err := g.Grab(context.Background(), "github.com/user/repo//modules/vpc?ref=v1.0.0", "./vpc")
    if err != nil {
        log.Fatal(err)
    }

    // Download from S3
    err = g.Grab(context.Background(), "s3.amazonaws.com/my-bucket/config.tar.gz", "./config")
    if err != nil {
        log.Fatal(err)
    }
}
```

## CLI

A CLI tool is included for testing and quick downloads:

```bash
go install github.com/liamg/grabber/cmd/grabber@latest
```

```bash
# Download a file
grabber https://example.com/file.tar.gz ./output

# Clone a Git repo subdirectory
grabber github.com/user/repo//modules/vpc ./vpc

# Download with checksum verification
grabber -c e3b0c44298fc1c14... https://example.com/file.tar.gz ./output

# Copy a local file
grabber ./path/to/source ./destination
```

Run `grabber --help` for all available flags.

## Installation

**Library:**

```
go get github.com/liamg/grabber
```

**CLI:**

```
go install github.com/liamg/grabber/cmd/grabber@latest
```

## Status

Early development. API is not yet stable.

## Known Limitations

- **Git credential helpers require system `git`** — go-git doesn't support `credential.helper` from `~/.gitconfig`, so grabber shells out to `git credential fill` when `git` is on `PATH`. Without system `git`, credential helpers won't work — use `WithGitSSHKey()`, `WithHTTPSCredential()`, or embed credentials in the URL instead.

## License

[Unlicense](./LICENSE).
