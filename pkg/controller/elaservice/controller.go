/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package elaservice

import (
	"fmt"
	"log"
	"time"

	"github.com/golang/glog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	"github.com/google/elafros/pkg/apis/ela/v1alpha1"
	clientset "github.com/google/elafros/pkg/client/clientset/versioned"
	elascheme "github.com/google/elafros/pkg/client/clientset/versioned/scheme"
	informers "github.com/google/elafros/pkg/client/informers/externalversions"
	listers "github.com/google/elafros/pkg/client/listers/ela/v1alpha1"
	"github.com/google/elafros/pkg/controller"
	"github.com/google/elafros/pkg/controller/util"
)

var serviceKind = v1alpha1.SchemeGroupVersion.WithKind("ElaService")

const (
	controllerAgentName = "elaservice-controller"

	// SuccessSynced is used as part of the Event 'reason' when a Foo is synced
	SuccessSynced = "Synced"
	// ErrResourceExists is used as part of the Event 'reason' when a Foo fails
	// to sync due to a Deployment of the same name already existing.
	ErrResourceExists = "ErrResourceExists"

	// MessageResourceSynced is the message used for an Event fired when a Foo
	// is synced successfully
	MessageResourceSynced = "ElaService synced successfully"
)

// RevisionRoute represents a single target to route to.
// Basically represents a k8s service representing a specific Revision
// and how much of the traffic goes to it.
type RevisionRoute struct {
	Service string
	Weight  int
}

// +controller:group=ela,version=v1alpha1,kind=ElaService,resource=elaservices
type ElaServiceControllerImpl struct {
	// kubeclientset is a standard kubernetes clientset
	kubeclientset  kubernetes.Interface
	elaclientset clientset.Interface

	// lister indexes properties about RevisionTemplate
	lister listers.ElaServiceLister
	synced cache.InformerSynced

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	workqueue workqueue.RateLimitingInterface
	// recorder is an event recorder for recording Event resources to the
	// Kubernetes API.
	recorder record.EventRecorder
}

// Init initializes the controller and is called by the generated code
// Registers eventhandlers to enqueue events
// config - client configuration for talking to the apiserver
// si - informer factory shared across all controllers for listening to events and indexing resource properties
// reconcileKey - function for mapping queue keys to resource names
//TODO(vaikas): somewhat generic (generic behavior)
func NewController(
	kubeclientset kubernetes.Interface,
	elaclientset clientset.Interface,
	kubeInformerFactory kubeinformers.SharedInformerFactory,
	elaInformerFactory informers.SharedInformerFactory,
	config *rest.Config) controller.Interface {

	log.Printf("ElaService controller Init")

	// obtain a reference to a shared index informer for the ElaServices type.
	informer := elaInformerFactory.Elafros().V1alpha1().ElaServices()

	// Create event broadcaster
	// Add ela types to the default Kubernetes Scheme so Events can be
	// logged for ela types.
	elascheme.AddToScheme(scheme.Scheme)
	glog.V(4).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})

	controller := &ElaServiceControllerImpl{
		kubeclientset:  kubeclientset,
		elaclientset: elaclientset,
		lister:         informer.Lister(),
		synced:         informer.Informer().HasSynced,
		workqueue:      workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "ElaServices"),
		recorder:       recorder,
	}

	glog.Info("Setting up event handlers")
	// Set up an event handler for when RevisionTemplate resources change
	informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueElaService,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueElaService(new)
		},
	})

	return controller

}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
//TODO(grantr): generic
func (c *ElaServiceControllerImpl) Run(threadiness int, stopCh <-chan struct{}) error {
	defer runtime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	glog.Info("Starting ElaService controller")

	// Wait for the caches to be synced before starting workers
	glog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.synced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	glog.Info("Starting workers")
	// Launch two workers to process Foo resources
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	glog.Info("Started workers")
	<-stopCh
	glog.Info("Shutting down workers")

	return nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
//TODO(grantr): generic
func (c *ElaServiceControllerImpl) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
//TODO(grantr): generic
func (c *ElaServiceControllerImpl) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			c.workqueue.Forget(obj)
			runtime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// Foo resource to be synced.
		if err := c.syncHandler(key); err != nil {
			return fmt.Errorf("error syncing '%s': %s", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(obj)
		glog.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		runtime.HandleError(err)
		return true
	}

	return true
}

// enqueueElaService takes a ElaService resource and
// converts it into a namespace/name string which is then put onto the work
// queue. This method should *not* be passed resources of any type other than
// ElaService.
//TODO(grantr): generic
func (c *ElaServiceControllerImpl) enqueueElaService(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		runtime.HandleError(err)
		return
	}
	c.workqueue.AddRateLimited(key)
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the Foo resource
// with the current status of the resource.
//TODO(grantr): not generic
func (c *ElaServiceControllerImpl) syncHandler(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	// Get the ElaService resource with this namespace/name
	es, err := c.lister.ElaServices(namespace).Get(name)
	if err != nil {
		// The resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			runtime.HandleError(fmt.Errorf("elaservice '%s' in work queue no longer exists", key))
			return nil
		}

		return err
	}

	glog.Infof("Running reconcile ElaService for %s\n%+v\n", es.Name, es)

	// Create a placeholder service that is simply used by istio as a placeholder.
	// This service could eventually be the 'router' service that will get all the
	// fallthrough traffic if there are no route rules (revisions to target).
	// This is one way to implement the 0->1. For now, we'll just create a placeholder
	// that selects nothing.
	log.Printf("Creating/Updating placeholder k8s services")
	err = c.createPlaceholderService(es, namespace)
	if err != nil {
		return err
	}

	// Then create the Ingress rule for this service
	log.Printf("Creating or updating ingress rule")
	err = c.createOrUpdateIngress(es, namespace)
	if err != nil {
		if !apierrs.IsAlreadyExists(err) {
			log.Printf("Failed to create ingress rule: %s", err)
			return err
		}
	}

	// Then create the actual route rules.
	log.Printf("Creating istio route rules")
	err = c.createOrUpdateRoutes(es, namespace)
	if err != nil {
		log.Printf("Failed to create Routes: %s", err)
		return err
	}

	c.recorder.Event(es, corev1.EventTypeNormal, SuccessSynced, MessageResourceSynced)
	return nil
}

