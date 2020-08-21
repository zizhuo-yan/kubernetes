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
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestDetermineNeededServiceUpdates(t *testing.T) {
	testCases := []struct {
		name  string
		a     sets.String
		b     sets.String
		union sets.String
		xor   sets.String
	}{
		{
			name:  "no services changed",
			a:     sets.NewString("a", "b", "c"),
			b:     sets.NewString("a", "b", "c"),
			xor:   sets.NewString(),
			union: sets.NewString("a", "b", "c"),
		},
		{
			name:  "all old services removed, new services added",
			a:     sets.NewString("a", "b", "c"),
			b:     sets.NewString("d", "e", "f"),
			xor:   sets.NewString("a", "b", "c", "d", "e", "f"),
			union: sets.NewString("a", "b", "c", "d", "e", "f"),
		},
		{
			name:  "all old services removed, no new services added",
			a:     sets.NewString("a", "b", "c"),
			b:     sets.NewString(),
			xor:   sets.NewString("a", "b", "c"),
			union: sets.NewString("a", "b", "c"),
		},
		{
			name:  "no old services, but new services added",
			a:     sets.NewString(),
			b:     sets.NewString("a", "b", "c"),
			xor:   sets.NewString("a", "b", "c"),
			union: sets.NewString("a", "b", "c"),
		},
		{
			name:  "one service removed, one service added, two unchanged",
			a:     sets.NewString("a", "b", "c"),
			b:     sets.NewString("b", "c", "d"),
			xor:   sets.NewString("a", "d"),
			union: sets.NewString("a", "b", "c", "d"),
		},
		{
			name:  "no services",
			a:     sets.NewString(),
			b:     sets.NewString(),
			xor:   sets.NewString(),
			union: sets.NewString(),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			retval := determineNeededServiceUpdates(testCase.a, testCase.b, false)
			if !retval.Equal(testCase.xor) {
				t.Errorf("%s (with podChanged=false): expected: %v  got: %v", testCase.name, testCase.xor.List(), retval.List())
			}

			retval = determineNeededServiceUpdates(testCase.a, testCase.b, true)
			if !retval.Equal(testCase.union) {
				t.Errorf("%s (with podChanged=true): expected: %v  got: %v", testCase.name, testCase.union.List(), retval.List())
			}
		})
	}
}

