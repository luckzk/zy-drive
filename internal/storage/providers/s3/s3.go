// SPDX-License-Identifier: MPL-2.0
// Package s3 implements storage.Provider for S3-compatible storage
// (Amazon S3, Cloudflare R2, MinIO, Wasabi, VersityGW) using the
// official aws-sdk-go-v2. It is the first driver: the one that validates
// the Provider interface, because S3 is prefix-based while Dropbox is
// path-based and Drive/OneDrive are ID-based.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/netproxy"
	"github.com/luckzk/zy-drive/internal/storage"
)

const (
	// minPartSize is the S3 multipart minimum (5 MiB).
	minPartSize = 5 << 20
	// maxFileSize is the S3 single-object maximum (5 TiB).
	maxFileSize = 5 << 40
)

// Provider is an S3-backed storage.Provider. Refs are keys relative to
// the bucket root; "" is the root. S3 has no real folders: a "folder" is
// a key prefix, and Mkdir writes a zero-byte placeholder object.
type Provider struct {
	client *s3.Client
	bucket string
	prefix string // key prefix inside the bucket, "" for the whole bucket
}

// New builds an S3 provider. cfg.Endpoint may point at any S3-compatible
// service (R2, MinIO...); cfg.Region is required by the SDK even for
// custom endpoints. cfg.Extra["pathStyle"]="true" enables path-style
// addressing for MinIO and most self-hosted gateways.
func New(cfg storage.Config) (storage.Provider, error) {
	if cfg.Extra == nil {
		cfg.Extra = map[string]string{}
	}
	bucket := cfg.Extra["bucket"]
	if bucket == "" {
		return nil, fmt.Errorf("s3: bucket is required (cfg.Extra[\"bucket\"])")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	opts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
	}
	if cfg.AccessKey != "" {
		opts = append(opts, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKey, cfg.SecretKey, "")))
	}
	awsCfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, &storage.ProviderError{Op: "New", Provider: "s3", Err: err}
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.Extra["pathStyle"] == "true"
		// Proxy-aware HTTP client (Settings -> Network): S3/R2
		// requests follow the user's proxy like every other
		// driver. Set here, not via config.WithHTTPClient:
		// LoadDefaultConfig tries to add custom RootCAs to a
		// plain *http.Client and fails when the environment
		// carries any (sandbox MITM CA). No timeout: the SDK
		// default had none either.
		o.HTTPClient = netproxy.NewClient(0)
	})
	return &Provider{client: client, bucket: bucket, prefix: strings.Trim(cfg.Extra["prefix"], "/")}, nil
}

// Caps declares S3 capabilities. Version support assumes bucket
// versioning is enabled; the UI shows the Versions menu only when the
// bucket actually reports versions (checked by the driver at runtime).
func (p *Provider) Caps() domain.Caps {
	return domain.Caps{
		CanMkdir: true, CanRename: true, CanDelete: true, DeleteToTrash: false,
		SupportsServerSideCopy:  true,
		SupportsVersions:        true,
		SupportsShare:           true, // presigned URLs
		SupportsSearch:          false,
		SupportsDelta:           false, // no cursor; full scan + ETag compare
		SupportsThumbnail:       false,
		SupportsResumableUpload: true,
		SupportsEncryption:      true,
		SupportsStorageClass:    true,
		SupportsPresignedURL:    true,
		IDBased:                 false,
		MaxFileSize:             maxFileSize,
		MinPartSize:             minPartSize,
	}
}

// key converts a display ref into a full object key.
func (p *Provider) key(ref string) string {
	ref = strings.Trim(ref, "/")
	if p.prefix == "" {
		return ref
	}
	if ref == "" {
		return p.prefix
	}
	return p.prefix + "/" + ref
}

// List returns direct children of ref. Pagination is consumed inside.
func (p *Provider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	prefix := p.key(ref)
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	paginator := s3.NewListObjectsV2Paginator(p.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(p.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})
	var out []domain.Node
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, p.wrap("List", err)
		}
		for _, cp := range page.CommonPrefixes {
			key := aws.ToString(cp.Prefix)
			out = append(out, domain.Node{
				Ref:   p.displayRef(key),
				Path:  "/" + p.displayRef(key),
				Name:  baseName(strings.TrimSuffix(key, "/")),
				IsDir: true, Size: -1,
			})
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			if key == prefix { // the directory placeholder itself
				continue
			}
			out = append(out, domain.Node{
				Ref:      p.displayRef(key),
				Path:     "/" + p.displayRef(key),
				Name:     baseName(key),
				Size:     aws.ToInt64(obj.Size),
				ModTime:  aws.ToTime(obj.LastModified),
				Hash:     strings.Trim(aws.ToString(obj.ETag), `"`),
				HashKind: "etag",
				Mime:     mimeOf(key),
			})
		}
	}
	return out, nil
}

