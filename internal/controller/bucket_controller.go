package controller

import (
	"context"
	"encoding/json"
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

// BucketReconciler provisions RGW buckets and owns the bucket's S3 policy. It
// aggregates every BucketAccess that targets the bucket into a single policy
// document, so the policy stays convergent no matter the order of events.
type BucketReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Ceph     *ceph.Client
	Endpoint string
}

// +kubebuilder:rbac:groups=ceph.unkin.net,resources=buckets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ceph.unkin.net,resources=buckets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ceph.unkin.net,resources=buckets/finalizers,verbs=update

func (r *BucketReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var b v1alpha1.Bucket
	if err := r.Get(ctx, req.NamespacedName, &b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	bucketName := orDefault(b.Spec.BucketName, b.Name)

	if !b.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&b, finalizer) {
			if !b.Spec.RetainOnDelete {
				if err := r.Ceph.DeleteBucket(ctx, bucketName, b.Spec.PurgeOnDelete); err != nil {
					return r.fail(ctx, &b, "DeleteFailed", err)
				}
			}
			controllerutil.RemoveFinalizer(&b, finalizer)
			if err := r.Update(ctx, &b); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&b, finalizer) {
		if err := r.Update(ctx, &b); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Resolve the owning user.
	var owner v1alpha1.ObjectStoreUser
	if err := r.Get(ctx, types.NamespacedName{Namespace: b.Namespace, Name: b.Spec.OwnerRef}, &owner); err != nil {
		if apierrors.IsNotFound(err) {
			return r.pending(ctx, &b, "OwnerMissing", fmt.Sprintf("waiting for ObjectStoreUser %q", b.Spec.OwnerRef))
		}
		return r.fail(ctx, &b, "OwnerLookupFailed", err)
	}
	if owner.Status.UID == "" || owner.Status.Phase != "Ready" {
		return r.pending(ctx, &b, "OwnerNotReady", fmt.Sprintf("ObjectStoreUser %q not ready", b.Spec.OwnerRef))
	}
	ownerUID := owner.Status.UID

	// Ensure the bucket exists. Record adoption once: whether the RGW bucket
	// already existed the first time we reconciled this resource. status.BucketID
	// is only set on a successful reconcile, so a Pending wait on the owner (or a
	// transient failure) does not pollute the signal.
	firstObserve := b.Status.BucketID == ""
	info, err := r.Ceph.GetBucket(ctx, bucketName)
	if ceph.IsNotFound(err) {
		createSpec := ceph.CreateBucketSpec{
			Bucket:          bucketName,
			OwnerUID:        ownerUID,
			Zonegroup:       b.Spec.Zonegroup,
			PlacementTarget: b.Spec.PlacementTarget,
		}
		if ol := b.Spec.ObjectLock; ol != nil && ol.Enabled {
			createSpec.LockEnabled = true
			createSpec.LockMode = string(ol.Mode)
			createSpec.LockDays = ol.Days
			createSpec.LockYears = ol.Years
		}
		info, err = r.Ceph.CreateBucket(ctx, createSpec)
		if err != nil {
			return r.fail(ctx, &b, "CreateFailed", err)
		}
		logger.Info("created bucket", "bucket", bucketName, "owner", ownerUID)
		if firstObserve {
			b.Status.Adopted = false
		}
	} else if err != nil {
		return r.fail(ctx, &b, "LookupFailed", err)
	} else if firstObserve {
		b.Status.Adopted = true
		logger.Info("adopted existing bucket", "bucket", bucketName, "owner", ownerUID)
	}
	bucketID := info.InstanceID()

	// Placement is fixed at creation: RGW cannot move an existing bucket between
	// placement targets. If the live bucket sits on a different target than the
	// spec asks for (a changed spec, or an adopted bucket that predates the
	// request), surface a clear error instead of ever deleting/recreating it. An
	// empty PlacementTarget imposes no constraint.
	if pc := placementConflict(b.Spec.PlacementTarget, info.PlacementRule); pc {
		b.Status.PlacementTarget = info.PlacementRule
		return r.fail(ctx, &b, "PlacementImmutable", fmt.Errorf(
			"bucket %q is on placement target %q but spec requests %q; RGW cannot move a bucket between placement targets",
			bucketName, info.PlacementRule, b.Spec.PlacementTarget))
	}

	// Versioning (forced on when object lock is enabled).
	if b.Spec.Versioning || (b.Spec.ObjectLock != nil && b.Spec.ObjectLock.Enabled) {
		if err := r.Ceph.SetBucketVersioning(ctx, bucketName, bucketID, ownerUID, true); err != nil {
			return r.fail(ctx, &b, "VersioningFailed", err)
		}
	}

	// Tags.
	if len(b.Spec.Tags) > 0 {
		tj, err := ceph.BuildTagJSON(b.Spec.Tags)
		if err != nil {
			return r.fail(ctx, &b, "TagsFailed", err)
		}
		if tj != "" {
			if err := r.Ceph.SetBucketTags(ctx, bucketName, bucketID, ownerUID, tj); err != nil {
				return r.fail(ctx, &b, "TagsFailed", err)
			}
		}
	}

	// Bucket default quota (applied to the owner).
	if q := b.Spec.Quota; q != nil {
		if err := r.Ceph.SetUserQuota(ctx, ownerUID, "bucket", q.Enabled, q.MaxSizeBytes, q.MaxObjects); err != nil {
			return r.fail(ctx, &b, "QuotaFailed", err)
		}
	}

	// Render and apply the aggregate S3 policy from all BucketAccess grants,
	// unless the bucket opts out of policy management. The merge preserves any
	// statements the operator does not own, so an adopted bucket keeps its
	// existing policy.
	principals := 0
	if managePolicy(&b) {
		grants, p, err := r.collectGrants(ctx, b.Namespace, b.Name)
		if err != nil {
			return r.fail(ctx, &b, "GrantsFailed", err)
		}
		existing, err := r.Ceph.GetBucketPolicy(ctx, bucketName, ownerUID)
		if err != nil {
			return r.fail(ctx, &b, "PolicyReadFailed", err)
		}
		policy, err := ceph.MergeBucketPolicy(existing, bucketName, grants)
		if err != nil {
			return r.fail(ctx, &b, "PolicyBuildFailed", err)
		}
		if err := r.Ceph.SetBucketPolicy(ctx, bucketName, bucketID, ownerUID, policy); err != nil {
			return r.fail(ctx, &b, "PolicyFailed", err)
		}
		principals = p
	}

	b.Status.Phase = "Ready"
	b.Status.BucketName = bucketName
	b.Status.BucketID = bucketID
	b.Status.Owner = ownerUID
	b.Status.PlacementTarget = info.PlacementRule
	b.Status.PolicyPrincipals = int32(principals)
	b.Status.ObservedGeneration = b.Generation
	setReady(&b.Status.Conditions, b.Generation, true, "Provisioned", "bucket provisioned")
	if err := r.Status().Update(ctx, &b); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueSteady}, nil
}

