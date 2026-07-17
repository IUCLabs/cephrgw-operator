package ceph

import (
	"context"
	"net/http"
	"net/url"
)

// UserKey is an S3 access/secret key pair belonging to an RGW user.
type UserKey struct {
	User      string `json:"user"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// User is the subset of an RGW user record the operator consumes.
type User struct {
	UID         string    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"`
	MaxBuckets  int       `json:"max_buckets"`
	Suspended   int       `json:"suspended"`
	Keys        []UserKey `json:"keys"`
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

type createUserRequest struct {
	UID         string `json:"uid"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	MaxBuckets  *int32 `json:"max_buckets,omitempty"`
	Suspended   bool   `json:"suspended"`
	GenerateKey bool   `json:"generate_key"`
}

type updateUserRequest struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	MaxBuckets  *int32 `json:"max_buckets,omitempty"`
	Suspended   bool   `json:"suspended"`
}

// GetUser fetches an RGW user by uid, returning an *APIError with status 404
// (see IsNotFound) when it does not exist.
func (c *Client) GetUser(ctx context.Context, uid string) (*User, error) {
	var u User
	if err := c.do(ctx, http.MethodGet, "/api/rgw/user/"+url.PathEscape(uid), nil, &u, ""); err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser creates an RGW user, asking the dashboard to generate an S3 key
// pair. The returned User carries the generated keys.
func (c *Client) CreateUser(ctx context.Context, spec UserSpec) (*User, error) {
	req := createUserRequest{
		UID:         spec.UID,
		DisplayName: firstNonEmpty(spec.DisplayName, spec.UID),
		Email:       spec.Email,
		MaxBuckets:  spec.MaxBuckets,
		Suspended:   spec.Suspended,
		GenerateKey: true,
	}
	var u User
	if err := c.do(ctx, http.MethodPost, "/api/rgw/user", req, &u, ""); err != nil {
		return nil, err
	}
	return &u, nil
}

// UpdateUser reconciles the mutable attributes of an existing RGW user.
func (c *Client) UpdateUser(ctx context.Context, spec UserSpec) (*User, error) {
	req := updateUserRequest{
		DisplayName: firstNonEmpty(spec.DisplayName, spec.UID),
		Email:       spec.Email,
		MaxBuckets:  spec.MaxBuckets,
		Suspended:   spec.Suspended,
	}
	var u User
	if err := c.do(ctx, http.MethodPut, "/api/rgw/user/"+url.PathEscape(spec.UID), req, &u, ""); err != nil {
		return nil, err
	}
	return &u, nil
}

// DeleteUser removes an RGW user. A 404 is treated as success.
func (c *Client) DeleteUser(ctx context.Context, uid string) error {
	err := c.do(ctx, http.MethodDelete, "/api/rgw/user/"+url.PathEscape(uid), nil, nil, "")
	if IsNotFound(err) {
		return nil
	}
	return err
}

type quotaRequest struct {
	QuotaType  string `json:"quota_type"`
	Enabled    bool   `json:"enabled"`
	MaxSizeKb  int64  `json:"max_size_kb"`
	MaxObjects int64  `json:"max_objects"`
}

// SetUserQuota applies a quota to a user. quotaType is "user" or "bucket" (the
// latter sets the per-bucket default for buckets the user owns). A nil or
// negative limit means unlimited for that dimension.
func (c *Client) SetUserQuota(ctx context.Context, uid, quotaType string, enabled bool, maxSizeBytes, maxObjects *int64) error {
	req := quotaRequest{
		QuotaType:  quotaType,
		Enabled:    enabled,
		MaxSizeKb:  bytesToKb(maxSizeBytes),
		MaxObjects: valueOr(maxObjects, -1),
	}
	return c.do(ctx, http.MethodPut, "/api/rgw/user/"+url.PathEscape(uid)+"/quota", req, nil, "")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func bytesToKb(b *int64) int64 {
	if b == nil || *b < 0 {
		return -1
	}
	return *b / 1024
}

func valueOr(v *int64, fallback int64) int64 {
	if v == nil || *v < 0 {
		return fallback
	}
	return *v
}
