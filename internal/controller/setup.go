package controller

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/IUCLabs/cephrgw-operator/internal/ceph"
)

// SetupAll registers every controller with the manager.
func SetupAll(mgr ctrl.Manager, cephClient *ceph.Client, endpoint string) error {
	if err := (&ObjectStoreUserReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Ceph:     cephClient,
		Endpoint: endpoint,
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&BucketReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Ceph:     cephClient,
		Endpoint: endpoint,
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&BucketAccessReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Ceph:     cephClient,
		Endpoint: endpoint,
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	return nil
}