func (c *ElaServiceControllerImpl) createPlaceholderService(u *v1alpha1.ElaService, ns string) error {
	service := MakeElaServiceK8SService(u)
	serviceRef := metav1.NewControllerRef(u, serviceKind)
	service.OwnerReferences = append(service.OwnerReferences, *serviceRef)

	sc := c.kubeclientset.Core().Services(ns)
	_, err := sc.Create(service)
	if err != nil {
		if !apierrs.IsAlreadyExists(err) {
			log.Printf("Failed to create service: %s", err)
			return err
		}
	}
	log.Printf("Created service: %q", service.Name)
	return nil
}

func (c *ElaServiceControllerImpl) createOrUpdateIngress(es *v1alpha1.ElaService, ns string) error {
	ingressName := util.GetElaK8SIngressName(es)

	ic := c.kubeclientset.Extensions().Ingresses(ns)

	// Check to see if we need to create or update
	ingress := MakeElaServiceIngress(es, ns)
	serviceRef := metav1.NewControllerRef(es, serviceKind)
	ingress.OwnerReferences = append(ingress.OwnerReferences, *serviceRef)

	_, err := ic.Get(ingressName, metav1.GetOptions{})
	if err != nil {
		if !apierrs.IsNotFound(err) {
			return err
		}
		_, createErr := ic.Create(ingress)
		log.Printf("Created ingress %q", ingress.Name)
		return createErr
	}
	return nil
}

func (c *ElaServiceControllerImpl) getRoutes(u *v1alpha1.ElaService) ([]RevisionRoute, error) {
	log.Printf("Figuring out routes for ElaService: %s", u.Name)
	ret := []RevisionRoute{}
	for _, tt := range u.Spec.Rollout.Traffic {
		rr, err := c.getRouteForTrafficTarget(tt, u.Namespace)
		if err != nil {
			log.Printf("Failed to get a route for target %+v : %q", tt, err)
			return nil, err
		}
		ret = append(ret, rr)
	}
	return ret, nil
}

func (c *ElaServiceControllerImpl) getRouteForTrafficTarget(tt v1alpha1.TrafficTarget, ns string) (RevisionRoute, error) {
	elaNS := util.GetElaNamespaceName(ns)
	// If template specified, fetch last revision otherwise use Revision
	revisionName := tt.Revision
	if tt.RevisionTemplate != "" {
		rtClient := c.elaclientset.ElafrosV1alpha1().RevisionTemplates(ns)
		rt, err := rtClient.Get(tt.RevisionTemplate, metav1.GetOptions{})
		if err != nil {
			return RevisionRoute{}, err
		}
		revisionName = rt.Status.Latest
	}
	prClient := c.elaclientset.ElafrosV1alpha1().Revisions(ns)
	rev, err := prClient.Get(revisionName, metav1.GetOptions{})
	if err != nil {
		log.Printf("Failed to fetch Revision: %s : %s", revisionName, err)
		return RevisionRoute{}, err
	}
	return RevisionRoute{Service: fmt.Sprintf("%s.%s", rev.Status.ServiceName, elaNS), Weight: tt.Percent}, nil
}

func (c *ElaServiceControllerImpl) createOrUpdateRoutes(u *v1alpha1.ElaService, ns string) error {
	// grab a client that's specific to RouteRule.
	routeClient := c.elaclientset.ConfigV1alpha2().RouteRules(ns)
	if routeClient == nil {
		log.Printf("Failed to create resource client")
		return fmt.Errorf("Couldn't get a routeClient")
	}

	routes, err := c.getRoutes(u)
	if err != nil {
		log.Printf("Failed to get routes for %s : %q", u.Name, err)
		return err
	}
	if len(routes) == 0 {
		log.Printf("No routes were found for the service %q", u.Name)
		return nil
	}
	for _, r := range routes {
		log.Printf("Adding a route to %q Weight: %d", r.Service, r.Weight)
	}

	routeRuleName := util.GetElaIstioRouteRuleName(u)
	routeRules, err := routeClient.Get(routeRuleName, metav1.GetOptions{})
	if err != nil {
		if !apierrs.IsNotFound(err) {
			return err
		}
		routeRules = MakeElaServiceIstioRoutes(u, ns, routes)
		_, createErr := routeClient.Create(routeRules)
		return createErr
	}

	routeRules.Spec = MakeElaServiceIstioSpec(u, ns, routes)
	_, err = routeClient.Update(routeRules)
	if err != nil {
		return err
	}
	return nil
}
