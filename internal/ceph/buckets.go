package ceph

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// BucketInfo is the subset of an RGW bucket record the operator consumes.
// Different Ceph releases name the id/name fields slightly differently, so the
// struct captures the known variants and Name/ID normalise them.
type BucketInfo struct {
	Bucket string `json:"bucket"`
	Bid    string `json:"bid"`
	ID     string `json:"id"`
	Owner  string `json:"owner"`
}

// Name returns the bucket name regardless of the field the dashboard used.
func (b *BucketInfo) Name() string {
	if b.Bucket != "" {
		return b.Bucket
	}
	return b.Bid
}

// InstanceID returns the RGW bucket instance id.
func (b *BucketInfo) InstanceID() string { return b.ID }

// CreateBucketSpec describes a bucket to create.
type CreateBucketSpec struct {
	Bucket          string
	OwnerUID        string
	Zonegroup       string
	PlacementTarget string
	LockEnabled     bool
	LockMode        string
	LockDays        *int32
	LockYears       *int32
}

type createBucketRequest struct {
	Bucket          string `json:"bucket"`
	UID             string `json:"uid"`
	Zonegroup       string `json:"zonegroup,omitempty"`
	PlacementTarget string `json:"placement_target,omitempty"`
	LockEnabled     string `json:"lock_enabled"`
	LockMode        string `json:"lock_mode,omitempty"`
	LockDays        string `json:"lock_retention_period_days,omitempty"`
	LockYears       string `json:"lock_retention_period_years,omitempty"`
}

// GetBucket fetches a bucket by name, returning an *APIError with status 404
// (see IsNotFound) when it does not exist.
func (c *Client) GetBucket(ctx context.Context, name string) (*BucketInfo, error) {
	var b BucketInfo
	if err := c.do(ctx, http.MethodGet, "/api/rgw/bucket/"+url.PathEscape(name), nil, &b, ""); err != nil {
		return nil, err
	}
	return &b, nil
}

// CreateBucket provisions a bucket owned by spec.OwnerUID.
func (c *Client) CreateBucket(ctx context.Context, spec CreateBucketSpec) (*BucketInfo, error) {
	req := createBucketRequest{
		Bucket:          spec.Bucket,
		UID:             spec.OwnerUID,
		Zonegroup:       spec.Zonegroup,
		PlacementTarget: spec.PlacementTarget,
		LockEnabled:     strconv.FormatBool(spec.LockEnabled),
		LockMode:        spec.LockMode,
	}
	if spec.LockDays != nil {
		req.LockDays = strconv.Itoa(int(*spec.LockDays))
	}
	if spec.LockYears != nil {
		req.LockYears = strconv.Itoa(int(*spec.LockYears))
	}
	var b BucketInfo
	if err := c.do(ctx, http.MethodPost, "/api/rgw/bucket", req, &b, ""); err != nil {
		return nil, err
	}
	return &b, nil
}

type setBucketRequest struct {
	BucketID        string  `json:"bucket_id"`
	UID             string  `json:"uid"`
	VersioningState *string `json:"versioning_state,omitempty"`
	BucketPolicy    *string `json:"bucket_policy,omitempty"`
	Tags            *string `json:"tags,omitempty"`
}

// SetBucketVersioning enables or suspends S3 versioning on a bucket.
func (c *Client) SetBucketVersioning(ctx context.Context, name, bucketID, ownerUID string, enabled bool) error {
	state := "Suspended"
	if enabled {
		state = "Enabled"
	}
	req := setBucketRequest{BucketID: bucketID, UID: ownerUID, VersioningState: &state}
	return c.do(ctx, http.MethodPut, "/api/rgw/bucket/"+url.PathEscape(name), req, nil, "")
}

// SetBucketPolicy replaces the S3 bucket policy. An empty policy string asks the
// dashboard to clear it; not every release honours clearing, so callers should
// treat a clear as best-effort.
func (c *Client) SetBucketPolicy(ctx context.Context, name, bucketID, ownerUID, policy string) error {
	req := setBucketRequest{BucketID: bucketID, UID: ownerUID, BucketPolicy: &policy}
	return c.do(ctx, http.MethodPut, "/api/rgw/bucket/"+url.PathEscape(name), req, nil, "")
}

// SetBucketTags replaces the bucket tag set. tagsJSON is the RGW/S3 tag JSON
// (a list of {"Key","Value"} objects).
func (c *Client) SetBucketTags(ctx context.Context, name, bucketID, ownerUID, tagsJSON string) error {
	req := setBucketRequest{BucketID: bucketID, UID: ownerUID, Tags: &tagsJSON}
	return c.do(ctx, http.MethodPut, "/api/rgw/bucket/"+url.PathEscape(name), req, nil, "")
}

// DeleteBucket removes a bucket. When purge is true its objects are deleted too;
// otherwise deletion of a non-empty bucket fails. A 404 is treated as success.
func (c *Client) DeleteBucket(ctx context.Context, name string, purge bool) error {
	path := "/api/rgw/bucket/" + url.PathEscape(name) + "?purge_objects=" + strconv.FormatBool(purge)
	err := c.do(ctx, http.MethodDelete, path, nil, nil, "")
	if IsNotFound(err) {
		return nil
	}
	return err
}