// collectGrants returns the deduplicated set of grants for a bucket, drawn from
// every ready, non-deleting BucketAccess that references it, plus the count of
// distinct principals.
func (r *BucketReconciler) collectGrants(ctx context.Context, namespace, bucketRefName string) ([]ceph.Grant, int, error) {
	var list v1alpha1.BucketAccessList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, 0, err
	}
	seen := map[string]struct{}{}
	principals := map[string]struct{}{}
	var grants []ceph.Grant
	for i := range list.Items {
		ba := &list.Items[i]
		if ba.Spec.BucketRef != bucketRefName {
			continue
		}
		if !ba.DeletionTimestamp.IsZero() {
			continue
		}
		if ba.Status.UID == "" {
			continue
		}
		g := grantFromAccess(ba.Status.UID, ba)
		key := grantKey(g)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		principals[ba.Status.UID] = struct{}{}
		grants = append(grants, g)
	}
	return grants, len(principals), nil
}

// grantFromAccess translates a BucketAccess spec into the ceph grant model,
// carrying the fine-grained scoping (paths, actions, conditions, raw statements).
func grantFromAccess(uid string, ba *v1alpha1.BucketAccess) ceph.Grant {
	g := ceph.Grant{
		UID:     uid,
		Level:   string(ba.Spec.Level),
		Paths:   ba.Spec.Paths,
		Actions: ba.Spec.Actions,
	}
	if c := ba.Spec.Conditions; c != nil {
		g.Conditions = &ceph.GrantConditions{
			SourceIPs:           c.SourceIPs,
			SecureTransportOnly: c.SecureTransportOnly,
		}
	}
	for _, s := range ba.Spec.RawStatements {
		g.Raw = append(g.Raw, ceph.RawStatement{
			Sid:       s.Sid,
			Effect:    s.Effect,
			Actions:   s.Actions,
			Resources: s.Resources,
			Condition: s.Conditions,
		})
	}
	return g
}

