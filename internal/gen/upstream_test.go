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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const header = `package resource

import (
	"fmt"

	ackerrors "github.com/aws-controllers-k8s/runtime/pkg/errors"
)

type resource struct{ ko *struct{ Spec struct{ ClusterName, Name, Type *string } } }

`

// TestParseAdoptionFields_MultiKey covers the ordinary generated shape: several
// required keys, each guarded. Order must be preserved, because CR names are
// built by walking bindings in declaration order.
func TestParseAdoptionFields_MultiKey(t *testing.T) {
	code := []byte(header + `
func (r *resource) PopulateResourceFromAnnotation(fields map[string]string) error {
	f0, ok := fields["clusterName"]
	if !ok {
		return ackerrors.NewTerminalError(fmt.Errorf("required field missing: clusterName"))
	}
	r.ko.Spec.ClusterName = &f0
	f1, ok := fields["name"]
	if !ok {
		return ackerrors.NewTerminalError(fmt.Errorf("required field missing: name"))
	}
	r.ko.Spec.Name = &f1
	return nil
}
`)
	required, optional, _, err := parseAdoptionFields("resource.go", code)
	require.NoError(t, err)
	assert.Equal(t, []string{"clusterName", "name"}, required)
	assert.Empty(t, optional)
}

// TestParseAdoptionFields_UnderscoreKey is a regression test for a key named "type_",
// which the prototype's character-class regex could not match. It silently reported two
// resources as needing only "name", so the controller refused the adoption.
func TestParseAdoptionFields_UnderscoreKey(t *testing.T) {
	code := []byte(header + `
func (r *resource) PopulateResourceFromAnnotation(fields map[string]string) error {
	f0, ok := fields["name"]
	if !ok {
		return ackerrors.NewTerminalError(fmt.Errorf("required field missing: name"))
	}
	r.ko.Spec.Name = &f0
	f1, ok := fields["type_"]
	if !ok {
		return ackerrors.NewTerminalError(fmt.Errorf("required field missing: type_"))
	}
	r.ko.Spec.Type = &f1
	return nil
}
`)
	required, _, _, err := parseAdoptionFields("resource.go", code)
	require.NoError(t, err)
	assert.Equal(t, []string{"name", "type_"}, required,
		"an underscored key must be reported; missing it drops a required identifier")
}

// TestParseAdoptionFields_ARNPrimary covers a resource whose identifier is its
// ARN. The temporary is named differently from the fN convention, which must not
// matter.
func TestParseAdoptionFields_ARNPrimary(t *testing.T) {
	code := []byte(header + `
func (r *resource) PopulateResourceFromAnnotation(fields map[string]string) error {
	resourceARN, ok := fields["arn"]
	if !ok {
		return ackerrors.NewTerminalError(fmt.Errorf("required field missing: arn"))
	}
	_ = resourceARN
	return nil
}
`)
	required, optional, _, err := parseAdoptionFields("resource.go", code)
	require.NoError(t, err)
	assert.Equal(t, []string{"arn"}, required)
	assert.Empty(t, optional)
}

// TestParseAdoptionFields_UnguardedIsOptional verifies the required/optional split turns
// on the guard rather than the read. The required list is what the CLI demands, so an
// unguarded key must never be promoted into it.
func TestParseAdoptionFields_UnguardedIsOptional(t *testing.T) {
	code := []byte(header + `
func (r *resource) PopulateResourceFromAnnotation(fields map[string]string) error {
	f0, ok := fields["name"]
	if !ok {
		return ackerrors.NewTerminalError(fmt.Errorf("required field missing: name"))
	}
	r.ko.Spec.Name = &f0
	f1, ok := fields["clusterName"]
	if ok {
		r.ko.Spec.ClusterName = &f1
	}
	return nil
}
`)
	required, optional, _, err := parseAdoptionFields("resource.go", code)
	require.NoError(t, err)
	assert.Equal(t, []string{"name"}, required)
	assert.Equal(t, []string{"clusterName"}, optional)
}

// TestParseAdoptionFields_ElseBranchIsRequired is a regression test for the two entries
// that shipped an incomplete contract, where a nested spec field is read in the if's init
// and rejected from the else branch.
//
// Reading only the guarded shape reported eks:IdentityProviderConfig as needing just
// clusterName, and note the ok variable is "f0ok" so the guard must match what the read
// actually bound.
func TestParseAdoptionFields_ElseBranchIsRequired(t *testing.T) {
	code := []byte(header + `
func (r *resource) PopulateResourceFromAnnotation(fields map[string]string) error {
	primaryKey, ok := fields["clusterName"]
	if !ok {
		return ackerrors.NewTerminalError(fmt.Errorf("required field missing: clusterName"))
	}
	r.ko.Spec.ClusterName = &primaryKey

	if f0, f0ok := fields["identityProviderConfigName"]; f0ok {
		r.ko.Spec.Name = &f0
	} else {
		return ackerrors.MissingNameIdentifier
	}
	return nil
}
`)
	required, optional, _, err := parseAdoptionFields("resource.go", code)
	require.NoError(t, err)
	assert.Equal(t, []string{"clusterName", "identityProviderConfigName"}, required,
		"a key rejected from an else branch is required; dropping it ships a catalog entry the controller refuses")
	assert.Empty(t, optional)
}