// There are 3*5 possibilities(3 types of RestartPolicy by 5 types of PodPhase).
// Not listing them all here. Just listing all of the 3 false cases and 3 of the
// 12 true cases.
func TestShouldPodBeInEndpoints(t *testing.T) {
	testCases := []struct {
		name            string
		pod             *v1.Pod
		publishNotReady bool
		expected        bool
	}{
		// Pod should not be in endpoints:
		{
			name: "Failed pod with Never RestartPolicy",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					RestartPolicy: v1.RestartPolicyNever,
				},
				Status: v1.PodStatus{
					Phase: v1.PodFailed,
					PodIP: "1.2.3.4",
				},
			},
			expected: false,
		},
		{
			name: "Succeeded pod with Never RestartPolicy",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					RestartPolicy: v1.RestartPolicyNever,
				},
				Status: v1.PodStatus{
					Phase: v1.PodSucceeded,
					PodIP: "1.2.3.4",
				},
			},
			expected: false,
		},
		{
			name: "Succeeded pod with OnFailure RestartPolicy",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					RestartPolicy: v1.RestartPolicyOnFailure,
				},
				Status: v1.PodStatus{
					Phase: v1.PodSucceeded,
					PodIP: "1.2.3.4",
				},
			},
			expected: false,
		},
		{
			name: "Empty Pod IPs, Running pod with OnFailure RestartPolicy",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					RestartPolicy: v1.RestartPolicyNever,
				},
				Status: v1.PodStatus{
					Phase:  v1.PodRunning,
					PodIP:  "",
					PodIPs: []v1.PodIP{},
				},
			},
			expected: false,
		},
		{
			name: "Terminating Pod",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: v1.PodSpec{},
				Status: v1.PodStatus{
					Phase: v1.PodRunning,
					PodIP: "1.2.3.4",
				},
			},
			publishNotReady: false,
			expected:        false,
		},
		// Pod should be in endpoints:
		{
			name: "Failed pod with Always RestartPolicy",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					RestartPolicy: v1.RestartPolicyAlways,
				},
				Status: v1.PodStatus{
					Phase: v1.PodFailed,
					PodIP: "1.2.3.4",
				},
			},
			expected: true,
		},
		{
			name: "Pending pod with Never RestartPolicy",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					RestartPolicy: v1.RestartPolicyNever,
				},
				Status: v1.PodStatus{
					Phase: v1.PodPending,
					PodIP: "1.2.3.4",
				},
			},
			expected: true,
		},
		{
			name: "Unknown pod with OnFailure RestartPolicy",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					RestartPolicy: v1.RestartPolicyOnFailure,
				},
				Status: v1.PodStatus{
					Phase: v1.PodUnknown,
					PodIP: "1.2.3.4",
				},
			},
			expected: true,
		},
		{
			name: "Running pod with Never RestartPolicy",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					RestartPolicy: v1.RestartPolicyNever,
				},
				Status: v1.PodStatus{
					Phase: v1.PodRunning,
					PodIP: "1.2.3.4",
				},
			},
			expected: true,
		},
		{
			name: "Multiple Pod IPs, Running pod with OnFailure RestartPolicy",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					RestartPolicy: v1.RestartPolicyNever,
				},
				Status: v1.PodStatus{
					Phase:  v1.PodRunning,
					PodIPs: []v1.PodIP{{IP: "1.2.3.4"}, {IP: "1234::5678:0000:0000:9abc:def0"}},
				},
			},
			expected: true,
		},
		{
			name: "Terminating Pod with publish not ready",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: v1.PodSpec{},
				Status: v1.PodStatus{
					Phase: v1.PodRunning,
					PodIP: "1.2.3.4",
				},
			},
			publishNotReady: true,
			expected:        true,
		},
	}

	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			result := ShouldPodBeInEndpoints(test.pod, test.publishNotReady)
			if result != test.expected {
				t.Errorf("expected: %t, got: %t", test.expected, result)
			}
		})
	}
}

func TestShouldSetHostname(t *testing.T) {
	testCases := map[string]struct {
		pod      *v1.Pod
		service  *v1.Service
		expected bool
	}{
		"all matching": {
			pod:      genSimplePod("ns", "foo", "svc-name"),
			service:  genSimpleSvc("ns", "svc-name"),
			expected: true,
		},
		"all matching, hostname not set": {
			pod:      genSimplePod("ns", "", "svc-name"),
			service:  genSimpleSvc("ns", "svc-name"),
			expected: false,
		},
		"all set, different name/subdomain": {
			pod:      genSimplePod("ns", "hostname", "subdomain"),
			service:  genSimpleSvc("ns", "name"),
			expected: false,
		},
		"all set, different namespace": {
			pod:      genSimplePod("ns1", "hostname", "svc-name"),
			service:  genSimpleSvc("ns2", "svc-name"),
			expected: false,
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			result := ShouldSetHostname(testCase.pod, testCase.service)
			if result != testCase.expected {
				t.Errorf("expected: %t, got: %t", testCase.expected, result)
			}
		})
	}
}

func genSimplePod(namespace, hostname, subdomain string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
		},
		Spec: v1.PodSpec{
			Hostname:  hostname,
			Subdomain: subdomain,
		},
	}
}

