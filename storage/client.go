// Package storage uploads and downloads files against an S3-compatible
// object store (AWS S3, MinIO, Garage, etc.) via the MinIO client.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/text/unicode/norm"
)

var (
	// ErrUnavailable means the client could not be configured or could not
	// reach the bucket — the endpoint is down, credentials are wrong, or a
	// required Settings field is missing.
	ErrUnavailable = errors.New("storage: not configured or unreachable")
	// ErrObjectMissing means the requested object is not in the bucket, as
	// opposed to any other failure reaching the store.
	ErrObjectMissing = errors.New("storage: object not found")
)

// Settings holds the connection details for an S3-compatible endpoint.
type Settings struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	// Region defaults to "us-east-1" when empty. Most self-hosted
	// S3-compatible backends ignore its value, but the underlying SDK
	// still requires one to be set.
	Region string
	// InsecureHTTP disables TLS. Leave it false (the default) unless the
	// endpoint is a local/dev instance with no HTTPS listener — the zero
	// value must stay on the secure choice so a caller that forgets to
	// set it doesn't send credentials in plaintext.
	InsecureHTTP bool
}

// Client uploads and downloads objects against a bucket. It holds no
// package-level state, so callers own its lifecycle — the same way they do
// dbpool.Pool — and multiple independent Clients can coexist in one
// process.
type Client struct {
	mc       *minio.Client
	bucket   string
	endpoint string
}

// New connects to the endpoint in s and verifies the bucket exists. A
// failed connection is not retried internally: callers that need
// retry-until-successful semantics (e.g. not to fail startup on a
// transient storage outage) should call New again themselves.
func New(ctx context.Context, s Settings) (*Client, error) {
	missing := make([]string, 0, 4)
	for _, f := range []struct{ name, value string }{
		{"Endpoint", s.Endpoint},
		{"AccessKey", s.AccessKey},
		{"SecretKey", s.SecretKey},
		{"Bucket", s.Bucket},
	} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: missing settings: %s", ErrUnavailable, strings.Join(missing, ", "))
	}

	region := s.Region
	if region == "" {
		region = "us-east-1"
	}

	mc, err := minio.New(s.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(s.AccessKey, s.SecretKey, ""),
		Secure: !s.InsecureHTTP,
		Region: region,
		// Explicit path-style (host/bucket/object) lookup: in automatic
		// mode the SDK may address the bucket as a subdomain, and a
		// self-hosted S3-compatible endpoint rarely has DNS wired for that.
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: create client: %v", ErrUnavailable, err)
	}

	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	exists, err := mc.BucketExists(pctx, s.Bucket)
	if err != nil {
		return nil, fmt.Errorf("%w: connect to bucket %q: %v", ErrUnavailable, s.Bucket, err)
	}
	if !exists {
		return nil, fmt.Errorf("%w: bucket %q does not exist", ErrUnavailable, s.Bucket)
	}

	return &Client{mc: mc, bucket: s.Bucket, endpoint: s.Endpoint}, nil
}

// Destination describes where the client points, for logging without
// exposing credentials.
func (c *Client) Destination() string {
	return fmt.Sprintf("%s (bucket %q)", c.endpoint, c.bucket)
}

// UploadInput is the raw content of a file to store.
//
// MimeType is trusted as given and stored as the object's Content-Type. If
// an uploaded object is later served back to a browser, callers must not
// forward an arbitrary caller-supplied MimeType unchecked — either force
// Content-Disposition: attachment or validate it against an allow-list,
// since a stored file can otherwise trigger stored XSS when rendered
// inline.
type UploadInput struct {
	Name     string
	MimeType string
	Content  []byte
	// FolderPath is a "directory" prefix for the object key (e.g.
	// month/category). Each segment is slugified; there's no real folder
	// to create since S3 keys are flat and "/" is only a display
	// convention.
	FolderPath []string
}

// Upload stores content and returns the generated object key. The key is
// the only caller-relevant output — Name, MimeType and size are unchanged
// echoes of the input, so Upload doesn't hand them back.
func (c *Client) Upload(ctx context.Context, in UploadInput) (id string, err error) {
	if in.Name == "" {
		return "", errors.New("storage: upload requires a name")
	}

	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	key := objectKey(in.FolderPath, in.Name)
	_, err = c.mc.PutObject(ctx, c.bucket, key,
		bytes.NewReader(in.Content), int64(len(in.Content)),
		minio.PutObjectOptions{ContentType: in.MimeType},
	)
	if err != nil {
		return "", fmt.Errorf("storage: upload %s: %w", key, err)
	}
	return key, nil
}

// Download returns the raw content of an object previously stored by
// Upload, addressed by the id it returned.
func (c *Client) Download(ctx context.Context, id string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	obj, err := c.mc.GetObject(ctx, c.bucket, id, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("storage: download %s: %w", id, err)
	}
	defer obj.Close()

	// GetObject doesn't contact the server: the real error (missing
	// object, bad credentials) only surfaces once the body is read.
	content, err := io.ReadAll(obj)
	if err != nil {
		if isMissingObject(err) {
			return nil, fmt.Errorf("%w: %s", ErrObjectMissing, id)
		}
		return nil, fmt.Errorf("storage: download %s: %w", id, err)
	}
	return content, nil
}

// isMissingObject distinguishes "this object isn't there" from any other
// storage failure, so callers know whether to re-upload or retry later.
func isMissingObject(err error) bool {
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchKey", "NoSuchBucket":
		return true
	}
	return false
}

// objectKey builds the object key: the folder path is only a prefix. The
// file name always gets a short random prefix because two uploads in the
// same folder can share a name, and a repeated key silently overwrites the
// previous object.
func objectKey(folderPath []string, name string) string {
	segments := make([]string, 0, len(folderPath)+1)
	for _, folder := range folderPath {
		if clean := slugify(folder); clean != "" {
			segments = append(segments, clean)
		}
	}
	if len(segments) == 0 {
		segments = append(segments, "unsorted")
	}
	slug := slugify(name)
	if slug == "" {
		// A name made only of symbols (e.g. "***") slugifies to nothing —
		// fall back rather than leaving a trailing dash with no readable
		// name in the key.
		slug = "file"
	}
	short := uuid.NewString()[:8]
	segments = append(segments, short+"-"+slug)
	return strings.Join(segments, "/")
}

// slugify normalizes text into something safe as an S3 key segment: no
// accents, spaces, or path separators. Diacritics are stripped through
// Unicode NFD decomposition (á becomes a + a combining acute mark, and the
// mark is dropped) rather than a hardcoded rune table, so it isn't limited
// to one language.
//
// Dropping '/' and '\\' here also means no caller-supplied name or folder
// segment can escape its slot in the generated key: the only characters
// that survive are alphanumeric, '.', '-' and '_'.
func slugify(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(strings.TrimSpace(s))) {
		switch {
		case unicode.Is(unicode.Mn, r):
			// Combining mark introduced by NFD decomposition — drop it to
			// strip the accent from the base letter kept above.
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		case unicode.IsSpace(r) || r == '/' || r == '\\':
			b.WriteRune('-')
		}
	}
	return strings.Trim(collapseDashes(b.String()), "-")
}

func collapseDashes(s string) string {
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}
