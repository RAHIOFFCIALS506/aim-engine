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
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode/utf8"
)

const sectionWidth = 60

// truncateRunes truncates s to maxRunes runes, appending "..." if truncated.
func truncateRunes(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes-3]) + "..."
}

// RenderJSON writes v as indented JSON to w.
func RenderJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// RenderScanResult writes scan results in istioctl analyze style:
// one line per diagnostic, plus a summary footer.
func RenderScanResult(w io.Writer, result *ScanResult, scope string) {
	color := ColorEnabled(w)
	if len(result.Diagnostics) == 0 {
		fmt.Fprintf(w, "✔ No issues found when analyzing %d AIMService(s) (%s)\n",
			result.ServicesScanned, scope)
		return
	}
	for _, d := range result.Diagnostics {
		fmt.Fprintf(w, "%s [%s] (%s) %s\n",
			colorizeSeverity(d.Severity, color), d.Code, d.Resource, d.Message)
	}
	fmt.Fprintf(w, "\n%d AIMService(s) scanned (%s), %d with issues\n",
		result.ServicesScanned, scope, result.ServicesWithIssues)
}

// RenderDiagnostics writes diagnostics in a tabular format matching istioctl analyze style.
func RenderDiagnostics(w io.Writer, diagnostics []Diagnostic) {
	color := ColorEnabled(w)
	if len(diagnostics) == 0 {
		fmt.Fprintln(w, "No issues found.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "CODE\tSEVERITY\tRESOURCE\tMESSAGE\n")
	for _, d := range diagnostics {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.Code, colorizeSeverity(d.Severity, color), d.Resource, truncateRunes(d.Message, 60))
	}
	tw.Flush()
}

// RenderTreeText writes a human-readable tree to w.
func RenderTreeText(w io.Writer, tree *TreeNode) error {
	color := ColorEnabled(w)
	ew := &errWriter{w: w}
	printTreeNode(ew, tree, "", true, true, color)
	return ew.err
}

// Render writes a human-readable diagnostic report to w.
func Render(w io.Writer, r *Report) error {
	color := ColorEnabled(w)

	fmt.Fprintf(w, "%s  [%s]\n\n",
		colorizeBold(r.Resource.Kind+"/"+r.Resource.Name, color),
		colorizeStatus(r.Resource.Status, color))

	printTreeNode(w, r.Tree, "", true, true, color)
	fmt.Fprintln(w)

	if len(r.Diagnostics) > 0 {
		renderSection(w, "DIAGNOSTICS", color)
		for _, d := range r.Diagnostics {
			fmt.Fprintf(w, "%s [%s] %s\n", colorizeSeverity(d.Severity, color), d.Code, d.Message)
		}
		fmt.Fprintln(w)
	}

	if len(r.Conditions) > 0 {
		renderSection(w, "CONDITIONS", color)
		renderConditions(w, r.Conditions, color)
		fmt.Fprintln(w)
	}

	if len(r.EnvVars) > 0 {
		renderSection(w, "EFFECTIVE ENV", color)
		renderEnvVars(w, r.EnvVars, color)
		fmt.Fprintln(w)
	}

	if len(r.Events) > 0 {
		renderSection(w, "RECENT EVENTS", color)
		renderEvents(w, r.Events, color)
		fmt.Fprintln(w)
	}

	return nil
}

// errWriter wraps an io.Writer and captures the first write error.
type errWriter struct {
	w   io.Writer
	err error
}

func (ew *errWriter) Write(p []byte) (int, error) {
	if ew.err != nil {
		return 0, ew.err
	}
	n, err := ew.w.Write(p)
	if err != nil {
		ew.err = err
	}
	return n, err
}

func renderSection(w io.Writer, title string, color bool) {
	pad := sectionWidth - len(title) - 5
	if pad < 3 {
		pad = 3
	}
	line := fmt.Sprintf("━━ %s %s", title, strings.Repeat("━", pad))
	fmt.Fprintln(w, colorizeFaint(line, color))
}

func printTreeNode(w io.Writer, node *TreeNode, prefix string, isLast bool, isRoot bool, color bool) {
	if node == nil {
		return
	}

	connector := "├── "
	if isLast {
		connector = "└── "
	}
	if isRoot {
		connector = ""
	}

	annotation := ""
	if node.Annotation != "" {
		annotation = "  " + colorizeFaint(node.Annotation, color)
	}

	fmt.Fprintf(w, "%s%s%s/%s  [%s]%s\n", prefix, connector, node.Kind, node.Name, colorizeStatus(node.Status, color), annotation)

	childPrefix := prefix
	if !isRoot {
		if isLast {
			childPrefix += "    "
		} else {
			childPrefix += "│   "
		}
	}

	for i, child := range node.Children {
		printTreeNode(w, child, childPrefix, i == len(node.Children)-1, false, color)
	}
}

func renderConditions(w io.Writer, conditions []ConditionRow, color bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "TYPE\tSTATUS\tREASON\tMESSAGE\n")
	for _, c := range conditions {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Type, colorizeConditionStatus(c.Status, color), c.Reason, truncateRunes(c.Message, 60))
	}
	tw.Flush()
}

func renderEnvVars(w io.Writer, envVars []EnvEntry, color bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, e := range envVars {
		fmt.Fprintf(tw, "%s\t= %s\t%s\n", e.Name, truncateRunes(e.Value, 40), colorizeFaint("← "+e.Source, color))
	}
	tw.Flush()
}

func renderEvents(w io.Writer, events []EventRow, color bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "AGE\tTYPE\tREASON\tOBJECT\tMESSAGE\n")
	for _, e := range events {
		eventType := e.Type
		if color && e.Type == "Warning" {
			eventType = ansiYellow + e.Type + ansiReset
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Age, eventType, e.Reason, e.Object, truncateRunes(e.Message, 50))
	}
	tw.Flush()
}
