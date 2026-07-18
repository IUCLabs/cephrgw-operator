package v1alpha1

// Quota describes an RGW quota applied to a user or a bucket. A nil limit (or a
// negative value) means unlimited for that dimension.
type Quota struct {
	// Enabled turns the quota on. When false the other fields are ignored and
	// the quota is disabled on the target.
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// MaxSizeBytes caps the total size in bytes. Nil or negative means unlimited.
	// +optional
	MaxSizeBytes *int64 `json:"maxSizeBytes,omitempty"`

	// MaxObjects caps the number of objects. Nil or negative means unlimited.
	// +optional
	MaxObjects *int64 `json:"maxObjects,omitempty"`
}

// SecretKeyRef points at a single key within a Secret in the same namespace.
type SecretKeyRef struct {
	// Name is the Secret name.
	Name string `json:"name"`
	// Key is the data key within the Secret.
	Key string `json:"key"`
}