func genSimpleSvc(namespace, name string) *v1.Service {
	return &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

func Test_podChanged(t *testing.T) {
	testCases := []struct {
		testName      string
		modifier      func(*v1.Pod, *v1.Pod)
		podChanged    bool
		labelsChanged bool
	}{
		{
			testName:      "no changes",
			modifier:      func(old, new *v1.Pod) {},
			podChanged:    false,
			labelsChanged: false,
		}, {
			testName: "change NodeName",
			modifier: func(old, new *v1.Pod) {
				new.Spec.NodeName = "changed"
			},
			// NodeName can only change before the pod has an IP, and we don't care about the
			// pod yet at that point so we ignore this change
			podChanged:    false,
			labelsChanged: false,
		}, {
			testName: "change ResourceVersion",
			modifier: func(old, new *v1.Pod) {
				new.ObjectMeta.ResourceVersion = "changed"
			},
			// ResourceVersion is intentionally ignored if nothing else changed
			podChanged:    false,
			labelsChanged: false,
		}, {
			testName: "add primary IPv4",
			modifier: func(old, new *v1.Pod) {
				new.Status.PodIP = "1.2.3.4"
				new.Status.PodIPs = []v1.PodIP{{IP: "1.2.3.4"}}
			},
			podChanged:    true,
			labelsChanged: false,
		}, {
			testName: "modify primary IPv4",
			modifier: func(old, new *v1.Pod) {
				old.Status.PodIP = "1.2.3.4"
				old.Status.PodIPs = []v1.PodIP{{IP: "1.2.3.4"}}
				new.Status.PodIP = "2.3.4.5"
				new.Status.PodIPs = []v1.PodIP{{IP: "2.3.4.5"}}
			},
			podChanged:    true,
			labelsChanged: false,
		}, {
			testName: "add primary IPv6",
			modifier: func(old, new *v1.Pod) {
				new.Status.PodIP = "fd00:10:96::1"
				new.Status.PodIPs = []v1.PodIP{{IP: "fd00:10:96::1"}}
			},
			podChanged:    true,
			labelsChanged: false,
		}, {
			testName: "modify primary IPv6",
			modifier: func(old, new *v1.Pod) {
				old.Status.PodIP = "fd00:10:96::1"
				old.Status.PodIPs = []v1.PodIP{{IP: "fd00:10:96::1"}}
				new.Status.PodIP = "fd00:10:96::2"
				new.Status.PodIPs = []v1.PodIP{{IP: "fd00:10:96::2"}}
			},
			podChanged:    true,
			labelsChanged: false,
		}, {
			testName: "add secondary IP",
			modifier: func(old, new *v1.Pod) {
				old.Status.PodIP = "1.2.3.4"
				old.Status.PodIPs = []v1.PodIP{{IP: "1.2.3.4"}}
				new.Status.PodIP = "1.2.3.4"
				new.Status.PodIPs = []v1.PodIP{{IP: "1.2.3.4"}, {IP: "fd00:10:96::1"}}
			},
			podChanged:    true,
			labelsChanged: false,
		}, {
			testName: "modify secondary IP",
			modifier: func(old, new *v1.Pod) {
				old.Status.PodIP = "1.2.3.4"
				old.Status.PodIPs = []v1.PodIP{{IP: "1.2.3.4"}, {IP: "fd00:10:96::1"}}
				new.Status.PodIP = "1.2.3.4"
				new.Status.PodIPs = []v1.PodIP{{IP: "1.2.3.4"}, {IP: "fd00:10:96::2"}}
			},
			podChanged:    true,
			labelsChanged: false,
		}, {
			testName: "remove secondary IP",
			modifier: func(old, new *v1.Pod) {
				old.Status.PodIP = "1.2.3.4"
				old.Status.PodIPs = []v1.PodIP{{IP: "1.2.3.4"}, {IP: "fd00:10:96::1"}}
				new.Status.PodIP = "1.2.3.4"
				new.Status.PodIPs = []v1.PodIP{{IP: "1.2.3.4"}}
			},
			podChanged:    true,
			labelsChanged: false,
		}, {
			testName: "change readiness",
			modifier: func(old, new *v1.Pod) {
				new.Status.Conditions[0].Status = v1.ConditionTrue
			},
			podChanged:    true,
			labelsChanged: false,
		}, {
			testName: "mark for deletion",
			modifier: func(old, new *v1.Pod) {
				now := metav1.NewTime(time.Now().UTC())
				new.ObjectMeta.DeletionTimestamp = &now
			},
			podChanged:    true,
			labelsChanged: false,
		}, {
			testName: "add label",
			modifier: func(old, new *v1.Pod) {
				new.Labels["label"] = "new"
			},
			podChanged:    false,
			labelsChanged: true,
		}, {
			testName: "modify label",
			modifier: func(old, new *v1.Pod) {
				old.Labels["label"] = "old"
				new.Labels["label"] = "new"
			},
			podChanged:    false,
			labelsChanged: true,
		}, {
			testName: "remove label",
			modifier: func(old, new *v1.Pod) {
				old.Labels["label"] = "old"
			},
			podChanged:    false,
			labelsChanged: true,
		},
	}

	orig := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "test",
			Name:      "pod",
			Labels:    map[string]string{"foo": "bar"},
		},
		Status: v1.PodStatus{
			Conditions: []v1.PodCondition{
				{Type: v1.PodReady, Status: v1.ConditionFalse},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.testName, func(t *testing.T) {
			old := orig.DeepCopy()
			new := old.DeepCopy()
			tc.modifier(old, new)

			podChanged, labelsChanged := podEndpointsChanged(old, new)
			if podChanged != tc.podChanged {
				t.Errorf("Expected podChanged to be %t, got %t", tc.podChanged, podChanged)
			}
			if labelsChanged != tc.labelsChanged {
				t.Errorf("Expected labelsChanged to be %t, got %t", tc.labelsChanged, labelsChanged)
			}
		})
	}
}

