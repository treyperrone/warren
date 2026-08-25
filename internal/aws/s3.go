package aws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Entry is one row of a bucket listing: an object, or a common prefix standing in for a
// folder. S3 has no folders — the delimiter view is a rendering — but it is the rendering
// every human expects, so the picker speaks it.
type S3Entry struct {
	Key      string // full key for objects, full prefix (trailing /) for prefixes
	IsPrefix bool
	Size     int64
	Modified time.Time
}

// Name is the entry's last path segment, what a directory-style listing displays.
func (e S3Entry) Name() string {
	k := strings.TrimSuffix(e.Key, "/")
	if i := strings.LastIndex(k, "/"); i >= 0 {
		k = k[i+1:]
	}
	if e.IsPrefix {
		return k + "/"
	}
	return k
}

// SessionSource yields the CURRENT session each time it is called. S3 transfers can outlive
// one set of role credentials — a multi-gigabyte download crosses the hour mark — and a
// static snapshot taken at keypress fails mid-multipart when the TUI's background renewal
// has long since minted fresh keys. The SDK asks the provider per request, so a source that
// answers with whatever is freshest keeps long transfers alive for free.
type SessionSource func() *Session

func (get SessionSource) provider() aws.CredentialsProviderFunc {
	return func(context.Context) (aws.Credentials, error) {
		s := get()
		if s == nil {
			return aws.Credentials{}, errors.New("no credentials available")
		}
		return aws.Credentials{
			AccessKeyID:     s.AccessKeyID,
			SecretAccessKey: s.SecretAccessKey,
			SessionToken:    s.SessionToken,
			CanExpire:       !s.Expires.IsZero(),
			Expires:         s.Expires,
		}, nil
	}
}

func s3Client(ctx context.Context, get SessionSource, region string) (*s3.Client, error) {
	if region == "" {
		region = get().Region
	}
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(get.provider()),
	)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(cfg), nil
}

// ListBuckets returns every bucket the credentials can list, sorted by name.
func ListBuckets(ctx context.Context, get SessionSource) ([]string, error) {
	client, err := s3Client(ctx, get, "")
	if err != nil {
		return nil, err
	}
	out, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, fmt.Errorf("list buckets: %w", err)
	}
	var names []string
	for _, b := range out.Buckets {
		names = append(names, aws.ToString(b.Name))
	}
	sort.Strings(names)
	return names, nil
}

// BucketRegion resolves where a bucket actually lives. Buckets are listed globally but
// transfers must speak to the right region, and guessing wrong yields a redirect error that
// blames the bucket name.
func BucketRegion(ctx context.Context, get SessionSource, bucket string) (string, error) {
	client, err := s3Client(ctx, get, "")
	if err != nil {
		return "", err
	}
	region, err := manager.GetBucketRegion(ctx, client, bucket)
	if err != nil {
		return "", fmt.Errorf("resolving region for %s: %w", bucket, err)
	}
	return region, nil
}

// ListS3Objects lists one delimiter level of a bucket: the prefixes and objects directly
// under prefix, folders first, fully paginated so a 3000-object prefix is not silently a
// 1000-object listing.
func ListS3Objects(ctx context.Context, get SessionSource, bucket, region, prefix string) ([]S3Entry, error) {
	client, err := s3Client(ctx, get, region)
	if err != nil {
		return nil, err
	}
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})

	var dirs, files []S3Entry
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list s3://%s/%s: %w", bucket, prefix, err)
		}
		for _, p := range page.CommonPrefixes {
			dirs = append(dirs, S3Entry{Key: aws.ToString(p.Prefix), IsPrefix: true})
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			// The prefix itself appears as a zero-byte object when someone "created a
			// folder" in the console; listing it as a row named "" helps nobody.
			if key == prefix {
				continue
			}
			files = append(files, S3Entry{
				Key:      key,
				Size:     aws.ToInt64(o.Size),
				Modified: aws.ToTime(o.LastModified),
			})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Key < dirs[j].Key })
	sort.Slice(files, func(i, j int) bool { return files[i].Key < files[j].Key })
	return append(dirs, files...), nil
}

// safeLocalName reduces an S3 key's last segment to a name that cannot leave destDir. Keys
// are attacker-adjacent input — anyone with PutObject in a shared bucket names them — and
// on Windows a segment like `..\..\Startup\evil.bat` would otherwise ride filepath.Join
// out of ~/Downloads entirely (S3Entry.Name only splits on '/', so backslashes survive it).
func safeLocalName(key string) string {
	base := S3Entry{Key: key}.Name()
	base = strings.NewReplacer("/", "_", `\`, "_").Replace(base)
	if base == "" || base == "." || base == ".." {
		return "object"
	}
	return base
}

// DownloadObject fetches one object into destDir under its (sanitized) base name, refusing
// to overwrite: a collision appends " (2)" style suffixes, because silently replacing a
// local file is the one outcome a download must never have. The bytes land in a .part file
// renamed only on success, so an interrupted or failed transfer can never leave something
// that LOOKS downloaded.
func DownloadObject(ctx context.Context, get SessionSource, bucket, region, key, destDir string) (string, error) {
	client, err := s3Client(ctx, get, region)
	if err != nil {
		return "", err
	}

	base := safeLocalName(key)
	// O_EXCL does the not-overwriting, atomically — a Stat-then-Create probe is a race and
	// follows symlinks. Any error other than "exists" is a real answer (permissions, name
	// too long), not a collision to suffix around forever.
	var dest string
	var part *os.File
	for n := 1; ; n++ {
		name := base
		if n > 1 {
			ext := filepath.Ext(base)
			name = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(base, ext), n, ext)
		}
		dest = filepath.Join(destDir, name)
		part, err = os.OpenFile(dest+".part", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- destDir is warren-chosen; the name is sanitized above
		if err == nil {
			if _, statErr := os.Stat(dest); errors.Is(statErr, os.ErrNotExist) {
				break // .part claimed and the final name is free
			}
			_ = part.Close()
			_ = os.Remove(dest + ".part")
			continue // final name taken: next suffix
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if n > 999 {
			return "", fmt.Errorf("gave up finding a free name for %s in %s", base, destDir)
		}
	}

	_, err = manager.NewDownloader(client).Download(ctx, part, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	closeErr := part.Close()
	if err != nil {
		_ = os.Remove(dest + ".part")
		return "", fmt.Errorf("download s3://%s/%s: %w", bucket, key, err)
	}
	if closeErr != nil {
		_ = os.Remove(dest + ".part")
		return "", closeErr
	}
	if err := os.Rename(dest+".part", dest); err != nil {
		return "", err
	}
	return dest, nil
}

// UploadFile puts one local file at prefix+basename. The transfer manager gives multipart
// and retry for free, which is what makes "drag a 5GB image into the window" not a trap.
func UploadFile(ctx context.Context, get SessionSource, bucket, region, prefix, path string) (string, error) {
	client, err := s3Client(ctx, get, region)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path) // #nosec G304 -- path is the file the user dragged or typed; uploading it is the request
	if err != nil {
		return "", err
	}
	defer f.Close()

	key := prefix + filepath.Base(path)
	if _, err := manager.NewUploader(client).Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   f,
	}); err != nil {
		return "", fmt.Errorf("upload to s3://%s/%s: %w", bucket, key, err)
	}
	return key, nil
}
