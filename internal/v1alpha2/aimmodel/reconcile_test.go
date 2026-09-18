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

package aimmodel

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aimv1alpha1 "github.com/amd-enterprise-ai/aim-engine/api/v1alpha1"
	aimv1alpha2 "github.com/amd-enterprise-ai/aim-engine/api/v1alpha2"
	"github.com/amd-enterprise-ai/aim-engine/internal/constants"
	controllerutils "github.com/amd-enterprise-ai/aim-engine/internal/controller/utils"
	"github.com/amd-enterprise-ai/aim-engine/internal/utils"
	"github.com/amd-enterprise-ai/aim-engine/internal/v1alpha2/aimprofile"
	"github.com/amd-enterprise-ai/aim-engine/internal/v1alpha2/profileyaml"
)

var errListNodesFailed = errors.New("list nodes failed")

const testBaseImageRef = "ghcr.io/silogen/aim-base:0.8.5"

func TestFetchRemoteState_ReadsChildProfileSetAfterDerivationIsRemoved(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := aimv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "transitioning", Namespace: "team-a", UID: "model-uid"},
	}
	childName, err := childProfileSetName(model.Name)
	if err != nil {
		t.Fatalf("childProfileSetName() error = %v", err)
	}
	child := &aimv1alpha2.AIMProfileSet{
		ObjectMeta: metav1.ObjectMeta{Name: childName, Namespace: model.Namespace},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(child).
		WithIndex(&aimv1alpha2.AIMProfile{}, ManagedModelUIDIndexKey, func(obj client.Object) []string {
			if uid := obj.GetAnnotations()[annotationModelUID]; uid != "" {
				return []string{uid}
			}
			return nil
		}).
		Build()

	fetch := (&ModelReconciler{}).FetchRemoteState(
		context.Background(),
		fakeClient,
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
	)
	if fetch.childProfileSet.Error != nil || fetch.childProfileSet.Value == nil {
		t.Fatalf("child fetch = %#v, want existing child even without active derivation", fetch.childProfileSet)
	}
	if fetch.childProfileSet.Value.Name != childName {
		t.Fatalf("child name = %q, want %q", fetch.childProfileSet.Value.Name, childName)
	}
}

func TestHasLegacyFields_DetectsLegacyCompatibilityFields(t *testing.T) {
	t.Parallel()

	spec := aimv1alpha1.AIMModelSpec{
		Image:                  "quay.io/amd/aim-qwen:0.10.0",
		RuntimeConfigRef:       aimv1alpha1.RuntimeConfigRef{Name: "fast"},
		DefaultServiceTemplate: "default-template",
		Discovery:              &aimv1alpha1.AIMModelDiscoveryConfig{ExtractMetadata: ptr.To(true)},
		ModelSources: []aimv1alpha1.AIMModelSource{{
			ModelID:   "org/model",
			SourceURI: "hf://org/model",
		}},
		ImageMetadata: &aimv1alpha1.ImageMetadata{
			Model: &aimv1alpha1.ModelMetadata{CanonicalName: "org/model"},
		},
	}

	if !spec.HasLegacyFields() {
		t.Fatalf("HasLegacyFields() = false, want true for spec with legacy fields populated")
	}

	nativeSpec := aimv1alpha1.AIMModelSpec{
		Image: "quay.io/amd/aim-qwen:0.10.0",
		ProfileCopy: &aimv1alpha1.AIMProfileSetSpec{
			Selector: aimv1alpha1.ProfileSelector{AimId: "qwen/qwen3-32b"},
		},
	}
	if nativeSpec.HasLegacyFields() {
		t.Fatalf("HasLegacyFields() = true, want false for image+profileCopy spec")
	}
}

func TestEnsureBaseImageBridge_PopulatesBothSides(t *testing.T) {
	t.Parallel()

	specWithBaseImageMetadata := aimv1alpha1.AIMModelSpec{
		ImageMetadata: &aimv1alpha1.ImageMetadata{BaseImageRef: testBaseImageRef},
	}
	status := &aimv1alpha1.AIMModelStatus{}
	EnsureBaseImageBridge(&specWithBaseImageMetadata, status)
	if status.BaseImage != testBaseImageRef {
		t.Fatalf("status.BaseImage = %q, want %q", status.BaseImage, testBaseImageRef)
	}
	if status.ImageMetadata == nil || status.ImageMetadata.BaseImageRef != testBaseImageRef {
		t.Fatalf("status.ImageMetadata.BaseImageRef = %#v, want carried through", status.ImageMetadata)
	}

	statusOnly := &aimv1alpha1.AIMModelStatus{BaseImage: testBaseImageRef}
	EnsureBaseImageBridge(&aimv1alpha1.AIMModelSpec{}, statusOnly)
	if statusOnly.ImageMetadata == nil || statusOnly.ImageMetadata.BaseImageRef != testBaseImageRef {
		t.Fatalf("status.ImageMetadata.BaseImageRef should mirror status.BaseImage, got %#v", statusOnly.ImageMetadata)
	}
}

func TestBuildDesiredProfileSet_UsesDiscoveryCacheForImageBackedCopy(t *testing.T) {
	t.Parallel()

	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-finetuned-qwen",
			Namespace: "ml-team",
			UID:       "model-uid",
		},
		Spec: aimv1alpha1.AIMModelSpec{
			Image: "quay.io/amd/aim-qwen3-32b-instruct:0.10.0",
			ProfileCopy: &aimv1alpha1.AIMProfileSetSpec{
				Selector: aimv1alpha1.ProfileSelector{
					AimId:   "qwen/qwen3-32b",
					ModelId: "qwen/qwen3-32b-fp8",
				},
				VersionPolicy: aimv1alpha1.ProfileVersionPolicyPinned,
				Version:       "0.10.0",
				Image:         "amdenterpriseai/aim-base:0.10.0",
				Overrides: &aimv1alpha1.ProfileOverrides{
					ModelSources: []aimv1alpha1.AIMModelSource{{
						ModelID:   "my-org/qwen3-32b-fp8",
						SourceURI: "s3://bucket/fp8",
					}},
				},
			},
		},
	}

	cacheName, err := discoveryCacheName(model.Name)
	if err != nil {
		t.Fatalf("discoveryCacheName() error = %v", err)
	}
	ref := &aimv1alpha1.DiscoveryCacheReference{Name: cacheName, Namespace: "ml-team"}
	profileSetName, err := childProfileSetName(model.Name)
	if err != nil {
		t.Fatalf("childProfileSetName() error = %v", err)
	}
	desired := buildDesiredProfileSet(model, ref, profileSetName)
	if desired == nil {
		t.Fatal("buildDesiredProfileSet() = nil, want child AIMProfileSet")
	}
	if desired.Name != profileSetName {
		t.Fatalf("child set name = %q, want %q", desired.Name, profileSetName)
	}
	if desired.Spec.SourceRef == nil {
		t.Fatal("child set SourceRef = nil, want generated discovery cache ref")
	}
	if desired.Spec.SourceRef.Name != ref.Name {
		t.Fatalf("child set SourceRef = %#v, want %#v", desired.Spec.SourceRef, ref)
	}
	if desired.Spec.SourceRef.Kind != aimv1alpha1.ProfileSourceKindConfigMap {
		t.Fatalf("child set SourceRef kind = %q, want ConfigMap", desired.Spec.SourceRef.Kind)
	}
	if desired.Spec.Image != "amdenterpriseai/aim-base:0.10.0" {
		t.Fatalf("child set spec.image = %q, want runtime image override", desired.Spec.Image)
	}
}

