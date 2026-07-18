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

	// Zonegroup optionally pins the bucket to a specific RGW zonegroup.
	// +optional
	Zonegroup string `json:"zonegroup,omitempty"`

	// PlacementTarget optionally selects a non-default placement target/pool.
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
	// PolicyPrincipals is the number of extra principals granted via
	// BucketAccess and reflected in the bucket policy.
	// +optional
	PolicyPrincipals int32 `json:"policyPrincipals,omitempty"`
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
// +kubebuilder:printcolumn:name="Grants",type=integer,JSONPath=`.status.policyPrincipals`
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
