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

// Package debuglog provides opt-in diagnostic logging, enabled with --debug or
// ACK_DEBUG=1. Everything goes to stderr so debug output never contaminates the YAML
// on stdout.
package debuglog

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

var (
	mu      sync.Mutex
	enabled bool
	out     io.Writer = os.Stderr
)

func Enable() {
	mu.Lock()
	defer mu.Unlock()
	enabled = true
}

// Enabled reports whether debug logging is on, for callers that want to skip
// building an expensive message.
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return enabled
}

func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	out = w
}

// Logf writes a debug line. Nothing is emitted unless debugging is enabled.
func Logf(format string, args ...interface{}) {
	mu.Lock()
	defer mu.Unlock()
	if !enabled {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	fmt.Fprint(out, "debug: "+msg)
}

// Section writes a visually distinct heading, so a long debug run is scannable.
func Section(name string) {
	Logf("── %s ──", name)
}
