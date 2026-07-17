package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/IUCLabs/cephrgw-operator/api/v1alpha1"
	"github.com/IUCLabs/cephrgw-operator/internal/ceph"
)

// BucketAccessReconciler ensures the principal for a grant exists (creating a
// dedicated RGW user when none is referenced) and delivers its keys. The bucket
// policy itself is owned and rendered by the Bucket controller, which watches
// BucketAccess objects.
type BucketAccessReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Ceph     *ceph.Client
	Endpoint string
}

// +kubebuilder:rbac:groups=ceph.unkin.net,resources=bucketaccesses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ceph.unkin.net,resources=bucketaccesses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ceph.unkin.net,resources=bucketaccesses/finalizers,verbs=update

func (r *BucketAccessReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ba v1alpha1.BucketAccess
	if err := r.Get(ctx, req.NamespacedName, &ba); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	managed := ba.Spec.UserRef == ""
	uid, err := r.resolveUID(ctx, &ba)
	if err != nil {
		return r.fail(ctx, &ba, "ResolveFailed", err)
	}

	if !ba.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&ba, finalizer) {
			// Only delete a user the operator created for this grant.
			if managed && uid != "" {
				if err := r.Ceph.DeleteUser(ctx, uid); err != nil {
					return r.fail(ctx, &ba, "DeleteFailed", err)
				}
			}
			controllerutil.RemoveFinalizer(&ba, finalizer)
			if err := r.Update(ctx, &ba); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&ba, finalizer) {
		if err := r.Update(ctx, &ba); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Resolve the referenced Bucket so we can label the credential Secret and
	// gate the grant on the bucket existing.
	var bucket v1alpha1.Bucket
	if err := r.Get(ctx, types.NamespacedName{Namespace: ba.Namespace, Name: ba.Spec.BucketRef}, &bucket); err != nil {
		if apierrors.IsNotFound(err) {
			return r.pending(ctx, &ba, "BucketMissing", fmt.Sprintf("waiting for Bucket %q", ba.Spec.BucketRef))
		}
		return r.fail(ctx, &ba, "BucketLookupFailed", err)
	}
	bucketName := orDefault(bucket.Status.BucketName, orDefault(bucket.Spec.BucketName, bucket.Name))

	secretName := ba.Status.SecretName
	if managed {
		if uid == "" {
			uid = fmt.Sprintf("%s-%s", ba.Spec.BucketRef, ba.Name)
		}
		secretName = orDefault(ba.Spec.SecretName, ba.Name+"-rgw")

		user, err := r.ensureUser(ctx, uid)
		if err != nil {
			return r.fail(ctx, &ba, "UserFailed", err)
		}
		key, ok := user.S3Key()
		if !ok {
			return r.fail(ctx, &ba, "NoKeys", fmt.Errorf("user %s has no S3 keys", uid))
		}
		if err := upsertSecret(ctx, r.Client, r.Scheme, &ba, secretName, ba.Namespace,
			credentialSecretData(key, uid, r.Endpoint, bucketName)); err != nil {
			return r.fail(ctx, &ba, "SecretFailed", err)
		}
	}

	ba.Status.Phase = "Ready"
	ba.Status.UID = uid
	ba.Status.SecretName = secretName
	ba.Status.Bound = true
	ba.Status.ObservedGeneration = ba.Generation
	setReady(&ba.Status.Conditions, ba.Generation, true, "Granted",
		fmt.Sprintf("%s access for %s applied to bucket %s", ba.Spec.Level, uid, bucketName))
	if err := r.Status().Update(ctx, &ba); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("bucket access reconciled", "bucket", bucketName, "uid", uid, "level", ba.Spec.Level)
	return ctrl.Result{RequeueAfter: requeueSteady}, nil
}

// resolveUID returns the RGW uid this grant targets: the referenced
// ObjectStoreUser's provisioned uid, or the managed uid derived from the spec.
func (r *BucketAccessReconciler) resolveUID(ctx context.Context, ba *v1alpha1.BucketAccess) (string, error) {
	if ba.Spec.UserRef == "" {
		if ba.Spec.UID != "" {
			return ba.Spec.UID, nil
		}
		// Derived lazily in Reconcile once we know it is not a deletion no-op.
		return "", nil
	}
	var osu v1alpha1.ObjectStoreUser
	if err := r.Get(ctx, types.NamespacedName{Namespace: ba.Namespace, Name: ba.Spec.UserRef}, &osu); err != nil {
		return "", err
	}
	if osu.Status.UID == "" {
		return "", fmt.Errorf("ObjectStoreUser %q not ready", ba.Spec.UserRef)
	}
	return osu.Status.UID, nil
}

func (r *BucketAccessReconciler) ensureUser(ctx context.Context, uid string) (*ceph.User, error) {
	if _, err := r.Ceph.GetUser(ctx, uid); ceph.IsNotFound(err) {
		if _, err := r.Ceph.CreateUser(ctx, ceph.UserSpec{UID: uid, DisplayName: uid}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return r.Ceph.GetUser(ctx, uid)
}

func (r *BucketAccessReconciler) pending(ctx context.Context, ba *v1alpha1.BucketAccess, reason, msg string) (ctrl.Result, error) {
	ba.Status.Phase = "Pending"
	ba.Status.Bound = false
	ba.Status.ObservedGeneration = ba.Generation
	setReady(&ba.Status.Conditions, ba.Generation, false, reason, msg)
	if err := r.Status().Update(ctx, ba); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueShort}, nil
}

func (r *BucketAccessReconciler) fail(ctx context.Context, ba *v1alpha1.BucketAccess, reason string, cause error) (ctrl.Result, error) {
	ba.Status.Phase = "Error"
	ba.Status.Bound = false
	ba.Status.ObservedGeneration = ba.Generation
	setReady(&ba.Status.Conditions, ba.Generation, false, reason, cause.Error())
	if err := r.Status().Update(ctx, ba); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, cause
}

func (r *BucketAccessReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.BucketAccess{}).
		Watches(&v1alpha1.ObjectStoreUser{}, handler.EnqueueRequestsFromMapFunc(r.accessForUser)).
		Complete(r)
}

// accessForUser maps an ObjectStoreUser change to every BucketAccess that
// references it, so a grant binds as soon as its user becomes ready.
func (r *BucketAccessReconciler) accessForUser(ctx context.Context, obj client.Object) []reconcile.Request {
	osu, ok := obj.(*v1alpha1.ObjectStoreUser)
	if !ok {
		return nil
	}
	var list v1alpha1.BucketAccessList
	if err := r.List(ctx, &list, client.InNamespace(osu.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if list.Items[i].Spec.UserRef == osu.Name {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: list.Items[i].Namespace, Name: list.Items[i].Name,
			}})
		}
	}
	return reqs
}
