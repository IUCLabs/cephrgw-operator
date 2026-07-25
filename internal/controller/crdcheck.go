package controller

import (
	"context"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
)

// The CRD version check reads CustomResourceDefinitions to compare the
// installed schema against what this operator expects.
//
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list

// crdSentinel names a CRD and a spec property that only exists in the schema
// version shipped alongside this operator build. If the installed CRD lacks the
// sentinel, its schema predates this operator and strict decoding of new spec
// fields will silently fail. Keep this list in one place so new fields are easy
// to register as sentinels.
type crdSentinel struct {
	// crd is the metadata.name of the CustomResourceDefinition.
	crd string
	// specProperty is a key expected under
	// .spec.versions[].schema.openAPIV3Schema.properties.spec.properties.
	specProperty string
}

// crdSentinels is the authoritative list checked at startup. Extend it whenever
// a new spec field is added that older CRDs would reject.
var crdSentinels = []crdSentinel{
	{crd: "buckets.ceph.unkin.net", specProperty: "managePolicy"},
	{crd: "objectstoreusers.ceph.unkin.net", specProperty: "retainOnDelete"},
	{crd: "bucketaccesses.ceph.unkin.net", specProperty: "rawStatements"},
}

// CheckCRDVersions verifies that every CRD this operator owns is installed and
// carries the schema fields this build expects. It is advisory only: it logs a
// distinct WARNING per problem and never returns an error or exits, so a stale
// or missing CRD cannot block startup.
func CheckCRDVersions(ctx context.Context, cfg *rest.Config) {
	log := ctrl.Log.WithName("crd-version-check")

	client, err := apiextensionsclient.NewForConfig(cfg)
	if err != nil {
		log.Error(err, "unable to build apiextensions client; skipping CRD version check")
		return
	}

	for _, s := range crdSentinels {
		crd, err := client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, s.crd, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			log.Info("WARNING: CRD is not installed — apply the CRDs matching this operator version",
				"crd", s.crd)
			continue
		}
		if err != nil {
			log.Error(err, "unable to read CRD; cannot verify it matches this operator version",
				"crd", s.crd)
			continue
		}
		if !crdHasSpecProperty(crd, s.specProperty) {
			log.Info("WARNING: CRD is out of date — apply the CRDs matching this operator version",
				"crd", s.crd, "missingField", "spec."+s.specProperty)
		}
	}
}

// crdHasSpecProperty reports whether any served/stored version of the CRD
// declares the given property under spec.
func crdHasSpecProperty(crd *apiextensionsv1.CustomResourceDefinition, property string) bool {
	for _, v := range crd.Spec.Versions {
		schema := v.Schema
		if schema == nil || schema.OpenAPIV3Schema == nil {
			continue
		}
		specSchema, ok := schema.OpenAPIV3Schema.Properties["spec"]
		if !ok {
			continue
		}
		if _, ok := specSchema.Properties[property]; ok {
			return true
		}
	}
	return false
}
