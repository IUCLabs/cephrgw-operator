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

// parsedPolicy is a fuller parse of a rendered policy for the fine-grained tests.
type parsedPolicy struct {
	Statement []struct {
		Sid       string                         `json:"Sid"`
		Effect    string                         `json:"Effect"`
		Principal map[string][]string            `json:"Principal"`
		Action    []string                       `json:"Action"`
		Resource  []string                       `json:"Resource"`
		Condition map[string]map[string][]string `json:"Condition"`
	} `json:"Statement"`
}

func parsePolicy(t *testing.T, raw string) parsedPolicy {
	t.Helper()
	var doc parsedPolicy
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v\n%s", err, raw)
	}
	return doc
}

func TestBuildBucketPolicyPaths(t *testing.T) {
	raw, err := BuildBucketPolicy("data", []Grant{
		{UID: "reader", Level: LevelReadOnly, Paths: []string{"team-a/", "/shared/inbox/"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	doc := parsePolicy(t, raw)

	var objResources []string
	for _, s := range doc.Statement {
		for _, a := range s.Action {
			if a == "s3:GetObject" {
				objResources = s.Resource
			}
		}
	}
	want := map[string]bool{
		"arn:aws:s3:::data/shared/inbox/*": false,
		"arn:aws:s3:::data/team-a/*":       false,
	}
	if len(objResources) != len(want) {
		t.Fatalf("expected %d object resources, got %v", len(want), objResources)
	}
	for _, r := range objResources {
		if _, ok := want[r]; !ok {
			t.Fatalf("unexpected object resource %q (leading slash not trimmed?)", r)
		}
		want[r] = true
	}
	for r, seen := range want {
		if !seen {
			t.Fatalf("missing object resource %q", r)
		}
	}
}

func TestBuildBucketPolicyActionsOverride(t *testing.T) {
	raw, err := BuildBucketPolicy("data", []Grant{
		{UID: "svc", Level: LevelReadOnly, Actions: []string{"s3:GetObject", "s3:PutObject"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	doc := parsePolicy(t, raw)
	if len(doc.Statement) != 1 {
		t.Fatalf("expected a single custom-action statement, got %d", len(doc.Statement))
	}
	s := doc.Statement[0]
	if len(s.Action) != 2 || s.Action[0] != "s3:GetObject" || s.Action[1] != "s3:PutObject" {
		t.Fatalf("actions not taken verbatim: %v", s.Action)
	}
	// Custom-action statement lists both the bucket and object resources.
	if len(s.Resource) != 2 || s.Resource[0] != "arn:aws:s3:::data" || s.Resource[1] != "arn:aws:s3:::data/*" {
		t.Fatalf("unexpected resources: %v", s.Resource)
	}
}

func TestBuildBucketPolicyConditions(t *testing.T) {
	raw, err := BuildBucketPolicy("data", []Grant{
		{UID: "reader", Level: LevelReadOnly, Conditions: &GrantConditions{
			SourceIPs:           []string{"192.0.2.0/24"},
			SecureTransportOnly: true,
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	doc := parsePolicy(t, raw)
	for _, s := range doc.Statement {
		if s.Condition == nil {
			t.Fatalf("statement %q missing condition block", s.Sid)
		}
		if ip := s.Condition["IpAddress"]["aws:SourceIp"]; len(ip) != 1 || ip[0] != "192.0.2.0/24" {
			t.Fatalf("unexpected SourceIp condition: %v", s.Condition["IpAddress"])
		}
		if tls := s.Condition["Bool"]["aws:SecureTransport"]; len(tls) != 1 || tls[0] != "true" {
			t.Fatalf("unexpected SecureTransport condition: %v", s.Condition["Bool"])
		}
	}
}

func TestBuildBucketPolicyRawStatements(t *testing.T) {
	raw, err := BuildBucketPolicy("data", []Grant{
		{UID: "svc", Level: LevelReadOnly, Raw: []RawStatement{
			{
				Effect:    "Deny",
				Actions:   []string{"s3:DeleteObject"},
				Resources: []string{"locked/", "arn:aws:s3:::other/*"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	doc := parsePolicy(t, raw)
	if len(doc.Statement) != 1 {
		t.Fatalf("expected 1 raw statement, got %d", len(doc.Statement))
	}
	s := doc.Statement[0]
	if s.Effect != "Deny" {
		t.Fatalf("raw effect not honoured: %q", s.Effect)
	}
	// The operator fills in the principal; bucket-relative prefixes are expanded
	// while explicit ARNs pass through.
	if len(s.Principal["AWS"]) != 1 || !strings.HasSuffix(s.Principal["AWS"][0], "user/svc") {
		t.Fatalf("raw statement principal not injected: %v", s.Principal)
	}
	if len(s.Resource) != 2 || s.Resource[0] != "arn:aws:s3:::data/locked/*" || s.Resource[1] != "arn:aws:s3:::other/*" {
		t.Fatalf("unexpected raw resources: %v", s.Resource)
	}
}

func TestMergeBucketPolicyPreservesForeign(t *testing.T) {
	// An existing policy with a foreign statement (unknown Sid, and a scalar
	// condition value S3 allows but our typed struct does not model).
	existing := `{"Version":"2012-10-17","Statement":[` +
		`{"Sid":"AllowPublicRead","Effect":"Allow","Principal":"*","Action":["s3:GetObject"],` +
		`"Resource":"arn:aws:s3:::data/public/*","Condition":{"Bool":{"aws:SecureTransport":"true"}}}]}`

	merged, err := MergeBucketPolicy(existing, "data", []Grant{{UID: "reader", Level: LevelReadOnly}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	doc := parsePolicyRaw(t, merged)

	var sawForeign, sawManaged bool
	for _, raw := range doc.Statement {
		var s struct {
			Sid       string `json:"Sid"`
			Condition map[string]map[string]any
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("statement not valid JSON: %v", err)
		}
		if s.Sid == "AllowPublicRead" {
			sawForeign = true
			// The scalar condition value must survive verbatim.
			if v := s.Condition["Bool"]["aws:SecureTransport"]; v != "true" {
				t.Fatalf("foreign scalar condition mangled: %v", s.Condition)
			}
		}
		if isManagedSid(s.Sid) {
			sawManaged = true
		}
	}
	if !sawForeign {
		t.Fatal("foreign statement was dropped")
	}
	if !sawManaged {
		t.Fatal("operator statement missing from merge")
	}
}

func TestMergeBucketPolicyReplacesManaged(t *testing.T) {
	// Two rounds: an existing policy already carrying the operator's statements
	// must not accumulate duplicates when re-merged.
	first, err := MergeBucketPolicy("", "data", []Grant{{UID: "reader", Level: LevelReadOnly}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := MergeBucketPolicy(first, "data", []Grant{{UID: "reader", Level: LevelReadOnly}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if first != second {
		t.Fatalf("re-merging its own policy was not idempotent:\n first=%s\nsecond=%s", first, second)
	}
	// A legacy (unprefixed) operator statement must also be recognised and
	// replaced rather than preserved as foreign.
	legacy := `{"Version":"2012-10-17","Statement":[` +
		`{"Sid":"readonlybktreader","Effect":"Allow","Principal":{"AWS":["arn:aws:iam:::user/reader"]},` +
		`"Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::data"]}]}`
	merged, err := MergeBucketPolicy(legacy, "data", []Grant{{UID: "reader", Level: LevelReadOnly}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, raw := range parsePolicyRaw(t, merged).Statement {
		var s struct {
			Sid string `json:"Sid"`
		}
		_ = json.Unmarshal(raw, &s)
		if s.Sid == "readonlybktreader" {
			t.Fatal("legacy operator statement was preserved instead of replaced")
		}
	}
}

func TestMergeBucketPolicyForeignOnlyKept(t *testing.T) {
	existing := `{"Version":"2012-10-17","Statement":[` +
		`{"Sid":"AllowPublicRead","Effect":"Allow","Principal":"*","Action":["s3:GetObject"],` +
		`"Resource":"arn:aws:s3:::data/*"}]}`
	// No grants: the operator adds nothing but must not wipe the foreign policy.
	merged, err := MergeBucketPolicy(existing, "data", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if merged == "" {
		t.Fatal("merge cleared a policy that had a foreign statement")
	}
	if len(parsePolicyRaw(t, merged).Statement) != 1 {
		t.Fatalf("expected the single foreign statement, got %s", merged)
	}
}

func TestMergeBucketPolicyEmpty(t *testing.T) {
	merged, err := MergeBucketPolicy("", "data", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if merged != "" {
		t.Fatalf("expected empty policy, got %q", merged)
	}
}

// parsePolicyRaw parses a policy keeping statements as raw JSON.
func parsePolicyRaw(t *testing.T, raw string) struct {
	Version   string            `json:"Version"`
	Statement []json.RawMessage `json:"Statement"`
} {
	t.Helper()
	var doc struct {
		Version   string            `json:"Version"`
		Statement []json.RawMessage `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v\n%s", err, raw)
	}
	return doc
}

func TestBuildBucketPolicyFineGrainedDeterministic(t *testing.T) {
	grants := []Grant{
		{UID: "reader", Level: LevelReadOnly, Paths: []string{"a/", "b/"}},
		{UID: "svc", Level: LevelReadWrite, Conditions: &GrantConditions{SecureTransportOnly: true}},
	}
	a, err := BuildBucketPolicy("data", grants)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := BuildBucketPolicy("data", []Grant{grants[1], grants[0]})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a != b {
		t.Fatalf("fine-grained policy is order-dependent:\n a=%s\n b=%s", a, b)
	}
}
