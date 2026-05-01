// Package files — bucket_storage.go defines the BucketStorage interface for
// bucket file operations. Template ships LocalBucketStorage (local volume);
// the SaaS binary injects S3BucketStorage at startup.
package files

import (
	"io"
	"time"

	"github.com/0xdps/mesahub-core/filetoken"
)

// PresignDownloadOpts are the options for a presigned download token request.
type PresignDownloadOpts struct {
	ExpiresIn int // seconds; ≤ 0 uses the filetoken default
}

// PresignDownloadResult is returned by BucketStorage.PresignDownload.
type PresignDownloadResult struct {
	Token     string // signed HMAC token — caller builds the URL
	TokenID   string // set for local backend; empty for S3/R2
	ExpiresAt time.Time
	ExpiresIn int
}

// PresignUploadOpts are the options for a presigned upload token request.
type PresignUploadOpts struct {
	Namespace   string // bucket slug
	Filename    string
	ContentType string
	FolderPath  string
	ExpiresIn   int // seconds; ≤ 0 uses the filetoken default
}

// PresignUploadResult is returned by BucketStorage.PresignUpload.
type PresignUploadResult struct {
	Token     string // signed HMAC token — caller builds the URL
	ExpiresAt time.Time
}

// BucketStorage abstracts over the storage backend used by bucket file
// operations. All implementations must be safe for concurrent use.
type BucketStorage interface {
	Upload(in UploadInput) (UploadResult, error)
	GetBlobReader(contentHash string) (io.ReadCloser, error)
	GetBlobBytes(contentHash string) ([]byte, error)
	GetByID(id string) (*StoredFile, error)
	List(namespace string, limit, offset int, folderPrefix, sort, order string) (ListFilesResult, error)
	SumBytesForNamespace(ns string) int64
	DeleteByID(id, namespace string) error
	StorageMetrics() (MetricsResult, error)

	// PresignDownload creates a short-lived files:read token for file id in namespace ns.
	// The caller is responsible for constructing the shortlink URL from the returned Token.
	// For S3/R2 backends, Token may be a complete presigned S3 URL — caller must detect
	// this (strings.HasPrefix(token, "http")) and return it directly instead of wrapping.
	PresignDownload(id, ns string, opts PresignDownloadOpts) (PresignDownloadResult, error)

	// PresignUpload creates a pre-authorised files:write token for namespace ns.
	// The caller is responsible for constructing the upload URL from the returned Token.
	PresignUpload(opts PresignUploadOpts) (PresignUploadResult, error)

	// GetRedirectURL returns a time-limited direct-access URL for the file,
	// bypassing the server. Returns ("", nil) for the local backend.
	// Returns a presigned S3/R2 URL for cloud backends.
	GetRedirectURL(id, ns string, expiresIn int) (string, error)
}

// LocalBucketStorage wraps *Storage to implement BucketStorage using the local
// volume. This is the default backend shipped with the template binary.
type LocalBucketStorage struct {
	s *Storage
}

// NewLocalBucketStorage returns a BucketStorage backed by the local volume.
func NewLocalBucketStorage(s *Storage) *LocalBucketStorage {
	return &LocalBucketStorage{s: s}
}

func (l *LocalBucketStorage) Upload(in UploadInput) (UploadResult, error) {
	return l.s.Upload(in)
}

func (l *LocalBucketStorage) GetBlobReader(contentHash string) (io.ReadCloser, error) {
	return l.s.GetBlobReader(contentHash)
}

func (l *LocalBucketStorage) GetBlobBytes(contentHash string) ([]byte, error) {
	return l.s.GetBlobBytes(contentHash)
}

func (l *LocalBucketStorage) GetByID(id string) (*StoredFile, error) {
	return l.s.GetByID(id)
}

func (l *LocalBucketStorage) List(namespace string, limit, offset int, folderPrefix, sort, order string) (ListFilesResult, error) {
	return l.s.List(namespace, limit, offset, folderPrefix, sort, order)
}

func (l *LocalBucketStorage) SumBytesForNamespace(ns string) int64 {
	return l.s.SumBytesForNamespace(ns)
}

func (l *LocalBucketStorage) DeleteByID(id, namespace string) error {
	return l.s.DeleteByID(id, namespace)
}

func (l *LocalBucketStorage) StorageMetrics() (MetricsResult, error) {
	return l.s.StorageMetrics()
}

// PresignDownload creates a files:read token for the given file.
// The caller constructs the shortlink URL from the returned Token.
func (l *LocalBucketStorage) PresignDownload(id, ns string, opts PresignDownloadOpts) (PresignDownloadResult, error) {
	f, err := l.s.GetByID(id)
	if err != nil {
		return PresignDownloadResult{}, err
	}
	if f == nil || f.DBName != ns {
		return PresignDownloadResult{}, ErrFileNotFound
	}

	result, err := filetoken.Create(ns, opts.ExpiresIn)
	if err != nil {
		return PresignDownloadResult{}, err
	}

	return PresignDownloadResult{
		Token:     result.Token,
		TokenID:   result.TokenID,
		ExpiresAt: result.ExpiresAt,
		ExpiresIn: result.ExpiresIn,
	}, nil
}

// PresignUpload creates a files:write token for the given namespace.
// The caller constructs the upload URL from the returned Token.
func (l *LocalBucketStorage) PresignUpload(opts PresignUploadOpts) (PresignUploadResult, error) {
	result, err := filetoken.CreateUpload(opts.Namespace, opts.Filename, opts.ContentType, opts.FolderPath, opts.ExpiresIn)
	if err != nil {
		return PresignUploadResult{}, err
	}

	return PresignUploadResult{
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
	}, nil
}

// GetRedirectURL returns ("", nil) for the local backend — files are served via
// X-Sendfile and never via a third-party direct URL.
func (l *LocalBucketStorage) GetRedirectURL(id, ns string, expiresIn int) (string, error) {
	return "", nil
}
