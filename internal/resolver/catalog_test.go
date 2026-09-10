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

package resolver

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/ackctl/metadata"
)

// Header values used to fill the ARN envelope of every synthetic ARN. They are
// distinct from the path sentinels so a binding that reads the account when it
// meant a path segment (or vice versa) shows up as a mismatch rather than a
// coincidental pass.
const (
	testPartition = "aws"
	testRegion    = "us-west-2"
	testAccount   = "123456789012"
)

// sentinel embeds the placeholder's own name so a failure says which segment was read. It
// holds no ':' or '/', the delimiters the compiled template splits on.
func sentinel(placeholder string) string {
	return "sentinel-" + strings.ToLower(placeholder)
}

// TestCatalogRoundTrips fills every placeholder with a unique sentinel and asserts each
// binding renders what its From says.
//
// It cannot prove a key is bound to the RIGHT placeholder, since the expected values come
// from the same bindings under test; that is left to the real-ARN cases in resolver_test.go.
func TestCatalogRoundTrips(t *testing.T) {
	catalog, err := metadata.Load()
	require.NoError(t, err)
	res, err := New(catalog)
	require.NoError(t, err)

	resources := catalog.Resources()
	require.NotEmpty(t, resources, "embedded catalog is empty")

	for _, r := range resources {
		t.Run(r.Service+"/"+r.Kind, func(t *testing.T) {
			require.NotEmpty(t, r.ResourceTypeFilter,
				"supported resource has no Tagging API type filter")
			assertTypeFilterWellFormed(t, r.ResourceTypeFilter)
			require.NotEmpty(t, r.Bindings, "supported resource has no bindings")

			if r.ARNPrimary {
				assertARNPrimaryRoundTrips(t, res, r)
				return
			}

			arnStr, want := synthesize(t, r)
			got, rerr := res.ResolveARNForResource(arnStr, r)
			require.NoError(t, rerr, "synthetic ARN built from this resource's own template must resolve\n  arn: %s", arnStr)

			assert.Equal(t, want, got.Fields,
				"each binding must recover its own placeholder\n  template: %s\n  arn:      %s",
				r.ARNTemplate, arnStr)
		})
	}
}

// assertTypeFilterWellFormed rejects a filter the Tagging API cannot accept.
// lambda:CodeSigningConfig shipped as "lambda:code signing config" and matched nothing.
func assertTypeFilterWellFormed(t *testing.T, tf string) {
	t.Helper()
	service, resourceType, found := strings.Cut(tf, ":")
	require.True(t, found, "type filter %q is not service:resource-type", tf)
	assert.NotEmpty(t, service, "type filter %q has an empty service", tf)
	assert.NotEmpty(t, resourceType, "type filter %q has an empty resource type", tf)
	for _, part := range []string{service, resourceType} {
		assert.NotContains(t, part, " ", "type filter %q contains a space", tf)
	}
}

func assertARNPrimaryRoundTrips(t *testing.T, res *Resolver, r metadata.Resource) {
	t.Helper()
	require.Len(t, r.Bindings, 1, "an ARN-primary resource binds exactly one key")
	assert.Equal(t, "arn", r.Bindings[0].Key)
	assert.Equal(t, "${ARN}", r.Bindings[0].From)

	arnStr := fmt.Sprintf("arn:%s:%s:%s:%s:%s/sentinel-name",
		testPartition, strings.SplitN(r.ResourceTypeFilter, ":", 2)[0],
		testRegion, testAccount, strings.SplitN(r.ResourceTypeFilter, ":", 2)[1])
	got, err := res.ResolveARNForResource(arnStr, r)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"arn": arnStr}, got.Fields)
}

// synthesize substitutes a unique sentinel for every placeholder and returns the
// adoption-fields map a correct resolver must produce.
func synthesize(t *testing.T, r metadata.Resource) (string, map[string]string) {
	t.Helper()

	subst := func(s string) string {
		return placeholderRE.ReplaceAllStringFunc(s, func(match string) string {
			name := placeholderRE.FindStringSubmatch(match)[1]
			switch strings.ToLower(name) {
			case "partition":
				return testPartition
			case "region":
				return testRegion
			case "account":
				return testAccount
			default:
				return sentinel(name)
			}
		})
	}

	arnStr := subst(r.ARNTemplate)
	require.NotContains(t, arnStr, "${",
		"unsubstituted placeholder left in synthetic ARN for %s/%s", r.Service, r.Kind)

	want := make(map[string]string, len(r.Bindings))
	for _, b := range r.Bindings {
		from := b.From
		// ${ARN} means the whole matched ARN, which is only knowable after the
		// rest of the template is rendered.
		from = strings.ReplaceAll(from, "${ARN}", arnStr)
		want[b.Key] = subst(from)
	}
	return arnStr, want
}
