package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ObjectLockMode is the S3 object-lock retention mode.
// +kubebuilder:validation:Enum=GOVERNANCE;COMPLIANCE
type ObjectLockMode string

const (
	ObjectLockGovernance ObjectLockMode = "GOVERNANCE"
	ObjectLockCompliance ObjectLockMode = "COMPLIANCE"
)

// ObjectLock configures S3 object lock (WORM) on a bucket. Object lock can only
// be enabled at creation time and requires versioning.
type ObjectLock struct {
	// Enabled turns on object lock for the bucket.
	Enabled bool `json:"enabled"`
	// Mode is the default retention mode applied to new objects.
	// +optional
	Mode ObjectLockMode `json:"mode,omitempty"`
	// Days is the default retention period in days. Mutually exclusive with Years.
	// +optional
	Days *int32 `json:"days,omitempty"`
	// Years is the default retention period in years. Mutually exclusive with Days.
	// +optional
	Years *int32 `json:"years,omitempty"`
}

// BucketSpec defines a Ceph RGW (S3) bucket owned by an ObjectStoreUser.
type BucketSpec struct {
	// BucketName is the S3 bucket name. Defaults to metadata.name. Immutable.
	// +optional
	BucketName string `json:"bucketName,omitempty"`

	// OwnerRef names the ObjectStoreUser (in this namespace) that owns the
	// bucket. The owner always has full control; grant additional principals
	// with BucketAccess objects.
	OwnerRef string `json:"ownerRef"`

	// Zonegroup optionally pins the bucket to a specific RGW zonegroup by its
	// api-name. Empty (the default) uses the cluster's local/master zonegroup, so
	// PlacementTarget selection works without naming the zonegroup. Immutable:
	// RGW resolves the zonegroup at bucket creation and cannot move it afterwards.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="zonegroup is immutable; RGW fixes it at bucket creation"
	// +optional
	Zonegroup string `json:"zonegroup,omitempty"`

	// PlacementTarget optionally selects the RGW placement target that backs the
	// bucket, choosing which pools (and thus replication/erasure profile) store
	// its data. Empty (the default) uses the owning user's default_placement, or
	// the zonegroup default. The valid values are cluster configuration, not a
	// fixed set; on this estate the two configured targets are
	// "default-placement" (3x replicated) and "ec" (4+1 erasure-coded).
	//
	// Immutable: RGW chooses the placement at bucket creation (from the S3
	// LocationConstraint) and cannot move an existing bucket between placement
	// targets. Set it on a fresh Bucket; changing it later is rejected, and if a
	// pre-existing bucket is on a different placement the operator reports an
	// error instead of recreating it.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="placementTarget is immutable; RGW cannot move a bucket between placement targets"
	// +optional
	PlacementTarget string `json:"placementTarget,omitempty"`

	// Versioning enables S3 object versioning on the bucket.
	// +optional
	Versioning bool `json:"versioning,omitempty"`

	// ObjectLock configures S3 object lock. Enabling it forces versioning on.
	// +optional
	ObjectLock *ObjectLock `json:"objectLock,omitempty"`

	// Quota optionally applies a bucket-level quota.
	// +optional
	Quota *Quota `json:"quota,omitempty"`

	// Tags are bucket tags (key/value) applied to the bucket.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// ManagePolicy controls whether the operator manages the bucket's S3 policy
	// from BucketAccess grants. When true (the default) the operator reconciles
	// its own statements while preserving any statements it does not own, so it
	// is safe to adopt a bucket that already has a policy. Set to false to leave
	// the bucket policy entirely untouched (BucketAccess grants then have no
	// effect on this bucket).
	// +kubebuilder:default=true
	// +optional
	ManagePolicy *bool `json:"managePolicy,omitempty"`

	// RetainOnDelete keeps the RGW bucket (and its objects) when the Bucket
	// resource is deleted. By default the operator removes the empty bucket;
	// it never purges objects unless PurgeOnDelete is also set.
	// +optional
	RetainOnDelete bool `json:"retainOnDelete,omitempty"`

	// PurgeOnDelete deletes the bucket together with all objects it contains
	// when the Bucket resource is removed. Dangerous; defaults to false.
	// +optional
	PurgeOnDelete bool `json:"purgeOnDelete,omitempty"`
}

// BucketStatus reports observed bucket state.
type BucketStatus struct {
	// Phase is a coarse lifecycle summary (Pending/Ready/Error).
	// +optional
	Phase string `json:"phase,omitempty"`
	// BucketName is the provisioned S3 bucket name.
	// +optional
	BucketName string `json:"bucketName,omitempty"`
	// BucketID is the RGW internal bucket instance id.
	// +optional
	BucketID string `json:"bucketID,omitempty"`
	// Owner is the RGW uid that owns the bucket.
	// +optional
	Owner string `json:"owner,omitempty"`
	// PlacementTarget is the placement target RGW actually stores the bucket on,
	// read back from the live bucket. It makes placement drift (a bucket landing
	// on a different target than spec requested) visible.
	// +optional
	PlacementTarget string `json:"placementTarget,omitempty"`
	// PolicyPrincipals is the number of extra principals granted via
	// BucketAccess and reflected in the bucket policy.
	// +optional
	PolicyPrincipals int32 `json:"policyPrincipals,omitempty"`
	// Adopted reports that the RGW bucket already existed when the operator
	// first reconciled this resource (it was taken over, not created).
	// +optional
	Adopted bool `json:"adopted,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=bkt
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.status.bucketName`
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.status.owner`
// +kubebuilder:printcolumn:name="Placement",type=string,JSONPath=`.status.placementTarget`
// +kubebuilder:printcolumn:name="Grants",type=integer,JSONPath=`.status.policyPrincipals`
// +kubebuilder:printcolumn:name="Adopted",type=boolean,JSONPath=`.status.adopted`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`

// Bucket is a Ceph RGW S3 bucket.
type Bucket struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketSpec   `json:"spec,omitempty"`
	Status BucketStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BucketList contains a list of Bucket.
type BucketList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Bucket `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Bucket{}, &BucketList{})
}
