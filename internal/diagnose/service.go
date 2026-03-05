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
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	servingv1beta1 "github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aimv1alpha1 "github.com/amd-enterprise-ai/aim-engine/api/v1alpha1"
	"github.com/amd-enterprise-ai/aim-engine/internal/aimruntimeconfig"
	"github.com/amd-enterprise-ai/aim-engine/internal/aimservice"
	"github.com/amd-enterprise-ai/aim-engine/internal/constants"
)

const maxEvents = 20

func debugService(ctx context.Context, c client.Client, name, namespace string) (*Report, error) {
	var svc aimv1alpha1.AIMService
	if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &svc); err != nil {
		return nil, fmt.Errorf("get AIMService %s/%s: %w", namespace, name, err)
	}

	// Fetch the InferenceService for ground-truth env vars (may not exist yet)
	var isvc *servingv1beta1.InferenceService
	if isvcName, err := aimservice.GenerateInferenceServiceName(svc.Name, svc.Namespace); err == nil {
		var obj servingv1beta1.InferenceService
		if err := c.Get(ctx, client.ObjectKey{Name: isvcName, Namespace: svc.Namespace}, &obj); err == nil {
			isvc = &obj
		}
	}

	report := &Report{
		Resource: ResourceInfo{
			Kind:      "AIMService",
			Name:      svc.Name,
			Namespace: svc.Namespace,
			Status:    string(svc.Status.Status),
		},
	}

	report.Tree = buildServiceTree(ctx, c, &svc)

	var diagnostics []Diagnostic
	for _, analyzer := range serviceAnalyzers {
		diagnostics = append(diagnostics, analyzer(ctx, c, &svc)...)
	}
	report.Diagnostics = diagnostics

	report.Conditions = extractConditions(svc.Status.Conditions)
	report.EnvVars = buildEnvEntries(ctx, c, &svc, isvc)
	report.Events = collectServiceEvents(ctx, c, &svc)

	return report, nil
}

func treeService(ctx context.Context, c client.Client, name, namespace string) (*TreeNode, error) {
	var svc aimv1alpha1.AIMService
	if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &svc); err != nil {
		return nil, fmt.Errorf("get AIMService %s/%s: %w", namespace, name, err)
	}
	return buildServiceTree(ctx, c, &svc), nil
}

// buildServiceTree walks the AIMService dependency graph and returns a tree.
func buildServiceTree(ctx context.Context, c client.Client, svc *aimv1alpha1.AIMService) *TreeNode {
	root := &TreeNode{
		Kind:   "AIMService",
		Name:   svc.Name,
		Status: string(svc.Status.Status),
	}

	// Model
	if ref := svc.Status.ResolvedModel; ref != nil {
		root.Children = append(root.Children, fetchModelNode(ctx, c, ref))
	} else {
		root.Children = append(root.Children, &TreeNode{
			Kind: "Model", Name: modelRefName(svc), Status: "(not resolved)",
		})
	}

	// Template
	if ref := svc.Status.ResolvedTemplate; ref != nil {
		root.Children = append(root.Children, fetchTemplateNode(ctx, c, ref))
	} else {
		root.Children = append(root.Children, &TreeNode{
			Kind: "Template", Name: templateRefName(svc), Status: "(not resolved)",
		})
	}

	// RuntimeConfig
	if ref := svc.Status.ResolvedRuntimeConfig; ref != nil {
		root.Children = append(root.Children, &TreeNode{
			Kind: runtimeConfigKind(ref), Name: ref.Name, Status: "Resolved",
		})
	}

	// InferenceService (derived name)
	isvcName, err := aimservice.GenerateInferenceServiceName(svc.Name, svc.Namespace)
	if err == nil {
		root.Children = append(root.Children, fetchInferenceServiceNode(ctx, c, isvcName, svc.Namespace))
	}

	// HTTPRoute (derived name)
	routeName, err := aimservice.GenerateHTTPRouteName(svc.Name, svc.Namespace)
	if err == nil {
		root.Children = append(root.Children, fetchHTTPRouteNode(ctx, c, routeName, svc.Namespace))
	}

	// TemplateCache
	if svc.Status.Cache != nil && svc.Status.Cache.TemplateCacheRef != nil {
		ref := svc.Status.Cache.TemplateCacheRef
		root.Children = append(root.Children, fetchTemplateCacheNode(ctx, c, ref, svc.Namespace))
	}

	return root
}

