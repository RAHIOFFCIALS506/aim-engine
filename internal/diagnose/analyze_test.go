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

func TestAnalyze_FailedService(t *testing.T) {
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
	diagnostics, err := Analyze(context.Background(), c, Options{
		ResourceType: "service",
		Name:         "test-svc",
		Namespace:    "default",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(diagnostics) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diagnostics))
	}

	d := diagnostics[0]
	if d.Code != "AIM001" {
		t.Errorf("expected code AIM001, got %s", d.Code)
	}
	if d.Severity != SeverityError {
		t.Errorf("expected severity %s, got %s", SeverityError, d.Severity)
	}
	if !strings.Contains(d.Resource, "test-svc") {
		t.Errorf("expected resource to contain test-svc, got %s", d.Resource)
	}
	if !strings.Contains(d.Message, "ModelNotFound") {
		t.Errorf("expected message to contain ModelNotFound, got %s", d.Message)
	}
}

func TestAnalyze_HealthyService(t *testing.T) {
	scheme := testScheme()
	svc := &aimv1alpha1.AIMService{
		ObjectMeta: metav1.ObjectMeta{Name: "test-svc", Namespace: "default"},
		Spec: aimv1alpha1.AIMServiceSpec{
			Model: aimv1alpha1.AIMServiceModel{Name: ptr.To("test-model")},
		},
		Status: aimv1alpha1.AIMServiceStatus{
			Status: constants.AIMStatusStarting,
		},
	}

	c := testClient(scheme, svc)
	diagnostics, err := Analyze(context.Background(), c, Options{
		ResourceType: "service",
		Name:         "test-svc",
		Namespace:    "default",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(diagnostics) != 0 {
		t.Errorf("expected 0 diagnostics for healthy service, got %d", len(diagnostics))
	}
}

func TestAnalyze_JSONRoundTrip(t *testing.T) {
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
	diagnostics, err := Analyze(context.Background(), c, Options{
		ResourceType: "service",
		Name:         "test-svc",
		Namespace:    "default",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	if err := RenderJSON(&buf, diagnostics); err != nil {
		t.Fatalf("RenderJSON error: %v", err)
	}

	var decoded []Diagnostic
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON unmarshal error: %v", err)
	}

	if len(decoded) != 1 {
		t.Fatalf("expected 1 diagnostic after round-trip, got %d", len(decoded))
	}
	if decoded[0].Code != "AIM001" {
		t.Errorf("expected code AIM001 after round-trip, got %s", decoded[0].Code)
	}
}

func TestAnalyze_UnsupportedResource(t *testing.T) {
	c := testClient(testScheme())
	_, err := Analyze(context.Background(), c, Options{
		ResourceType: "foobar",
		Name:         "test",
		Namespace:    "default",
	})
	if err == nil {
		t.Fatal("expected error for unsupported resource type")
	}
}
