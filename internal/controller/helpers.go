package controller

import (
	"context"
	"net/url"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/IUCLabs/cephrgw-operator/internal/ceph"
)

// finalizer guards external RGW state (users, buckets, policy statements) so it
// is cleaned up before the Kubernetes object disappears.
const finalizer = "ceph.unkin.net/finalizer"

// requeueSteady is the resync interval for healthy objects; it lets the
// operator heal drift made directly against RGW.
const requeueSteady = 10 * time.Minute

// requeueShort backs off on transient "waiting for a dependency" states.
const requeueShort = 30 * time.Second

// setReady sets the standard Ready condition on a status conditions slice.
func setReady(conds *[]metav1.Condition, gen int64, ok bool, reason, msg string) {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		ObservedGeneration: gen,
		Reason:             reason,
		Message:            truncate(msg, 32000),
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// upsertSecret creates or updates an owner-referenced Opaque Secret with data.
func upsertSecret(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner client.Object, name, namespace string, data map[string][]byte) error {
	sec := &corev1.Secret{}
	sec.Name = name
	sec.Namespace = namespace
	_, err := controllerutil.CreateOrUpdate(ctx, c, sec, func() error {
		sec.Type = corev1.SecretTypeOpaque
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		for k, v := range data {
			sec.Data[k] = v
		}
		return controllerutil.SetControllerReference(owner, sec, scheme)
	})
	return err
}

// credentialSecretData assembles the conventional S3/AWS credential keys.
func credentialSecretData(key ceph.UserKey, uid, endpoint, bucket string) map[string][]byte {
	data := map[string][]byte{
		"AWS_ACCESS_KEY_ID":     []byte(key.AccessKey),
		"AWS_SECRET_ACCESS_KEY": []byte(key.SecretKey),
		"RGW_UID":               []byte(uid),
	}
	if endpoint != "" {
		data["S3_ENDPOINT"] = []byte(endpoint)
		if host := hostOf(endpoint); host != "" {
			data["BUCKET_HOST"] = []byte(host)
		}
	}
	if bucket != "" {
		data["BUCKET_NAME"] = []byte(bucket)
	}
	return data
}

func hostOf(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint
	}
	return u.Host
}