func TestServiceSelectorCache_GetPodServiceMemberships(t *testing.T) {
	cache := NewServiceSelectorCache()
	for i := 0; i < 3; i++ {
		service := newService(fmt.Sprintf("service-%d", i), "test", map[string]string{"app": fmt.Sprintf("test-%d", i)})
		cache.Update(fmt.Sprintf("%s/%s", service.Namespace, service.Name), service)
	}
	var pods []*v1.Pod
	for i := 0; i < 5; i++ {
		pod := newPod(fmt.Sprintf("test-pod-%d", i), "test", map[string]string{
			"app":   fmt.Sprintf("test-%d", i),
			"label": fmt.Sprintf("label-%d", i),
		})
		pods = append(pods, pod)
	}
	tests := []struct {
		name   string
		pod    *v1.Pod
		expect sets.String
	}{
		{
			name:   "get servicesMemberships for pod-0",
			pod:    pods[0],
			expect: sets.NewString("test/service-0"),
		},
		{
			name:   "get servicesMemberships for pod-1",
			pod:    pods[1],
			expect: sets.NewString("test/service-1"),
		},
		{
			name:   "get servicesMemberships for pod-2",
			pod:    pods[2],
			expect: sets.NewString("test/service-2"),
		},
		{
			name:   "get servicesMemberships for pod-3",
			pod:    pods[3],
			expect: sets.NewString(),
		},
		{
			name:   "get servicesMemberships for pod-4",
			pod:    pods[4],
			expect: sets.NewString(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			services := cache.GetPodServiceMemberships(test.pod)
			if !services.Equal(test.expect) {
				t.Errorf("Expect service %v, but got %v", test.expect, services)
			}
		})
	}
}

func TestServiceSelectorCache_Update(t *testing.T) {
	var selectors []labels.Set
	for i := 0; i < 5; i++ {
		selector := labels.Set(map[string]string{"app": fmt.Sprintf("test-%d", i)})
		selectors = append(selectors, selector)
	}
	tests := []struct {
		name             string
		key              string
		cache            *ServiceSelectorCache
		updatedSVC       *v1.Service
		expect           labels.Set
		expectLabelCache map[string]map[string]*ServiceSelector
	}{
		{
			name:       "add test/service-0",
			key:        "test/service-0",
			cache:      generateServiceSelectorCache(map[string]labels.Set{}),
			updatedSVC: newService("service-0", "test", map[string]string{"app": "test-0"}),
			expect:     selectors[0],
		},
		{
			name:       "add test/service-1",
			key:        "test/service-1",
			cache:      generateServiceSelectorCache(map[string]labels.Set{"test/service-0": selectors[0]}),
			updatedSVC: newService("service-1", "test", map[string]string{"app": "test-1"}),
			expect:     selectors[1],
		},
		{
			name:       "update test/service-2",
			key:        "test/service-2",
			cache:      generateServiceSelectorCache(map[string]labels.Set{"test/service-2": selectors[2]}),
			updatedSVC: newService("service-2", "test", map[string]string{"app": "test-0"}),
			expect:     selectors[0],
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selector := test.cache.Update(test.key, test.updatedSVC)
			if !reflect.DeepEqual(selector, test.expect) {
				t.Errorf("Expect selector %v , but got %v", test.expect, selector)
			}
		})
	}
}