func fetchModelNode(ctx context.Context, c client.Client, ref *aimv1alpha1.AIMResolvedReference) *TreeNode {
	if ref.Scope == aimv1alpha1.AIMResolutionScopeCluster {
		var obj aimv1alpha1.AIMClusterModel
		node := fetchNode(ctx, c, client.ObjectKey{Name: ref.Name}, &obj, "AIMClusterModel",
			func() string { return string(obj.Status.Status) })
		node.Annotation = "(cluster-scoped)"
		return node
	}
	var obj aimv1alpha1.AIMModel
	return fetchNode(ctx, c, client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, &obj, "AIMModel",
		func() string { return string(obj.Status.Status) })
}

func fetchTemplateNode(ctx context.Context, c client.Client, ref *aimv1alpha1.AIMResolvedReference) *TreeNode {
	if ref.Scope == aimv1alpha1.AIMResolutionScopeCluster {
		var obj aimv1alpha1.AIMClusterServiceTemplate
		node := fetchNode(ctx, c, client.ObjectKey{Name: ref.Name}, &obj, "AIMClusterServiceTemplate",
			func() string { return string(obj.Status.Status) })
		node.Annotation = "(cluster-scoped)"
		return node
	}
	var obj aimv1alpha1.AIMServiceTemplate
	return fetchNode(ctx, c, client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, &obj, "AIMServiceTemplate",
		func() string { return string(obj.Status.Status) })
}

func fetchInferenceServiceNode(ctx context.Context, c client.Client, name, namespace string) *TreeNode {
	var obj servingv1beta1.InferenceService
	node := fetchNode(ctx, c, client.ObjectKey{Name: name, Namespace: namespace}, &obj, "InferenceService",
		func() string { return inferenceServiceStatus(&obj) })

	// Add pod children if ISVC exists
	if node.Status != "(not found)" && !strings.HasPrefix(node.Status, "(error") {
		node.Children = fetchPodNodes(ctx, c, name, namespace)
	}
	return node
}

func fetchHTTPRouteNode(ctx context.Context, c client.Client, name, namespace string) *TreeNode {
	var obj gatewayapiv1.HTTPRoute
	return fetchNode(ctx, c, client.ObjectKey{Name: name, Namespace: namespace}, &obj, "HTTPRoute",
		func() string { return "exists" })
}

func fetchTemplateCacheNode(ctx context.Context, c client.Client, ref *aimv1alpha1.AIMResolvedReference, namespace string) *TreeNode {
	var obj aimv1alpha1.AIMTemplateCache
	ns := ref.Namespace
	if ns == "" {
		ns = namespace
	}
	return fetchNode(ctx, c, client.ObjectKey{Name: ref.Name, Namespace: ns}, &obj, "AIMTemplateCache",
		func() string { return string(obj.Status.Status) })
}

func fetchPodNodes(ctx context.Context, c client.Client, isvcName, namespace string) []*TreeNode {
	var podList corev1.PodList
	if err := c.List(ctx, &podList,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.LabelKServeInferenceService: isvcName},
	); err != nil {
		return nil
	}
	var nodes []*TreeNode
	for i := range podList.Items {
		pod := &podList.Items[i]
		nodes = append(nodes, &TreeNode{
			Kind:       "Pod",
			Name:       pod.Name,
			Status:     podEffectiveStatus(pod),
			Annotation: pod.Spec.NodeName,
		})
	}
	return nodes
}

