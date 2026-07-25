package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ObjectStoreUserSpec defines a Ceph RGW (S3) user. The operator creates the
// user through the radosgw Admin Ops API and writes its generated access/secret
// key pair into a Kubernetes Secret. The key material is never stored on the
// resource itself.
type ObjectStoreUserSpec struct {
	// UID is the RGW user id. Defaults to metadata.name. Immutable once created.
	// +optional
	UID string `json:"uid,omitempty"`

	// DisplayName is the human-readable name for the user. Defaults to the UID.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// Email is an optional email address recorded on the user.
	// +optional
	Email string `json:"email,omitempty"`

	// MaxBuckets caps how many buckets the user may own. A negative value
	// disables bucket creation; 0 leaves the RGW default. Defaults to 1000.
	// +kubebuilder:default=1000
	// +optional
	MaxBuckets *int32 `json:"maxBuckets,omitempty"`

	// Suspended manages the user's suspended state: true suspends the user so
	// its keys stop working, false resumes it. When unset the operator does not
	// touch the suspended state (useful when adopting an existing user).
	// +optional
	Suspended *bool `json:"suspended,omitempty"`

	// Quota optionally applies a user-level quota.
	// +optional
	Quota *Quota `json:"quota,omitempty"`

	// SecretName is the Secret the operator writes the access/secret key into.
	// Defaults to "<name>-rgw". The Secret holds AWS_ACCESS_KEY_ID,
	// AWS_SECRET_ACCESS_KEY, BUCKET_HOST and the RGW uid.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// RetainOnDelete keeps the RGW user (and its keys) when the ObjectStoreUser
	// resource is deleted, instead of removing it. Set this before adopting an
	// existing user you may later want to hand back. Defaults to false.
	// +optional
	RetainOnDelete bool `json:"retainOnDelete,omitempty"`
}

// ObjectStoreUserStatus reports observed user state.
type ObjectStoreUserStatus struct {
	// Phase is a coarse lifecycle summary (Pending/Ready/Error).
	// +optional
	Phase string `json:"phase,omitempty"`
	// UID is the RGW user id that was provisioned.
	// +optional
	UID string `json:"uid,omitempty"`
	// SecretName is the Secret holding the user's credentials.
	// +optional
	SecretName string `json:"secretName,omitempty"`
	// Adopted reports that the RGW user already existed when the operator first
	// reconciled this resource (it was taken over, not created).
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
// +kubebuilder:resource:shortName=osu
// +kubebuilder:printcolumn:name="UID",type=string,JSONPath=`.status.uid`
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.status.secretName`
// +kubebuilder:printcolumn:name="Adopted",type=boolean,JSONPath=`.status.adopted`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`

// ObjectStoreUser is a Ceph RGW S3 user whose keys are delivered into a Secret.
type ObjectStoreUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ObjectStoreUserSpec   `json:"spec,omitempty"`
	Status ObjectStoreUserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ObjectStoreUserList contains a list of ObjectStoreUser.
type ObjectStoreUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ObjectStoreUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ObjectStoreUser{}, &ObjectStoreUserList{})
}