func TestServiceSelectorCache_UpdateServiceLabelsCache(t *testing.T) {
	t.Logf("add test/service-0")
	testKey := "test/service-0"
	cache := generateServiceSelectorCache(map[string]labels.Set{})
	svc1 := newService("service-1", "test", map[string]string{"app": "test-1"})
	svc2 := newService("service-1", "test", map[string]string{"app": "test-1", "app2": "test-2"})
	svc3 := newService("service-1", "test", map[string]string{"app": "test-x", "app2": "test-2"})
	svc4 := newService("service-1", "test", map[string]string{"app2": "test-2"})
	// add labels app=test-1
	cache.Update(testKey, svc1)
	nsCache, ok := cache.serviceLabelsCache["test"]
	if !ok {
		t.Fatalf("unexpected service labels cache:%v", cache.serviceLabelsCache)
	}
	if len(nsCache) != 1 || nsCache["app=test-1"] == nil {
		t.Fatalf("unexpected service labels cache:%v", cache.serviceLabelsCache)
	}
	t.Logf("update svc1, add new label, serviceCache: %v", cache.serviceLabelsCache)
	// add labels app2=test-2
	cache.Update(testKey, svc2)
	if !ok {
		t.Fatalf("unexpected service labels cache:%v", cache.serviceLabelsCache)
	}
	if len(nsCache) != 2 || nsCache["app=test-1"] == nil || nsCache["app2=test-2"] == nil {
		t.Fatalf("unexpected service labels cache:%v", cache.serviceLabelsCache)
	}
	t.Logf("update svc2, add new label, serviceCache: %v", cache.serviceLabelsCache)
	// update labels app=test-x
	cache.Update(testKey, svc3)
	if !ok {
		t.Fatalf("unexpected service labels cache:%v", cache.serviceLabelsCache)
	}
	if len(nsCache["app=test-1"]) != 0 || len(nsCache["app2=test-2"]) != 1 || len(nsCache["app=test-x"]) != 1 {
		t.Fatalf("unexpected service labels cache:%v", cache.serviceLabelsCache)
	}
	t.Logf("update svc3, update service label value, serviceCache: %v", cache.serviceLabelsCache)
	// delete labels app=test-x
	cache.Update(testKey, svc4)
	if !ok {
		t.Fatalf("unexpected service labels cache:%v", cache.serviceLabelsCache)
	}
	if len(nsCache["app=test-1"]) != 0 || len(nsCache["app2=test-2"]) != 1 {
		t.Fatalf("unexpected service labels cache:%v", cache.serviceLabelsCache)
	}
	t.Logf("update svc4, delete label, serviceCache: %v", cache.serviceLabelsCache)
	// delete svc.
	cache.Delete(testKey, svc4)
	if len(nsCache["app=test-1"]) != 0 || len(nsCache["app2=test-2"]) != 0 || len(nsCache["app=test-x"]) != 0 || len(cache.selectorCache) != 0 {
		t.Fatalf("unexpected service labels cache:%v", cache.serviceLabelsCache)
	}
	t.Logf("delete svc4, serviceCache: %v, selector cache: %v", cache.serviceLabelsCache, cache.selectorCache)
}

func newService(name, ns string, selector map[string]string) *v1.Service {
	return &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: v1.ServiceSpec{
			Selector: selector,
		},
	}
}

func newPod(name, ns string, labels map[string]string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			Labels:    labels,
		},
	}
}

func generateServiceSelectorCache(cache map[string]labels.Set) *ServiceSelectorCache {
	return &ServiceSelectorCache{
		selectorCache:      cache,
		serviceLabelsCache: map[string]map[string]map[string]*ServiceSelector{},
	}
}

func BenchmarkGetPodServiceMemberships(b *testing.B) {
	// init service cache.
	cache := NewServiceSelectorCache()
	for i := 0; i < 1000; i++ {
		service := newService(fmt.Sprintf("service-%d", i), "test", map[string]string{"app": fmt.Sprintf("test-%d", i)})
		cache.Update(fmt.Sprintf("%s/%s", service.Namespace, service.Name), service)
	}

	pod := newPod("test-pod-0", "test", map[string]string{"app": "test-0"})
	expect := sets.NewString("test/service-0")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		services := cache.GetPodServiceMemberships(pod)
		if len(services) != len(expect) {
			b.Errorf("Expect services size %d, but got: %v", len(expect), len(services))
		}
	}
}

