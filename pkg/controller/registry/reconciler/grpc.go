package reconciler

import (
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/operator-framework/operator-lifecycle-manager/pkg/api/apis/operators/v1alpha1"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/lib/operatorclient"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/lib/operatorlister"
	"github.com/operator-framework/operator-lifecycle-manager/pkg/lib/ownerutil"
)

// grpcCatalogSourceDecorator wraps CatalogSource to add additional methods
type grpcCatalogSourceDecorator struct {
	*v1alpha1.CatalogSource
}

func (s *grpcCatalogSourceDecorator) Selector() labels.Selector {
	return labels.SelectorFromValidatedSet(map[string]string{
		CatalogSourceLabelKey: s.GetName(),
	})
}

func (s *grpcCatalogSourceDecorator) Labels() map[string]string {
	return map[string]string{
		CatalogSourceLabelKey: s.GetName(),
	}
}

func (s *grpcCatalogSourceDecorator) Service() *v1.Service {
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.GetName(),
			Namespace: s.GetNamespace(),
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:       "grpc",
					Port:       50051,
					TargetPort: intstr.FromInt(50051),
				},
			},
			Selector: s.Labels(),
		},
	}
	ownerutil.AddOwner(svc, s.CatalogSource, false, false)
	return svc
}

func (s *grpcCatalogSourceDecorator) Pod() *v1.Pod {
	pod := Pod(s.CatalogSource, "registry-server", s.Spec.Image, s.Labels(), 5, 10)
	ownerutil.AddOwner(pod, s.CatalogSource, false, false)
	return pod
}

type GrpcRegistryReconciler struct {
	now      nowFunc
	Lister   operatorlister.OperatorLister
	OpClient operatorclient.ClientInterface
}

var _ RegistryReconciler = &GrpcRegistryReconciler{}

func (c *GrpcRegistryReconciler) currentService(source grpcCatalogSourceDecorator) *v1.Service {
	serviceName := source.Service().GetName()
	service, err := c.Lister.CoreV1().ServiceLister().Services(source.GetNamespace()).Get(serviceName)
	if err != nil {
		logrus.WithField("service", serviceName).Warn("couldn't find service in cache")
		return nil
	}
	return service
}

func (c *GrpcRegistryReconciler) currentPods(source grpcCatalogSourceDecorator) []*v1.Pod {
	pods, err := c.Lister.CoreV1().PodLister().Pods(source.GetNamespace()).List(source.Selector())
	if err != nil {
		logrus.WithError(err).Warn("couldn't find pod in cache")
		return nil
	}
	if len(pods) > 1 {
		logrus.WithField("selector", source.Selector()).Warn("multiple pods found for selector")
	}
	return pods
}

func (c *GrpcRegistryReconciler) currentPodsWithCorrectImage(source grpcCatalogSourceDecorator) []*v1.Pod {
	pods, err := c.Lister.CoreV1().PodLister().Pods(source.GetNamespace()).List(labels.SelectorFromValidatedSet(source.Labels()))
	if err != nil {
		logrus.WithError(err).Warn("couldn't find pod in cache")
		return nil
	}
	found := []*v1.Pod{}
	for _, p := range pods {
		if p.Spec.Containers[0].Image == source.Spec.Image {
			found = append(found, p)
		}
	}
	return found
}

// EnsureRegistryServer ensures that all components of registry server are up to date.
func (c *GrpcRegistryReconciler) EnsureRegistryServer(catalogSource *v1alpha1.CatalogSource) error {
	source := grpcCatalogSourceDecorator{catalogSource}

	// if service status is nil, we force create every object to ensure they're created the first time
	overwrite := source.Status.RegistryServiceStatus == nil
	// recreate the pod if no existing pod is serving the latest image
	overwritePod := overwrite || len(c.currentPodsWithCorrectImage(source)) == 0 || c.updateRegistryPodByDigest(source)

	//TODO: if any of these error out, we should write a status back (possibly set RegistryServiceStatus to nil so they get recreated)
	if err := c.ensurePod(source, overwritePod); err != nil {
		return errors.Wrapf(err, "error ensuring pod: %s", source.Pod().GetName())
	}
	if err := c.ensureService(source, overwrite); err != nil {
		return errors.Wrapf(err, "error ensuring service: %s", source.Service().GetName())
	}

	if overwritePod {
		now := c.now()
		catalogSource.Status.RegistryServiceStatus = &v1alpha1.RegistryServiceStatus{
			CreatedAt:        now,
			Protocol:         "grpc",
			ServiceName:      source.Service().GetName(),
			ServiceNamespace: source.GetNamespace(),
			Port:             fmt.Sprintf("%d", source.Service().Spec.Ports[0].Port),
		}
	}
	return nil
}