// grantKey is a stable fingerprint of a grant used to collapse duplicate
// BucketAccess objects that would render identical policy statements.
func grantKey(g ceph.Grant) string {
	b, _ := json.Marshal(g)
	return string(b)
}

// managePolicy reports whether the operator should reconcile this bucket's S3
// policy. A nil ManagePolicy (the CRD default) is treated as true.
func managePolicy(b *v1alpha1.Bucket) bool {
	return b.Spec.ManagePolicy == nil || *b.Spec.ManagePolicy
}

// placementConflict reports whether a bucket's live placement target violates
// the spec. An empty spec placement imposes no constraint (the bucket may sit on
// whatever default it was created with). Otherwise the live placement must match
// exactly, since RGW cannot move a bucket between placement targets.
func placementConflict(specPlacement, livePlacement string) bool {
	if specPlacement == "" {
		return false
	}
	return specPlacement != livePlacement
}

func (r *BucketReconciler) pending(ctx context.Context, b *v1alpha1.Bucket, reason, msg string) (ctrl.Result, error) {
	b.Status.Phase = "Pending"
	b.Status.ObservedGeneration = b.Generation
	setReady(&b.Status.Conditions, b.Generation, false, reason, msg)
	if err := r.Status().Update(ctx, b); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueShort}, nil
}

func (r *BucketReconciler) fail(ctx context.Context, b *v1alpha1.Bucket, reason string, cause error) (ctrl.Result, error) {
	b.Status.Phase = "Error"
	b.Status.ObservedGeneration = b.Generation
	setReady(&b.Status.Conditions, b.Generation, false, reason, cause.Error())
	if err := r.Status().Update(ctx, b); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, cause
}

func (r *BucketReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Bucket{}).
		Watches(&v1alpha1.BucketAccess{}, handler.EnqueueRequestsFromMapFunc(r.bucketForAccess)).
		Watches(&v1alpha1.ObjectStoreUser{}, handler.EnqueueRequestsFromMapFunc(r.bucketsForOwner)).
		Complete(r)
}

// bucketForAccess maps a BucketAccess change to its referenced Bucket.
func (r *BucketReconciler) bucketForAccess(_ context.Context, obj client.Object) []reconcile.Request {
	ba, ok := obj.(*v1alpha1.BucketAccess)
	if !ok || ba.Spec.BucketRef == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ba.Namespace, Name: ba.Spec.BucketRef}}}
}

// bucketsForOwner maps an ObjectStoreUser change to every Bucket it owns.
func (r *BucketReconciler) bucketsForOwner(ctx context.Context, obj client.Object) []reconcile.Request {
	osu, ok := obj.(*v1alpha1.ObjectStoreUser)
	if !ok {
		return nil
	}
	var list v1alpha1.BucketList
	if err := r.List(ctx, &list, client.InNamespace(osu.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if list.Items[i].Spec.OwnerRef == osu.Name {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: list.Items[i].Namespace, Name: list.Items[i].Name,
			}})
		}
	}
	return reqs
}
