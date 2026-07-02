package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/liamg/grabber"
	"github.com/liamg/grabber/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	showVersion := flag.Bool("version", false, "print version information and exit")
	checksum := flag.String("checksum", "", "SHA-256 checksum to verify (hex-encoded)")
	flag.StringVar(checksum, "c", "", "SHA-256 checksum to verify (hex-encoded)")
	noExtract := flag.Bool("no-extract", false, "disable automatic archive extraction")
	gitDepth := flag.Int("git-depth", 0, "shallow clone depth for Git repos")
	gitSSHKey := flag.String("git-ssh-key", "", "path to SSH private key file for Git")
	gitInsecure := flag.Bool("git-insecure", false, "skip SSH host key verification for Git")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: grabber [flags] <url> [dst]\n\n")
		fmt.Fprintf(os.Stderr, "Download files and directories from various sources.\n\n")
		fmt.Fprintf(os.Stderr, "Arguments:\n")
		fmt.Fprintf(os.Stderr, "  url    Source URL (required)\n")
		fmt.Fprintf(os.Stderr, "  dst    Destination path (default: current directory)\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return nil
	}

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		return fmt.Errorf("missing required argument: url")
	}

	url := args[0]
	dst := "."
	if len(args) >= 2 {
		dst = args[1]
	}

	var opts []grabber.Option
	if *noExtract {
		opts = append(opts, grabber.WithAutoExtract(false))
	}
	if *gitDepth > 0 {
		opts = append(opts, grabber.WithGitDepth(*gitDepth))
	}
	if *gitSSHKey != "" {
		key, err := os.ReadFile(*gitSSHKey)
		if err != nil {
			return fmt.Errorf("reading SSH key: %w", err)
		}
		opts = append(opts, grabber.WithGitSSHKey(key))
	}
	if *gitInsecure {
		opts = append(opts, grabber.WithGitInsecureSkipHostKeyVerify())
	}

	g := grabber.New(opts...)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	fmt.Fprintf(os.Stderr, "Downloading %s -> %s\n", url, dst)

	if *checksum != "" {
		if err := g.GrabWithSHA256Checksum(ctx, url, dst, *checksum); err != nil {
			return err
		}
	} else {
		if err := g.Grab(ctx, url, dst); err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stderr, "Done.\n")
	return nil
}