func TestBuildDesiredProfileSet_ImageBackedProfilesWaitsForCacheAndInjectsCredentials(t *testing.T) {
	t.Parallel()

	count := int32(1)
	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "llama-partitioned",
			Namespace: "ml-team",
			UID:       "model-uid",
		},
		Spec: aimv1alpha1.AIMModelSpec{
			Image: "amdenterpriseai/aim-meta-llama-llama-3-1-8b-instruct:0.11.1",
			Profiles: &aimv1alpha1.AIMModelProfilesSpec{
				DerivedFrom: &aimv1alpha1.AIMModelProfilesDerivedFrom{
					SourceRef: &aimv1alpha1.ProfileSourceRef{Name: "user-supplied-cache"},
					Selector: aimv1alpha1.ProfileSelector{
						AcceleratorModel: "MI300X",
						AcceleratorCount: &count,
						ModelRef: &aimv1alpha1.ProfileSelectorModelRef{
							Name: "user-supplied-model",
						},
					},
				},
				VersionPolicy: aimv1alpha1.ProfileVersionPolicyPinned,
				Version:       "0.11.1",
				Overrides: &aimv1alpha1.ProfileOverrides{
					AcceleratorCount:            &count,
					AcceleratorPartitioningMode: "CPX-NPS4",
				},
			},
			ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "registry-creds"}},
			ServiceAccountName: "aim-puller",
		},
	}

	profileSetName, err := childProfileSetName(model.Name)
	if err != nil {
		t.Fatalf("childProfileSetName() error = %v", err)
	}
	if got := buildDesiredProfileSet(model, nil, profileSetName); got != nil {
		t.Fatalf("buildDesiredProfileSet() before discovery = %#v, want nil", got)
	}

	cacheRef := &aimv1alpha1.DiscoveryCacheReference{
		Name:      "llama-discovery-cache",
		Namespace: model.Namespace,
	}
	desired := buildDesiredProfileSet(model, cacheRef, profileSetName)
	if desired == nil {
		t.Fatal("buildDesiredProfileSet() after discovery = nil")
	}
	if desired.Spec.SourceRef == nil || desired.Spec.SourceRef.Name != cacheRef.Name {
		t.Fatalf("child sourceRef = %#v, want model-owned cache %q", desired.Spec.SourceRef, cacheRef.Name)
	}
	if desired.Spec.SourceRef.Kind != aimv1alpha1.ProfileSourceKindConfigMap {
		t.Fatalf("child sourceRef kind = %q, want ConfigMap", desired.Spec.SourceRef.Kind)
	}
	if desired.Spec.Selector.ModelRef != nil {
		t.Fatalf("child selector.modelRef = %#v, want image-owned scope", desired.Spec.Selector.ModelRef)
	}
	if len(desired.Spec.ImagePullSecrets) != 1 || desired.Spec.ImagePullSecrets[0].Name != "registry-creds" {
		t.Fatalf("child imagePullSecrets = %#v, want top-level model credentials", desired.Spec.ImagePullSecrets)
	}
	if desired.Spec.ServiceAccountName != "aim-puller" {
		t.Fatalf("child serviceAccountName = %q, want %q", desired.Spec.ServiceAccountName, "aim-puller")
	}
	if desired.Spec.Selector.AcceleratorCount == nil || *desired.Spec.Selector.AcceleratorCount != 1 {
		t.Fatalf("child selector acceleratorCount = %#v, want 1", desired.Spec.Selector.AcceleratorCount)
	}
}

func TestBuildDesiredProfileSet_PreservesNestedCredentialsWhenTopLevelUnset(t *testing.T) {
	t.Parallel()

	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "derived", Namespace: "ml-team"},
		Spec: aimv1alpha1.AIMModelSpec{
			DerivedFrom: &aimv1alpha1.AIMProfileSetSpec{
				Selector:           aimv1alpha1.ProfileSelector{AimId: "amd/example"},
				ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "nested-creds"}},
				ServiceAccountName: "nested-service-account",
			},
		},
	}
	profileSetName, err := childProfileSetName(model.Name)
	if err != nil {
		t.Fatalf("childProfileSetName() error = %v", err)
	}
	desired := buildDesiredProfileSet(model, nil, profileSetName)
	if desired == nil {
		t.Fatal("buildDesiredProfileSet() = nil")
	}
	if len(desired.Spec.ImagePullSecrets) != 1 || desired.Spec.ImagePullSecrets[0].Name != "nested-creds" {
		t.Fatalf("child imagePullSecrets = %#v, want nested credentials preserved", desired.Spec.ImagePullSecrets)
	}
	if desired.Spec.ServiceAccountName != "nested-service-account" {
		t.Fatalf("child serviceAccountName = %q, want nested value preserved", desired.Spec.ServiceAccountName)
	}
}

func TestBuildDesiredOfficialProfiles_MarksProfilesAsImageDerived(t *testing.T) {
	t.Parallel()

	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "qwen", Namespace: "team-a", UID: "model-uid"},
	}
	sourceContract, err := profileyaml.Inspect([]byte(`metadata:
  engine: vllm
  gpu: MI300X
  gpu_count: 1
  manual_selection_only: false
  metric: latency
  precision: fp16
  type: optimized
  capabilities:
    reasoning: true
engine_args: {}
env_vars: {}
`))
	if err != nil {
		t.Fatalf("Inspect source profile YAML: %v", err)
	}
	catalog := aimprofile.DiscoveryCatalog{
		Profiles: []aimprofile.DiscoveryCatalogItem{{
			BaseImage:    "quay.io/amd/aim-base:0.10.0",
			YAMLContract: sourceContract,
			Spec: aimv1alpha2.AIMProfileSpecCommon{
				AimId:     "qwen/Qwen3-32B",
				ModelId:   "qwen/Qwen3-32B",
				ProfileId: "tp2",
			},
		}},
	}

	desired, err := buildDesiredOfficialProfiles(model, catalog, nil, nil)
	if err != nil {
		t.Fatalf("buildDesiredOfficialProfiles() error = %v", err)
	}
	if len(desired) != 1 {
		t.Fatalf("len(desired) = %d, want 1", len(desired))
	}

	profile, ok := desired[0].Object.(*aimv1alpha2.AIMProfile)
	if !ok {
		t.Fatalf("desired object = %T, want *AIMProfile", desired[0].Object)
	}
	if profile.Namespace != "team-a" {
		t.Fatalf("profile namespace = %q, want %q", profile.Namespace, "team-a")
	}
	wantName, err := utils.GenerateDerivedName(
		[]string{"qwen", "tp2"},
		utils.WithHashSource("qwen", "tp2"),
	)
	if err != nil {
		t.Fatalf("GenerateDerivedName() error = %v", err)
	}
	if profile.Name != wantName {
		t.Fatalf("profile name = %q, want %q", profile.Name, wantName)
	}
	if profile.Annotations[aimprofile.AnnotationProfileSource] != aimprofile.ProfileSourceImage {
		t.Fatalf("profile source = %q, want %q", profile.Annotations[aimprofile.AnnotationProfileSource], aimprofile.ProfileSourceImage)
	}
	if !aimprofile.IsProfileCopyable(profile.Annotations) {
		t.Fatal("image-derived profile should be marked copyable")
	}
	discoveredContract, found, err := profileyaml.FromAnnotations(profile.Annotations)
	if err != nil {
		t.Fatalf("parse discovered profile YAML contract: %v", err)
	}
	if !found {
		t.Fatal("discovered profile YAML contract annotation is missing")
	}
	for _, field := range []string{"gpu", "gpu_count", "manual_selection_only"} {
		if !discoveredContract.HasMetadataField(field) {
			t.Errorf("discovered profile YAML contract missing %q: %s", field, discoveredContract.Encode())
		}
	}
	extensions := discoveredContract.Extensions()
	if _, ok := extensions.Metadata["capabilities"]; !ok {
		t.Errorf("discovered profile dropped capabilities: %#v", extensions.Metadata)
	}
}

