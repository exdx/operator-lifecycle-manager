package reconciler

import (
	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/controller/registry/grpc"
)

type GrpcAddressRegistryReconciler struct {
	now nowFunc
}

var _ RegistryEnsurer = &GrpcAddressRegistryReconciler{}
var _ RegistryChecker = &GrpcAddressRegistryReconciler{}
var _ RegistryReconciler = &GrpcAddressRegistryReconciler{}

// EnsureRegistryServer ensures a registry server exists for the given CatalogSource.
func (g *GrpcAddressRegistryReconciler) EnsureRegistryServer(catalogSource *v1alpha1.CatalogSource) error {
	now := g.now()
	catalogSource.Status.RegistryServiceStatus = &v1alpha1.RegistryServiceStatus{
		CreatedAt: now,
		Protocol:  "grpc",
	}

	return nil
}

// CheckRegistryServer returns true if the given CatalogSource is considered healthy; false otherwise.
func (g *GrpcAddressRegistryReconciler) CheckRegistryServer(catalogSource *v1alpha1.CatalogSource, store *grpc.SourceStore) (healthy bool, err error) {
	return healthCheck(catalogSource, store)

}
