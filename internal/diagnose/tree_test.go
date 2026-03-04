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
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	aimv1alpha1 "github.com/amd-enterprise-ai/aim-engine/api/v1alpha1"
	"github.com/amd-enterprise-ai/aim-engine/internal/constants"
)

func TestTree_AllReady(t *testing.T) {
	scheme := testScheme()

	model := &aimv1alpha1.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "test-model", Namespace: "default"},
		Spec:       aimv1alpha1.AIMModelSpec{Image: "ghcr.io/test/model:v1"},
		Status:     aimv1alpha1.AIMModelStatus{Status: constants.AIMStatusReady},
	}

	template := &aimv1alpha1.AIMServiceTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "test-template", Namespace: "default"},
		Spec: aimv1alpha1.AIMServiceTemplateSpec{
			AIMServiceTemplateSpecCommon: aimv1alpha1.AIMServiceTemplateSpecCommon{
				ModelName: "test-model",
			},
		},
		Status: aimv1alpha1.AIMServiceTemplateStatus{Status: constants.AIMStatusReady},
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
		},
	}

	c := testClient(scheme, model, template, svc)
	tree, err := Tree(context.Background(), c, Options{
		ResourceType: "service",
		Name:         "test-svc",
		Namespace:    "default",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tree.Kind != "AIMService" {
		t.Errorf("expected root kind AIMService, got %s", tree.Kind)
	}
	if tree.Status != "Starting" {
		t.Errorf("expected root status Starting, got %s", tree.Status)
	}

	kinds := make(map[string]string)
	for _, child := range tree.Children {
		kinds[child.Kind] = child.Status
	}
	if kinds["AIMModel"] != "Ready" {
		t.Errorf("expected AIMModel Ready, got %q", kinds["AIMModel"])
	}
	if kinds["AIMServiceTemplate"] != "Ready" {
		t.Errorf("expected AIMServiceTemplate Ready, got %q", kinds["AIMServiceTemplate"])
	}
}

func TestTree_JSONRoundTrip(t *testing.T) {
	scheme := testScheme()

	model := &aimv1alpha1.AIMModel{
		ObjectMeta: metav1.ObjectMeta{Name: "test-model", Namespace: "default"},
		Spec:       aimv1alpha1.AIMModelSpec{Image: "ghcr.io/test/model:v1"},
		Status:     aimv1alpha1.AIMModelStatus{Status: constants.AIMStatusReady},
	}

	svc := &aimv1alpha1.AIMService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-svc", Namespace: "default"},
		Spec: aimv1alpha1.AIMServiceSpec{
			Model: aimv1alpha1.AIMServiceModel{Name: ptr.To("test-model")},
		},
		Status: aimv1alpha1.AIMServiceStatus{
			Status: constants.AIMStatusStarting,
			ResolvedModel: &aimv1alpha1.AIMResolvedReference{
				Name: "test-model", Namespace: "default",
				Scope: aimv1alpha1.AIMResolutionScopeNamespace,
			},
		},
	}

	c := testClient(scheme, model, svc)
	tree, err := Tree(context.Background(), c, Options{
		ResourceType: "service",
		Name:         "test-svc",
		Namespace:    "default",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	if err := RenderJSON(&buf, tree); err != nil {
		t.Fatalf("RenderJSON error: %v", err)
	}

	var decoded TreeNode
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON unmarshal error: %v", err)
	}

	if decoded.Kind != "AIMService" {
		t.Errorf("expected kind AIMService after round-trip, got %s", decoded.Kind)
	}
	if decoded.Name != "test-svc" {
		t.Errorf("expected name test-svc after round-trip, got %s", decoded.Name)
	}
	if len(decoded.Children) == 0 {
		t.Error("expected children after round-trip")
	}
}

func TestTree_ServiceNotFound(t *testing.T) {
	c := testClient(testScheme())
	_, err := Tree(context.Background(), c, Options{
		ResourceType: "service",
		Name:         "nonexistent",
		Namespace:    "default",
	})
	if err == nil {
		t.Fatal("expected error for missing service")
	}
	if !strings.Contains(err.Error(), "AIMService") {
		t.Errorf("expected AIMService in error message, got: %v", err)
	}
}

func TestTree_UnsupportedResource(t *testing.T) {
	c := testClient(testScheme())
	_, err := Tree(context.Background(), c, Options{
		ResourceType: "foobar",
		Name:         "test",
		Namespace:    "default",
	})
	if err == nil {
		t.Fatal("expected error for unsupported resource type")
	}
}
