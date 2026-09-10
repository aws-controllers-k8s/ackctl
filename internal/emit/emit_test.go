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

package emit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/ackctl/internal/resolver"
	"github.com/aws-controllers-k8s/ackctl/internal/tagging"
	"github.com/aws-controllers-k8s/ackctl/metadata"
)

func nodegroupResolved() *resolver.Resolved {
	return nodegroupFor("my-cluster", "my-ng")
}

func nodegroupFor(cluster, ng string) *resolver.Resolved {
	return &resolver.Resolved{
		Fields: map[string]string{"clusterName": cluster, "name": ng},
		Resource: metadata.Resource{
			Kind:    "Nodegroup",
			Group:   "eks.services.k8s.aws",
			Version: "v1alpha1",
			Bindings: []metadata.Binding{
				{Key: "clusterName", From: "${ClusterName}"},
				{Key: "name", From: "${NodegroupName}"},
			},
		},
	}
}

const testSet = "platform-prod"
const testARN = "arn:aws:eks:us-west-2:111:nodegroup/my-cluster/my-ng/uuid"
const testRegion = "us-west-2"

// testOpts is the default flag surface: observe-only.
func testOpts() Options {
	return Options{
		Policy:         PolicyAdopt,
		ReadOnly:       true,
		DeletionPolicy: DeletionPolicyRetain,
		AdoptionSet:    testSet,
		Region:         testRegion,
	}
}

func TestBuild_AdoptPolicy(t *testing.T) {
	opts := testOpts()
	opts.Namespace = "default"
	cr, err := Build(nodegroupResolved(), "ng-my-ng", opts)
	require.NoError(t, err)

	assert.Equal(t, "eks.services.k8s.aws/v1alpha1", cr.APIVersion)
	assert.Equal(t, "Nodegroup", cr.Kind)
	assert.Equal(t, "ng-my-ng", cr.Metadata.Name)
	assert.Equal(t, "default", cr.Metadata.Namespace)

	ann := cr.Metadata.Annotations
	assert.Equal(t, "adopt", ann[annotationAdoptionPolicy])

	// adoption-fields must be valid JSON with exactly the resolved keys.
	var fields map[string]string
	require.NoError(t, json.Unmarshal([]byte(ann[annotationAdoptionFields]), &fields))
	assert.Equal(t, map[string]string{"clusterName": "my-cluster", "name": "my-ng"}, fields)

	assert.Empty(t, cr.Spec, "spec must be omitted for adopt (controller populates it)")
}

// TestBuild_ObserveOnlyByDefault pins the defaults that make a first run safe, since adoption
// points a CR at infrastructure ACK did not create.
func TestBuild_ObserveOnlyByDefault(t *testing.T) {
	cr, err := Build(nodegroupResolved(), "ng", testOpts())
	require.NoError(t, err)

	ann := cr.Metadata.Annotations
	// ACK models read-only adoption as adoption-policy:adopt PLUS a separate
	// read-only annotation, not as a policy value.
	assert.Equal(t, "adopt", ann[annotationAdoptionPolicy])
	assert.Equal(t, "true", ann[annotationReadOnly])
	assert.Equal(t, "retain", ann[annotationDeletionPolicy])
}

func TestBuild_ManagedIsOptIn(t *testing.T) {
	opts := testOpts()
	opts.ReadOnly = false
	opts.DeletionPolicy = DeletionPolicyDelete
	cr, err := Build(nodegroupResolved(), "ng", opts)
	require.NoError(t, err)

	ann := cr.Metadata.Annotations
	// Emitted explicitly rather than omitted: a generated manifest should state
	// all three decisions, so reading one CR answers "may ACK change this?"
	// without knowing the runtime's defaults.
	assert.Equal(t, "false", ann[annotationReadOnly])
	assert.Equal(t, "delete", ann[annotationDeletionPolicy])
}

// TestBuild_EmitsQueryRegion covers the correctness requirement in Build's doc: the runtime
// prefers this annotation and will not override it.
func TestBuild_EmitsQueryRegion(t *testing.T) {
	cr, err := Build(nodegroupResolved(), "ng", testOpts())
	require.NoError(t, err)
	assert.Equal(t, testRegion, cr.Metadata.Annotations[annotationRegion])

	// owner-account-id is deliberately absent: it drives ACK's CARM path and can
	// stop a controller resolving a role on a cluster with no CARM config.
	assert.NotContains(t, cr.Metadata.Annotations, "services.k8s.aws/owner-account-id")
}

func TestBuild_RefusesWithoutRegion(t *testing.T) {
	opts := testOpts()
	opts.Region = ""
	_, err := Build(nodegroupResolved(), "ng", opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "region")
}

