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

package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/ackctl/metadata"
)

func overridesPath() string {
	return filepath.Join("..", "..", "metadata", "overrides.json")
}

func loadCommittedOverrides(t *testing.T) *Overrides {
	t.Helper()
	o, err := LoadOverrides(overridesPath())
	require.NoError(t, err, "the committed override table must load")
	return o
}

// TestCommittedOverridesLoad catches a malformed or unexplained override at test time rather
// than when someone next regenerates.
func TestCommittedOverridesLoad(t *testing.T) {
	o := loadCommittedOverrides(t)
	assert.NotEmpty(t, o.Resources, "the table should not be silently empty")
}

// TestNoOrphanedOverrides catches an override keyed to a resource that no longer exists, which
// does nothing forever and reads as settled history rather than dead config.
func TestNoOrphanedOverrides(t *testing.T) {
	o := loadCommittedOverrides(t)
	cat, err := metadata.Load()
	require.NoError(t, err)

	known := map[string]bool{}
	for _, r := range cat.Resources() {
		known[r.Service+":"+r.ResourceDir] = true
	}
	for _, u := range cat.Unsupported() {
		known[u.Resource] = true
	}

	for key := range o.Resources {
		assert.True(t, known[key],
			"resources override %q names no resource in the catalog: it is dead config, "+
				"and reads as though the case is already handled", key)
	}
	for key := range o.Unsupported {
		assert.True(t, known[key], "unsupported override %q names no resource in the catalog", key)
	}
	for key := range o.Bindings {
		resourceKey, _, ok := strings.Cut(key, ".")
		require.True(t, ok, "binding override key %q must be \"service:resource_dir.key\"", key)
		assert.True(t, known[resourceKey],
			"binding override %q names no resource in the catalog", key)
	}
}

// TestBindingOverridesApplyToTheCatalog checks a binding override is visible in the shipped
// catalog, since these entries escape the name-agreement rule and nothing else verifies them.
func TestBindingOverridesApplyToTheCatalog(t *testing.T) {
	o := loadCommittedOverrides(t)
	cat, err := metadata.Load()
	require.NoError(t, err)

	bindingsOf := map[string]map[string]string{}
	for _, r := range cat.Resources() {
		m := map[string]string{}
		for _, tmpl := range r.Templates {
			for _, b := range tmpl.Bindings {
				m[b.Key] = b.From
			}
		}
		bindingsOf[r.Service+":"+r.ResourceDir] = m
	}

	for key, ov := range o.Bindings {
		resourceKey, field, _ := strings.Cut(key, ".")
		bindings, supported := bindingsOf[resourceKey]
		if !supported {
			// Withheld for an unrelated reason; the override is not reachable but
			// is not wrong either. TestNoOrphanedOverrides covers a bad key.
			continue
		}
		assert.Equal(t, ov.From, bindings[field],
			"binding override %q is not reflected in the catalog: it did not apply", key)
	}
}

// TestUnsupportedOverrideReasonsReachUsers pins the reason text to what the CLI prints, which
// is the entire explanation a user gets.
func TestUnsupportedOverrideReasonsReachUsers(t *testing.T) {
	o := loadCommittedOverrides(t)
	cat, err := metadata.Load()
	require.NoError(t, err)

	reasons := map[string]string{}
	for _, u := range cat.Unsupported() {
		reasons[u.Resource] = u.Reason
	}
	for key, ov := range o.Unsupported {
		assert.Equal(t, ov.Reason, reasons[key],
			"unsupported override %q: the catalog reason differs from the override's, "+
				"so the user is told something other than what was decided", key)
	}
}

// TestOverridesFileIsFormatted keeps the table reviewable, since it is read in review far more
// often than it is written.
func TestOverridesFileIsFormatted(t *testing.T) {
	raw, err := os.ReadFile(overridesPath())
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "\t",
		"overrides.json is indented with spaces; a tab will show up as noise in review")
	assert.True(t, strings.HasSuffix(string(raw), "}\n"),
		"overrides.json should end with a single trailing newline")
}