// podEffectiveStatus returns the most relevant status string for a pod,
// preferring container-level reasons (CrashLoopBackOff, ImagePullBackOff, etc.)
// over the pod-level phase which can be misleadingly "Running".
func podEffectiveStatus(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return fmt.Sprintf("Init:%s", cs.State.Waiting.Reason)
		}
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
			return fmt.Sprintf("Init:%s", cs.State.Terminated.Reason)
		}
	}
	return string(pod.Status.Phase)
}

// fetchNode is a generic helper that fetches a K8s object and returns a tree node.
func fetchNode(ctx context.Context, c client.Client, key client.ObjectKey, obj client.Object, kind string, statusFn func() string) *TreeNode {
	node := &TreeNode{Kind: kind, Name: key.Name}
	if err := c.Get(ctx, key, obj); err != nil {
		if apierrors.IsNotFound(err) {
			node.Status = "(not found)"
		} else {
			node.Status = fmt.Sprintf("(error: %v)", err)
		}
		return node
	}
	node.Status = statusFn()
	return node
}

func inferenceServiceStatus(isvc *servingv1beta1.InferenceService) string {
	for _, cond := range isvc.Status.Conditions {
		if cond.Type == conditionReady {
			if cond.Status == "True" {
				return conditionReady
			}
			return fmt.Sprintf("%s (%s)", cond.Reason, cond.Message)
		}
	}
	return "Unknown"
}

// extractConditions converts metav1.Conditions to display rows.
func extractConditions(conditions []metav1.Condition) []ConditionRow {
	rows := make([]ConditionRow, 0, len(conditions))
	for _, c := range conditions {
		rows = append(rows, ConditionRow{
			Type:    c.Type,
			Status:  string(c.Status),
			Reason:  c.Reason,
			Message: c.Message,
		})
	}
	return rows
}

// buildEnvEntries reads the actual env vars from the InferenceService container
// and annotates each with its originating AIM resource layer.
// Returns nil when the InferenceService hasn't been created yet.
func buildEnvEntries(ctx context.Context, c client.Client, svc *aimv1alpha1.AIMService, isvc *servingv1beta1.InferenceService) []EnvEntry {
	if isvc == nil || len(isvc.Spec.Predictor.Containers) == 0 {
		return nil
	}
	actualEnv := isvc.Spec.Predictor.Containers[0].Env

	// Fetch AIM resource layers for source attribution only
	var templateSpec *aimv1alpha1.AIMServiceTemplateSpecCommon
	if ref := svc.Status.ResolvedTemplate; ref != nil {
		templateSpec = fetchTemplateSpec(ctx, c, ref)
	}

	var runtimeConfig *aimv1alpha1.AIMRuntimeConfigCommon
	rcName := svc.Spec.Name
	rcResult := aimruntimeconfig.FetchMergedRuntimeConfig(ctx, c, rcName, svc.Namespace)
	if rcResult.Value != nil {
		runtimeConfig = rcResult.Value
	}

	return annotateEnvSources(actualEnv, svc, templateSpec, runtimeConfig)
}

func fetchTemplateSpec(ctx context.Context, c client.Client, ref *aimv1alpha1.AIMResolvedReference) *aimv1alpha1.AIMServiceTemplateSpecCommon {
	if ref.Scope == aimv1alpha1.AIMResolutionScopeCluster {
		var tpl aimv1alpha1.AIMClusterServiceTemplate
		if err := c.Get(ctx, client.ObjectKey{Name: ref.Name}, &tpl); err != nil {
			return nil
		}
		return &tpl.Spec.AIMServiceTemplateSpecCommon
	}
	var tpl aimv1alpha1.AIMServiceTemplate
	if err := c.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, &tpl); err != nil {
		return nil
	}
	return &tpl.Spec.AIMServiceTemplateSpecCommon
}