func TestParseDeletionPolicy(t *testing.T) {
	for _, ok := range []string{"retain", "delete"} {
		_, err := ParseDeletionPolicy(ok)
		assert.NoError(t, err, ok)
	}
	_, err := ParseDeletionPolicy("Retain")
	assert.Error(t, err, "the annotation value is lowercase; accepting other casing would emit an invalid one")
}

// TestDefaultAdoptionSet_DerivedFromSelector keeps the generated set name stable across
// identical runs, which a random or time-based suffix would break.
func TestDefaultAdoptionSet_DerivedFromSelector(t *testing.T) {
	tags := []tagging.Filter{{Key: "env", Value: "prod"}, {Key: "team", Value: "platform"}}
	got := DefaultAdoptionSet("eks", "Nodegroup", "us-west-2", tags)
	assert.Regexp(t, `^eks-nodegroup-[0-9a-f]{8}$`, got)
	assert.Equal(t, got, DefaultAdoptionSet("eks", "Nodegroup", "us-west-2", tags),
		"same selector must derive the same set name")

	// A different selector gets its own set, so two collections stay distinct.
	assert.NotEqual(t, got, DefaultAdoptionSet("eks", "Nodegroup", "us-east-1", tags))
	assert.NotEqual(t, got, DefaultAdoptionSet("eks", "Nodegroup", "us-west-2",
		[]tagging.Filter{{Key: "env", Value: "dev"}, {Key: "team", Value: "platform"}}))
	// Tag keys are hashed with their values, so a key/value shuffle that spells
	// the same concatenation must still differ.
	assert.NotEqual(t,
		DefaultAdoptionSet("eks", "Nodegroup", "us-west-2", []tagging.Filter{{Key: "a", Value: "bc"}}),
		DefaultAdoptionSet("eks", "Nodegroup", "us-west-2", []tagging.Filter{{Key: "ab", Value: "c"}}))
}

func bucketResolved(name string) *resolver.Resolved {
	return &resolver.Resolved{
		Fields: map[string]string{"name": name},
		Resource: metadata.Resource{Kind: "Bucket",
			Bindings: []metadata.Binding{{Key: "name", From: "${BucketName}"}}},
	}
}

func TestDefaultName(t *testing.T) {
	// name = every identifier value (the primary key) + an ARN digest.
	name := DefaultName(nodegroupResolved(), testARN)
	assert.Regexp(t, `^my-cluster-my-ng-[0-9a-f]{8}$`, name)

	// ARN-primary: bindings carry only "arn", so use the ARN tail.
	arnPrimary := &resolver.Resolved{
		Fields: map[string]string{"arn": "arn:aws:sns:us-east-1:1:my-topic"},
		Resource: metadata.Resource{Kind: "Topic",
			Bindings: []metadata.Binding{{Key: "arn", From: "${ARN}"}}},
	}
	name = DefaultName(arnPrimary, "arn:aws:sns:us-east-1:1:my-topic")
	assert.Regexp(t, `^my-topic-[0-9a-f]{8}$`, name)
}

// TestDefaultName_PreventsDoubleAdoption is why names derive from the resource's identity
// rather than the adoption set: ACK has no cross-CR ownership guard.
func TestDefaultName_PreventsDoubleAdoption(t *testing.T) {
	r := nodegroupFor("prod-cluster", "workers")
	arn := "arn:aws:eks:us-west-2:111:nodegroup/prod-cluster/workers/uuid-a"

	// Same AWS resource => same CR name, every time, regardless of adoption set.
	assert.Equal(t, DefaultName(r, arn), DefaultName(r, arn),
		"re-adopting the same resource must regenerate the same name")
}

