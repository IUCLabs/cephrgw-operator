package ceph

import (
	"encoding/json"
	"sort"
	"strings"
)

// Access levels mirrored from the API package to avoid an import cycle; the
// controllers translate their typed level into these strings.
const (
	LevelReadOnly  = "read-only"
	LevelReadWrite = "read-write"
	LevelFull      = "full"
)

// Grant couples an RGW user id with the access level to grant it on a bucket.
type Grant struct {
	UID   string
	Level string
}

type policyDocument struct {
	Version   string            `json:"Version"`
	Statement []policyStatement `json:"Statement"`
}

type policyStatement struct {
	Sid       string              `json:"Sid"`
	Effect    string              `json:"Effect"`
	Principal map[string][]string `json:"Principal"`
	Action    []string            `json:"Action"`
	Resource  []string            `json:"Resource"`
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
// principal its requested level. It returns "" when there are no grants so the
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
	objectARN := bucketARN + "/*"

	doc := policyDocument{Version: "2012-10-17"}
	for _, g := range sorted {
		principal := map[string][]string{"AWS": {"arn:aws:iam:::user/" + g.UID}}
		switch g.Level {
		case LevelFull:
			doc.Statement = append(doc.Statement, policyStatement{
				Sid:       sid("full", g.UID),
				Effect:    "Allow",
				Principal: principal,
				Action:    []string{"s3:*"},
				Resource:  []string{bucketARN, objectARN},
			})
		default:
			doc.Statement = append(doc.Statement,
				policyStatement{
					Sid:       sid(g.Level+"-bkt", g.UID),
					Effect:    "Allow",
					Principal: principal,
					Action:    bucketActions[g.Level],
					Resource:  []string{bucketARN},
				},
				policyStatement{
					Sid:       sid(g.Level+"-obj", g.UID),
					Effect:    "Allow",
					Principal: principal,
					Action:    objectActions[g.Level],
					Resource:  []string{objectARN},
				},
			)
		}
	}

	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(b), nil
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

// BuildTagJSON renders bucket tags in the JSON form the dashboard expects.
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
