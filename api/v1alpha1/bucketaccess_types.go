package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AccessLevel is the level of access a principal is granted to a bucket. The
// operator translates it into an S3 bucket policy statement.
// +kubebuilder:validation:Enum=read-only;read-write;full
type AccessLevel string

const (
	// AccessReadOnly grants object GET and bucket LIST (s3:GetObject,
	// s3:ListBucket and friends).
	AccessReadOnly AccessLevel = "read-only"
	// AccessReadWrite grants read plus object PUT/DELETE and multipart.
	AccessReadWrite AccessLevel = "read-write"
	// AccessFull grants s3:* on the bucket and its objects.
	AccessFull AccessLevel = "full"
)

// BucketAccessSpec grants an RGW user a level of access to a Bucket by
// maintaining a statement in the bucket's S3 policy. If UserRef is empty the
// operator provisions a dedicated user for this grant and writes its keys into
// a Secret; otherwise it grants an existing ObjectStoreUser.
type BucketAccessSpec struct {
	// BucketRef names the Bucket (in this namespace) to grant access to.
	BucketRef string `json:"bucketRef"`

	// Level is the access level to grant.
	Level AccessLevel `json:"level"`

	// UserRef optionally names an existing ObjectStoreUser (in this namespace)
	// to grant. When set, the operator does not create or delete a user and
	// SecretName is ignored (that user already owns its own credential Secret).
	// +optional
	UserRef string `json:"userRef,omitempty"`

	// UID overrides the id of the dedicated user created when UserRef is empty.
	// Defaults to "<bucket>-<name>". Ignored when UserRef is set.
	// +optional
	UID string `json:"uid,omitempty"`

	// SecretName is the Secret the operator writes credentials into for the
	// dedicated user it creates (UserRef empty). Defaults to "<name>-rgw".
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// BucketAccessStatus reports observed grant state.
type BucketAccessStatus struct {
	// Phase is a coarse lifecycle summary (Pending/Ready/Error).
	// +optional
	Phase string `json:"phase,omitempty"`
	// UID is the RGW user id that was granted access.
	// +optional
	UID string `json:"uid,omitempty"`
	// SecretName is the Secret holding the dedicated user's credentials, if any.
	// +optional
	SecretName string `json:"secretName,omitempty"`
	// Bound reports whether the grant is reflected in the bucket policy.
	// +optional
	Bound bool `json:"bound,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ba
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.spec.bucketRef`
// +kubebuilder:printcolumn:name="Level",type=string,JSONPath=`.spec.level`
// +kubebuilder:printcolumn:name="UID",type=string,JSONPath=`.status.uid`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`

// BucketAccess grants an RGW user read-only, read-write or full access to a
// Bucket via the bucket's S3 policy.
type BucketAccess struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketAccessSpec   `json:"spec,omitempty"`
	Status BucketAccessStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BucketAccessList contains a list of BucketAccess.
type BucketAccessList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BucketAccess `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BucketAccess{}, &BucketAccessList{})
}