func TestBuildDesiredOfficialProfiles_PrimaryFallbackFromRecommendedDeployments(t *testing.T) {
	t.Parallel()

	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "qwen", Namespace: "team-a", UID: "model-uid"},
	}
	specCommon := func(profileID, accel string, count int32, prec aimv1alpha1.AIMPrecision, metric aimv1alpha1.AIMMetric) aimv1alpha2.AIMProfileSpecCommon {
		return aimv1alpha2.AIMProfileSpecCommon{
			AimId:            "Qwen/Qwen3-32B",
			ModelId:          "Qwen/Qwen3-32B",
			ProfileId:        profileID,
			Engine:           "vllm",
			AcceleratorModel: accel,
			AcceleratorCount: count,
			Precision:        prec,
			Metric:           metric,
		}
	}

	catalog := aimprofile.DiscoveryCatalog{
		Profiles: []aimprofile.DiscoveryCatalogItem{
			{
				// Matches the recommended deployment by (gpu, count, precision, metric)
				// tuple and the YAML didn't stamp `primary` itself, so the fallback
				// should flip Primary to true.
				PrimaryExplicit: false,
				YAMLContract:    profileyaml.DefaultContract(),
				Spec:            specCommon("vllm-mi300x-fp16-tp1-latency", "MI300X", 1, "fp16", "latency"),
			},
			{
				// Same image, no matching recommended deployment for fp8 — fallback
				// leaves Primary at the catalog default (false).
				PrimaryExplicit: false,
				YAMLContract:    profileyaml.DefaultContract(),
				Spec:            specCommon("vllm-mi300x-fp8-tp1-latency", "MI300X", 1, "fp8", "latency"),
			},
			{
				// YAML explicitly set Primary=false (PrimaryExplicit=true). Even
				// though this spec matches a recommended deployment, the fallback
				// MUST NOT override an explicit value from the image author.
				PrimaryExplicit: true,
				YAMLContract:    profileyaml.DefaultContract(),
				Spec:            specCommon("vllm-mi300x-fp16-tp1-throughput", "MI300X", 1, "fp16", "throughput"),
			},
			{
				// Recommended deployment uses profileId — exact match on ProfileId
				// wins regardless of the other fields.
				PrimaryExplicit: false,
				YAMLContract:    profileyaml.DefaultContract(),
				Spec:            specCommon("vllm-mi300x-named-special", "MI300X", 1, "fp8", "latency"),
			},
		},
	}
	rds := []aimv1alpha1.RecommendedDeployment{
		{GPUModel: "MI300X", GPUCount: 1, Precision: "fp16", Metric: "latency"},
		{GPUModel: "MI300X", GPUCount: 1, Precision: "fp16", Metric: "throughput"},
		{ProfileId: "vllm-mi300x-named-special"},
	}
	// Provide a fake MI300X node so isSupportedProfile keeps every entry in
	// the desired set; otherwise the matchingNodes==0 filter trims them all
	// before the Primary fallback ever runs.
	nodes := []corev1.Node{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "n1",
			Labels: map[string]string{
				"feature.node.kubernetes.io/aim-accelerator.MI300X": "8",
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				"amd.com/gpu": resource.MustParse("8"),
			},
		},
	}}

	desired, err := buildDesiredOfficialProfiles(model, catalog, nodes, rds)
	if err != nil {
		t.Fatalf("buildDesiredOfficialProfiles() error = %v", err)
	}
	if len(desired) != 4 {
		t.Fatalf("len(desired) = %d, want 4", len(desired))
	}

	got := map[string]bool{}
	for _, d := range desired {
		profile := d.Object.(*aimv1alpha2.AIMProfile)
		got[profile.Spec.ProfileId] = profile.Spec.Primary
	}
	want := map[string]bool{
		"vllm-mi300x-fp16-tp1-latency":    true,
		"vllm-mi300x-fp8-tp1-latency":     false,
		"vllm-mi300x-fp16-tp1-throughput": false,
		"vllm-mi300x-named-special":       true,
	}
	for id, wantPrimary := range want {
		if got[id] != wantPrimary {
			t.Errorf("profile %q: Primary = %v, want %v", id, got[id], wantPrimary)
		}
	}
}

