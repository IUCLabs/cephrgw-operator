package ceph

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Access levels mirrored from the API package to avoid an import cycle; the
// controllers translate their typed level into these strings.
const (
	LevelReadOnly  = "read-only"
	LevelReadWrite = "read-write"
	LevelFull      = "full"
)

// GrantConditions restricts when a grant's statements apply. The zero value adds
// no conditions.
type GrantConditions struct {
	// SourceIPs restricts the grant to these CIDRs (S3 aws:SourceIp).
	SourceIPs []string
	// SecureTransportOnly requires TLS (S3 aws:SecureTransport).
	SecureTransportOnly bool
}

// RawStatement is a caller-supplied S3 policy statement for a grant.
type RawStatement struct {
	Sid       string
	Effect    string
	Actions   []string
	Resources []string
	Condition map[string]map[string][]string
}

// Grant couples an RGW user id with the access it should have on a bucket. The
// simple form is a Level; Paths, Actions and Conditions refine it, and Raw
// replaces it entirely with caller-supplied statements.
type Grant struct {
	UID   string
	Level string
	// Paths scopes object-level access to these key prefixes; empty = whole
	// bucket.
	Paths []string
	// Actions overrides the level's action set; empty = derive from Level.
	Actions []string
	// Conditions optionally restricts when the grant applies.
	Conditions *GrantConditions
	// Raw, when non-empty, replaces Level/Actions/Paths/Conditions with these
	// statements (the operator still fills in a Principal when one is omitted).
	Raw []RawStatement
}

type policyDocument struct {
	Version   string            `json:"Version"`
	Statement []policyStatement `json:"Statement"`
}

type policyStatement struct {
	Sid       string                         `json:"Sid,omitempty"`
	Effect    string                         `json:"Effect"`
	Principal map[string][]string            `json:"Principal,omitempty"`
	Action    []string                       `json:"Action"`
	Resource  []string                       `json:"Resource"`
	Condition map[string]map[string][]string `json:"Condition,omitempty"`
}

// bucket-level and object-level S3 actions per access level.
var bucketActions = map[string][]string{
	LevelReadOnly: {
		"s3:ListBucket",
		"s3:GetBucketLocation",
		"s3:ListBucketVersions",
	},
	LevelReadWrite: {
		"s3:ListBucket",
		"s3:GetBucketLocation",
		"s3:ListBucketVersions",
		"s3:ListBucketMultipartUploads",
	},
}

var objectActions = map[string][]string{
	LevelReadOnly: {
		"s3:GetObject",
		"s3:GetObjectVersion",
		"s3:GetObjectTagging",
	},
	LevelReadWrite: {
		"s3:GetObject",
		"s3:GetObjectVersion",
		"s3:GetObjectTagging",
		"s3:PutObject",
		"s3:PutObjectTagging",
		"s3:DeleteObject",
		"s3:DeleteObjectVersion",
		"s3:AbortMultipartUpload",
		"s3:ListMultipartUploadParts",
	},
}

// BuildBucketPolicy renders a deterministic S3 bucket policy granting each
// principal its requested access. It returns "" when there are no grants so the
// caller can clear the policy.
func BuildBucketPolicy(bucket string, grants []Grant) (string, error) {
	if len(grants) == 0 {
		return "", nil
	}

	sorted := make([]Grant, len(grants))
	copy(sorted, grants)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].UID == sorted[j].UID {
			return sorted[i].Level < sorted[j].Level
		}
		return sorted[i].UID < sorted[j].UID
	})

	bucketARN := "arn:aws:s3:::" + bucket

	doc := policyDocument{Version: "2012-10-17"}
	for _, g := range sorted {
		doc.Statement = append(doc.Statement, statementsForGrant(bucketARN, g)...)
	}

	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// statementsForGrant renders the policy statements for a single grant.
