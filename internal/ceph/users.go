package ceph

import (
	"context"

	"github.com/ceph/go-ceph/rgw/admin"
)

// UserKey is an S3 access/secret key pair belonging to an RGW user.
type UserKey struct {
	User      string
	AccessKey string
	SecretKey string
}

// User is the subset of an RGW user record the operator consumes.
type User struct {
	UID         string
	DisplayName string
	Email       string
	MaxBuckets  int
	Suspended   int
	Keys        []UserKey
}

// S3Key returns the first access/secret key pair, if any.
func (u *User) S3Key() (UserKey, bool) {
	if len(u.Keys) == 0 {
		return UserKey{}, false
	}
	return u.Keys[0], true
}

// UserSpec describes the desired state of an RGW user.
type UserSpec struct {
	UID         string
	DisplayName string
	Email       string
	MaxBuckets  *int32
	Suspended   bool
}

// fromAdminUser converts a go-ceph admin.User into the subset the operator uses.
func fromAdminUser(u admin.User) *User {
	out := &User{
		UID:         u.ID,
		DisplayName: u.DisplayName,
		Email:       u.Email,
		MaxBuckets:  derefInt(u.MaxBuckets),
		Suspended:   derefInt(u.Suspended),
	}
	for _, k := range u.Keys {
		out.Keys = append(out.Keys, UserKey{User: k.User, AccessKey: k.AccessKey, SecretKey: k.SecretKey})
	}
	return out
}

// GetUser fetches an RGW user by uid, returning an error classified by
// IsNotFound (admin.ErrNoSuchUser) when it does not exist.
func (c *Client) GetUser(ctx context.Context, uid string) (*User, error) {
	u, err := c.admin.GetUser(ctx, admin.User{ID: uid})
	if err != nil {
		return nil, err
	}
	return fromAdminUser(u), nil
}

// CreateUser creates an RGW user, asking radosgw to generate an S3 key pair. The
// returned User carries the generated keys.
func (c *Client) CreateUser(ctx context.Context, spec UserSpec) (*User, error) {
	u, err := c.admin.CreateUser(ctx, admin.User{
		ID:          spec.UID,
		DisplayName: firstNonEmpty(spec.DisplayName, spec.UID),
		Email:       spec.Email,
		MaxBuckets:  int32PtrToIntPtr(spec.MaxBuckets),
		Suspended:   boolToIntPtr(spec.Suspended),
		GenerateKey: boolPtr(true),
	})
	if err != nil {
		return nil, err
	}
	c.forgetIdentity(spec.UID)
	return fromAdminUser(u), nil
}

// UpdateUser reconciles the mutable attributes of an existing RGW user.
func (c *Client) UpdateUser(ctx context.Context, spec UserSpec) (*User, error) {
	u, err := c.admin.ModifyUser(ctx, admin.User{
		ID:          spec.UID,
		DisplayName: firstNonEmpty(spec.DisplayName, spec.UID),
		Email:       spec.Email,
		MaxBuckets:  int32PtrToIntPtr(spec.MaxBuckets),
		Suspended:   boolToIntPtr(spec.Suspended),
	})
	if err != nil {
		return nil, err
	}
	return fromAdminUser(u), nil
}

// DeleteUser removes an RGW user. A NoSuchUser response is treated as success.
func (c *Client) DeleteUser(ctx context.Context, uid string) error {
	err := c.admin.RemoveUser(ctx, admin.User{ID: uid})
	c.forgetIdentity(uid)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// SetUserQuota applies a quota to a user. quotaType is "user" or "bucket" (the
// latter sets the per-bucket default for buckets the user owns). A nil or
// negative limit means unlimited for that dimension.
func (c *Client) SetUserQuota(ctx context.Context, uid, quotaType string, enabled bool, maxSizeBytes, maxObjects *int64) error {
	maxSize := valueOr(maxSizeBytes, -1)
	maxObj := valueOr(maxObjects, -1)
	return c.admin.SetUserQuota(ctx, admin.QuotaSpec{
		UID:        uid,
		QuotaType:  quotaType,
		Enabled:    &enabled,
		MaxSize:    &maxSize,
		MaxObjects: &maxObj,
	})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func valueOr(v *int64, fallback int64) int64 {
	if v == nil || *v < 0 {
		return fallback
	}
	return *v
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func boolPtr(b bool) *bool { return &b }

func int32PtrToIntPtr(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

func boolToIntPtr(b bool) *int {
	v := 0
	if b {
		v = 1
	}
	return &v
}
