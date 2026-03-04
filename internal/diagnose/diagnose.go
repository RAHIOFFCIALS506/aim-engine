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
	Resource    ResourceInfo   `json:"resource"`
	Tree        *TreeNode      `json:"tree"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
	Conditions  []ConditionRow `json:"conditions"`
	EnvVars     []EnvEntry     `json:"envVars,omitempty"`
	Events      []EventRow     `json:"events,omitempty"`
}

// ResourceInfo identifies the root resource being diagnosed.
type ResourceInfo struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
}

// TreeNode represents one node in the dependency tree.
type TreeNode struct {
	Kind       string      `json:"kind"`
	Name       string      `json:"name"`
	Status     string      `json:"status"`
	Annotation string      `json:"annotation,omitempty"`
	Children   []*TreeNode `json:"children,omitempty"`
}

// ConditionRow holds one Kubernetes condition for display.
type ConditionRow struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}

// EnvEntry is a merged environment variable with source attribution.
type EnvEntry struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

// EventRow holds one Kubernetes event for display.
type EventRow struct {
	Age     string `json:"age"`
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Object  string `json:"object"`
	Message string `json:"message"`
}

// Severity indicates the severity level of a diagnostic finding.
type Severity string

const (
	SeverityError   Severity = "Error"
	SeverityWarning Severity = "Warning"
	SeverityInfo    Severity = "Info"
)

// DiagnosticCode defines a diagnostic rule with its default severity and message template.
type DiagnosticCode struct {
	Code     string
	Severity Severity
	Template string
}

var (
	AIM001 = DiagnosticCode{Code: "AIM001", Severity: SeverityError, Template: "AIMService is in Failed state"}
	AIM003 = DiagnosticCode{Code: "AIM003", Severity: SeverityError, Template: "Pod container not running"}
)

// Diagnostic represents a single diagnostic finding from the analyzer.
type Diagnostic struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Resource string   `json:"resource"`
	Message  string   `json:"message"`
}

// NewDiagnostic creates a Diagnostic from a DiagnosticCode with an optional detail suffix.
func NewDiagnostic(dc DiagnosticCode, resource, detail string) Diagnostic {
	msg := dc.Template
	if detail != "" {
		msg = dc.Template + ": " + detail
	}
	return Diagnostic{
		Code:     dc.Code,
		Severity: dc.Severity,
		Resource: resource,
		Message:  msg,
	}
}

// ScanResult holds the aggregated output from scanning multiple resources.
type ScanResult struct {
	Diagnostics        []Diagnostic `json:"diagnostics"`
	ServicesScanned    int          `json:"servicesScanned"`
	ServicesWithIssues int          `json:"servicesWithIssues"`
}

// HasErrors returns true if any diagnostic has Error severity.
func HasErrors(diagnostics []Diagnostic) bool {
	for _, d := range diagnostics {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}

// CountErrors returns the number of diagnostics with Error severity.
func CountErrors(diagnostics []Diagnostic) int {
	n := 0
	for _, d := range diagnostics {
		if d.Severity == SeverityError {
			n++
		}
	}
	return n
}

type resourceHandler struct {
	debug   func(ctx context.Context, c client.Client, name, ns string) (*Report, error)
	tree    func(ctx context.Context, c client.Client, name, ns string) (*TreeNode, error)
	analyze func(ctx context.Context, c client.Client, name, ns string) ([]Diagnostic, error)
}

var handlers = map[string]resourceHandler{
	"service": {
		debug:   debugService,
		tree:    treeService,
		analyze: analyzeService,
	},
}

func dispatch[T any](opts Options, pick func(resourceHandler) func(context.Context, client.Client, string, string) (T, error)) (func(context.Context, client.Client, string, string) (T, error), error) {
	rt := normalizeResourceType(opts.ResourceType)
	h, ok := handlers[rt]
	if !ok {
		supported := make([]string, 0, len(handlers))
		for k := range handlers {
			supported = append(supported, k)
		}
		return nil, fmt.Errorf("unsupported resource type %q (supported: %s)", opts.ResourceType, strings.Join(supported, ", "))
	}
	return pick(h), nil
}

// Run diagnoses the named AIM resource and returns a report.
func Run(ctx context.Context, c client.Client, opts Options) (*Report, error) {
	fn, err := dispatch(opts, func(h resourceHandler) func(context.Context, client.Client, string, string) (*Report, error) {
		return h.debug
	})
	if err != nil {
		return nil, err
	}
	return fn(ctx, c, opts.Name, opts.Namespace)
}

// Tree returns the dependency tree for the named AIM resource.
func Tree(ctx context.Context, c client.Client, opts Options) (*TreeNode, error) {
	fn, err := dispatch(opts, func(h resourceHandler) func(context.Context, client.Client, string, string) (*TreeNode, error) {
		return h.tree
	})
	if err != nil {
		return nil, err
	}
	return fn(ctx, c, opts.Name, opts.Namespace)
}

// Analyze runs diagnostic rules against the named AIM resource.
func Analyze(ctx context.Context, c client.Client, opts Options) ([]Diagnostic, error) {
	fn, err := dispatch(opts, func(h resourceHandler) func(context.Context, client.Client, string, string) ([]Diagnostic, error) {
		return h.analyze
	})
	if err != nil {
		return nil, err
	}
	return fn(ctx, c, opts.Name, opts.Namespace)
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