func statementsForGrant(bucketARN string, g Grant) []policyStatement {
	principal := map[string][]string{"AWS": {"arn:aws:iam:::user/" + g.UID}}

	if len(g.Raw) > 0 {
		out := make([]policyStatement, 0, len(g.Raw))
		for i, rs := range g.Raw {
			st := policyStatement{
				Sid:       firstNonEmpty(rs.Sid, sid("raw", g.UID)+strconv.Itoa(i)),
				Effect:    firstNonEmpty(rs.Effect, "Allow"),
				Principal: principal,
				Action:    rs.Actions,
				Resource:  resolveResources(bucketARN, rs.Resources),
				Condition: rs.Condition,
			}
			out = append(out, st)
		}
		return out
	}

	cond := buildCondition(g.Conditions)
	objectARNs := objectResources(bucketARN, g.Paths)

	if len(g.Actions) > 0 {
		return []policyStatement{{
			Sid:       sid("custom", g.UID),
			Effect:    "Allow",
			Principal: principal,
			Action:    g.Actions,
			Resource:  append([]string{bucketARN}, objectARNs...),
			Condition: cond,
		}}
	}

	if g.Level == LevelFull {
		return []policyStatement{{
			Sid:       sid("full", g.UID),
			Effect:    "Allow",
			Principal: principal,
			Action:    []string{"s3:*"},
			Resource:  append([]string{bucketARN}, objectARNs...),
			Condition: cond,
		}}
	}

	return []policyStatement{
		{
			Sid:       sid(g.Level+"-bkt", g.UID),
			Effect:    "Allow",
			Principal: principal,
			Action:    bucketActions[g.Level],
			Resource:  []string{bucketARN},
			Condition: cond,
		},
		{
			Sid:       sid(g.Level+"-obj", g.UID),
			Effect:    "Allow",
			Principal: principal,
			Action:    objectActions[g.Level],
			Resource:  objectARNs,
			Condition: cond,
		},
	}
}

// objectResources renders the object-level resource ARNs for a grant: the whole
// bucket ("<bucket>/*") when no paths are given, or one "<bucket>/<prefix>*" per
// prefix (deduplicated and sorted for determinism).
func objectResources(bucketARN string, paths []string) []string {
	if len(paths) == 0 {
		return []string{bucketARN + "/*"}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimPrefix(p, "/")
		arn := bucketARN + "/" + p + "*"
		if _, dup := seen[arn]; dup {
			continue
		}
		seen[arn] = struct{}{}
		out = append(out, arn)
	}
	sort.Strings(out)
	return out
}

// resolveResources renders raw-statement resources: entries that already look
// like ARNs pass through verbatim; bucket-relative prefixes become
// "<bucket>/<prefix>*". An empty list defaults to the whole bucket and objects.
func resolveResources(bucketARN string, resources []string) []string {
	if len(resources) == 0 {
		return []string{bucketARN, bucketARN + "/*"}
	}
	out := make([]string, 0, len(resources))
	for _, r := range resources {
		switch {
		case strings.HasPrefix(r, "arn:"):
			out = append(out, r)
		case r == "" || r == "/":
			out = append(out, bucketARN+"/*")
		default:
			out = append(out, bucketARN+"/"+strings.TrimPrefix(r, "/")+"*")
		}
	}
	return out
}

// buildCondition renders the S3 condition block for a grant, or nil when there
// is nothing to add.
func buildCondition(c *GrantConditions) map[string]map[string][]string {
	if c == nil {
		return nil
	}
	cond := map[string]map[string][]string{}
	if len(c.SourceIPs) > 0 {
		cond["IpAddress"] = map[string][]string{"aws:SourceIp": c.SourceIPs}
	}
	if c.SecureTransportOnly {
		cond["Bool"] = map[string][]string{"aws:SecureTransport": {"true"}}
	}
	if len(cond) == 0 {
		return nil
	}
	return cond
}

// sid builds a policy statement id that only contains characters S3 accepts.
func sid(prefix, uid string) string {
	var b strings.Builder
	b.WriteString(strings.ReplaceAll(prefix, "-", ""))
	for _, r := range uid {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// BuildTagJSON renders bucket tags as a {Key,Value} JSON list, the intermediate
// form SetBucketTags parses and re-encodes into the S3 Tagging XML document.
func BuildTagJSON(tags map[string]string) (string, error) {
	if len(tags) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type kv struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	}
	out := make([]kv, 0, len(keys))
	for _, k := range keys {
		out = append(out, kv{Key: k, Value: tags[k]})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
