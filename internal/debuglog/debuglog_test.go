// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package debuglog

import (
	"bytes"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// reset returns the package to its default state so tests do not leak into each
// other (enabled is package-level).
func reset(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	mu.Lock()
	enabled = false
	out = &buf
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		enabled = false
		mu.Unlock()
	})
	return &buf
}

// Silence by default matters: debug output on a quiet run would be noise, and
// anything written to stdout would corrupt the YAML being piped to kubectl.
func TestDisabledByDefault(t *testing.T) {
	buf := reset(t)
	Logf("should not appear")
	assert.Empty(t, buf.String())
	assert.False(t, Enabled())
}

func TestEnabledWrites(t *testing.T) {
	buf := reset(t)
	Enable()
	assert.True(t, Enabled())
	Logf("region %s", "us-west-2")
	assert.Equal(t, "debug: region us-west-2\n", buf.String())
}

// Every line is prefixed and newline-terminated, so debug output interleaved
// with the normal stderr summary stays readable.
func TestFormatting(t *testing.T) {
	buf := reset(t)
	Enable()
	Logf("no trailing newline")
	Logf("has trailing newline\n")
	Section("discover")
	assert.Equal(t,
		"debug: no trailing newline\ndebug: has trailing newline\ndebug: ── discover ──\n",
		buf.String())
}

// Logf is called from paginated loops; concurrent use must not race or interleave
// partial lines.
func TestConcurrentLogfIsSafe(t *testing.T) {
	buf := reset(t)
	Enable()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Logf("line")
		}()
	}
	wg.Wait()
	assert.Equal(t, 50, bytes.Count(buf.Bytes(), []byte("debug: line\n")))
}
