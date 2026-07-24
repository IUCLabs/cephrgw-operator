package ceph

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/ceph/go-ceph/rgw/admin"
)

// BucketInfo is the subset of an RGW bucket record the operator consumes, as
// returned by the Admin Ops API (GET /admin/bucket).
type BucketInfo struct {
	Bucket string
	Bid    string
	ID     string
	Owner  string
}

// Name returns the bucket name regardless of the field radosgw used.
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

// GetBucket fetches a bucket by name via the Admin Ops API, returning an error
// classified by IsNotFound (admin.ErrNoSuchBucket) when it does not exist.
func (c *Client) GetBucket(ctx context.Context, name string) (*BucketInfo, error) {
	b, err := c.admin.GetBucketInfo(ctx, admin.Bucket{Bucket: name})
	if err != nil {
		return nil, err
	}
	return &BucketInfo{Bucket: b.Bucket, ID: b.ID, Owner: b.Owner}, nil
}

// CreateBucket provisions a bucket owned by spec.OwnerUID. The Admin Ops API
// cannot create buckets, so the operator issues an S3 CreateBucket signed as the
// owner (which makes the owner the bucket owner directly).
func (c *Client) CreateBucket(ctx context.Context, spec CreateBucketSpec) (*BucketInfo, error) {
	owner, err := c.asOwner(ctx, spec.OwnerUID)
	if err != nil {
		return nil, err
	}

	input := &s3.CreateBucketInput{Bucket: aws.String(spec.Bucket)}
	if loc := locationConstraint(spec.Zonegroup, spec.PlacementTarget); loc != "" {
		input.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(loc),
		}
	}
	if spec.LockEnabled {
		input.ObjectLockEnabledForBucket = aws.Bool(true)
	}

	if _, err := c.s3.CreateBucket(ctx, input, owner); err != nil && !IsConflict(err) {
		return nil, err
	}

	// Apply a default object-lock retention when requested.
	if spec.LockEnabled && spec.LockMode != "" && (spec.LockDays != nil || spec.LockYears != nil) {
		if err := c.setObjectLockDefault(ctx, owner, spec); err != nil {
			return nil, err
		}
	}

	return c.GetBucket(ctx, spec.Bucket)
}

// SetBucketVersioning enables or suspends S3 versioning on a bucket. bucketID is
// unused (kept for call-site stability).
func (c *Client) SetBucketVersioning(ctx context.Context, name, bucketID, ownerUID string, enabled bool) error {
	owner, err := c.asOwner(ctx, ownerUID)
	if err != nil {
		return err
	}
	status := s3types.BucketVersioningStatusSuspended
	if enabled {
		status = s3types.BucketVersioningStatusEnabled
	}
	_, err = c.s3.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket:                  aws.String(name),
		VersioningConfiguration: &s3types.VersioningConfiguration{Status: status},
	}, owner)
	return err
}

// SetBucketPolicy replaces the S3 bucket policy. An empty policy clears it.
// bucketID is unused (kept for call-site stability).
func (c *Client) SetBucketPolicy(ctx context.Context, name, bucketID, ownerUID, policy string) error {
	owner, err := c.asOwner(ctx, ownerUID)
	if err != nil {
		return err
	}
	if policy == "" {
		_, err := c.s3.DeleteBucketPolicy(ctx, &s3.DeleteBucketPolicyInput{Bucket: aws.String(name)}, owner)
		if IsNotFound(err) {
			return nil
		}
		return err
	}
	_, err = c.s3.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{
		Bucket: aws.String(name),
		Policy: aws.String(policy),
	}, owner)
	return err
}

// GetBucketPolicy returns the bucket's current S3 policy JSON, or "" when it has
// none. It is signed as the bucket owner.
func (c *Client) GetBucketPolicy(ctx context.Context, name, ownerUID string) (string, error) {
	owner, err := c.asOwner(ctx, ownerUID)
	if err != nil {
		return "", err
	}
	out, err := c.s3.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: aws.String(name)}, owner)
	if err != nil {
		if IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	if out.Policy == nil {
		return "", nil
	}
	return *out.Policy, nil
}

// SetBucketTags replaces the bucket tag set. tagsJSON is the JSON produced by
// BuildTagJSON (a list of {"Key","Value"} objects). bucketID is unused (kept for
// call-site stability).
func (c *Client) SetBucketTags(ctx context.Context, name, bucketID, ownerUID, tagsJSON string) error {
	owner, err := c.asOwner(ctx, ownerUID)
	if err != nil {
		return err
	}

	type tag struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	}
	var tags []tag
	if tagsJSON != "" {
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
			return fmt.Errorf("ceph: parse bucket tags: %w", err)
		}
	}
	if len(tags) == 0 {
		_, err := c.s3.DeleteBucketTagging(ctx, &s3.DeleteBucketTaggingInput{Bucket: aws.String(name)}, owner)
		if IsNotFound(err) {
			return nil
		}
		return err
	}

	tagSet := make([]s3types.Tag, 0, len(tags))
	for _, t := range tags {
		tagSet = append(tagSet, s3types.Tag{Key: aws.String(t.Key), Value: aws.String(t.Value)})
	}
	_, err = c.s3.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket:  aws.String(name),
		Tagging: &s3types.Tagging{TagSet: tagSet},
	}, owner)
	return err
}

// DeleteBucket removes a bucket via the Admin Ops API. When purge is true its
// objects are deleted too. A NoSuchBucket response is treated as success.
func (c *Client) DeleteBucket(ctx context.Context, name string, purge bool) error {
	err := c.admin.RemoveBucket(ctx, admin.Bucket{Bucket: name, PurgeObject: &purge})
	if IsNotFound(err) {
		return nil
	}
	return err
}

// setObjectLockDefault sets the bucket's default object-lock retention.
func (c *Client) setObjectLockDefault(ctx context.Context, owner func(*s3.Options), spec CreateBucketSpec) error {
	_, err := c.s3.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{
		Bucket: aws.String(spec.Bucket),
		ObjectLockConfiguration: &s3types.ObjectLockConfiguration{
			ObjectLockEnabled: s3types.ObjectLockEnabledEnabled,
			Rule: &s3types.ObjectLockRule{
				DefaultRetention: &s3types.DefaultRetention{
					Mode:  s3types.ObjectLockRetentionMode(spec.LockMode),
					Days:  spec.LockDays,
					Years: spec.LockYears,
				},
			},
		},
	}, owner)
	return err
}

// locationConstraint renders the RGW LocationConstraint from a zonegroup and
// placement target ("<zonegroup>:<placement>"), or "" for default placement.
func locationConstraint(zonegroup, placement string) string {
	loc := zonegroup
	if placement != "" {
		loc = zonegroup + ":" + placement
	}
	return loc
}
