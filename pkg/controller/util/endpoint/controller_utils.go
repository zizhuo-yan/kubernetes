/*
Copyright 2019 The Kubernetes Authors.

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

package endpoint

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"sync"

	v1 "k8s.io/api/core/v1"
	discovery "k8s.io/api/discovery/v1beta1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	"k8s.io/kubernetes/pkg/apis/core/v1/helper"
	"k8s.io/kubernetes/pkg/util/hash"
	utilnet "k8s.io/utils/net"
)

// ServiceSelectorCache is a cache of service selectors to avoid high CPU consumption caused by frequent calls to AsSelectorPreValidated (see #73527)
type ServiceSelectorCache struct {
	selectorLock  sync.RWMutex
	selectorCache map[string]labels.Set

	serviceLabelsLock sync.RWMutex
	// service label cache, map[namespace]map[labelKey]map[serviceName]*ServiceSelector
	serviceLabelsCache map[string]map[string]map[string]*ServiceSelector
}

// ServiceSelector is a service label cache
type ServiceSelector struct {
	serviceName string
	serviceKey  string
	selectorSet labels.Set
}


// NewServiceSelectorCache init ServiceSelectorCache for both endpoint controller and endpointSlice controller.
func NewServiceSelectorCache() *ServiceSelectorCache {
	return &ServiceSelectorCache{
		selectorCache:      map[string]labels.Set{},
		serviceLabelsCache: map[string]map[string]map[string]*ServiceSelector{},
	}
}

// updateServiceLabelsCache can update or add a selector in ServiceSelectorCache.serviceLabelsCache while service's selector changed
func (sc *ServiceSelectorCache) updateServiceLabelsCache(key string, svc *v1.Service, oldSelector labels.Set) {
	sc.serviceLabelsLock.Lock()
	defer sc.serviceLabelsLock.Unlock()
	// update service label cache
	nsLabelsCache, ok := sc.serviceLabelsCache[svc.Namespace]
	if !ok {
		nsLabelsCache = map[string]map[string]*ServiceSelector{}
		// update namespace service label cache
		sc.serviceLabelsCache[svc.Namespace] = nsLabelsCache
	}

	oldkvMap := convertSelectorToLabelsKVMap(oldSelector)
	newkvMap := convertSelectorToLabelsKVMap(svc.Spec.Selector)
	serviceSelector := &ServiceSelector{
		serviceName: svc.Name,
		serviceKey:  key,
		selectorSet: labels.Set(svc.Spec.Selector),
	}
	// delete old label cache
	for labelKey := range oldkvMap {
		svcCache, exist := nsLabelsCache[labelKey]
		if !exist {
			continue
		}
		delete(svcCache, svc.Name)
		// clean empty map by key label=key
		if len(svcCache) == 0 {
			delete(nsLabelsCache, labelKey)
		}
	}
	// add/update label key.
	for labelKey := range newkvMap {
		svcCache, exist := nsLabelsCache[labelKey]
		if !exist {
			svcCache = map[string]*ServiceSelector{}
			nsLabelsCache[labelKey] = svcCache
		}
		svcCache[svc.Name] = serviceSelector
	}
}
// Update can update or add a selector in ServiceSelectorCache while service's selector changed.
func (sc *ServiceSelectorCache) Update(key string, svc *v1.Service) labels.Set {
	sc.selectorLock.Lock()
	defer sc.selectorLock.Unlock()
	selector := labels.Set(svc.Spec.Selector)

	// update selector cache by service key
	oldSelector, ok := sc.selectorCache[key]
	if ok && reflect.DeepEqual(selector, oldSelector) {
		return selector
	}

	// update service selector cache
	sc.selectorCache[key] = selector
	// update service label cache
	sc.updateServiceLabelsCache(key, svc, oldSelector)
	return selector
}

// deleteServiceLabelsCache can delete service from service labels cache.
func (sc *ServiceSelectorCache) deleteServiceLabelsCache(svc *v1.Service, selector labels.Set) {
	sc.serviceLabelsLock.Lock()
	defer sc.serviceLabelsLock.Unlock()
	// update label cache
	nsLabelCache := sc.serviceLabelsCache[svc.Namespace]
	for k, v := range selector {
		labelKey := k + "=" + v
		svcCache, ok := nsLabelCache[labelKey]
		if !ok {
			continue
		}
		delete(svcCache, svc.Name)
		// clean empty map by key labelKey
		if len(svcCache) == 0 {
			delete(nsLabelCache, labelKey)
		}
	}
}
// Delete can delete selector which exist in ServiceSelectorCache.
func (sc *ServiceSelectorCache) Delete(key string, svc *v1.Service) {
	sc.selectorLock.Lock()
	defer sc.selectorLock.Unlock()
	selector, ok := sc.selectorCache[key]
	if !ok {
		return
	}

	delete(sc.selectorCache, key)
	sc.deleteServiceLabelsCache(svc, selector)
}

// GetPodServiceMemberships returns a set of Service keys for Services that have
// a selector matching the given pod.
func (sc *ServiceSelectorCache) GetPodServiceMemberships(pod *v1.Pod) sets.String {
	sc.serviceLabelsLock.RLock()
	defer sc.serviceLabelsLock.RUnlock()
	set := sets.String{}
	labelCacheCount := map[*ServiceSelector]int{}
	nsLabelsCache, ok := sc.serviceLabelsCache[pod.Namespace]
	if !ok {
		return set
	}

	// get service keys from label cache by pod labels.
	for k, v := range pod.Labels {
		labelKey := k + "=" + v
		svcCache, ok := nsLabelsCache[labelKey]
		if !ok {
			continue
		}
		for _, svcSelector := range svcCache {
			labelCacheCount[svcSelector] ++
		}
	}
	for svcSelector, count := range labelCacheCount {
		if count == len(svcSelector.selectorSet) {
			set.Insert(svcSelector.serviceKey)
		}
	}
	return set
}

// PortMapKey is used to uniquely identify groups of endpoint ports.
type PortMapKey string

// NewPortMapKey generates a PortMapKey from endpoint ports.
func NewPortMapKey(endpointPorts []discovery.EndpointPort) PortMapKey {
	sort.Sort(portsInOrder(endpointPorts))
	return PortMapKey(DeepHashObjectToString(endpointPorts))
}

// DeepHashObjectToString creates a unique hash string from a go object.
func DeepHashObjectToString(objectToWrite interface{}) string {
	hasher := md5.New()
	hash.DeepHashObject(hasher, objectToWrite)
	return hex.EncodeToString(hasher.Sum(nil)[0:])
}

// ShouldPodBeInEndpoints returns true if a specified pod should be in an
// endpoints object. Terminating pods are only included if publishNotReady is true.
func ShouldPodBeInEndpoints(pod *v1.Pod, publishNotReady bool) bool {
	if len(pod.Status.PodIP) == 0 && len(pod.Status.PodIPs) == 0 {
		return false
	}

	if !publishNotReady && pod.DeletionTimestamp != nil {
		return false
	}

	if pod.Spec.RestartPolicy == v1.RestartPolicyNever {
		return pod.Status.Phase != v1.PodFailed && pod.Status.Phase != v1.PodSucceeded
	}

	if pod.Spec.RestartPolicy == v1.RestartPolicyOnFailure {
		return pod.Status.Phase != v1.PodSucceeded
	}

	return true
}

// ShouldSetHostname returns true if the Hostname attribute should be set on an
// Endpoints Address or EndpointSlice Endpoint.
func ShouldSetHostname(pod *v1.Pod, svc *v1.Service) bool {
	return len(pod.Spec.Hostname) > 0 && pod.Spec.Subdomain == svc.Name && svc.Namespace == pod.Namespace
}

// podEndpointsChanged returns two boolean values. The first is true if the pod has
// changed in a way that may change existing endpoints. The second value is true if the
// pod has changed in a way that may affect which Services it matches.
func podEndpointsChanged(oldPod, newPod *v1.Pod) (bool, bool) {
	// Check if the pod labels have changed, indicating a possible
	// change in the service membership
	labelsChanged := false
	if !reflect.DeepEqual(newPod.Labels, oldPod.Labels) ||
		!hostNameAndDomainAreEqual(newPod, oldPod) {
		labelsChanged = true
	}

	// If the pod's deletion timestamp is set, remove endpoint from ready address.
	if newPod.DeletionTimestamp != oldPod.DeletionTimestamp {
		return true, labelsChanged
	}
	// If the pod's readiness has changed, the associated endpoint address
	// will move from the unready endpoints set to the ready endpoints.
	// So for the purposes of an endpoint, a readiness change on a pod
	// means we have a changed pod.
	if podutil.IsPodReady(oldPod) != podutil.IsPodReady(newPod) {
		return true, labelsChanged
	}

	// Check if the pod IPs have changed
	if len(oldPod.Status.PodIPs) != len(newPod.Status.PodIPs) {
		return true, labelsChanged
	}
	for i := range oldPod.Status.PodIPs {
		if oldPod.Status.PodIPs[i].IP != newPod.Status.PodIPs[i].IP {
			return true, labelsChanged
		}
	}

	// Endpoints may also reference a pod's Name, Namespace, UID, and NodeName, but
	// the first three are immutable, and NodeName is immutable once initially set,
	// which happens before the pod gets an IP.

	return false, labelsChanged
}

// GetServicesToUpdateOnPodChange returns a set of Service keys for Services
// that have potentially been affected by a change to this pod.
func (sc *ServiceSelectorCache) GetServicesToUpdateOnPodChange(old, cur interface{}) sets.String {
	newPod := cur.(*v1.Pod)
	oldPod := old.(*v1.Pod)
	if newPod.ResourceVersion == oldPod.ResourceVersion {
		// Periodic resync will send update events for all known pods.
		// Two different versions of the same pod will always have different RVs
		return sets.String{}
	}

	podChanged, labelsChanged := podEndpointsChanged(oldPod, newPod)

	// If both the pod and labels are unchanged, no update is needed
	if !podChanged && !labelsChanged {
		return sets.String{}
	}

	services := sc.GetPodServiceMemberships(newPod)
	if labelsChanged {
		oldServices := sc.GetPodServiceMemberships(oldPod)
		services = determineNeededServiceUpdates(oldServices, services, podChanged)
	}

	return services
}

// GetPodFromDeleteAction returns a pointer to a pod if one can be derived from
// obj (could be a *v1.Pod, or a DeletionFinalStateUnknown marker item).
func GetPodFromDeleteAction(obj interface{}) *v1.Pod {
	if pod, ok := obj.(*v1.Pod); ok {
		// Enqueue all the services that the pod used to be a member of.
		// This is the same thing we do when we add a pod.
		return pod
	}
	// If we reached here it means the pod was deleted but its final state is unrecorded.
	tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
		return nil
	}
	pod, ok := tombstone.Obj.(*v1.Pod)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a Pod: %#v", obj))
		return nil
	}
	return pod
}

func hostNameAndDomainAreEqual(pod1, pod2 *v1.Pod) bool {
	return pod1.Spec.Hostname == pod2.Spec.Hostname &&
		pod1.Spec.Subdomain == pod2.Spec.Subdomain
}

func determineNeededServiceUpdates(oldServices, services sets.String, podChanged bool) sets.String {
	if podChanged {
		// if the labels and pod changed, all services need to be updated
		services = services.Union(oldServices)
	} else {
		// if only the labels changed, services not common to both the new
		// and old service set (the disjuntive union) need to be updated
		services = services.Difference(oldServices).Union(oldServices.Difference(services))
	}
	return services
}

// portsInOrder helps sort endpoint ports in a consistent way for hashing.
type portsInOrder []discovery.EndpointPort

func (sl portsInOrder) Len() int      { return len(sl) }
func (sl portsInOrder) Swap(i, j int) { sl[i], sl[j] = sl[j], sl[i] }
func (sl portsInOrder) Less(i, j int) bool {
	h1 := DeepHashObjectToString(sl[i])
	h2 := DeepHashObjectToString(sl[j])
	return h1 < h2
}

// IsIPv6Service checks if svc should have IPv6 endpoints
func IsIPv6Service(svc *v1.Service) bool {
	if helper.IsServiceIPSet(svc) {
		return utilnet.IsIPv6String(svc.Spec.ClusterIP)
	} else if svc.Spec.IPFamily != nil {
		return *svc.Spec.IPFamily == v1.IPv6Protocol
	} else {
		// FIXME: for legacy headless Services with no IPFamily, the current
		// thinking is that we should use the cluster default. Unfortunately
		// the endpoint controller doesn't know the cluster default. For now,
		// assume it's IPv4.
		return false
	}
}

// convertSelectorToLabelsKVMap() convert map from map[key]value to map[key=value]bool
func convertSelectorToLabelsKVMap(labelMap map[string]string) map[string]bool {
	kvMap := map[string]bool{}
	for k, v := range labelMap {
		kv := k + "=" + v
		kvMap[kv] = true
	}
	return kvMap
}
