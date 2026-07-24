package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/IUCLabs/cephrgw-operator/api/v1alpha1"
	"github.com/IUCLabs/cephrgw-operator/internal/ceph"
)

// ObjectStoreUserReconciler provisions RGW users and delivers their keys.
type ObjectStoreUserReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Ceph     *ceph.Client
	Endpoint string
}

// +kubebuilder:rbac:groups=ceph.unkin.net,resources=objectstoreusers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ceph.unkin.net,resources=objectstoreusers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ceph.unkin.net,resources=objectstoreusers/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

func (r *ObjectStoreUserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var osu v1alpha1.ObjectStoreUser
	if err := r.Get(ctx, req.NamespacedName, &osu); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	uid := orDefault(osu.Spec.UID, osu.Name)
	secretName := orDefault(osu.Spec.SecretName, osu.Name+"-rgw")

	if !osu.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&osu, finalizer) {
			if osu.Spec.RetainOnDelete {
				logger.Info("retaining RGW user on delete", "uid", uid)
			} else if err := r.Ceph.DeleteUser(ctx, uid); err != nil {
				return r.fail(ctx, &osu, "DeleteFailed", err)
			}
			controllerutil.RemoveFinalizer(&osu, finalizer)
			if err := r.Update(ctx, &osu); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&osu, finalizer) {
		if err := r.Update(ctx, &osu); err != nil {
			return ctrl.Result{}, err
		}
	}

	spec := ceph.UserSpec{
		UID:         uid,
		DisplayName: osu.Spec.DisplayName,
		Email:       osu.Spec.Email,
		MaxBuckets:  osu.Spec.MaxBuckets,
		Suspended:   osu.Spec.Suspended,
	}

	// Record adoption once: whether the RGW user already existed the first time
	// we reconciled this resource (taken over rather than created). status.UID is
	// only set on a successful reconcile, so it is a clean "never provisioned"
	// signal that transient failures do not pollute.
	firstObserve := osu.Status.UID == ""
	_, getErr := r.Ceph.GetUser(ctx, uid)
	switch {
	case ceph.IsNotFound(getErr):
		if _, err := r.Ceph.CreateUser(ctx, spec); err != nil {
			return r.fail(ctx, &osu, "CreateFailed", err)
		}
		logger.Info("created RGW user", "uid", uid)
		if firstObserve {
			osu.Status.Adopted = false
		}
	case getErr != nil:
		return r.fail(ctx, &osu, "LookupFailed", getErr)
	default:
		if _, err := r.Ceph.UpdateUser(ctx, spec); err != nil {
			return r.fail(ctx, &osu, "UpdateFailed", err)
		}
		if firstObserve {
			osu.Status.Adopted = true
			logger.Info("adopted existing RGW user", "uid", uid)
		}
	}

	if q := osu.Spec.Quota; q != nil {
		if err := r.Ceph.SetUserQuota(ctx, uid, "user", q.Enabled, q.MaxSizeBytes, q.MaxObjects); err != nil {
			return r.fail(ctx, &osu, "QuotaFailed", err)
		}
	}

	user, err := r.Ceph.GetUser(ctx, uid)
	if err != nil {
		return r.fail(ctx, &osu, "LookupFailed", err)
	}
	key, ok := user.S3Key()
	if !ok {
		return r.fail(ctx, &osu, "NoKeys", fmt.Errorf("user %s has no S3 keys", uid))
	}

	if err := upsertSecret(ctx, r.Client, r.Scheme, &osu, secretName, osu.Namespace,
		credentialSecretData(key, uid, r.Endpoint, "")); err != nil {
		return r.fail(ctx, &osu, "SecretFailed", err)
	}

	osu.Status.Phase = "Ready"
	osu.Status.UID = uid
	osu.Status.SecretName = secretName
	osu.Status.ObservedGeneration = osu.Generation
	setReady(&osu.Status.Conditions, osu.Generation, true, "Provisioned", "RGW user provisioned")
	if err := r.Status().Update(ctx, &osu); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueSteady}, nil
}

func (r *ObjectStoreUserReconciler) fail(ctx context.Context, osu *v1alpha1.ObjectStoreUser, reason string, cause error) (ctrl.Result, error) {
	osu.Status.Phase = "Error"
	osu.Status.ObservedGeneration = osu.Generation
	setReady(&osu.Status.Conditions, osu.Generation, false, reason, cause.Error())
	if err := r.Status().Update(ctx, osu); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, cause
}

func (r *ObjectStoreUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ObjectStoreUser{}).
		Complete(r)
}