func TestMatchesRecommendedDeployment_TupleAndProfileIdMatching(t *testing.T) {
	t.Parallel()

	spec := aimv1alpha2.AIMProfileSpecCommon{
		ProfileId:        "vllm-mi300x-fp16-tp1-latency",
		AcceleratorModel: "MI300X",
		AcceleratorCount: 1,
		Precision:        "fp16",
		Metric:           "latency",
	}

	cases := []struct {
		name string
		rds  []aimv1alpha1.RecommendedDeployment
		want bool
	}{
		{"empty list", nil, false},
		{"exact tuple match", []aimv1alpha1.RecommendedDeployment{{GPUModel: "MI300X", GPUCount: 1, Precision: "fp16", Metric: "latency"}}, true},
		{"case-insensitive tuple", []aimv1alpha1.RecommendedDeployment{{GPUModel: "mi300x", GPUCount: 1, Precision: "FP16", Metric: "LATENCY"}}, true},
		{"partial wildcard (gpuModel only)", []aimv1alpha1.RecommendedDeployment{{GPUModel: "MI300X"}}, true},
		{"profileId exact match", []aimv1alpha1.RecommendedDeployment{{ProfileId: "vllm-mi300x-fp16-tp1-latency"}}, true},
		{"profileId mismatch wins over tuple", []aimv1alpha1.RecommendedDeployment{{ProfileId: "different-id", GPUModel: "MI300X", GPUCount: 1, Precision: "fp16", Metric: "latency"}}, false},
		{"precision mismatch", []aimv1alpha1.RecommendedDeployment{{GPUModel: "MI300X", GPUCount: 1, Precision: "fp8", Metric: "latency"}}, false},
		{"gpuCount mismatch", []aimv1alpha1.RecommendedDeployment{{GPUModel: "MI300X", GPUCount: 2, Precision: "fp16", Metric: "latency"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesRecommendedDeployment(spec, tc.rds); got != tc.want {
				t.Errorf("matchesRecommendedDeployment() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGeneratedChildNames_TruncateLongModelNamesWithStableHash(t *testing.T) {
	t.Parallel()

	modelName := "this-is-a-very-long-model-name-that-would-overflow-kubernetes-name-length-if-a-suffix-were-appended-directly"

	cacheName, err := discoveryCacheName(modelName)
	if err != nil {
		t.Fatalf("discoveryCacheName() error = %v", err)
	}
	if len(cacheName) > 63 {
		t.Fatalf("len(cacheName) = %d, want <= 63", len(cacheName))
	}

	profileSetName, err := childProfileSetName(modelName)
	if err != nil {
		t.Fatalf("childProfileSetName() error = %v", err)
	}
	if len(profileSetName) > 63 {
		t.Fatalf("len(profileSetName) = %d, want <= 63", len(profileSetName))
	}
	if cacheName == profileSetName {
		t.Fatalf("generated names collide: %q", cacheName)
	}
}

func TestSummarizeManagedProfiles_UsesDirectDerivativesOnly(t *testing.T) {
	t.Parallel()

	desired := []desiredProfile{
		{Object: &aimv1alpha2.AIMProfile{ObjectMeta: metav1.ObjectMeta{Name: "managed-one", Namespace: "ml-team"}}},
		{Object: &aimv1alpha2.AIMProfile{ObjectMeta: metav1.ObjectMeta{Name: "managed-two", Namespace: "ml-team"}}},
	}
	existing := []managedProfile{
		{
			Object: &aimv1alpha2.AIMProfile{ObjectMeta: metav1.ObjectMeta{Name: "managed-one", Namespace: "ml-team"}},
			Status: aimv1alpha2.AIMProfileStatus{Status: constants.AIMStatusReady},
		},
		{
			Object: &aimv1alpha2.AIMProfile{ObjectMeta: metav1.ObjectMeta{Name: "external-match", Namespace: "ml-team"}},
			Status: aimv1alpha2.AIMProfileStatus{Status: constants.AIMStatusNotAvailable},
		},
	}

	summary, existingDesired := summarizeManagedProfiles(desired, existing)

	if summary.Total != 2 {
		t.Fatalf("Total = %d, want 2", summary.Total)
	}
	if summary.Ready != 1 {
		t.Fatalf("Ready = %d, want 1", summary.Ready)
	}
	if summary.NotAvailable != 0 {
		t.Fatalf("NotAvailable = %d, want 0 because external profiles must not be aggregated", summary.NotAvailable)
	}
	if existingDesired != 1 {
		t.Fatalf("ExistingDesiredCount = %d, want 1", existingDesired)
	}
}

func TestComposeState_NodeListFailurePreventsPruningManagedProfiles(t *testing.T) {
	t.Parallel()

	reconciler := &ModelReconciler{}
	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "qwen", Namespace: "team-a", UID: "model-uid"},
		Spec:       aimv1alpha1.AIMModelSpec{Image: "quay.io/amd/aim-qwen:0.10.0"},
	}
	catalog := aimprofile.DiscoveryCatalog{
		AimID: "qwen/qwen3-32b",
		Profiles: []aimprofile.DiscoveryCatalogItem{{
			Name:         "tp2",
			Version:      "0.10.0",
			YAMLContract: profileyaml.DefaultContract(),
			Spec: aimv1alpha2.AIMProfileSpecCommon{
				AimId:     "qwen/qwen3-32b",
				ModelId:   "qwen/qwen3-32b-fp8",
				ProfileId: "tp2",
				Engine:    "vllm",
			},
		}},
	}
	fetch := ModelFetchResult{
		model: model,
		discovery: DiscoveryDecision{
			Catalog: &catalog,
			State:   &aimv1alpha1.ModelDiscoveryState{},
			Reason:  "DiscoveryCacheValid",
		},
		nodes: controllerutils.FetchResult[[]corev1.Node]{Error: errListNodesFailed},
		existingProfiles: controllerutils.FetchResult[[]managedProfile]{Value: []managedProfile{{
			Object: &aimv1alpha2.AIMProfile{ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: "team-a"}},
		}}},
	}

	obs := reconciler.ComposeState(context.Background(), controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model}, fetch)
	plan := reconciler.PlanResources(context.Background(), controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model}, obs)

	if obs.BuildErr == nil {
		t.Fatal("BuildErr = nil, want infrastructure error when node listing fails")
	}
	if obs.pruneSafe {
		t.Fatal("pruneSafe = true, want false when desired profiles could not be resolved")
	}
	if len(plan.GetToDelete()) != 0 {
		t.Fatalf("deletes = %#v, want none when node inventory is unavailable", plan.GetToDelete())
	}
}

func TestComposeState_ProfileInventoryFailuresBlockNativeMutation(t *testing.T) {
	readErr := errors.New("profile inventory read failed")
	tests := []struct {
		name  string
		model *aimv1alpha2.AIMModel
		fetch func(*aimv1alpha2.AIMModel) ModelFetchResult
	}{
		{
			name: "managed profile list",
			model: &aimv1alpha2.AIMModel{
				ObjectMeta: metav1.ObjectMeta{Name: "generated", Namespace: "team-a", UID: "model-uid"},
				Spec:       aimv1alpha1.AIMModelSpec{ModelID: "Qwen/Qwen3.5-0.8B"},
			},
			fetch: func(model *aimv1alpha2.AIMModel) ModelFetchResult {
				return ModelFetchResult{
					model: model,
					nodes: controllerutils.FetchResult[[]corev1.Node]{
						Value: []corev1.Node{testNVIDIANode()},
					},
					existingProfiles: controllerutils.FetchResult[[]managedProfile]{
						Value: []managedProfile{{
							Object: &aimv1alpha2.AIMProfile{
								ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: model.Namespace},
							},
						}},
						Error: readErr,
					},
					runtimeConfig: controllerutils.FetchResult[*aimv1alpha1.AIMRuntimeConfigCommon]{
						Value: generatedProfileRuntimeConfig(),
					},
				}
			},
		},
		{
			name: "child profile set",
			model: &aimv1alpha2.AIMModel{
				ObjectMeta: metav1.ObjectMeta{Name: "derived", Namespace: "team-a", UID: "model-uid"},
				Spec: aimv1alpha1.AIMModelSpec{
					DerivedFrom: &aimv1alpha1.AIMProfileSetSpec{
						Selector: aimv1alpha1.ProfileSelector{AimId: "qwen/qwen3"},
					},
				},
			},
			fetch: func(model *aimv1alpha2.AIMModel) ModelFetchResult {
				return ModelFetchResult{
					model: model,
					existingProfiles: controllerutils.FetchResult[[]managedProfile]{
						Value: []managedProfile{{
							Object: &aimv1alpha2.AIMProfile{
								ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: model.Namespace},
							},
						}},
					},
					childProfileSet: controllerutils.FetchResult[*aimv1alpha2.AIMProfileSet]{
						Error: readErr,
					},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &ModelReconciler{}
			fetch := tt.fetch(tt.model)
			obs := reconciler.ComposeState(
				context.Background(),
				controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: tt.model},
				fetch,
			)
			plan := reconciler.PlanResources(
				context.Background(),
				controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: tt.model},
				obs,
			)

			if obs.BuildErr == nil || obs.pruneSafe {
				t.Fatalf("observation did not preserve read failure: %#v", obs)
			}
			if len(plan.GetToApply()) != 0 ||
				len(plan.GetToApplyWithoutOwnerRef()) != 0 ||
				len(plan.GetToApplyWithForce()) != 0 ||
				len(plan.GetToDelete()) != 0 {
				t.Fatalf("read failure allowed native mutation: %#v", plan)
			}
			foundBlockingError := false
			for _, health := range obs.componentHealth {
				if len(health.Errors) > 0 && !health.NonBlocking {
					foundBlockingError = true
				}
			}
			if !foundBlockingError {
				t.Fatalf("read failure was not surfaced as blocking health: %#v", obs.componentHealth)
			}
		})
	}
}

func TestComposeState_DerivationPrunesOnlyAfterCurrentReplacementIsReady(t *testing.T) {
	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "derived", Namespace: "team-a", UID: "model-uid"},
		Spec: aimv1alpha1.AIMModelSpec{
			DerivedFrom: &aimv1alpha1.AIMProfileSetSpec{
				Selector: aimv1alpha1.ProfileSelector{AimId: "qwen/qwen3"},
			},
		},
	}
	child := &aimv1alpha2.AIMProfileSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "derived-profiles",
			Namespace:  model.Namespace,
			Generation: 2,
		},
		Spec: *model.Spec.DerivedFrom.DeepCopy(),
		Status: aimv1alpha1.AIMProfileSetStatus{
			ObservedGeneration: 1,
			Status:             constants.AIMStatusReady,
			ManagedProfiles: aimv1alpha1.ManagedProfileCounts{
				Total: 1,
				Ready: 1,
			},
		},
	}
	fetch := ModelFetchResult{
		model: model,
		existingProfiles: controllerutils.FetchResult[[]managedProfile]{
			Value: []managedProfile{{
				Object: &aimv1alpha2.AIMProfile{
					ObjectMeta: metav1.ObjectMeta{Name: "predecessor", Namespace: model.Namespace},
				},
			}},
		},
		childProfileSet: controllerutils.FetchResult[*aimv1alpha2.AIMProfileSet]{Value: child},
	}
	reconciler := &ModelReconciler{}

	stale := reconciler.ComposeState(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		fetch,
	)
	stalePlan := reconciler.PlanResources(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		stale,
	)
	if stale.pruneSafe || len(stalePlan.GetToDelete()) != 0 {
		t.Fatalf("stale replacement permitted pruning: observation %#v plan %#v", stale, stalePlan)
	}

	child.Status.ObservedGeneration = child.Generation
	current := reconciler.ComposeState(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		fetch,
	)
	currentPlan := reconciler.PlanResources(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		current,
	)
	if !current.pruneSafe || len(currentPlan.GetToDelete()) != 1 {
		t.Fatalf("current Ready replacement did not permit bounded prune: observation %#v plan %#v", current, currentPlan)
	}

	child.Spec.Selector.AimId = "different/source"
	mismatched := reconciler.ComposeState(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		fetch,
	)
	mismatchedPlan := reconciler.PlanResources(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		mismatched,
	)
	if mismatched.ProfileSetCurrent || mismatched.pruneSafe || len(mismatchedPlan.GetToDelete()) != 0 {
		t.Fatalf("spec-mismatched child permitted pruning: observation %#v plan %#v", mismatched, mismatchedPlan)
	}
}

