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
	"math/rand"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/ackctl/internal/emit"
	"github.com/aws-controllers-k8s/ackctl/internal/resolver"
	"github.com/aws-controllers-k8s/ackctl/metadata"
)

// testEmitOpts is runAdopt's default option set: observe-only, in one region.
func testEmitOpts() emit.Options {
	return emit.Options{
		Policy:         emit.PolicyAdopt,
		ReadOnly:       true,
		DeletionPolicy: emit.DeletionPolicyRetain,
		AdoptionSet:    "platform-prod",
		Namespace:      "default",
		Region:         "us-west-2",
	}
}

// buildDoc mirrors runAdopt's emit path: resolve each ARN, name it, build the
// CR, sort by name, and render. It takes ARNs in a caller-chosen order so tests
// can simulate the Tagging API returning pages in a different order.
func buildDoc(t *testing.T, arns []string) []byte {
	t.Helper()
	catalog, err := metadata.Load()
	require.NoError(t, err)
	res, err := resolver.New(catalog)
	require.NoError(t, err)
	target, ok := catalog.LookupByServiceKind("eks", "Nodegroup")
	require.True(t, ok)

	var crs []*emit.CR
	for _, arn := range arns {
		r, err := res.ResolveARNForResource(arn, target)
		require.NoError(t, err)
		cr, err := emit.Build(r, emit.DefaultName(r, arn), testEmitOpts())
		require.NoError(t, err)
		crs = append(crs, cr)
	}
	sort.Slice(crs, func(i, j int) bool {
		return crs[i].Metadata.Name < crs[j].Metadata.Name
	})
	doc, err := emit.Document(crs)
	require.NoError(t, err)
	return doc
}

var nodegroupARNs = []string{
	"arn:aws:eks:us-west-2:111122223333:nodegroup/prod-cluster/ng-general/a1b2-uuid",
	"arn:aws:eks:us-west-2:111122223333:nodegroup/prod-cluster/ng-spot/c3d4-uuid",
	"arn:aws:eks:us-west-2:111122223333:nodegroup/prod-cluster/ng-gpu/e5f6-uuid",
	"arn:aws:eks:us-west-2:111122223333:nodegroup/staging-cluster/ng-general/g7h8-uuid",
	"arn:aws:eks:us-east-1:111122223333:nodegroup/prod-cluster/ng-general/i9j0-uuid",
}

// TestConsecutiveRunsAreIdentical is the core idempotency property: running
// ack again with the same inputs must produce byte-identical output, so
// each AWS resource maps to the same CR (and `kubectl create` recognises it as
// already adopted rather than creating a duplicate).
func TestConsecutiveRunsAreIdentical(t *testing.T) {
	first := buildDoc(t, nodegroupARNs)
	for i := 0; i < 5; i++ {
		assert.Equal(t, string(first), string(buildDoc(t, nodegroupARNs)),
			"run %d differed from the first run", i+2)
	}
}

// TestOutputIsIndependentOfDiscoveryOrder is what makes the above hold against
// a real AWS account: GetResources is paginated and gives no ordering guarantee,
// so the same resources can arrive in a different order between runs. Sorting by
// the (unique) CR name must normalise that away.
func TestOutputIsIndependentOfDiscoveryOrder(t *testing.T) {
	want := buildDoc(t, nodegroupARNs)

	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20; i++ {
		shuffled := make([]string, len(nodegroupARNs))
		copy(shuffled, nodegroupARNs)
		rng.Shuffle(len(shuffled), func(a, b int) {
			shuffled[a], shuffled[b] = shuffled[b], shuffled[a]
		})
		assert.Equal(t, string(want), string(buildDoc(t, shuffled)),
			"output changed when discovery order changed (shuffle %d)", i)
	}
}

// TestAdoptionFieldsJSONIsStable guards the one place a Go map reaches the
// output: adoption-fields is json.Marshal'd from map[string]string. Marshal
// sorts map keys, so the annotation is byte-stable — but that is a property we
// depend on, so assert it rather than assume it.
func TestAdoptionFieldsJSONIsStable(t *testing.T) {
	catalog, err := metadata.Load()
	require.NoError(t, err)
	res, err := resolver.New(catalog)
	require.NoError(t, err)
	target, ok := catalog.LookupByServiceKind("eks", "Nodegroup")
	require.True(t, ok)

	arn := nodegroupARNs[0]
	var first string
	for i := 0; i < 50; i++ {
		r, err := res.ResolveARNForResource(arn, target)
		require.NoError(t, err)
		cr, err := emit.Build(r, "n", testEmitOpts())
		require.NoError(t, err)
		got := cr.Metadata.Annotations["services.k8s.aws/adoption-fields"]
		if i == 0 {
			first = got
			continue
		}
		require.Equal(t, first, got, "adoption-fields JSON key order is unstable")
	}
}

// TestSortKeyIsTotalOrder: sorting by name only yields a stable document if no
// two CRs share a name. Ties would leave those CRs in AWS's arbitrary
// pagination order, making consecutive runs differ.
func TestSortKeyIsTotalOrder(t *testing.T) {
	catalog, err := metadata.Load()
	require.NoError(t, err)
	res, err := resolver.New(catalog)
	require.NoError(t, err)
	target, ok := catalog.LookupByServiceKind("eks", "Nodegroup")
	require.True(t, ok)

	seen := map[string]string{}
	for _, arn := range nodegroupARNs {
		r, err := res.ResolveARNForResource(arn, target)
		require.NoError(t, err)
		name := emit.DefaultName(r, arn)
		prev, dup := seen[name]
		require.False(t, dup, "name %q generated for both %s and %s", name, prev, arn)
		seen[name] = arn
	}
}
