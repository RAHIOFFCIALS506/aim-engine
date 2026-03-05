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

package diagnose

import (
	"bytes"
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kservev1beta1 "github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aimv1alpha1 "github.com/amd-enterprise-ai/aim-engine/api/v1alpha1"
	"github.com/amd-enterprise-ai/aim-engine/internal/aimservice"
	"github.com/amd-enterprise-ai/aim-engine/internal/constants"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(aimv1alpha1.AddToScheme(s))
	utilruntime.Must(kservev1beta1.AddToScheme(s))
	utilruntime.Must(gatewayapiv1.Install(s))
	return s
}

func testClient(scheme *runtime.Scheme, objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&aimv1alpha1.AIMService{}, &aimv1alpha1.AIMModel{}, &aimv1alpha1.AIMServiceTemplate{}).
		Build()
}

func TestRun_UnsupportedResource(t *testing.T) {
	c := testClient(testScheme())
	_, err := Run(context.Background(), c, Options{
		ResourceType: "foobar",
		Name:         "test",
		Namespace:    "default",
	})
	if err == nil {
		t.Fatal("expected error for unsupported resource type")
	}
	if !strings.Contains(err.Error(), "unsupported resource type") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRun_ServiceNotFound(t *testing.T) {
	c := testClient(testScheme())
	_, err := Run(context.Background(), c, Options{
		ResourceType: "service",
		Name:         "nonexistent",
		Namespace:    "default",
	})
	if err == nil {
		t.Fatal("expected error for missing service")
	}
}

func TestRun_ServiceAlias(t *testing.T) {
	c := testClient(testScheme())
	_, err := Run(context.Background(), c, Options{
		ResourceType: "svc",
		Name:         "nonexistent",
		Namespace:    "default",
	})
	if err == nil {
		t.Fatal("expected error for missing service (alias should resolve)")
	}
	if !strings.Contains(err.Error(), "AIMService") {
		t.Errorf("expected AIMService error, got: %v", err)
	}
}

func TestRun_ModelNotFound(t *testing.T) {
	scheme := testScheme()
	svc := &aimv1alpha1.AIMService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-svc", Namespace: "default"},
		Spec: aimv1alpha1.AIMServiceSpec{
			Model: aimv1alpha1.AIMServiceModel{Name: ptr.To("nonexistent-model")},
		},
		Status: aimv1alpha1.AIMServiceStatus{
			Status: constants.AIMStatusFailed,
			Conditions: []metav1.Condition{
				{Type: "ModelReady", Status: metav1.ConditionFalse, Reason: "ModelNotFound"},
			},
		},
	}

	c := testClient(scheme, svc)
	report, err := Run(context.Background(), c, Options{
		ResourceType: "service",
		Name:         "test-svc",
		Namespace:    "default",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Resource.Status != "Failed" {
		t.Errorf("expected status Failed, got %s", report.Resource.Status)
	}

	// Tree should have model node with "(not resolved)" status
	foundModel := false
	for _, child := range report.Tree.Children {
		if child.Kind == "Model" {
			foundModel = true
			if child.Status != "(not resolved)" {
				t.Errorf("expected model status '(not resolved)', got %q", child.Status)
			}
		}
	}
	if !foundModel {
		t.Error("expected Model node in tree")
	}

	// Conditions should include ModelNotFound
	foundCondition := false
	for _, c := range report.Conditions {
		if c.Type == "ModelReady" && c.Reason == "ModelNotFound" {
			foundCondition = true
		}
	}
	if !foundCondition {
		t.Error("expected ModelReady/ModelNotFound condition")
	}

	// Render should succeed
	var buf bytes.Buffer
	if err := Render(&buf, report); err != nil {
		t.Fatalf("render error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "ModelNotFound") {
		t.Error("rendered output should contain ModelNotFound")
	}
	if !strings.Contains(output, "Failed") {
		t.Error("rendered output should contain Failed")
	}
}

func mustISVCName(t *testing.T, svcName, namespace string) string {
	t.Helper()
	name, err := aimservice.GenerateInferenceServiceName(svcName, namespace)
	if err != nil {
		t.Fatalf("GenerateInferenceServiceName(%q, %q): %v", svcName, namespace, err)
	}
	return name
}

func makeISVC(name, namespace string, env []corev1.EnvVar) *kservev1beta1.InferenceService {
	return &kservev1beta1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: kservev1beta1.InferenceServiceSpec{
			Predictor: kservev1beta1.PredictorSpec{
				PodSpec: kservev1beta1.PodSpec{
					Containers: []corev1.Container{
						{Name: "kserve-container", Env: env},
					},
				},
			},
		},
	}
}

func TestRun_AllReady(t *testing.T) {
	scheme := testScheme()

	model := &aimv1alpha1.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "test-model", Namespace: "default"},
		Spec:       aimv1alpha1.AIMModelSpec{Image: "ghcr.io/test/model:v1"},
		Status: aimv1alpha1.AIMModelStatus{
			Status: constants.AIMStatusReady,
		},
	}

	template := &aimv1alpha1.AIMServiceTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "test-template", Namespace: "default"},
		Spec: aimv1alpha1.AIMServiceTemplateSpec{
			AIMServiceTemplateSpecCommon: aimv1alpha1.AIMServiceTemplateSpecCommon{
				ModelName: "test-model",
			},
		},
		Status: aimv1alpha1.AIMServiceTemplateStatus{
			Status: constants.AIMStatusReady,
		},
	}

	svc := &aimv1alpha1.AIMService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-svc", Namespace: "default"},
		Spec: aimv1alpha1.AIMServiceSpec{
			Model:    aimv1alpha1.AIMServiceModel{Name: ptr.To("test-model")},
			Template: aimv1alpha1.AIMServiceTemplateConfig{Name: "test-template"},
		},
		Status: aimv1alpha1.AIMServiceStatus{
			Status: constants.AIMStatusStarting,
			ResolvedModel: &aimv1alpha1.AIMResolvedReference{
				Name: "test-model", Namespace: "default",
				Scope: aimv1alpha1.AIMResolutionScopeNamespace,
			},
			ResolvedTemplate: &aimv1alpha1.AIMResolvedReference{
				Name: "test-template", Namespace: "default",
				Scope: aimv1alpha1.AIMResolutionScopeNamespace,
			},
			Conditions: []metav1.Condition{
				{Type: "ModelReady", Status: metav1.ConditionTrue, Reason: "ModelResolved"},
				{Type: "TemplateReady", Status: metav1.ConditionTrue, Reason: "Resolved"},
			},
		},
	}

	isvcName := mustISVCName(t, "test-svc", "default")
	isvc := makeISVC(isvcName, "default", []corev1.EnvVar{
		{Name: "AIM_CACHE_PATH", Value: "/workspace/cache"},
		{Name: "VLLM_ENABLE_METRICS", Value: "true"},
	})

	c := testClient(scheme, model, template, svc, isvc)
	report, err := Run(context.Background(), c, Options{
		ResourceType: "service",
		Name:         "test-svc",
		Namespace:    "default",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Resource.Status != "Starting" {
		t.Errorf("expected status Starting, got %s", report.Resource.Status)
	}

	// Check tree nodes
	kinds := make(map[string]string)
	for _, child := range report.Tree.Children {
		kinds[child.Kind] = child.Status
	}
	if kinds["AIMModel"] != conditionReady {
		t.Errorf("expected AIMModel Ready, got %q", kinds["AIMModel"])
	}
	if kinds["AIMServiceTemplate"] != conditionReady {
		t.Errorf("expected AIMServiceTemplate Ready, got %q", kinds["AIMServiceTemplate"])
	}
	if _, ok := kinds["InferenceService"]; !ok {
		t.Error("expected InferenceService node in tree")
	}

	// EnvVars should include system defaults read from the actual ISVC
	foundCachePath := false
	for _, e := range report.EnvVars {
		if e.Name == "AIM_CACHE_PATH" && e.Source == envSourceSystem {
			foundCachePath = true
		}
	}
	if !foundCachePath {
		t.Error("expected AIM_CACHE_PATH env var with System source")
	}

	// Render should succeed and contain key info
	var buf bytes.Buffer
	if err := Render(&buf, report); err != nil {
		t.Fatalf("render error: %v", err)
	}
	output := buf.String()
	for _, want := range []string{"AIMModel", "AIMServiceTemplate", "Starting", "ModelResolved"} {
		if !strings.Contains(output, want) {
			t.Errorf("rendered output should contain %q", want)
		}
	}
}

func TestRun_NoEnvWithoutISVC(t *testing.T) {
	scheme := testScheme()
	svc := &aimv1alpha1.AIMService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-svc", Namespace: "default"},
		Spec: aimv1alpha1.AIMServiceSpec{
			Model: aimv1alpha1.AIMServiceModel{Name: ptr.To("test-model")},
			AIMServiceRuntimeConfig: aimv1alpha1.AIMServiceRuntimeConfig{
				Env: []corev1.EnvVar{
					{Name: "HF_TOKEN", Value: "my-token"},
				},
			},
		},
		Status: aimv1alpha1.AIMServiceStatus{
			Status: constants.AIMStatusStarting,
		},
	}

	c := testClient(scheme, svc)
	report, err := Run(context.Background(), c, Options{
		ResourceType: "service",
		Name:         "test-svc",
		Namespace:    "default",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(report.EnvVars) != 0 {
		t.Errorf("expected no env vars when ISVC does not exist, got %d", len(report.EnvVars))
	}
}

func TestRun_EnvSourceAttribution(t *testing.T) {
	scheme := testScheme()

	template := &aimv1alpha1.AIMServiceTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "test-template", Namespace: "default"},
		Spec: aimv1alpha1.AIMServiceTemplateSpec{
			AIMServiceTemplateSpecCommon: aimv1alpha1.AIMServiceTemplateSpecCommon{
				ModelName: "test-model",
				Env: []corev1.EnvVar{
					{Name: "HF_TOKEN", Value: "template-token"},
					{Name: "HTTP_PROXY", Value: "http://proxy:8080"},
				},
			},
		},
		Status: aimv1alpha1.AIMServiceTemplateStatus{Status: constants.AIMStatusReady},
	}

	svc := &aimv1alpha1.AIMService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-svc", Namespace: "default"},
		Spec: aimv1alpha1.AIMServiceSpec{
			Model:    aimv1alpha1.AIMServiceModel{Name: ptr.To("test-model")},
			Template: aimv1alpha1.AIMServiceTemplateConfig{Name: "test-template"},
			AIMServiceRuntimeConfig: aimv1alpha1.AIMServiceRuntimeConfig{
				Env: []corev1.EnvVar{
					{Name: "HF_TOKEN", Value: "service-token"},
				},
			},
		},
		Status: aimv1alpha1.AIMServiceStatus{
			Status: constants.AIMStatusStarting,
			ResolvedTemplate: &aimv1alpha1.AIMResolvedReference{
				Name: "test-template", Namespace: "default",
				Scope: aimv1alpha1.AIMResolutionScopeNamespace,
			},
		},
	}

	// ISVC with the actual env vars as the operator would have set them
	isvcName := mustISVCName(t, "test-svc", "default")
	isvc := makeISVC(isvcName, "default", []corev1.EnvVar{
		{Name: "AIM_CACHE_PATH", Value: "/workspace/cache"},
		{Name: "HF_TOKEN", Value: "service-token"},
		{Name: "HTTP_PROXY", Value: "http://proxy:8080"},
		{Name: "VLLM_ENABLE_METRICS", Value: "true"},
	})

	c := testClient(scheme, template, svc, isvc)
	report, err := Run(context.Background(), c, Options{
		ResourceType: "service",
		Name:         "test-svc",
		Namespace:    "default",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	envByName := make(map[string]EnvEntry)
	for _, e := range report.EnvVars {
		envByName[e.Name] = e
	}

	// HF_TOKEN should come from Service (highest precedence)
	if e, ok := envByName["HF_TOKEN"]; !ok {
		t.Error("expected HF_TOKEN env var")
	} else {
		if e.Value != "service-token" {
			t.Errorf("expected HF_TOKEN value 'service-token', got %q", e.Value)
		}
		if e.Source != "Service" {
			t.Errorf("expected HF_TOKEN source 'Service', got %q", e.Source)
		}
	}

	// HTTP_PROXY should come from Template
	if e, ok := envByName["HTTP_PROXY"]; !ok {
		t.Error("expected HTTP_PROXY env var")
	} else {
		if e.Source != "Template" {
			t.Errorf("expected HTTP_PROXY source 'Template', got %q", e.Source)
		}
	}

	// AIM_CACHE_PATH should come from System
	if e, ok := envByName["AIM_CACHE_PATH"]; !ok {
		t.Error("expected AIM_CACHE_PATH env var")
	} else {
		if e.Source != envSourceSystem {
			t.Errorf("expected AIM_CACHE_PATH source 'System', got %q", e.Source)
		}
	}
}

func TestRun_EnvReadsFromISVC(t *testing.T) {
	scheme := testScheme()

	svc := &aimv1alpha1.AIMService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-svc", Namespace: "default"},
		Spec: aimv1alpha1.AIMServiceSpec{
			Model: aimv1alpha1.AIMServiceModel{Name: ptr.To("test-model")},
			AIMServiceRuntimeConfig: aimv1alpha1.AIMServiceRuntimeConfig{
				Env: []corev1.EnvVar{
					{Name: "MY_VAR", Value: "from-service-spec"},
				},
			},
		},
		Status: aimv1alpha1.AIMServiceStatus{Status: constants.AIMStatusStarting},
	}

	// ISVC has a different value than what the service spec declares.
	// The report must show the actual ISVC value, not the service spec value.
	isvcName := mustISVCName(t, "test-svc", "default")
	isvc := makeISVC(isvcName, "default", []corev1.EnvVar{
		{Name: "MY_VAR", Value: "actual-deployed-value"},
	})

	c := testClient(scheme, svc, isvc)
	report, err := Run(context.Background(), c, Options{
		ResourceType: "service",
		Name:         "test-svc",
		Namespace:    "default",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(report.EnvVars) != 1 {
		t.Fatalf("expected 1 env var, got %d", len(report.EnvVars))
	}
	if report.EnvVars[0].Value != "actual-deployed-value" {
		t.Errorf("expected value from ISVC 'actual-deployed-value', got %q", report.EnvVars[0].Value)
	}
}