func TestComposeState_ImageBackedDerivationWaitsForDiscoveryWithoutPruning(t *testing.T) {
	t.Parallel()

	count := int32(1)
	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "llama-partitioned", Namespace: "team-a", UID: "model-uid"},
		Spec: aimv1alpha1.AIMModelSpec{
			Image: "amdenterpriseai/aim-meta-llama-llama-3-1-8b-instruct:0.11.1",
			Profiles: &aimv1alpha1.AIMModelProfilesSpec{
				DerivedFrom: &aimv1alpha1.AIMModelProfilesDerivedFrom{
					Selector: aimv1alpha1.ProfileSelector{
						AcceleratorModel: "MI300X",
						AcceleratorCount: &count,
					},
				},
				VersionPolicy: aimv1alpha1.ProfileVersionPolicyPinned,
				Version:       "0.11.1",
			},
		},
	}
	child := &aimv1alpha2.AIMProfileSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "old-child",
			Namespace:  model.Namespace,
			Generation: 1,
		},
		Spec: aimv1alpha1.AIMProfileSetSpec{
			SourceRef: &aimv1alpha1.ProfileSourceRef{Name: "old-cache"},
		},
		Status: aimv1alpha1.AIMProfileSetStatus{
			ObservedGeneration: 1,
			Status:             constants.AIMStatusReady,
			ManagedProfiles: aimv1alpha1.ManagedProfileCounts{
				Total: 1,
				Ready: 1,
			},
		},
	}
	existing := &aimv1alpha2.AIMProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "old-direct", Namespace: model.Namespace},
	}
	reconciler := &ModelReconciler{}
	obs := reconciler.ComposeState(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		ModelFetchResult{
			model: model,
			discovery: DiscoveryDecision{
				Progressing: true,
				Reason:      "DiscoveryJobRunning",
			},
			existingProfiles: controllerutils.FetchResult[[]managedProfile]{
				Value: []managedProfile{{Object: existing}},
			},
			childProfileSet: controllerutils.FetchResult[*aimv1alpha2.AIMProfileSet]{Value: child},
		},
	)
	plan := reconciler.PlanResources(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		obs,
	)

	if obs.DesiredProfileSet != nil {
		t.Fatalf("desired child before discovery = %#v, want nil", obs.DesiredProfileSet)
	}
	if obs.pruneSafe || len(plan.GetToDelete()) != 0 {
		t.Fatalf("pending discovery allowed pruning: observation %#v plan %#v", obs, plan)
	}
	foundProfileSetWait := false
	for _, health := range obs.componentHealth {
		if health.Component == componentProfileSet &&
			health.State == constants.AIMStatusProgressing &&
			health.Message == "Waiting for discovery to produce a profile cache" {
			foundProfileSetWait = true
		}
	}
	if !foundProfileSetWait {
		t.Fatalf("component health = %#v, want ProfileSet discovery wait", obs.componentHealth)
	}
}

func TestComposeState_ImageBackedDerivationRejectsDisabledDiscovery(t *testing.T) {
	t.Parallel()

	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid", Namespace: "team-a"},
		Spec: aimv1alpha1.AIMModelSpec{
			Image: "amdenterpriseai/aim-meta-llama-llama-3-1-8b-instruct:0.11.1",
			Discovery: &aimv1alpha1.AIMModelDiscoveryConfig{
				ExtractMetadata: ptr.To(false),
			},
			Profiles: &aimv1alpha1.AIMModelProfilesSpec{
				DerivedFrom: &aimv1alpha1.AIMModelProfilesDerivedFrom{
					Selector: aimv1alpha1.ProfileSelector{AcceleratorModel: "MI300X"},
				},
				VersionPolicy: aimv1alpha1.ProfileVersionPolicyPinned,
				Version:       "0.11.1",
			},
		},
	}
	obs := (&ModelReconciler{}).ComposeState(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		ModelFetchResult{
			model:            model,
			existingProfiles: controllerutils.FetchResult[[]managedProfile]{},
			childProfileSet:  controllerutils.FetchResult[*aimv1alpha2.AIMProfileSet]{},
		},
	)
	if obs.BuildErr == nil || !strings.Contains(obs.BuildErr.Error(), "extractMetadata") {
		t.Fatalf("BuildErr = %v, want image-backed discovery validation failure", obs.BuildErr)
	}
	if obs.DesiredProfileSet != nil || obs.pruneSafe {
		t.Fatalf("invalid image-backed derivation produced mutable state: %#v", obs)
	}
}

func TestComposeState_LegacyConflictingDerivationSourcesBlockMutation(t *testing.T) {
	t.Parallel()

	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-conflict", Namespace: "team-a", UID: "model-uid"},
		Spec: aimv1alpha1.AIMModelSpec{
			Profiles: &aimv1alpha1.AIMModelProfilesSpec{
				DerivedFrom: &aimv1alpha1.AIMModelProfilesDerivedFrom{
					SourceRef: &aimv1alpha1.ProfileSourceRef{
						Kind: aimv1alpha1.ProfileSourceKindConfigMap,
						Name: "source-catalog",
					},
					Selector: aimv1alpha1.ProfileSelector{
						ModelRef: &aimv1alpha1.ProfileSelectorModelRef{Name: "source-model"},
					},
				},
				VersionPolicy: aimv1alpha1.ProfileVersionPolicyAll,
			},
		},
	}
	stale := &aimv1alpha2.AIMProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: model.Namespace},
	}
	reconciler := &ModelReconciler{}
	obs := reconciler.ComposeState(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		ModelFetchResult{
			model: model,
			existingProfiles: controllerutils.FetchResult[[]managedProfile]{
				Value: []managedProfile{{Object: stale}},
			},
			childProfileSet: controllerutils.FetchResult[*aimv1alpha2.AIMProfileSet]{},
		},
	)
	plan := reconciler.PlanResources(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		obs,
	)

	if obs.BuildErr == nil || !strings.Contains(obs.BuildErr.Error(), "sourceRef") {
		t.Fatalf("BuildErr = %v, want conflicting-source validation error", obs.BuildErr)
	}
	if got := controllerutils.CategorizeError(obs.BuildErr).Category(); got != controllerutils.ErrorCategoryInvalidSpec {
		t.Fatalf("error category = %s, want InvalidSpec", got)
	}
	if obs.pruneSafe ||
		len(plan.GetToApply()) != 0 ||
		len(plan.GetToApplyWithoutOwnerRef()) != 0 ||
		len(plan.GetToDelete()) != 0 {
		t.Fatalf("legacy conflicting model derivation planned mutations: observation %#v plan %#v", obs, plan)
	}
}