// Stat returns metadata for one node.
func (p *Provider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	key := p.key(ref)
	out, err := p.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(p.bucket), Key: aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			// Maybe a directory: probe for children under prefix/.
			children, lerr := p.List(ctx, ref)
			if lerr == nil && len(children) > 0 {
				return domain.Node{Ref: ref, Path: "/" + strings.Trim(ref, "/"),
					Name: baseName(strings.TrimSuffix(ref, "/")), IsDir: true, Size: -1}, nil
			}
			return domain.Node{}, &storage.ProviderError{Op: "Stat", Provider: "s3",
				Err: fmt.Errorf("object not found: %s", ref)}
		}
		return domain.Node{}, p.wrap("Stat", err)
	}
	return domain.Node{
		Ref: ref, Path: "/" + strings.Trim(ref, "/"), Name: baseName(key),
		Size:    aws.ToInt64(out.ContentLength),
		ModTime: aws.ToTime(out.LastModified),
		Hash:    strings.Trim(aws.ToString(out.ETag), `"`), HashKind: "etag",
		Mime: aws.ToString(out.ContentType),
	}, nil
}

// OpenRead streams a file for download.
func (p *Provider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	out, err := p.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(p.bucket), Key: aws.String(p.key(ref)),
	})
	if err != nil {
		return nil, p.wrap("OpenRead", err)
	}
	return out.Body, nil
}

// Upload writes a file, using multipart upload for large files.
// The manager picks single vs multipart from the size automatically.
func (p *Provider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	uploader := manager.NewUploader(p.client, func(u *manager.Uploader) {
		u.PartSize = minPartSize
	})
	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(p.bucket), Key: aws.String(p.key(ref)), Body: r,
	})
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	return p.Stat(ctx, ref)
}

// Mkdir creates a zero-byte placeholder object for a folder prefix.
func (p *Provider) Mkdir(ctx context.Context, ref string) error {
	key := p.key(ref)
	if !strings.HasSuffix(key, "/") {
		key += "/"
	}
	_, err := p.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(p.bucket), Key: aws.String(key),
		Body: strings.NewReader(""),
	})
	return p.wrapErr("Mkdir", err)
}

// Rename copies the object server-side, then deletes the source.
// Directory rename is not a single S3 operation; callers rename files,
// and the app layer renames directories by listing + renaming children.
func (p *Provider) Rename(ctx context.Context, src, dst string) error {
	srcKey, dstKey := p.key(src), p.key(dst)
	_, err := p.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(p.bucket),
		Key:        aws.String(dstKey),
		CopySource: aws.String(p.bucket + "/" + srcKey),
	})
	if err != nil {
		return p.wrap("Rename", err)
	}
	if err := p.Delete(ctx, src); err != nil {
		return p.wrap("Rename", err)
	}
	return nil
}

// Delete removes one object. S3 delete is permanent; the UI wording
// follows Caps().DeleteToTrash == false.
func (p *Provider) Delete(ctx context.Context, ref string) error {
	_, err := p.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(p.bucket), Key: aws.String(p.key(ref)),
	})
	return p.wrapErr("Delete", err)
}

// displayRef strips the provider prefix back to a user-facing ref.
func (p *Provider) displayRef(key string) string {
	if p.prefix == "" {
		return strings.TrimSuffix(key, "/")
	}
	rest := strings.TrimPrefix(key, p.prefix+"/")
	return strings.TrimSuffix(rest, "/")
}

func (p *Provider) wrap(op string, err error) error {
	return &storage.ProviderError{Op: op, Provider: "s3", Err: err}
}

func (p *Provider) wrapErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return p.wrap(op, err)
}

func isNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey", "NoSuchBucket", "404":
			return true
		}
	}
	var nsk *types.NoSuchKey
	return errors.As(err, &nsk)
}

func baseName(key string) string {
	key = strings.TrimSuffix(key, "/")
	return path.Base(key)
}

func mimeOf(key string) string {
	switch strings.ToLower(path.Ext(key)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".md":
		return "text/plain"
	case ".zip":
		return "application/zip"
	default:
		return "application/octet-stream"
	}
}

// Ensure Provider implements storage.Provider at compile time.
// S3 has no quota API: there is no bucket-level "used/total" to
// query (HeadBucket/ListObjects would have to walk the whole
// bucket, and the account has no total to report). This driver
// deliberately does NOT implement storage.QuotaSource, so the
// App layer reports the mount as unlimited and the UI shows
// "不限量" instead of a made-up number.
var _ storage.Provider = (*Provider)(nil)
