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
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiBold   = "\033[1m"
	ansiFaint  = "\033[2m"
)

// ColorEnabled reports whether color output should be used for the given writer.
// Color is enabled when w is os.Stdout or os.Stderr and the file descriptor
// refers to a terminal, unless NO_COLOR is set.
func ColorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if f, ok := w.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}

func colorizeStatus(status string, color bool) string {
	if !color {
		return status
	}
	lower := strings.ToLower(status)
	switch {
	case lower == "ready" || lower == "running" || lower == "exists" || lower == "resolved":
		return ansiGreen + status + ansiReset
	case lower == "failed" || strings.Contains(lower, "backoff") ||
		strings.Contains(lower, "error") || strings.Contains(lower, "not found"):
		return ansiRed + status + ansiReset
	case lower == "pending" || lower == "starting" || lower == "progressing" ||
		lower == "unknown" || strings.HasPrefix(lower, "(not "):
		return ansiYellow + status + ansiReset
	default:
		return status
	}
}

func colorizeSeverity(sev Severity, color bool) string {
	if !color {
		return string(sev)
	}
	switch sev {
	case SeverityError:
		return ansiRed + ansiBold + string(sev) + ansiReset
	case SeverityWarning:
		return ansiYellow + string(sev) + ansiReset
	case SeverityInfo:
		return ansiCyan + string(sev) + ansiReset
	default:
		return string(sev)
	}
}

func colorizeConditionStatus(status string, color bool) string {
	if !color {
		return status
	}
	switch status {
	case "True":
		return ansiGreen + status + ansiReset
	case "False":
		return ansiRed + status + ansiReset
	default:
		return ansiYellow + status + ansiReset
	}
}

func colorizeFaint(s string, color bool) string {
	if !color {
		return s
	}
	return ansiFaint + s + ansiReset
}

func colorizeBold(s string, color bool) string {
	if !color {
		return s
	}
	return ansiBold + s + ansiReset
}
