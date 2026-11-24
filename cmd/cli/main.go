// Command mcm is entry three (optional): the CLI. It shares the
// accounts, mounts and sync jobs of the desktop app and is the tool
// for automation and debugging:
//
//	mcm ls s3://my-bucket/photos/
//	mcm cp ./dist s3://my-bucket/site/ -r
//	mcm sync ./photos s3://my-bucket/photos/ --dry-run
//
// V0.1: ls works against S3 (credentials from the environment);
// cp/sync are planned with --dry-run only.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/providers/s3"
	"github.com/luckzk/zy-drive/internal/version"
)

func main() {
	// --version is handled before flag.Parse: flag.Parse would reject
	// the undefined -version flag before we get a chance to check it.
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		fmt.Printf("mcm %s\n", version.AppVersion)
		return
	}
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mcm <ls|cp|sync> [args]\n")
		fmt.Fprintf(os.Stderr, "  mcm ls s3://<bucket>/<prefix>\n")
		fmt.Fprintf(os.Stderr, "  mcm sync <local| s3://...> <s3://...> --dry-run\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if args[0] == "--version" || args[0] == "-version" {
		fmt.Printf("mcm %s\n", version.AppVersion)
		return
	}
	ctx := context.Background()
	switch args[0] {
	case "ls":
		if err := cmdLs(ctx, args[1:]); err != nil {
			fatal(err)
		}
	case "cp", "sync":
		dryRun := false
		for _, a := range args[1:] {
			if a == "--dry-run" {
				dryRun = true
			}
		}
		if !dryRun {
			fmt.Fprintln(os.Stderr, "mcm: cp/sync without --dry-run is not implemented in V0.1")
			os.Exit(2)
		}
		fmt.Println("mcm: dry-run plan (V0.1 skeleton — no files are moved):")
		for _, a := range args[1:] {
			if a != "--dry-run" {
				fmt.Printf("  %s\n", a)
			}
		}
	default:
		fmt.Fprintf(os.Stderr, "mcm: unknown command %q\n", args[0])
		flag.Usage()
		os.Exit(2)
	}
}

// cmdLs lists an S3 location. Credentials come from the environment:
// AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY, endpoint via MCM_S3_ENDPOINT.
func cmdLs(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("mcm ls: missing location, e.g. s3://my-bucket/photos/")
	}
	loc := strings.TrimPrefix(args[0], "s3://")
	bucket, prefix, _ := strings.Cut(loc, "/")
	if bucket == "" {
		return fmt.Errorf("mcm ls: bucket is required, e.g. s3://my-bucket/")
	}
	cfg := storage.Config{
		Region:    envOr("AWS_REGION", "us-east-1"),
		AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		Endpoint:  os.Getenv("MCM_S3_ENDPOINT"),
		Extra:     map[string]string{"bucket": bucket, "prefix": ""},
	}
	p, err := s3.New(cfg)
	if err != nil {
		return err
	}
	nodes, err := p.List(ctx, strings.TrimSuffix(prefix, "/"))
	if err != nil {
		return err
	}
	for _, n := range nodes {
		kind := "file"
		if n.IsDir {
			kind = "dir "
		}
		fmt.Printf("%s %12d  %s  %s\n", kind, n.Size, n.ModTime.Format("2006-01-02 15:04"), n.Name)
	}
	_ = domain.ProviderS3
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "mcm:", err)
	os.Exit(1)
}
