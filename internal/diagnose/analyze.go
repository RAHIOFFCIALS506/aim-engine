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
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aimv1alpha1 "github.com/amd-enterprise-ai/aim-engine/api/v1alpha1"
	"github.com/amd-enterprise-ai/aim-engine/internal/aimservice"
	"github.com/amd-enterprise-ai/aim-engine/internal/constants"
)

// AnalyzeAll scans every AIMService in the given namespace (or all namespaces
// when namespace is "") and returns aggregated diagnostics. Resource names are
// qualified with namespace when scanning cross-namespace.
func AnalyzeAll(ctx context.Context, c client.Client, namespace string) (*ScanResult, error) {
	var list aimv1alpha1.AIMServiceList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list AIMServices: %w", err)
	}

	crossNamespace := namespace == ""
	result := &ScanResult{ServicesScanned: len(list.Items)}

	for i := range list.Items {
		svc := &list.Items[i]
		diags := runAnalyzers(ctx, c, svc)
		if len(diags) == 0 {
			continue
		}
		result.ServicesWithIssues++
		if crossNamespace {
			for j := range diags {
				diags[j].Resource = svc.Namespace + "/" + diags[j].Resource
			}
		}
		result.Diagnostics = append(result.Diagnostics, diags...)
	}
	return result, nil
}

// ServiceAnalyzer checks an AIMService and returns diagnostics.
type ServiceAnalyzer func(ctx context.Context, c client.Client, svc *aimv1alpha1.AIMService) []Diagnostic

var serviceAnalyzers = []ServiceAnalyzer{
	checkServiceNotRunning,
	checkPodFailures,
}

func analyzeService(ctx context.Context, c client.Client, name, namespace string) ([]Diagnostic, error) {
	var svc aimv1alpha1.AIMService
	if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &svc); err != nil {
		return nil, fmt.Errorf("get AIMService %s/%s: %w", namespace, name, err)
	}
	return runAnalyzers(ctx, c, &svc), nil
}

func runAnalyzers(ctx context.Context, c client.Client, svc *aimv1alpha1.AIMService) []Diagnostic {
	var diagnostics []Diagnostic
	for _, analyzer := range serviceAnalyzers {
		diagnostics = append(diagnostics, analyzer(ctx, c, svc)...)
	}
	return diagnostics
}

// checkServiceNotRunning reports AIM001 when the service status is Failed,
// including the reason from the most relevant False condition.
func checkServiceNotRunning(_ context.Context, _ client.Client, svc *aimv1alpha1.AIMService) []Diagnostic {
	if svc.Status.Status != constants.AIMStatusFailed {
		return nil
	}

	resource := fmt.Sprintf("AIMService/%s", svc.Name)
	reason := failureReason(svc.Status.Conditions)
	return []Diagnostic{NewDiagnostic(AIM001, resource, reason)}
}

// conditionPriority defines the order in which False conditions are checked.
// Upstream dependency conditions are checked first since they are the most
// common root cause of failures.
var conditionPriority = []string{
	"ModelReady",
	"TemplateReady",
	"RuntimeConfigReady",
	"InferenceServiceReady",
	"CacheReady",
	"HTTPRouteReady",
	"HPAReady",
}

func failureReason(conditions []metav1.Condition) string {
	byType := make(map[string]metav1.Condition, len(conditions))
	for _, c := range conditions {
		byType[c.Type] = c
	}

	for _, ct := range conditionPriority {
		c, ok := byType[ct]
		if ok && c.Status == metav1.ConditionFalse && c.Reason != "" {
			return formatConditionReason(c)
		}
	}

	// Fall back to any False condition not in the priority list.
	for _, c := range conditions {
		if c.Status == metav1.ConditionFalse && c.Reason != "" {
			return formatConditionReason(c)
		}
	}
	return ""
}

func formatConditionReason(c metav1.Condition) string {
	if c.Message != "" {
		return fmt.Sprintf("%s (%s)", c.Reason, c.Message)
	}
	return c.Reason
}

// checkPodFailures reports AIM003 for each pod with a container in a failure
// state (CrashLoopBackOff, ImagePullBackOff, ErrImagePull, etc.).
func checkPodFailures(ctx context.Context, c client.Client, svc *aimv1alpha1.AIMService) []Diagnostic {
	isvcName, err := aimservice.GenerateInferenceServiceName(svc.Name, svc.Namespace)
	if err != nil {
		return nil
	}

	var podList corev1.PodList
	if err := c.List(ctx, &podList,
		client.InNamespace(svc.Namespace),
		client.MatchingLabels{constants.LabelKServeInferenceService: isvcName},
	); err != nil {
		return nil
	}

	var diagnostics []Diagnostic
	for i := range podList.Items {
		pod := &podList.Items[i]
		resource := fmt.Sprintf("Pod/%s", pod.Name)

		for _, cs := range pod.Status.InitContainerStatuses {
			if d, ok := containerDiagnostic(cs, resource); ok {
				diagnostics = append(diagnostics, d)
			}
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if d, ok := containerDiagnostic(cs, resource); ok {
				diagnostics = append(diagnostics, d)
			}
		}
	}
	return diagnostics
}

func containerDiagnostic(cs corev1.ContainerStatus, resource string) (Diagnostic, bool) {
	if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
		reason := cs.State.Waiting.Reason
		switch reason {
		case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull",
			"CreateContainerConfigError", "InvalidImageName", "RunContainerError":
			detail := reason
			if cs.State.Waiting.Message != "" {
				detail = fmt.Sprintf("%s — %s", reason, cs.State.Waiting.Message)
			}
			return NewDiagnostic(AIM003, resource, detail), true
		}
	}
	if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
		detail := fmt.Sprintf("exited with code %d", cs.State.Terminated.ExitCode)
		if cs.State.Terminated.Reason != "" {
			detail = fmt.Sprintf("%s (%s)", cs.State.Terminated.Reason, detail)
		}
		return NewDiagnostic(AIM003, resource, detail), true
	}
	return Diagnostic{}, false
}