// TestDefaultName_NoCollisions regression-tests the confirmed collision bugs: using only the
// last identifier value collapsed distinct resources.
func TestDefaultName_NoCollisions(t *testing.T) {
	t.Run("multi-key resources stay distinct", func(t *testing.T) {
		a := DefaultName(nodegroupFor("prod-cluster", "workers"),
			"arn:aws:eks:us-west-2:111:nodegroup/prod-cluster/workers/uuid-a")
		b := DefaultName(nodegroupFor("staging-cluster", "workers"),
			"arn:aws:eks:us-west-2:111:nodegroup/staging-cluster/workers/uuid-b")
		assert.NotEqual(t, a, b)
		assert.Contains(t, a, "prod-cluster-workers")
		assert.Contains(t, b, "staging-cluster-workers")
	})

	t.Run("lossy sanitization stays distinct via the digest", func(t *testing.T) {
		// "My_Bucket.Prod" sanitizes to the same string as "my-bucket-prod".
		a := DefaultName(bucketResolved("My_Bucket.Prod"), "arn:aws:s3:::My_Bucket.Prod")
		b := DefaultName(bucketResolved("my-bucket-prod"), "arn:aws:s3:::my-bucket-prod")
		assert.NotEqual(t, a, b, "sanitization must not flatten distinct AWS names")
	})

	t.Run("same name in different regions stays distinct", func(t *testing.T) {
		// Most ACK resources are keyed by NAME, so identical identifiers exist
		// legitimately in other regions/accounts. The ARN digest separates them.
		east := DefaultName(nodegroupFor("my-cluster", "workers"),
			"arn:aws:eks:us-east-1:111:nodegroup/my-cluster/workers/uuid")
		west := DefaultName(nodegroupFor("my-cluster", "workers"),
			"arn:aws:eks:us-west-2:111:nodegroup/my-cluster/workers/uuid")
		assert.NotEqual(t, east, west, "same identifiers in two regions must not collide")
	})

	t.Run("same name in different accounts stays distinct", func(t *testing.T) {
		a := DefaultName(bucketResolved("shared"), "arn:aws:s3::111:shared")
		b := DefaultName(bucketResolved("shared"), "arn:aws:s3::222:shared")
		assert.NotEqual(t, a, b)
	})

	t.Run("names are deterministic across runs", func(t *testing.T) {
		arn := "arn:aws:s3:::My_Bucket.Prod"
		r := bucketResolved("My_Bucket.Prod")
		assert.Equal(t, DefaultName(r, arn), DefaultName(r, arn))
	})

	t.Run("respects the DNS-1123 length limit and keeps the digest", func(t *testing.T) {
		long := strings.Repeat("x", 300)
		r := nodegroupFor(long, long)
		name := DefaultName(r, "arn:aws:eks:us-west-2:111:nodegroup/"+long+"/"+long+"/u")
		assert.LessOrEqual(t, len(name), 253)
		assert.Regexp(t, `-[0-9a-f]{8}$`, name, "digest must survive truncation")
	})
}

func TestBuild_AdoptionSetLabel(t *testing.T) {
	opts := testOpts()
	opts.Namespace = "ns"
	cr, err := Build(nodegroupResolved(), "n", opts)
	require.NoError(t, err)
	assert.Equal(t, testSet, cr.Metadata.Labels[labelAdoptionSet])
}

func TestDocument_MultiCR(t *testing.T) {
	cr1, _ := Build(nodegroupResolved(), "a", testOpts())
	cr2, _ := Build(nodegroupResolved(), "b", testOpts())
	doc, err := Document([]*CR{cr1, cr2})
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(doc), "---\n"))
}

// TestDocument_SafeToConcatenate regression-tests output that did not prefix its first
// document, which made two concatenated runs lose everything after the first CR.
func TestDocument_SafeToConcatenate(t *testing.T) {
	cr, _ := Build(nodegroupResolved(), "a", testOpts())
	run, err := Document([]*CR{cr})
	require.NoError(t, err)

	// Every document, including the first, must be introduced by a separator.
	assert.True(t, strings.HasPrefix(string(run), "---\n"),
		"output must start with a separator so runs concatenate safely")

	// Two runs joined end-to-end must still yield two separated documents.
	combined := string(run) + string(run)
	assert.Equal(t, 2, strings.Count(combined, "---\n"))
	assert.Equal(t, 2, strings.Count(combined, "kind: Nodegroup"))
}

// TestDocument_Golden pins the exact manifest a run emits. Every line is load-bearing and
// breaks into valid YAML a reviewer would skim past.
func TestDocument_Golden(t *testing.T) {
	r := nodegroupFor("prod-cluster", "ng-general")
	arn := "arn:aws:eks:us-west-2:111122223333:nodegroup/prod-cluster/ng-general/a1b2-uuid"
	opts := testOpts()
	opts.Namespace = "default"

	cr, err := Build(r, DefaultName(r, arn), opts)
	require.NoError(t, err)
	doc, err := Document([]*CR{cr})
	require.NoError(t, err)

	const want = `---
apiVersion: eks.services.k8s.aws/v1alpha1
kind: Nodegroup
metadata:
  annotations:
    services.k8s.aws/adoption-fields: '{"clusterName":"prod-cluster","name":"ng-general"}'
    services.k8s.aws/adoption-policy: adopt
    services.k8s.aws/deletion-policy: retain
    services.k8s.aws/read-only: "true"
    services.k8s.aws/region: us-west-2
  labels:
    ack.k8s.aws/adoption-set: platform-prod
  name: prod-cluster-ng-general-21ca235d
  namespace: default
`
	assert.Equal(t, want, string(doc))
}
