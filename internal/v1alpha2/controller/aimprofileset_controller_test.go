// MIT License
//
// Copyright (c) 2025 Advanced Micro Devices, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package controller

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	aimv1alpha1 "github.com/amd-enterprise-ai/aim-engine/api/v1alpha1"
	aimv1alpha2 "github.com/amd-enterprise-ai/aim-engine/api/v1alpha2"
)

// TestDiscoveryCatalogConfigMapPredicate_PassesOnRemoval guards against the
// pre-fix behaviour where an update that removed all profile keys from a
// catalog ConfigMap was silently dropped by the watch predicate. The
// owning AIMProfileSet would then keep stale derived AIMProfiles around
// indefinitely. The predicate must fire when either the old or new
// snapshot looks like a discovery catalog so the prune path runs.
func TestDiscoveryCatalogConfigMapPredicate_PassesOnRemoval(t *testing.T) {
	catalog := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "ns"},
		Data: map[string]string{
			"profiles.bf16.yaml": "kind: AIMProfile\n",
		},
	}
	empty := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "catalog", Namespace: "ns"},
		Data:       map[string]string{},
	}

	p := discoveryCatalogConfigMapPredicate()
	if !p.Update(event.UpdateEvent{ObjectOld: catalog, ObjectNew: empty}) {
		t.Fatal("predicate must pass an update where profile keys were removed (old=catalog, new=empty)")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: empty, ObjectNew: catalog}) {
		t.Fatal("predicate must pass an update where profile keys were added (old=empty, new=catalog)")
	}
}

// TestDiscoveryCatalogConfigMapPredicate_DropsUnrelated ensures the
// predicate stays narrowly scoped: a ConfigMap with no profile keys on
// either side must NOT trigger a reconcile, keeping the operator from
// fanning out events for every unrelated ConfigMap update in the cluster.
func TestDiscoveryCatalogConfigMapPredicate_DropsUnrelated(t *testing.T) {
	noProfileKeys := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns"},
		Data:       map[string]string{"app.yaml": "foo: bar"},
	}
	p := discoveryCatalogConfigMapPredicate()
	if p.Create(event.CreateEvent{Object: noProfileKeys}) {
		t.Error("predicate must not fire on Create for unrelated ConfigMaps")
	}
	if p.Update(event.UpdateEvent{ObjectOld: noProfileKeys, ObjectNew: noProfileKeys}) {
		t.Error("predicate must not fire on Update when neither side is a catalog")
	}
	if p.Delete(event.DeleteEvent{Object: noProfileKeys}) {
		t.Error("predicate must not fire on Delete for unrelated ConfigMaps")
	}
}

func TestProfileSourceRefIndexValueSeparatesKindsAndScopes(t *testing.T) {
	t.Parallel()

	configMap := profileSourceRefIndexValue(aimv1alpha1.ProfileSourceKindConfigMap, "team-a", "source")
	namespaceModel := profileSourceRefIndexValue(aimv1alpha1.ProfileSourceKindAIMModel, "team-a", "source")
	clusterModel := profileSourceRefIndexValue(aimv1alpha1.ProfileSourceKindAIMClusterModel, "", "source")

	if configMap == namespaceModel || configMap == clusterModel || namespaceModel == clusterModel {
		t.Fatalf(
			"source indexes collide: ConfigMap=%q AIMModel=%q AIMClusterModel=%q",
			configMap,
			namespaceModel,
			clusterModel,
		)
	}
}

func TestModelSourceRefIndexValue(t *testing.T) {
	t.Parallel()

	namespaceModel := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "team-a"},
	}
	if got, want := modelSourceRefIndexValue(namespaceModel),
		profileSourceRefIndexValue(aimv1alpha1.ProfileSourceKindAIMModel, "team-a", "source"); got != want {
		t.Fatalf("namespace model index = %q, want %q", got, want)
	}

	clusterModel := &aimv1alpha2.AIMClusterModel{
		ObjectMeta: metav1.ObjectMeta{Name: "source"},
	}
	if got, want := modelSourceRefIndexValue(clusterModel),
		profileSourceRefIndexValue(aimv1alpha1.ProfileSourceKindAIMClusterModel, "", "source"); got != want {
		t.Fatalf("cluster model index = %q, want %q", got, want)
	}
}

func TestModelSourceRefIndexValuesForCache(t *testing.T) {
	t.Parallel()

	controller := true
	cache := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "source-discovery",
			Namespace: "team-a",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "AIMModel", Name: "namespace-source", Controller: &controller},
				{Kind: "AIMClusterModel", Name: "cluster-source", Controller: &controller},
				{Kind: "Deployment", Name: "ignored", Controller: &controller},
			},
		},
	}
	got := modelSourceRefIndexValuesForCache(cache)
	want := []string{
		profileSourceRefIndexValue(aimv1alpha1.ProfileSourceKindAIMModel, "team-a", "namespace-source"),
		profileSourceRefIndexValue(aimv1alpha1.ProfileSourceKindAIMClusterModel, "", "cluster-source"),
	}
	for _, value := range want {
		if !slices.Contains(got, value) {
			t.Fatalf("cache model source indexes = %v, missing %q", got, value)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("cache model source indexes = %v, want %v", got, want)
	}
}
