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
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

var spinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner renders a simple braille spinner on stderr while work is in progress.
// It is a no-op when stderr is not a terminal.
type Spinner struct {
	stop chan struct{}
	done chan struct{}
	mu   sync.Mutex
}

// NewSpinner starts a spinner with the given message on stderr.
// Returns immediately; call Stop() to clear the spinner line.
// If stderr is not a terminal, the spinner is inert.
func NewSpinner(msg string) *Spinner {
	s := &Spinner{
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		close(s.done)
		return s
	}

	go func() {
		defer close(s.done)
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-s.stop:
				fmt.Fprintf(os.Stderr, "\r\033[K")
				return
			case <-ticker.C:
				fmt.Fprintf(os.Stderr, "\r%s %s", spinnerFrames[i%len(spinnerFrames)], msg)
				i++
			}
		}
	}()
	return s
}

// Stop clears the spinner line and waits for the goroutine to finish.
func (s *Spinner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stop:
		// already stopped
	default:
		close(s.stop)
	}
	<-s.done
}