func TestPlanResources_RemovedDerivationRetainsChildUntilDirectProfilesReady(t *testing.T) {
	t.Parallel()

	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "team-a", UID: "model-uid"},
		Spec: aimv1alpha1.AIMModelSpec{
			Image: "amdenterpriseai/aim-meta-llama-llama-3-1-8b-instruct:0.11.1",
		},
	}
	catalog := aimprofile.DiscoveryCatalog{
		Profiles: []aimprofile.DiscoveryCatalogItem{{
			Name:         "mi300x-tp1",
			Version:      "0.11.1",
			YAMLContract: profileyaml.DefaultContract(),
			Spec: aimv1alpha2.AIMProfileSpecCommon{
				AimId:     "meta-llama/Llama-3.1-8B-Instruct",
				ModelId:   "meta-llama/Llama-3.1-8B-Instruct",
				ProfileId: "mi300x-tp1",
				Image:     model.Spec.Image,
				ModelSources: []aimv1alpha1.AIMModelSource{{
					ModelID:   "meta-llama/Llama-3.1-8B-Instruct",
					SourceURI: "hf://meta-llama/Llama-3.1-8B-Instruct",
				}},
			},
		}},
	}
	child := &aimv1alpha2.AIMProfileSet{
		ObjectMeta: metav1.ObjectMeta{Name: "old-derived", Namespace: model.Namespace},
	}
	reconciler := &ModelReconciler{}
	compose := func(existing []managedProfile) ModelObservation {
		return reconciler.ComposeState(
			context.Background(),
			controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
			ModelFetchResult{
				model: model,
				discovery: DiscoveryDecision{
					Catalog: &catalog,
					Reason:  "DiscoveryCacheValid",
				},
				existingProfiles: controllerutils.FetchResult[[]managedProfile]{Value: existing},
				childProfileSet:  controllerutils.FetchResult[*aimv1alpha2.AIMProfileSet]{Value: child},
			},
		)
	}

	pending := compose(nil)
	pendingPlan := reconciler.PlanResources(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		pending,
	)
	if len(pending.DesiredProfiles) != 1 {
		t.Fatalf("len(DesiredProfiles) = %d, want 1", len(pending.DesiredProfiles))
	}
	if len(pendingPlan.GetToDelete()) != 0 {
		t.Fatalf("old child deleted before replacement became ready: %#v", pendingPlan.GetToDelete())
	}

	desiredObject := pending.DesiredProfiles[0].Object.(*aimv1alpha2.AIMProfile).DeepCopy()
	desiredObject.Generation = 2
	desiredObject.Spec.AutoSelectionPolicy = aimv1alpha1.AIMProfileAutoSelectionPolicyOptimized
	desiredObject.Spec.AcceleratorPartitioningMode = aimprofile.PartitioningModeUnpartitioned
	ready := compose([]managedProfile{{
		Object: desiredObject,
		Status: aimv1alpha2.AIMProfileStatus{
			ObservedGeneration: 2,
			Status:             constants.AIMStatusReady,
		},
	}})
	readyPlan := reconciler.PlanResources(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		ready,
	)
	if len(readyPlan.GetToDelete()) != 1 || readyPlan.GetToDelete()[0].GetName() != child.Name {
		t.Fatalf("ready replacement did not retire old child: %#v", readyPlan.GetToDelete())
	}
}

// TestPlanResources_DiscoveredProfilesCarryOwnerRefBranch pins the contract
// that namespace AIMModel image-discovery profiles flow through the
// ownerRef-bearing apply branch (PlanResult.toApply, returned by GetToApply),
// not the manual-ownerRef branch (toApplyWithoutOwnerRef). Without a
// controller ownerRef, SourceModelFromOwnerRefs returns nil and the
// AIMProfile reconciler races with this builder over the source-model
// labels — see internal/v1alpha2/aimprofile/provenance.go and the F14
// finding in .tmp/v1alpha2-validation.md. The cluster sibling
// (cluster_reconcile.go:289-291) already does this; this test prevents
// regressing the namespace half back to ApplyWithoutOwnerRef.
func TestPlanResources_DiscoveredProfilesCarryOwnerRefBranch(t *testing.T) {
	t.Parallel()

	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "qwen", Namespace: "team-a", UID: "model-uid"},
		Spec:       aimv1alpha1.AIMModelSpec{Image: "quay.io/amd/aim-qwen:0.10.0"},
	}
	catalog := aimprofile.DiscoveryCatalog{
		AimID: "qwen/qwen3-32b",
		Profiles: []aimprofile.DiscoveryCatalogItem{{
			Name:         "tp2",
			Version:      "0.10.0",
			YAMLContract: profileyaml.DefaultContract(),
			Spec: aimv1alpha2.AIMProfileSpecCommon{
				AimId:        "qwen/qwen3-32b",
				ModelId:      "qwen/qwen3-32b-fp8",
				ProfileId:    "tp2",
				Engine:       "vllm",
				ModelSources: []aimv1alpha1.AIMModelSource{{ModelID: "qwen/qwen3-32b-fp8", SourceURI: "hf://qwen/qwen3-32b-fp8"}},
			},
		}},
	}

	reconciler := &ModelReconciler{}
	fetch := ModelFetchResult{
		model: model,
		discovery: DiscoveryDecision{
			Catalog: &catalog,
			State:   &aimv1alpha1.ModelDiscoveryState{},
			Reason:  "DiscoveryCacheValid",
		},
		nodes:            controllerutils.FetchResult[[]corev1.Node]{Value: nil, Error: nil},
		existingProfiles: controllerutils.FetchResult[[]managedProfile]{Value: nil, Error: nil},
	}

	obs := reconciler.ComposeState(context.Background(), controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model}, fetch)
	if len(obs.DesiredProfiles) != 1 {
		t.Fatalf("len(DesiredProfiles) = %d, want 1", len(obs.DesiredProfiles))
	}
	plan := reconciler.PlanResources(context.Background(), controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model}, obs)

	for _, obj := range plan.GetToApplyWithoutOwnerRef() {
		if _, ok := obj.(*aimv1alpha2.AIMProfile); ok {
			t.Fatalf("AIMProfile %s/%s landed in toApplyWithoutOwnerRef; F14 regressed: image-discovery profiles need a controller ownerRef so SourceModelFromOwnerRefs can resolve the producer", obj.GetNamespace(), obj.GetName())
		}
	}

	var profileApplyCount int
	for _, obj := range plan.GetToApply() {
		if _, ok := obj.(*aimv1alpha2.AIMProfile); ok {
			profileApplyCount++
		}
	}
	if profileApplyCount != 1 {
		t.Fatalf("AIMProfiles in toApply = %d, want 1 (the framework will stamp controller=true Kind=AIMModel during apply)", profileApplyCount)
	}
}

func TestBuildModelComponentHealth_PartialReadinessIsDegraded(t *testing.T) {
	t.Parallel()

	health := buildModelComponentHealth(ModelObservation{
		ModelFetchResult: ModelFetchResult{
			model: &aimv1alpha2.AIMModel{},
		},
		ManagedProfiles:      aimv1alpha1.ManagedProfileCounts{Total: 2, Ready: 1, NotAvailable: 1},
		ExistingDesiredCount: 2,
	})
	if len(health) != 1 {
		t.Fatalf("len(health) = %d, want 1", len(health))
	}
	if health[0].State != constants.AIMStatusDegraded {
		t.Fatalf("state = %q, want %q", health[0].State, constants.AIMStatusDegraded)
	}
}

func TestBuildModelComponentHealth_NoCompatibleRuntimeIncludesRejection(t *testing.T) {
	t.Parallel()

	const diagnostic = `No configured runtime fallback matches available cluster hardware: fallback "nvidia-vllm": node "mainbox": resource nvidia.com/gpu requested 1, allocatable 0`
	health := buildModelComponentHealth(ModelObservation{
		ModelFetchResult: ModelFetchResult{
			model: &aimv1alpha2.AIMModel{
				Spec: aimv1alpha1.AIMModelSpec{ModelID: "Qwen/Qwen3.5-0.8B"},
			},
		},
		ProfileGenerationFailureMessage: diagnostic,
	})
	if len(health) != 1 {
		t.Fatalf("len(health) = %d, want 1", len(health))
	}
	if health[0].Reason != "NoCompatibleRuntime" {
		t.Fatalf("reason = %q, want NoCompatibleRuntime", health[0].Reason)
	}
	if health[0].Message != diagnostic {
		t.Fatalf("message = %q, want %q", health[0].Message, diagnostic)
	}
}