// TestParseAdoptionFields_TopLevelReadWithElseGuard is the ec2:FlowLog shape:
// the same else-branch rejection, repeated, mixed with a guarded read. FlowLog
// shipped as needing only flowLogID.
func TestParseAdoptionFields_TopLevelReadWithElseGuard(t *testing.T) {
	code := []byte(header + `
func (r *resource) PopulateResourceFromAnnotation(fields map[string]string) error {
	primaryKey, ok := fields["flowLogID"]
	if !ok {
		return ackerrors.NewTerminalError(fmt.Errorf("required field missing: flowLogID"))
	}
	r.ko.Spec.Name = &primaryKey

	if resourceID, ok := fields["resourceID"]; ok {
		r.ko.Spec.ClusterName = &resourceID
	} else {
		return ackerrors.MissingNameIdentifier
	}

	if resourceType, ok := fields["resourceType"]; ok {
		r.ko.Spec.Type = &resourceType
	} else {
		return ackerrors.MissingNameIdentifier
	}
	return nil
}
`)
	required, _, _, err := parseAdoptionFields("resource.go", code)
	require.NoError(t, err)
	assert.Equal(t, []string{"flowLogID", "resourceID", "resourceType"}, required)
}

// TestParseAdoptionFields_AlternativeIdentifierNotRequired pins kafka:Cluster, which
// tests for a missing "arn" in order to offer an alternative rather than to refuse.
//
// Only the later unconditional read of "arn" is a requirement, so observations merge, and
// "name" read inside the fallback branch must not be hoisted into the contract.
func TestParseAdoptionFields_AlternativeIdentifierNotRequired(t *testing.T) {
	code := []byte(header + `
func (r *resource) PopulateResourceFromAnnotation(fields map[string]string) error {
	if _, ok := fields["arn"]; !ok {
		name, ok := fields["name"]
		if !ok {
			return ackerrors.NewTerminalError(fmt.Errorf("required field missing: one of arn or name"))
		}
		r.ko.Spec.Name = &name
		return nil
	}

	resourceARN, ok := fields["arn"]
	if !ok {
		return ackerrors.NewTerminalError(fmt.Errorf("required field missing: arn"))
	}
	_ = resourceARN
	return nil
}
`)
	required, optional, _, err := parseAdoptionFields("resource.go", code)
	require.NoError(t, err)
	assert.Equal(t, []string{"arn"}, required,
		"arn is required by its unconditional read, so the earlier presence test must not downgrade it")
	assert.Empty(t, optional,
		"name is read inside the fallback branch and is an alternative to arn, not a second requirement")
}

// TestParseAdoptionFields_NoFunc reports nothing for a resource that predates
// adoption support, rather than erroring: there is simply no contract to read.
func TestParseAdoptionFields_NoFunc(t *testing.T) {
	code := []byte(header + `
func (r *resource) Identifiers() error { return nil }
`)
	required, optional, _, err := parseAdoptionFields("resource.go", code)
	require.NoError(t, err)
	assert.Nil(t, required)
	assert.Nil(t, optional)
}

func TestParseDescriptor(t *testing.T) {
	code := []byte(`package resource

var GroupVersionResource = schema.GroupVersionResource{
	Group:    "eks.services.k8s.aws",
	Version:  "v1alpha1",
	Resource: "Nodegroups",
}

var GroupKind = metav1.GroupKind{
	Group: "eks.services.k8s.aws",
	Kind:  "Nodegroup",
}
`)
	group, kind, version, err := parseDescriptor("descriptor.go", code)
	require.NoError(t, err)
	assert.Equal(t, "eks.services.k8s.aws", group)
	assert.Equal(t, "Nodegroup", kind)
	assert.Equal(t, "v1alpha1", version)
}

// TestReleaseTagGate pins which tags may source the catalog: only a final vX.Y.Z
// at or above v0.1.0, so a pre-release never enters it.
func TestReleaseTagGate(t *testing.T) {
	eligible := func(tag string) bool {
		v, ok := parseStableSemver(tag)
		return ok && atLeast(v, minTag)
	}
	for _, tc := range []struct {
		tag  string
		want bool
	}{
		{"v0.1.0", true},
		{"v1.0.42", true},
		{"v0.0.9", false},
		{"v1.2.3-rc1", false},
		{"1.2.3", false},
		{"main", false},
		{"", false},
	} {
		assert.Equal(t, tc.want, eligible(tc.tag), "tag %q", tc.tag)
	}
}

// TestTagOrderingIsNumeric: tag selection must compare version components, not
// strings. Lexically "v1.10.0" sorts below "v1.9.0", which would pin a controller
// to an older release the moment it reached a two-digit minor.
func TestTagOrderingIsNumeric(t *testing.T) {
	newer, ok := parseStableSemver("v1.10.0")
	require.True(t, ok)
	older, ok := parseStableSemver("v1.9.0")
	require.True(t, ok)
	assert.True(t, atLeast(newer, older), "v1.10.0 must rank above v1.9.0")
	assert.False(t, atLeast(older, newer))
}
