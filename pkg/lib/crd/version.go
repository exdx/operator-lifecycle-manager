package crd

import (
	"fmt"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// V1Beta1 refers to the deprecated v1beta1 APIVersion of CRD objects
const V1Beta1Version string = "v1beta1"

// V1 refers to the new v1 APIVersion of CRD objects
const V1Version string = "v1"

var supportedCRDVersions = map[string]struct{}{
	V1Beta1Version: {},
	V1Version:      {},
}

// Version takes a CRD manifest and determines whether it is v1 or v1beta1 type based on the APIVersion.
func Version(manifest *string) (string, error) {
	if manifest == nil {
		return "", fmt.Errorf("empty CRD manifest")
	}

	dec := yaml.NewYAMLOrJSONDecoder(strings.NewReader(*manifest), 10)
	unst := &unstructured.Unstructured{}
	if err := dec.Decode(unst); err != nil {
		return "", err
	}

	v := unst.GroupVersionKind().Version
	if _, ok := supportedCRDVersions[v]; !ok {
		return "", fmt.Errorf("CRD APIVersion from manifest not supported: %s", v)
	}

	return v, nil
}

// RunStorageMigration determines whether the new CRD changes the storage version of the existing CRD.
// If true, OLM must run a migration process to ensure all CRs can be stored at the new version.
// See https://kubernetes.io/docs/tasks/access-kubernetes-api/custom-resources/custom-resource-definition-versioning/#upgrade-existing-objects-to-a-new-stored-version
func RunStorageMigration(oldCRD runtime.Object, newCRD runtime.Object) bool {
	newStoredVersions, oldStoredVersions := getStoredVersions(oldCRD, newCRD)

	for name := range oldStoredVersions {
		if _, ok := newStoredVersions[name]; ok {
			// new storage version exists in old CRD present on the cluster
			// no need to run migration
			return false
		}
	}
	// new storage version not present in old CRD
	// run storage migration operator
	return true
}

func getStoredVersions(oldCRD runtime.Object, newCRD runtime.Object) (newStoredVersions map[string]struct{}, oldStoredVersions map[string]struct{}) {
	oldStoredVersions = make(map[string]struct{})
	newStoredVersions = make(map[string]struct{})

	// find old storage versions by inspect the status field of the existing on-cluster CRD
	switch crd := oldCRD.(type) {
	case *apiextensionsv1.CustomResourceDefinition:
		for _, version := range crd.Status.StoredVersions {
			oldStoredVersions[version] = struct{}{}
		}
	case *apiextensionsv1beta1.CustomResourceDefinition:
		for _, version := range crd.Status.StoredVersions {
			oldStoredVersions[version] = struct{}{}
		}
	}

	switch crd := newCRD.(type) {
	case *apiextensionsv1.CustomResourceDefinition:
		for _, version := range crd.Spec.Versions {
			if version.Storage {
				newStoredVersions[version.Name] = struct{}{}
			}
		}
	case *apiextensionsv1beta1.CustomResourceDefinition:
		for _, version := range crd.Spec.Versions {
			if version.Storage {
				newStoredVersions[version.Name] = struct{}{}
			}
		}
	}

	return newStoredVersions, oldStoredVersions
}

// GetNewStorageVersion returns the storage version defined in the CRD.
// Only one version may be specified as the storage version.
func GetNewStorageVersion(crd runtime.Object) string {
	switch crd := crd.(type) {
	case *apiextensionsv1.CustomResourceDefinition:
		for _, version := range crd.Spec.Versions {
			if version.Storage {
				return version.Name
			}
		}
	case *apiextensionsv1beta1.CustomResourceDefinition:
		for _, version := range crd.Spec.Versions {
			if version.Storage {
				return version.Name
			}
		}
	}
	return ""
}