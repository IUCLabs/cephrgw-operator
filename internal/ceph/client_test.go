package ceph

import (
	"testing"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	"github.com/ceph/go-ceph/rgw/admin"
)

func TestNewClientValidation(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"ok", Config{Endpoint: "https://rgw:443", AccessKey: "a", SecretKey: "s"}, false},
		{"no endpoint", Config{AccessKey: "a", SecretKey: "s"}, true},
		{"no access key", Config{Endpoint: "https://rgw:443", SecretKey: "s"}, true},
		{"no secret key", Config{Endpoint: "https://rgw:443", AccessKey: "a"}, true},
		{"bad ca", Config{Endpoint: "https://rgw:443", AccessKey: "a", SecretKey: "s", CACert: []byte("not pem")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClient(tc.cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("NewClient err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"admin no such user", admin.ErrNoSuchUser, true},
		{"admin no such bucket", admin.ErrNoSuchBucket, true},
		{"admin no such key", admin.ErrNoSuchKey, true},
		{"s3 no such bucket", &s3types.NoSuchBucket{}, true},
		{"s3 no such key", &s3types.NoSuchKey{}, true},
		{"generic no such bucket policy", &smithy.GenericAPIError{Code: "NoSuchBucketPolicy"}, true},
		{"generic no such tag set", &smithy.GenericAPIError{Code: "NoSuchTagSet"}, true},
		{"admin user exists is not notfound", admin.ErrUserExists, false},
		{"unrelated", &smithy.GenericAPIError{Code: "AccessDenied"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotFound(tc.err); got != tc.want {
				t.Errorf("IsNotFound(%v)=%v want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsConflict(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"admin user exists", admin.ErrUserExists, true},
		{"admin bucket not empty", admin.ErrBucketNotEmpty, true},
		{"s3 bucket already owned by you", &s3types.BucketAlreadyOwnedByYou{}, true},
		{"s3 bucket already exists", &s3types.BucketAlreadyExists{}, true},
		{"generic bucket already exists", &smithy.GenericAPIError{Code: "BucketAlreadyExists"}, true},
		{"admin no such user is not conflict", admin.ErrNoSuchUser, false},
		{"unrelated", &smithy.GenericAPIError{Code: "AccessDenied"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsConflict(tc.err); got != tc.want {
				t.Errorf("IsConflict(%v)=%v want %v", tc.err, got, tc.want)
			}
		})
	}
}
