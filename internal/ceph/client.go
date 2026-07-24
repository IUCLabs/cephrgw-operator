// Package ceph is a small client for the Ceph RGW (radosgw) admin and S3 APIs,
// scoped to the user, bucket and policy operations the operator needs.
//
// It talks directly to radosgw (e.g. https://rgw.example.com:443) rather
// than the manager dashboard, via two native Go libraries:
//
//   - github.com/ceph/go-ceph/rgw/admin drives the RGW Admin Ops API
//     (/admin/...), signed with the operator's admin access/secret key, to
//     manage users, keys, quotas and bucket info/removal.
//   - github.com/aws/aws-sdk-go-v2/service/s3 drives the S3 API (/), signed as
//     the bucket's owner, to create buckets and set versioning, tagging, policy
//     and object lock — operations the Admin Ops API does not expose.
package ceph

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"
	awshttp "github.com/aws/smithy-go/transport/http"
	"github.com/ceph/go-ceph/rgw/admin"
)

// Config configures a Client.
type Config struct {
	// Endpoint is the radosgw root, e.g. https://rgw.example.com:443.
	Endpoint string
	// AccessKey / SecretKey are the S3 credentials of an RGW user holding the
	// admin caps the operator needs (users=*, buckets=*).
	AccessKey string
	SecretKey string
	// Region is the SigV4 credential-scope region used for S3 requests. radosgw
	// verifies the signature against whatever region the client used, so any
	// consistent value works; defaults to "default". (The go-ceph admin client
	// always signs its own requests with region "default".)
	Region string
	// CACert is an optional PEM bundle used to verify the radosgw TLS cert.
	CACert []byte
	// Insecure disables TLS verification (not recommended).
	Insecure bool
	// Timeout bounds each HTTP request. Defaults to 30s.
	Timeout time.Duration
}

// Client talks to radosgw. It is safe for concurrent use.
type Client struct {
	admin  *admin.API
	s3     *s3.Client
	region string

	// keyCache memoises owner uid -> S3 credentials (via the Admin Ops API) so
	// per-owner S3 calls do not re-fetch keys on every reconcile.
	mu       sync.Mutex
	keyCache map[string]aws.CredentialsProvider
}

// notFoundCodes and conflictCodes classify RGW/S3 error codes that surface only
// as a generic smithy.APIError (i.e. not a modeled S3 error type).
var notFoundCodes = map[string]bool{
	"NoSuchUser": true, "NoSuchBucket": true, "NoSuchKey": true,
	"NoSuchBucketPolicy": true, "NoSuchTagSet": true,
	"NoSuchTagSetError": true, "NotFound": true,
}

var conflictCodes = map[string]bool{
	"BucketAlreadyExists": true, "BucketAlreadyOwnedByYou": true, "UserAlreadyExists": true,
}

// IsNotFound reports whether err represents a missing user, bucket, key, policy
// or tag set, on either the admin or the S3 path.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, admin.ErrNoSuchUser) || errors.Is(err, admin.ErrNoSuchBucket) ||
		errors.Is(err, admin.ErrNoSuchKey) || errors.Is(err, admin.ErrNoSuchObject) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && notFoundCodes[apiErr.ErrorCode()] {
		return true
	}
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound {
		return true
	}
	return false
}

// IsConflict reports whether err represents an already-exists conflict on either
// the admin or the S3 path.
func IsConflict(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, admin.ErrUserExists) || errors.Is(err, admin.ErrEmailExists) ||
		errors.Is(err, admin.ErrKeyExists) || errors.Is(err, admin.ErrBucketNotEmpty) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && conflictCodes[apiErr.ErrorCode()] {
		return true
	}
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusConflict {
		return true
	}
	return false
}

// NewClient validates cfg and builds a Client.
func NewClient(cfg Config) (*Client, error) {
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	if endpoint == "" {
		return nil, fmt.Errorf("ceph: radosgw endpoint is required")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("ceph: radosgw admin access and secret key are required")
	}

	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.Insecure} //nolint:gosec // opt-in via config
	if len(cfg.CACert) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CACert) {
			return nil, fmt.Errorf("ceph: failed to parse CA certificate PEM")
		}
		tlsCfg.RootCAs = pool
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	region := cfg.Region
	if region == "" {
		region = "default"
	}

	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}

	adminAPI, err := admin.New(endpoint, cfg.AccessKey, cfg.SecretKey, httpClient)
	if err != nil {
		return nil, fmt.Errorf("ceph: build admin client: %w", err)
	}

	s3Client := s3.New(s3.Options{
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		HTTPClient:   httpClient,
		BaseEndpoint: aws.String(endpoint),
		// radosgw serves buckets path-style, not virtual-host style.
		UsePathStyle: true,
		// radosgw (pre-Reef backports) rejects the SDK's default CRC32 /
		// aws-chunked integrity protections; only send checksums when the API
		// requires them.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})

	return &Client{
		admin:    adminAPI,
		s3:       s3Client,
		region:   region,
		keyCache: map[string]aws.CredentialsProvider{},
	}, nil
}

// asOwner returns a per-call S3 option that signs the request as the RGW user
// uid, looking up (and caching) the user's first key pair via the Admin Ops API.
// Signing S3 sub-resource operations as the bucket owner (rather than the admin
// user) makes the owner the bucket owner directly and keeps RGW's per-user S3
// authorization intact.
func (c *Client) asOwner(ctx context.Context, uid string) (func(*s3.Options), error) {
	c.mu.Lock()
	provider, ok := c.keyCache[uid]
	c.mu.Unlock()
	if !ok {
		user, err := c.GetUser(ctx, uid)
		if err != nil {
			return nil, err
		}
		key, has := user.S3Key()
		if !has {
			return nil, fmt.Errorf("ceph: user %s has no S3 keys to sign bucket operations", uid)
		}
		provider = credentials.NewStaticCredentialsProvider(key.AccessKey, key.SecretKey, "")
		c.mu.Lock()
		c.keyCache[uid] = provider
		c.mu.Unlock()
	}
	return func(o *s3.Options) { o.Credentials = provider }, nil
}

// forgetIdentity drops any cached S3 credentials for uid, e.g. after its keys
// may have changed.
func (c *Client) forgetIdentity(uid string) {
	c.mu.Lock()
	delete(c.keyCache, uid)
	c.mu.Unlock()
}

// Ping verifies connectivity and that the admin credentials sign correctly. It
// asks the Admin Ops API for a sentinel user: a NoSuchUser answer still proves
// the request authenticated, so only transport/auth errors fail the check.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.GetUser(ctx, "cephrgw-operator-ping-nonexistent")
	if err == nil || IsNotFound(err) {
		return nil
	}
	return err
}
