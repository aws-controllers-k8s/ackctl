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

package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/ackctl/internal/tagging"
)

func TestParseTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want []tagging.Filter
	}{
		{"single pair", []string{"env=prod"},
			[]tagging.Filter{{Key: "env", Value: "prod"}}},
		{"several pairs", []string{"env=prod", "team=platform"},
			[]tagging.Filter{{Key: "env", Value: "prod"}, {Key: "team", Value: "platform"}}},
		{"bare key matches any value", []string{"Environment"},
			[]tagging.Filter{{Key: "Environment", Any: true}}},
		{"mixed bare and valued", []string{"env=prod", "Owner"},
			[]tagging.Filter{{Key: "env", Value: "prod"}, {Key: "Owner", Any: true}}},
		{"value keeps later separators", []string{"url=https://x/y=1"},
			[]tagging.Filter{{Key: "url", Value: "https://x/y=1"}}},
		{"case is preserved", []string{"Env=Prod"},
			[]tagging.Filter{{Key: "Env", Value: "Prod"}}},
		{"surrounding whitespace is trimmed", []string{"  env=prod  "},
			[]tagging.Filter{{Key: "env", Value: "prod"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTags(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestParseTags_EmptyValueIsNotAnyValue pins the distinction AWS makes and an earlier
// version of this code lost: a tag may hold an empty value, a different query from "this
// key with any value".
func TestParseTags_EmptyValueIsNotAnyValue(t *testing.T) {
	empty, err := parseTags([]string{"env="})
	require.NoError(t, err)
	assert.Equal(t, []tagging.Filter{{Key: "env", Value: "", Any: false}}, empty,
		"KEY= must ask for an empty value")

	anyVal, err := parseTags([]string{"env"})
	require.NoError(t, err)
	assert.Equal(t, []tagging.Filter{{Key: "env", Any: true}}, anyVal,
		"a bare KEY must ask for any value")

	assert.NotEqual(t, empty, anyVal, "the two must not collapse into the same query")
}

// TestParseTags_ValueMayContainSpaces covers a real AWS tag value. Splitting one flag
// string on whitespace silently turned "Name=my bucket" into two conditions, so each
// occurrence is now taken whole.
func TestParseTags_ValueMayContainSpaces(t *testing.T) {
	got, err := parseTags([]string{"Name=my bucket"})
	require.NoError(t, err)
	assert.Equal(t, []tagging.Filter{{Key: "Name", Value: "my bucket"}}, got)
}

// TestParseTags_RejectsDuplicateKey covers a selector that reads like a union but cannot
// match anything, since GetResources ANDs its tag filters.
func TestParseTags_RejectsDuplicateKey(t *testing.T) {
	_, err := parseTags([]string{"env=prod", "env=dev"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "twice")
}

func TestParseTags_RejectsEmptyKey(t *testing.T) {
	_, err := parseTags([]string{"=prod"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty key")
}

func TestParseTags_RejectsEmptyOccurrence(t *testing.T) {
	_, err := parseTags([]string{"env=prod", "  "})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

// TestAdoptionSetLengthIsValidated covers the half of the check that was missing: the
// charset was validated but not the length, so an over-long value was only rejected by the
// API server at apply time.
func TestAdoptionSetLengthIsValidated(t *testing.T) {
	prevSet, prevTags, prevDel := adoptAdoptionSet, adoptTags, adoptDeletionPolicy
	t.Cleanup(func() { adoptAdoptionSet, adoptTags, adoptDeletionPolicy = prevSet, prevTags, prevDel })
	adoptTags = []string{"env=prod"}
	adoptDeletionPolicy = "retain"

	adoptAdoptionSet = strings.Repeat("a", maxLabelValueLen+1)
	err := runAdopt(nil, nil)
	require.Error(t, err, "an over-long adoption set must be rejected before any AWS call")
	assert.Contains(t, err.Error(), "63")
}
