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
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Options configures a diagnostic run.
type Options struct {
	ResourceType string
	Name         string
	Namespace    string
}

// Report is the diagnostic output for a single AIM resource.
type Report struct {
	Resource   ResourceInfo
	Tree       *TreeNode
	Conditions []ConditionRow
	EnvVars    []EnvEntry // nil for non-service resources
	Events     []EventRow
}

// ResourceInfo identifies the root resource being diagnosed.
type ResourceInfo struct {
	Kind      string
	Name      string
	Namespace string
	Status    string
}

// TreeNode represents one node in the dependency tree.
type TreeNode struct {
	Kind       string
	Name       string
	Status     string
	Annotation string
	Children   []*TreeNode
}

// ConditionRow holds one Kubernetes condition for display.
type ConditionRow struct {
	Type    string
	Status  string
	Reason  string
	Message string
}

// EnvEntry is a merged environment variable with source attribution.
type EnvEntry struct {
	Name   string
	Value  string
	Source string
}

// EventRow holds one Kubernetes event for display.
type EventRow struct {
	Age     string
	Type    string
	Reason  string
	Object  string
	Message string
}

// Run diagnoses the named AIM resource and returns a report.
func Run(ctx context.Context, c client.Client, opts Options) (*Report, error) {
	switch normalizeResourceType(opts.ResourceType) {
	case "service":
		return debugService(ctx, c, opts.Name, opts.Namespace)
	default:
		return nil, fmt.Errorf("unsupported resource type %q (supported: service)", opts.ResourceType)
	}
}

var resourceAliases = map[string]string{
	"svc":         "service",
	"service":     "service",
	"services":    "service",
	"aimservice":  "service",
	"aimservices": "service",
}

func normalizeResourceType(rt string) string {
	lower := strings.ToLower(rt)
	if mapped, ok := resourceAliases[lower]; ok {
		return mapped
	}
	return lower
}
