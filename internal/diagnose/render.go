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
func RenderScanResult(w io.Writer, result *ScanResult, scope string) error {
	color := ColorEnabled(w)
	ew := &errWriter{w: w}
	if len(result.Diagnostics) == 0 {
		ew.printf("✔ No issues found when analyzing %d AIMService(s) (%s)\n",
			result.ServicesScanned, scope)
		return ew.err
	}
	for _, d := range result.Diagnostics {
		ew.printf("%s [%s] (%s) %s\n",
			colorizeSeverity(d.Severity, color), d.Code, d.Resource, d.Message)
	}
	ew.printf("\n%d AIMService(s) scanned (%s), %d with issues\n",
		result.ServicesScanned, scope, result.ServicesWithIssues)
	return ew.err
}

// RenderDiagnostics writes diagnostics in a tabular format matching istioctl analyze style.
func RenderDiagnostics(w io.Writer, diagnostics []Diagnostic) error {
	color := ColorEnabled(w)
	if len(diagnostics) == 0 {
		_, err := fmt.Fprintln(w, "No issues found.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "CODE\tSEVERITY\tRESOURCE\tMESSAGE\n")
	for _, d := range diagnostics {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.Code, colorizeSeverity(d.Severity, color), d.Resource, truncateRunes(d.Message, 60))
	}
	return tw.Flush()
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
	ew := &errWriter{w: w}

	ew.printf("%s  [%s]\n\n",
		colorizeBold(r.Resource.Kind+"/"+r.Resource.Name, color),
		colorizeStatus(r.Resource.Status, color))

	printTreeNode(ew, r.Tree, "", true, true, color)
	ew.println()

	if len(r.Diagnostics) > 0 {
		renderSection(ew, "DIAGNOSTICS", color)
		for _, d := range r.Diagnostics {
			ew.printf("%s [%s] %s\n", colorizeSeverity(d.Severity, color), d.Code, d.Message)
		}
		ew.println()
	}

	if len(r.Conditions) > 0 {
		renderSection(ew, "CONDITIONS", color)
		renderConditions(ew, r.Conditions, color)
		ew.println()
	}

	if len(r.EnvVars) > 0 {
		renderSection(ew, "EFFECTIVE ENV", color)
		renderEnvVars(ew, r.EnvVars, color)
		ew.println()
	}

	if len(r.Events) > 0 {
		renderSection(ew, "RECENT EVENTS", color)
		renderEvents(ew, r.Events, color)
		ew.println()
	}

	return ew.err
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

func (ew *errWriter) printf(format string, args ...any) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintf(ew.w, format, args...)
}

func (ew *errWriter) println(args ...any) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintln(ew.w, args...)
}

func renderSection(ew *errWriter, title string, color bool) {
	pad := sectionWidth - len(title) - 5
	if pad < 3 {
		pad = 3
	}
	line := fmt.Sprintf("━━ %s %s", title, strings.Repeat("━", pad))
	ew.println(colorizeFaint(line, color))
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

	_, _ = fmt.Fprintf(w, "%s%s%s/%s  [%s]%s\n", prefix, connector, node.Kind, node.Name, colorizeStatus(node.Status, color), annotation)

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

func renderConditions(ew *errWriter, conditions []ConditionRow, color bool) {
	tw := tabwriter.NewWriter(ew, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "TYPE\tSTATUS\tREASON\tMESSAGE\n")
	for _, c := range conditions {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Type, colorizeConditionStatus(c.Status, color), c.Reason, truncateRunes(c.Message, 60))
	}
	if err := tw.Flush(); ew.err == nil {
		ew.err = err
	}
}

func renderEnvVars(ew *errWriter, envVars []EnvEntry, color bool) {
	tw := tabwriter.NewWriter(ew, 0, 0, 2, ' ', 0)
	for _, e := range envVars {
		_, _ = fmt.Fprintf(tw, "%s\t= %s\t%s\n", e.Name, truncateRunes(e.Value, 40), colorizeFaint("← "+e.Source, color))
	}
	if err := tw.Flush(); ew.err == nil {
		ew.err = err
	}
}

func renderEvents(ew *errWriter, events []EventRow, color bool) {
	tw := tabwriter.NewWriter(ew, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "AGE\tTYPE\tREASON\tOBJECT\tMESSAGE\n")
	for _, e := range events {
		eventType := e.Type
		if color && e.Type == "Warning" {
			eventType = ansiYellow + e.Type + ansiReset
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Age, eventType, e.Reason, e.Object, truncateRunes(e.Message, 50))
	}
	if err := tw.Flush(); ew.err == nil {
		ew.err = err
	}
}