// annotateEnvSources determines which AIM resource layer each env var likely originated from.
// It checks layers in precedence order (service > template > runtimeConfig > system).
func annotateEnvSources(actual []corev1.EnvVar, svc *aimv1alpha1.AIMService, templateSpec *aimv1alpha1.AIMServiceTemplateSpecCommon, runtimeConfig *aimv1alpha1.AIMRuntimeConfigCommon) []EnvEntry {
	serviceEnv := envSet(svc.Spec.Env)
	var templateEnv map[string]struct{}
	if templateSpec != nil {
		templateEnv = envSet(templateSpec.Env)
	}
	var runtimeEnv map[string]struct{}
	if runtimeConfig != nil {
		runtimeEnv = envSet(runtimeConfig.Env)
	}

	entries := make([]EnvEntry, 0, len(actual))
	for _, e := range actual {
		source := envSourceSystem
		if _, ok := runtimeEnv[e.Name]; ok {
			source = "RuntimeConfig"
		}
		if _, ok := templateEnv[e.Name]; ok {
			source = "Template"
		}
		if _, ok := serviceEnv[e.Name]; ok {
			source = "Service"
		}
		entries = append(entries, EnvEntry{Name: e.Name, Value: e.Value, Source: source})
	}
	return entries
}

func envSet(vars []corev1.EnvVar) map[string]struct{} {
	m := make(map[string]struct{}, len(vars))
	for _, v := range vars {
		m[v.Name] = struct{}{}
	}
	return m
}

// collectServiceEvents fetches recent events for the service and its InferenceService.
func collectServiceEvents(ctx context.Context, c client.Client, svc *aimv1alpha1.AIMService) []EventRow {
	var allEvents []corev1.Event

	// Events for the AIMService itself
	allEvents = append(allEvents, fetchEventsForObject(ctx, c, svc.Name, svc.Namespace, svc.UID)...)

	// Events for the derived InferenceService
	if isvcName, err := aimservice.GenerateInferenceServiceName(svc.Name, svc.Namespace); err == nil {
		var isvc servingv1beta1.InferenceService
		if err := c.Get(ctx, client.ObjectKey{Name: isvcName, Namespace: svc.Namespace}, &isvc); err == nil {
			allEvents = append(allEvents, fetchEventsForObject(ctx, c, isvc.Name, isvc.Namespace, isvc.UID)...)
		}
	}

	sort.Slice(allEvents, func(i, j int) bool {
		return allEvents[i].LastTimestamp.After(allEvents[j].LastTimestamp.Time)
	})

	if len(allEvents) > maxEvents {
		allEvents = allEvents[:maxEvents]
	}

	rows := make([]EventRow, 0, len(allEvents))
	for _, e := range allEvents {
		rows = append(rows, EventRow{
			Age:     formatAge(e.LastTimestamp.Time),
			Type:    e.Type,
			Reason:  e.Reason,
			Object:  fmt.Sprintf("%s/%s", e.InvolvedObject.Kind, e.InvolvedObject.Name),
			Message: e.Message,
		})
	}
	return rows
}

func fetchEventsForObject(ctx context.Context, c client.Client, name, namespace string, uid types.UID) []corev1.Event {
	var eventList corev1.EventList
	// involvedObject.name is a server-side field selector supported by the API server
	if err := c.List(ctx, &eventList,
		client.InNamespace(namespace),
		client.MatchingFields{"involvedObject.name": name},
	); err != nil {
		return nil
	}
	var filtered []corev1.Event
	for _, e := range eventList.Items {
		if e.InvolvedObject.UID == uid {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func formatAge(t time.Time) string {
	if t.IsZero() {
		return "<unknown>"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func modelRefName(svc *aimv1alpha1.AIMService) string {
	if svc.Spec.Model.Name != nil {
		return *svc.Spec.Model.Name
	}
	if svc.Spec.Model.Image != nil {
		return *svc.Spec.Model.Image
	}
	return "(unknown)"
}

func templateRefName(svc *aimv1alpha1.AIMService) string {
	if svc.Spec.Template.Name != "" {
		return svc.Spec.Template.Name
	}
	return "(auto-select)"
}

func runtimeConfigKind(ref *aimv1alpha1.AIMResolvedReference) string {
	if ref.Scope == aimv1alpha1.AIMResolutionScopeCluster {
		return "AIMClusterRuntimeConfig"
	}
	return "AIMRuntimeConfig"
}
