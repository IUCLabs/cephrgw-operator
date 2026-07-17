package ceph

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildBucketPolicyEmpty(t *testing.T) {
	got, err := BuildBucketPolicy("data", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty policy for no grants, got %q", got)
	}
}

func TestBuildBucketPolicyDeterministic(t *testing.T) {
	a, err := BuildBucketPolicy("data", []Grant{
		{UID: "reader", Level: LevelReadOnly},
		{UID: "writer", Level: LevelReadWrite},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := BuildBucketPolicy("data", []Grant{
		{UID: "writer", Level: LevelReadWrite},
		{UID: "reader", Level: LevelReadOnly},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a != b {
		t.Fatalf("policy is order-dependent:\n a=%s\n b=%s", a, b)
	}
}

func TestBuildBucketPolicyStructure(t *testing.T) {
	raw, err := BuildBucketPolicy("data", []Grant{
		{UID: "reader", Level: LevelReadOnly},
		{UID: "admin", Level: LevelFull},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var doc struct {
		Version   string `json:"Version"`
		Statement []struct {
			Effect    string              `json:"Effect"`
			Principal map[string][]string `json:"Principal"`
			Action    []string            `json:"Action"`
			Resource  []string            `json:"Resource"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v\n%s", err, raw)
	}
	if doc.Version != "2012-10-17" {
		t.Fatalf("unexpected version %q", doc.Version)
	}
	// read-only -> two statements (bucket + object); full -> one statement.
	if len(doc.Statement) != 3 {
		t.Fatalf("expected 3 statements, got %d", len(doc.Statement))
	}

	var sawFullWildcard, sawReaderPrincipal bool
	for _, s := range doc.Statement {
		if s.Effect != "Allow" {
			t.Fatalf("expected Allow effect, got %q", s.Effect)
		}
		for _, a := range s.Action {
			if a == "s3:*" {
				sawFullWildcard = true
			}
		}
		for _, p := range s.Principal["AWS"] {
			if strings.HasSuffix(p, "user/reader") {
				sawReaderPrincipal = true
			}
		}
	}
	if !sawFullWildcard {
		t.Fatal("full grant did not produce an s3:* action")
	}
	if !sawReaderPrincipal {
		t.Fatal("reader principal ARN missing")
	}
}