type TestCase struct {
	Pod    *v1.Pod
	Expect sets.String
}

// Scenario I: each service have only one matched pod.
// TestCase I: update 1 pod
// TestCase II: update 100 pods
func BenchmarkGetPodServiceMemberships_1VS1_TestCaseI(b *testing.B) {
	// init service cache.
	cache := NewServiceSelectorCache()
	for i := 0; i < 10000; i++ {
		service := newService(fmt.Sprintf("service-%d", i), "test", map[string]string{"app": fmt.Sprintf("test-%d", i)})
		cache.Update(fmt.Sprintf("%s/%s", service.Namespace, service.Name), service)
	}
	getPod := func() (*v1.Pod, sets.String) {
		pod := newPod("test-pod-0", "test", map[string]string{"app": "test-0"})
		expect := sets.NewString("test-pod-0")
		return pod, expect
	}
	pod, _ := getPod()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.GetPodServiceMemberships(pod)
	}
}

func BenchmarkGetPodServiceMemberships_1VS1_TestCaseII(b *testing.B) {
	// init service cache.
	cache := NewServiceSelectorCache()
	for i := 0; i < 10000; i++ {
		service := newService(fmt.Sprintf("service-%d", i), "test", map[string]string{"app": fmt.Sprintf("test-%d", i)})
		cache.Update(fmt.Sprintf("%s/%s", service.Namespace, service.Name), service)
	}
	getPod := func() (*v1.Pod, sets.String) {
		rand.Seed(time.Now().UnixNano())
		i := rand.Intn(10000)
		pod := newPod(fmt.Sprintf("test-pod-%d", i), "test", map[string]string{
			"app": fmt.Sprintf("test-%d", i),
		})
		expect := sets.NewString(fmt.Sprintf("test/service-%d", i))
		return pod, expect
	}
	tcs := []TestCase{}
	for i := 0; i < 100; i++ {
		pod, expect := getPod()
		tc := TestCase{Pod: pod, Expect: expect}
		tcs = append(tcs, tc)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 100; j++ {
			cache.GetPodServiceMemberships(tcs[j].Pod)
		}
	}
}

// Scenario I: all of the service matched the same pod.
// TestCase I: update 1 pod
// TestCase II: update 100 pods
func BenchmarkGetPodServiceMemberships_NVS1_TestCaseI(b *testing.B) {
	// init service cache.
	cache := NewServiceSelectorCache()
	expect := sets.NewString()
	for i := 0; i < 10000; i++ {
		service := newService(fmt.Sprintf("service-%d", i), "test", map[string]string{"app": "test-0"})
		key := fmt.Sprintf("%s/%s", service.Namespace, service.Name)
		expect.Insert(key)
		cache.Update(key, service)
	}
	pod := newPod("test-pod-0", "test", map[string]string{"app": "test-0"})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.GetPodServiceMemberships(pod)
	}
}

func BenchmarkGetPodServiceMemberships_NVS1_TestCaseII(b *testing.B) {
	// init service cache.
	cache := NewServiceSelectorCache()
	expect := sets.NewString()
	for i := 0; i < 10000; i++ {
		service := newService(fmt.Sprintf("service-%d", i), "test", map[string]string{"app": "test-0"})
		key := fmt.Sprintf("%s/%s", service.Namespace, service.Name)
		expect.Insert(key)
		cache.Update(key, service)
	}
	getPod := func() (*v1.Pod, sets.String) {
		rand.Seed(time.Now().UnixNano())
		i := rand.Intn(10000)
		pod := newPod(fmt.Sprintf("test-pod-%d", i), "test", map[string]string{
			"app": "test-0",
		})
		expect := sets.NewString()
		return pod, expect
	}
	tcs := []TestCase{}
	for i := 0; i < 100; i++ {
		pod, _ := getPod()
		tc := TestCase{Pod: pod, Expect: expect}
		tcs = append(tcs, tc)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 100; j++ {
			cache.GetPodServiceMemberships(tcs[j].Pod)
		}
	}
}