func TestBuildModelComponentHealth_ProfileCopyZeroProfilesIsNotAvailable(t *testing.T) {
	t.Parallel()

	health := buildModelComponentHealth(ModelObservation{
		ModelFetchResult: ModelFetchResult{
			model: &aimv1alpha2.AIMModel{
				Spec: aimv1alpha1.AIMModelSpec{
					ProfileCopy: &aimv1alpha1.AIMProfileSetSpec{
						Selector: aimv1alpha1.ProfileSelector{AimId: "amd/example"},
					},
				},
			},
		},
		ManagedProfiles:      aimv1alpha1.ManagedProfileCounts{Total: 0, Ready: 0},
		ExistingDesiredCount: 1,
		ProfileSetCurrent:    true,
		ProfileSetStatus:     constants.AIMStatusReady,
	})
	if len(health) != 1 {
		t.Fatalf("len(health) = %d, want 1", len(health))
	}
	if health[0].State != constants.AIMStatusNotAvailable {
		t.Fatalf("state = %q, want %q", health[0].State, constants.AIMStatusNotAvailable)
	}
}

func TestComposeState_ProfileCopyUsesOnlyCurrentTerminalChildStatus(t *testing.T) {
	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "derived", Namespace: "team-a", UID: "model-uid"},
		Spec: aimv1alpha1.AIMModelSpec{
			ProfileCopy: &aimv1alpha1.AIMProfileSetSpec{
				Selector: aimv1alpha1.ProfileSelector{AimId: "amd/example"},
			},
		},
	}
	child := &aimv1alpha2.AIMProfileSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "derived-profiles",
			Namespace:  model.Namespace,
			Generation: 2,
		},
		Spec: *model.Spec.ProfileCopy.DeepCopy(),
		Status: aimv1alpha1.AIMProfileSetStatus{
			ObservedGeneration: 2,
			ManagedProfiles:    aimv1alpha1.ManagedProfileCounts{},
		},
	}
	reconciler := &ModelReconciler{}
	compose := func() ModelObservation {
		return reconciler.ComposeState(
			context.Background(),
			controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
			ModelFetchResult{
				model:            model,
				existingProfiles: controllerutils.FetchResult[[]managedProfile]{Value: nil},
				childProfileSet:  controllerutils.FetchResult[*aimv1alpha2.AIMProfileSet]{Value: child},
			},
		)
	}

	for _, childStatus := range []constants.AIMStatus{
		constants.AIMStatusNotAvailable,
		constants.AIMStatusDegraded,
		constants.AIMStatusFailed,
	} {
		t.Run(string(childStatus), func(t *testing.T) {
			child.Status.Status = childStatus
			child.Status.ObservedGeneration = child.Generation
			current := compose()
			if !current.ProfileSetCurrent || current.ProfileSetStatus != childStatus {
				t.Fatalf("current child status was not preserved: %#v", current)
			}
			if len(current.componentHealth) != 1 ||
				current.componentHealth[0].State != childStatus {
				t.Fatalf("current %s child health = %#v, want %s", childStatus, current.componentHealth, childStatus)
			}

			child.Status.ObservedGeneration = child.Generation - 1
			stale := compose()
			if stale.ProfileSetCurrent {
				t.Fatal("stale child status was treated as current")
			}
			if len(stale.componentHealth) != 1 ||
				stale.componentHealth[0].State != constants.AIMStatusProgressing {
				t.Fatalf("stale %s child health = %#v, want Progressing", childStatus, stale.componentHealth)
			}
		})
	}
}

func TestComposeState_CurrentZeroMatchProfileSetPrunesStaleDirectProfiles(t *testing.T) {
	t.Parallel()

	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "derived", Namespace: "team-a", UID: "model-uid"},
		Spec: aimv1alpha1.AIMModelSpec{
			Profiles: &aimv1alpha1.AIMModelProfilesSpec{
				DerivedFrom: &aimv1alpha1.AIMModelProfilesDerivedFrom{
					Selector: aimv1alpha1.ProfileSelector{AcceleratorModel: "MI300X"},
				},
				VersionPolicy: aimv1alpha1.ProfileVersionPolicyAll,
			},
		},
	}
	childName, err := childProfileSetName(model.Name)
	if err != nil {
		t.Fatalf("childProfileSetName() error = %v", err)
	}
	child := buildDesiredProfileSet(model, nil, childName)
	if child == nil {
		t.Fatal("buildDesiredProfileSet() = nil")
	}
	child.Generation = 2
	child.Status = aimv1alpha1.AIMProfileSetStatus{
		ObservedGeneration: 2,
		Status:             constants.AIMStatusNotAvailable,
		ManagedProfiles:    aimv1alpha1.ManagedProfileCounts{},
	}
	stale := &aimv1alpha2.AIMProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "old-direct", Namespace: model.Namespace},
	}

	reconciler := &ModelReconciler{}
	obs := reconciler.ComposeState(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		ModelFetchResult{
			model: model,
			existingProfiles: controllerutils.FetchResult[[]managedProfile]{
				Value: []managedProfile{{Object: stale}},
			},
			childProfileSet: controllerutils.FetchResult[*aimv1alpha2.AIMProfileSet]{Value: child},
		},
	)
	plan := reconciler.PlanResources(
		context.Background(),
		controllerutils.ReconcileContext[*aimv1alpha2.AIMModel]{Object: model},
		obs,
	)

	if !obs.ProfileSetCurrent || !obs.pruneSafe {
		t.Fatalf("current zero-match child did not permit prune: %#v", obs)
	}
	if len(plan.GetToDelete()) != 1 || plan.GetToDelete()[0].GetName() != stale.Name {
		t.Fatalf("deletes = %#v, want stale direct profile", plan.GetToDelete())
	}
}

func TestDerivationSpec_PreferDerivedFromOverProfileCopy(t *testing.T) {
	t.Parallel()

	derived := &aimv1alpha1.AIMProfileSetSpec{
		Selector: aimv1alpha1.ProfileSelector{AimId: "amd/derived"},
	}
	copy := &aimv1alpha1.AIMProfileSetSpec{
		Selector: aimv1alpha1.ProfileSelector{AimId: "amd/profile-copy"},
	}
	spec := &aimv1alpha1.AIMModelSpec{
		DerivedFrom: derived,
		ProfileCopy: copy,
	}
	got := derivationSpec(spec)
	if got != derived {
		t.Fatalf("derivationSpec() = %p, want spec.DerivedFrom %p (must win over ProfileCopy)", got, derived)
	}

	specCopyOnly := &aimv1alpha1.AIMModelSpec{ProfileCopy: copy}
	if got := derivationSpec(specCopyOnly); got != copy {
		t.Fatalf("derivationSpec() fallback = %p, want spec.ProfileCopy %p", got, copy)
	}

	if got := derivationSpec(&aimv1alpha1.AIMModelSpec{}); got != nil {
		t.Fatalf("derivationSpec(empty) = %p, want nil", got)
	}

	if got := derivationSpec(nil); got != nil {
		t.Fatalf("derivationSpec(nil) = %p, want nil", got)
	}
}

