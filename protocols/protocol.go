package protocols

import (
	"context"

	"github.com/liamg/grabber/settings"
)

type Protocol interface {
	// Prefix is a unique prefix that can optionally be added to the beginning of a URL to help with detection.
	// For example, "git::". All prefixes use "::", so this method just returns the part before "::".
	Prefix() string
	// Priority is used to determine the order in which protocols are checked. Higher priority protocols are checked first.
	// This is useful for protocols that have overlapping detection logic. For example, "https://github.com/user/repo.git`" could be detected by both a Git protocol and an HTTP protocol, so the Git protocol should have a higher priority.
	Priority() int
	// Detect checks if the given URL can be handled by this protocol. If it can, it returns a Downloadable that can be used to download the content at the URL. If it cannot, it returns nil and false.
	Detect(url string) (Downloadable, bool)
}

// ForcedDetector is an optional interface a Protocol may implement to detect
// URLs differently when its force-prefix was explicitly used (e.g. "s3::..."). A
// force-prefix means the caller has already committed to this protocol, so
// detection can be more permissive than the ambiguous auto-detection path (which
// is shared with every other protocol and must stay strict). When a URL carries
// a protocol's force-prefix the grabber calls DetectForced on that protocol;
// protocols that do not implement it fall back to Detect.
type ForcedDetector interface {
	DetectForced(url string) (Downloadable, bool)
}

type Downloadable interface {
	// Download downloads the content at the URL to a temporary directory. It returns true if a single file was downloaded (and should be extracted if it's an archive), or false if a directory was downloaded (and should be moved as-is).
	Download(ctx context.Context, tmpDir string, settings settings.Settings) (bool, error)
}
