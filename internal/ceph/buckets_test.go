package ceph

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocationConstraint(t *testing.T) {
	cases := []struct {
		name      string
		zonegroup string
		placement string
		want      string
	}{
		{"both empty -> no constraint", "", "", ""},
		{"placement only -> local zonegroup", "", "ec", ":ec"},
		{"placement only default target", "", "default-placement", ":default-placement"},
		{"zonegroup and placement", "default", "ec", "default:ec"},
		{"zonegroup only", "default", "", "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := locationConstraint(tc.zonegroup, tc.placement); got != tc.want {
				t.Errorf("locationConstraint(%q,%q)=%q want %q", tc.zonegroup, tc.placement, got, tc.want)
			}
		})
	}
}

// TestGetBucketPlacement verifies GetBucket surfaces the placement target and
// zonegroup from the Admin Ops bucket-stats response, so the controller can
// detect placement drift.
func TestGetBucketPlacement(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"bucket": "raw-archive",
			"id": "eae688bc-ee35-445d-9188-111b73c8b4a0.12345.1",
			"owner": "logarchiver",
			"zonegroup": "eae688bc-ee35-445d-9188-111b73c8b4a0",
			"placement_rule": "ec"
		}`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{Endpoint: srv.URL, AccessKey: "a", SecretKey: "s"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	info, err := c.GetBucket(context.Background(), "raw-archive")
	if err != nil {
		t.Fatalf("GetBucket: %v", err)
	}
	if info.PlacementRule != "ec" {
		t.Errorf("PlacementRule=%q want %q", info.PlacementRule, "ec")
	}
	if info.Zonegroup != "eae688bc-ee35-445d-9188-111b73c8b4a0" {
		t.Errorf("Zonegroup=%q unexpected", info.Zonegroup)
	}
	if info.Owner != "logarchiver" {
		t.Errorf("Owner=%q want logarchiver", info.Owner)
	}
	if info.Name() != "raw-archive" {
		t.Errorf("Name()=%q want raw-archive", info.Name())
	}
}