func TestBuildDesiredProfileSet_PrefersDerivedFromAndStampsSourceModelLabels(t *testing.T) {
	t.Parallel()

	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "qwen-derived",
			Namespace: "team-a",
			UID:       "model-uid",
		},
		Spec: aimv1alpha1.AIMModelSpec{
			DerivedFrom: &aimv1alpha1.AIMProfileSetSpec{
				Selector:      aimv1alpha1.ProfileSelector{AimId: "qwen/qwen3-32b"},
				VersionPolicy: aimv1alpha1.ProfileVersionPolicyPinned,
				Version:       "0.10.0",
			},
		},
	}

	profileSetName, err := childProfileSetName(model.Name)
	if err != nil {
		t.Fatalf("childProfileSetName() error = %v", err)
	}

	desired := buildDesiredProfileSet(model, nil, profileSetName)
	if desired == nil {
		t.Fatal("buildDesiredProfileSet() = nil, want child AIMProfileSet from DerivedFrom")
	}
	if desired.Spec.Selector.AimId != "qwen/qwen3-32b" {
		t.Fatalf("child set selector aimId = %q, want DerivedFrom selector", desired.Spec.Selector.AimId)
	}
	if got := desired.Labels[constants.LabelKeySourceModel]; got != model.Name {
		t.Fatalf("source-model label = %q, want %q", got, model.Name)
	}
	if got := desired.Labels[constants.LabelKeySourceModelScope]; got != constants.LabelValueSourceModelScopeNamespace {
		t.Fatalf("source-model-scope label = %q, want %q", got, constants.LabelValueSourceModelScopeNamespace)
	}
}

func TestBuildDesiredOfficialProfiles_StampsRoleAndOriginLabels(t *testing.T) {
	t.Parallel()

	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "qwen", Namespace: "team-a", UID: "model-uid"},
	}
	catalog := aimprofile.DiscoveryCatalog{
		Profiles: []aimprofile.DiscoveryCatalogItem{{
			BaseImage:    "quay.io/amd/aim-base:0.10.0",
			YAMLContract: profileyaml.DefaultContract(),
			Spec: aimv1alpha2.AIMProfileSpecCommon{
				AimId:     "qwen/Qwen3-32B",
				ModelId:   "qwen/Qwen3-32B",
				ProfileId: "tp2",
				Image:     "quay.io/amd/aim-qwen:0.10.0",
				ModelSources: []aimv1alpha1.AIMModelSource{{
					ModelID:   "qwen/Qwen3-32B",
					SourceURI: "hf://qwen/Qwen3-32B",
				}},
			},
		}},
	}

	desired, err := buildDesiredOfficialProfiles(model, catalog, nil, nil)
	if err != nil {
		t.Fatalf("buildDesiredOfficialProfiles() error = %v", err)
	}
	if len(desired) != 1 {
		t.Fatalf("len(desired) = %d, want 1", len(desired))
	}
	profile, ok := desired[0].Object.(*aimv1alpha2.AIMProfile)
	if !ok {
		t.Fatalf("desired object = %T, want *AIMProfile", desired[0].Object)
	}
	if got := profile.Labels[constants.LabelKeyProfileRole]; got != constants.LabelValueProfileRoleDeployable {
		t.Fatalf("profile-role label = %q, want %q", got, constants.LabelValueProfileRoleDeployable)
	}
	if got := profile.Labels[constants.LabelKeyProfileOrigin]; got != string(aimv1alpha1.ProfileOriginDiscovered) {
		t.Fatalf("profile-origin label = %q, want %q", got, aimv1alpha1.ProfileOriginDiscovered)
	}
	if got := profile.Labels[constants.LabelKeySourceModel]; got != model.Name {
		t.Fatalf("source-model label = %q, want %q", got, model.Name)
	}
	if got := profile.Labels[constants.LabelKeySourceModelScope]; got != constants.LabelValueSourceModelScopeNamespace {
		t.Fatalf("source-model-scope label = %q, want %q", got, constants.LabelValueSourceModelScopeNamespace)
	}
}

// TestBuildDesiredOfficialProfiles_BaseEntriesStampedAsBase covers the
// iteration-2 base-image (custom-model source) producer path: a catalog item
// from a base image has no aimId / modelSources, so the materialised profile
// must carry the `base` role label (and remain non-deployable per
// IsProfileDeployable). Also asserts the base/deployable counts surfaced via
// summarizeManagedProfiles so the AIMModel status reflects the mix.
func TestBuildDesiredOfficialProfiles_BaseEntriesStampedAsBase(t *testing.T) {
	t.Parallel()

	model := &aimv1alpha2.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-base", Namespace: "team-a", UID: "model-uid"},
	}
	catalog := aimprofile.DiscoveryCatalog{
		Profiles: []aimprofile.DiscoveryCatalogItem{{
			BaseImage:    "ghcr.io/silogen/aim-base-vllm:0.1.0",
			YAMLContract: profileyaml.DefaultContract(),
			Spec: aimv1alpha2.AIMProfileSpecCommon{
				ProfileId: "vllm-cpu-bf16-tp1-latency",
				Engine:    "vllm",
			},
		}},
	}

	desired, err := buildDesiredOfficialProfiles(model, catalog, nil, nil)
	if err != nil {
		t.Fatalf("buildDesiredOfficialProfiles() error = %v", err)
	}
	if len(desired) != 1 {
		t.Fatalf("len(desired) = %d, want 1", len(desired))
	}
	profile, ok := desired[0].Object.(*aimv1alpha2.AIMProfile)
	if !ok {
		t.Fatalf("desired object = %T, want *AIMProfile", desired[0].Object)
	}
	if got := profile.Labels[constants.LabelKeyProfileRole]; got != constants.LabelValueProfileRoleBase {
		t.Fatalf("profile-role label = %q, want %q for base-profile entry", got, constants.LabelValueProfileRoleBase)
	}
	if got := profile.Labels[constants.LabelKeyProfileOrigin]; got != string(aimv1alpha1.ProfileOriginDiscovered) {
		t.Fatalf("profile-origin label = %q, want %q", got, aimv1alpha1.ProfileOriginDiscovered)
	}
	if aimprofile.IsProfileDeployable(profile.Spec.AIMProfileSpecCommon) {
		t.Fatal("base profile spec must not satisfy IsProfileDeployable")
	}

	summary, _ := summarizeManagedProfiles(desired, nil)
	if summary.Total != 1 {
		t.Fatalf("summary.Total = %d, want 1", summary.Total)
	}
	if summary.Base != 1 {
		t.Fatalf("summary.Base = %d, want 1 for base-only catalog", summary.Base)
	}
	if summary.Deployable != 0 {
		t.Fatalf("summary.Deployable = %d, want 0 for base-only catalog", summary.Deployable)
	}
}

// TestSummarizeManagedProfiles_CountsDeployableEntries asserts the
// deployable counterpart: a fully-formed AIMProfile (aimId + modelSources)
// shows up under Deployable and not Base.
func TestSummarizeManagedProfiles_CountsDeployableEntries(t *testing.T) {
	t.Parallel()

	desired := []desiredProfile{{
		Object: &aimv1alpha2.AIMProfile{
			ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "team-a"},
			Spec: aimv1alpha2.AIMProfileSpec{AIMProfileSpecCommon: aimv1alpha2.AIMProfileSpecCommon{
				AimId: "qwen/Qwen3-32B",
				ModelSources: []aimv1alpha1.AIMModelSource{{
					ModelID:   "qwen/Qwen3-32B",
					SourceURI: "hf://qwen/Qwen3-32B",
				}},
			}},
		},
	}}

	summary, _ := summarizeManagedProfiles(desired, nil)
	if summary.Total != 1 {
		t.Fatalf("summary.Total = %d, want 1", summary.Total)
	}
	if summary.Deployable != 1 {
		t.Fatalf("summary.Deployable = %d, want 1 for deployable-only desired set", summary.Deployable)
	}
	if summary.Base != 0 {
		t.Fatalf("summary.Base = %d, want 0 for deployable-only desired set", summary.Base)
	}
}
