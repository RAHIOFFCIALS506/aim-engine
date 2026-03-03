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
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

const sectionWidth = 60

// Render writes a human-readable diagnostic report to w.
func Render(w io.Writer, r *Report) error {
	fmt.Fprintf(w, "%s/%s  [%s]\n\n", r.Resource.Kind, r.Resource.Name, r.Resource.Status)

	renderTree(w, r.Tree, "", true)
	fmt.Fprintln(w)

	if len(r.Conditions) > 0 {
		renderSection(w, "CONDITIONS")
		renderConditions(w, r.Conditions)
		fmt.Fprintln(w)
	}

	if len(r.EnvVars) > 0 {
		renderSection(w, "EFFECTIVE ENV")
		renderEnvVars(w, r.EnvVars)
		fmt.Fprintln(w)
	}

	if len(r.Events) > 0 {
		renderSection(w, "RECENT EVENTS")
		renderEvents(w, r.Events)
		fmt.Fprintln(w)
	}

	return nil
}

func renderSection(w io.Writer, title string) {
	pad := sectionWidth - len(title) - 5
	if pad < 3 {
		pad = 3
	}
	fmt.Fprintf(w, "━━ %s %s\n", title, strings.Repeat("━", pad))
}

func renderTree(w io.Writer, node *TreeNode, prefix string, isLast bool) {
	if node == nil {
		return
	}

	connector := "├── "
	if isLast {
		connector = "└── "
	}
	if prefix == "" {
		connector = ""
	}

	annotation := ""
	if node.Annotation != "" {
		annotation = "  " + node.Annotation
	}

	fmt.Fprintf(w, "%s%s%s/%s  [%s]%s\n", prefix, connector, node.Kind, node.Name, node.Status, annotation)

	childPrefix := prefix
	if prefix != "" {
		if isLast {
			childPrefix += "    "
		} else {
			childPrefix += "│   "
		}
	}

	for i, child := range node.Children {
		renderTree(w, child, childPrefix, i == len(node.Children)-1)
	}
}

func renderConditions(w io.Writer, conditions []ConditionRow) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "TYPE\tSTATUS\tREASON\tMESSAGE\n")
	for _, c := range conditions {
		msg := c.Message
		if len(msg) > 60 {
			msg = msg[:57] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Type, c.Status, c.Reason, msg)
	}
	tw.Flush()
}

func renderEnvVars(w io.Writer, envVars []EnvEntry) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, e := range envVars {
		val := e.Value
		if len(val) > 40 {
			val = val[:37] + "..."
		}
		fmt.Fprintf(tw, "%s\t= %s\t← %s\n", e.Name, val, e.Source)
	}
	tw.Flush()
}

func renderEvents(w io.Writer, events []EventRow) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "AGE\tTYPE\tREASON\tOBJECT\tMESSAGE\n")
	for _, e := range events {
		msg := e.Message
		if len(msg) > 50 {
			msg = msg[:47] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Age, e.Type, e.Reason, e.Object, msg)
	}
	tw.Flush()
}