func (c *GrpcRegistryReconciler) ensurePod(source grpcCatalogSourceDecorator, overwrite bool) error {
	currentPods := c.currentPods(source)
	if len(currentPods) > 0 {
		if !overwrite {
			return nil
		}
		for _, p := range currentPods {
			if err := c.OpClient.KubernetesInterface().CoreV1().Pods(source.GetNamespace()).Delete(p.GetName(), metav1.NewDeleteOptions(0)); err != nil {
				return errors.Wrapf(err, "error deleting old pod: %s", p.GetName())
			}
		}
	}
	_, err := c.OpClient.KubernetesInterface().CoreV1().Pods(source.GetNamespace()).Create(source.Pod())
	if err == nil {
		return nil
	}
	return errors.Wrapf(err, "error creating new pod: %s", source.Pod().GetGenerateName())
}

func (c *GrpcRegistryReconciler) ensureService(source grpcCatalogSourceDecorator, overwrite bool) error {
	service := source.Service()
	if c.currentService(source) != nil {
		if !overwrite {
			return nil
		}
		if err := c.OpClient.DeleteService(service.GetNamespace(), service.GetName(), metav1.NewDeleteOptions(0)); err != nil {
			return err
		}
	}
	_, err := c.OpClient.CreateService(service)
	return err
}

// updateRegistryPodByDigest is an internal method that verifies the latest catalog source by comparing image digests.
// If the digests do not match (i.e. there is a new version of the catalog source) then overwrite the catalog source pod.
// This is done to only have the container runtime (CRI-O) talk to the container registry.
func (c *GrpcRegistryReconciler) updateRegistryPodByDigest(source grpcCatalogSourceDecorator) bool {
	podDigestChan := make(chan string, 1)
	lastCheckedTimestamp := time.Time{}
	interval := source.Spec.Poll.Interval.Duration

	// check poll value is not zero (default poll value)
	// if polling interval is zero polling will not be done
	if interval == time.Duration(0) {
		return false
	}

	// check digest every poll interval
	if time.Now().After(lastCheckedTimestamp.Add(interval)) &&
		source.CreationTimestamp.Add(interval).Before(time.Now()) {
		lastCheckedTimestamp = time.Now()
		go c.getPodDigest(source, podDigestChan)
	}

	select {
	case newCatalogSourceImageDigest := <-podDigestChan:
		if newCatalogSourceImageDigest == source.Pod().Status.ContainerStatuses[0].ImageID {
			return false
		}
		// we have a new imageID in our catalog source container: a new version of the catalog source image exists
		// update catalog source pod
		return true
	default:
		return false
	}
}

// getPodDigest is an internal method that creates a pod using the latest catalog source.
// Once the pod comes up, it puts the container image digest onto a channel.
func (c *GrpcRegistryReconciler) getPodDigest(source grpcCatalogSourceDecorator, podChan chan string) {
	// remove label from pod to ensure service does accidentally route traffic to the pod
	p := source.Pod()
	p.Labels[CatalogSourceLabelKey] = ""

	pod, err := c.OpClient.KubernetesInterface().CoreV1().Pods(source.GetNamespace()).Create(p)
	if err != nil {
		logrus.WithField("pod", "catalog-source-pod").Warn("couldn't create new catalog source pod")
		return
	}

	var newCatalogSourceImage string
	// check pod to get container details
	// we are only interested in the container image id, to see if it matches the old version
	for i := 0; i < 10; i++ {
		newCatalogSourceImage = pod.Status.ContainerStatuses[0].ImageID
		if newCatalogSourceImage == "" {
			time.Sleep(10 * time.Second)
			continue
		} else {
			break
		}
	}

	if newCatalogSourceImage == "" {
		logrus.WithField("pod", pod.Name).Warn("couldn't run catalog source pod")
		return
	}


	logrus.WithField("pod", pod.Spec.Containers[0].Image).Info(fmt.Sprintf("found new image digest %s",
		pod.Status.ContainerStatuses[0].ImageID))

	podChan <- newCatalogSourceImage
}

// CheckRegistryServer returns true if the given CatalogSource is considered healthy; false otherwise.
func (c *GrpcRegistryReconciler) CheckRegistryServer(catalogSource *v1alpha1.CatalogSource) (healthy bool, err error) {
	source := grpcCatalogSourceDecorator{catalogSource}

	// Check on registry resources
	// TODO: add gRPC health check
	if len(c.currentPodsWithCorrectImage(source)) < 1 ||
		c.currentService(source) == nil {
		healthy = false
		return
	}

	healthy = true
	return
}
