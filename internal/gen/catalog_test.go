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

	"github.com/aws-controllers-k8s/ackctl/metadata"
)

func grammar(ns string, resources ...SvcRefResource) SvcRef {
	return SvcRef{ns: &SvcRefService{Namespace: ns, Resources: resources}}
}

func resource(typ string, templates ...string) SvcRefResource {
	r := SvcRefResource{Type: typ}
	for _, t := range templates {
		r.Forms = append(r.Forms, newARNForm(t))
	}
	return r
}

func nodegroup() Upstream {
	return Upstream{
		Service: "eks", ResourceDir: "nodegroup", Kind: "Nodegroup",
		Group: "eks.services.k8s.aws", Version: "v1alpha1",
		Required: []string{"clusterName", "name"},
	}
}

func keysOf(bindings []metadata.Binding) []string {
	out := make([]string, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, b.Key)
	}
	return out
}

func reasonsByKey(c *Catalog) map[string]string {
	out := map[string]string{}
	for _, u := range c.Unsupported {
		out[u.Resource] = u.Reason
	}
	return out
}

func build(t *testing.T, in BuildInput) *Catalog {
	t.Helper()
	if in.Overrides == nil {
		in.Overrides = &Overrides{}
	}
	cat, err := Build(in)
	require.NoError(t, err)
	return cat
}

func TestBuild_AutoDerivation(t *testing.T) {
	cat := build(t, BuildInput{
		Upstream: []Upstream{nodegroup()},
		Grammar: grammar("eks", resource("nodegroup",
			"arn:${Partition}:eks:${Region}:${Account}:nodegroup/${ClusterName}/${NodegroupName}/${UUID}")),
	})

	require.Len(t, cat.Resources, 1)
	require.Empty(t, cat.Unsupported)
	got := cat.Resources[0]
	assert.Equal(t, "eks:nodegroup", got.ResourceTypeFilter)
	require.Len(t, got.Templates, 1)
	bindings := got.Templates[0].Bindings
	assert.Equal(t, []string{"clusterName", "name"}, keysOf(bindings))
	assert.Equal(t, "${ClusterName}", bindings[0].From)
	assert.Equal(t, "${NodegroupName}", bindings[1].From)
}

// TestBuild_BindingOrderFollowsACK: bindings must come out in ACK's declaration
// order, not the order placeholders appear in the ARN. CR names are built by
// walking bindings in sequence, so re-ordering them would rename every
// already-adopted resource and defeat the one-CR-per-resource guarantee.
func TestBuild_BindingOrderFollowsACK(t *testing.T) {
	u := Upstream{
		Service: "wafv2", ResourceDir: "web_acl", Kind: "WebACL",
		Group: "wafv2.services.k8s.aws", Version: "v1alpha1",
		Required: []string{"name", "id", "scope"},
	}
	cat := build(t, BuildInput{
		Upstream: []Upstream{u},
		Grammar: grammar("wafv2", resource("webacl",
			"arn:${Partition}:wafv2:${Region}:${Account}:${Scope}/webacl/${Name}/${Id}")),
	})
	require.Len(t, cat.Resources, 1)
	assert.Equal(t, []string{"name", "id", "scope"}, keysOf(cat.Resources[0].Templates[0].Bindings))
}

// TestBuild_NothingIsDropped is the catalog's headline invariant: every scanned
// resource appears in exactly one of the two lists. An absent resource is
// indistinguishable from one the user mistyped, so a gap must always carry a
// reason.
func TestBuild_NothingIsDropped(t *testing.T) {
	upstream := []Upstream{
		nodegroup(),
		{Service: "kms", ResourceDir: "grant", Kind: "Grant", Required: []string{"grantID"}},
		{Service: "acmpca", ResourceDir: "certificate_authority_activation",
			Kind: "CertificateAuthorityActivation", Required: nil},
		{Service: "nope", ResourceDir: "thing", Kind: "Thing", Required: []string{"name"}},
	}
	cat := build(t, BuildInput{
		Upstream: upstream,
		Grammar: grammar("eks", resource("nodegroup",
			"arn:${Partition}:eks:${Region}:${Account}:nodegroup/${ClusterName}/${NodegroupName}/${UUID}")),
		Overrides: &Overrides{Unsupported: map[string]*UnsupportedOverride{
			"kms:grant": {Why: "grants carry no tags", Reason: "sub-resource not independently taggable"},
		}},
	})
	assert.Len(t, cat.Resources, 1)
	assert.Equal(t, len(upstream), len(cat.Resources)+len(cat.Unsupported),
		"every scanned resource must be supported or explained")

	reasons := reasonsByKey(cat)
	assert.Equal(t, "sub-resource not independently taggable", reasons["kms:grant"])
	assert.Equal(t, reasonNoIdentifiers, reasons["acmpca:certificate_authority_activation"],
		"a controller that declares no adoption identifiers must be reported, not dropped")
	assert.Equal(t, reasonNoGrammarService, reasons["nope:thing"])
}

// TestBuild_EmptyContractIsReported pins the acmpca:CertificateAuthorityActivation
// case: PopulateResourceFromAnnotation exists but is a bare `return nil`, so there
// is no identifier to put in adoption-fields. The scanner used to conflate that
// with "no such function" and drop the resource without a trace.
func TestBuild_EmptyContractIsReported(t *testing.T) {
	cat := build(t, BuildInput{
		Upstream: []Upstream{{Service: "acmpca", ResourceDir: "ca_activation",
			Kind: "CertificateAuthorityActivation", Required: nil}},
	})
	require.Empty(t, cat.Resources)
	require.Len(t, cat.Unsupported, 1)
	assert.Equal(t, reasonNoIdentifiers, cat.Unsupported[0].Reason)
	assert.Equal(t, "CertificateAuthorityActivation", cat.Unsupported[0].Kind,
		"the Kind must be recorded so `list adoptable --unsupported` can show it")
}

// TestBuild_ARNPrimaryStillNeedsTypeFilter: with nothing to decompose, a filter is
// the only thing an ARN-primary entry needs — and the only thing that can make it
// unusable. Without one the CLI cannot scope its GetResources query, so shipping
// the entry as supported would fail at runtime instead of at generation.
func TestBuild_ARNPrimaryStillNeedsTypeFilter(t *testing.T) {
	u := Upstream{Service: "sns", ResourceDir: "topic", Kind: "Topic",
		Required: []string{"arn"}, ARNPrimary: true}

	cat := build(t, BuildInput{Upstream: []Upstream{u}})
	require.Len(t, cat.Unsupported, 1)
	assert.Equal(t, reasonNoTypeFilter, cat.Unsupported[0].Reason)

	cat = build(t, BuildInput{
		Upstream: []Upstream{u},
		Grammar:  grammar("sns", resource("topic", "arn:${Partition}:sns:${Region}:${Account}:${TopicName}")),
	})
	require.Len(t, cat.Resources, 1)
	assert.Equal(t, "sns:topic", cat.Resources[0].ResourceTypeFilter)
	require.Len(t, cat.Resources[0].Templates, 1)
	assert.Equal(t, arnPrimaryTemplate, cat.Resources[0].Templates[0].ARNTemplate)
	assert.Equal(t, []metadata.Binding{{Key: "arn", From: "${ARN}"}}, cat.Resources[0].Templates[0].Bindings)
}

// TestBuild_UnsupportedOverrideWins covers the documentdb case: the ARN binds
// perfectly and the entry must STILL be withheld, because the ARN shape is shared
// with RDS and the distinguishing engine is not in it. No rule can infer that, so
// the override has to outrank successful derivation.
func TestBuild_UnsupportedOverrideWins(t *testing.T) {
	cat := build(t, BuildInput{
		Upstream: []Upstream{{Service: "documentdb", ResourceDir: "db_cluster",
			Kind: "DBCluster", Required: []string{"dbClusterIdentifier"}}},
		Grammar: grammar("rds", resource("cluster",
			"arn:${Partition}:rds:${Region}:${Account}:cluster:${DbClusterIdentifier}")),
		Overrides: &Overrides{Unsupported: map[string]*UnsupportedOverride{
			"documentdb:db_cluster": {Why: "shared ARN shape", Reason: "indistinguishable from rds:*"},
		}},
	})
	require.Empty(t, cat.Resources, "an unsupported override must outrank a clean binding")
	require.Len(t, cat.Unsupported, 1)
	assert.Equal(t, "indistinguishable from rds:*", cat.Unsupported[0].Reason)
}

// TestBuild_ResourceOverrideSuppliesTemplate covers the RDS abbreviations: the
// grammar labels the type "pg", which no normalization reaches from
// db_parameter_group. The override says which template to use; the key still binds
// by the ordinary name rule, so the override cannot smuggle in a mis-binding.
func TestBuild_ResourceOverrideSuppliesTemplate(t *testing.T) {
	cat := build(t, BuildInput{
		Upstream: []Upstream{{Service: "rds", ResourceDir: "db_parameter_group",
			Kind: "DBParameterGroup", Required: []string{"name"}}},
		Overrides: &Overrides{Resources: map[string]*ResourceOverride{
			"rds:db_parameter_group": {
				Why:                "label is the abbreviation 'pg'",
				ResourceTypeFilter: "rds:pg",
				ARNTemplates:       []string{"arn:${Partition}:rds:${Region}:${Account}:pg:${ParameterGroupName}"},
			},
		}},
	})
	require.Len(t, cat.Resources, 1)
	assert.Equal(t, "rds:pg", cat.Resources[0].ResourceTypeFilter)
	assert.Equal(t, "${ParameterGroupName}", cat.Resources[0].Templates[0].Bindings[0].From)
}

// TestBuild_BindingOverrideEscapesNameRule covers the two cases no name rule can
// reach: an abbreviation (${NaclId}) and a value needing literal text around the
// placeholder (an OU id, whose "ou-" prefix the template writes separately).
func TestBuild_BindingOverrideEscapesNameRule(t *testing.T) {
	tmpl := "arn:${Partition}:organizations::${Account}:ou/o-${OrganizationId}/ou-${OrganizationalUnitId}"
	cat := build(t, BuildInput{
		Upstream: []Upstream{{Service: "organizations", ResourceDir: "organizational_unit",
			Kind: "OrganizationalUnit", Required: []string{"id"}}},
		Grammar: grammar("organizations", resource("organizationalunit", tmpl)),
		Overrides: &Overrides{Bindings: map[string]*BindingOverride{
			"organizations:organizational_unit.id": {
				Why:  "the template writes the ou- prefix literally",
				From: "ou-${OrganizationalUnitId}",
			},
		}},
	})
	require.Len(t, cat.Resources, 1, "the override must rescue a binding the name rule refuses")
	assert.Equal(t, "ou-${OrganizationalUnitId}", cat.Resources[0].Templates[0].Bindings[0].From)
}

// TestBuild_BindingOverrideMustReferenceARealPlaceholder guards against an override
// outliving the template it was written for. The only runtime symptom would be "skipped"
// lines on stderr.
func TestBuild_BindingOverrideMustReferenceARealPlaceholder(t *testing.T) {
	_, err := Build(BuildInput{
		Upstream: []Upstream{{Service: "ec2", ResourceDir: "network_acl",
			Kind: "NetworkACL", Required: []string{"id"}}},
		Grammar: grammar("ec2", resource("network-acl",
			"arn:${Partition}:ec2:${Region}:${Account}:network-acl/${NetworkAclId}")),
		Overrides: &Overrides{Bindings: map[string]*BindingOverride{
			"ec2:network_acl.id": {Why: "abbreviation", From: "${NaclId}"},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in the ARN template")
}

// TestBuild_PicksTheFormsThatBind: a resource can publish several ARN shapes, and
// only some carry every identifier. A shape that lacks a required key is dropped;
// every shape that binds is kept, because an ARN can arrive in any of them.
func TestBuild_PicksTheFormsThatBind(t *testing.T) {
	cat := build(t, BuildInput{
		Upstream: []Upstream{{Service: "eventbridge", ResourceDir: "rule", Kind: "Rule",
			Required: []string{"name", "eventBusName"}}},
		Grammar: grammar("events", resource("rule",
			// first form lacks the bus, so it cannot bind eventBusName
			"arn:${Partition}:events:${Region}:${Account}:rule/${RuleName}",
			"arn:${Partition}:events:${Region}:${Account}:rule/${EventBusName}/${RuleName}")),
	})
	require.Len(t, cat.Resources, 1)
	require.Len(t, cat.Resources[0].Templates, 1, "the form missing a required key must be dropped")
	assert.Contains(t, cat.Resources[0].Templates[0].ARNTemplate, "${EventBusName}",
		"must fall through to the form that binds every key")
}

// TestBuild_KeepsEveryFormThatBinds is the elbv2 case: application, network and gateway
// load balancers spell their ARNs as three sibling shapes, and dropping any of them makes
// that variant silently unresolvable at runtime.
func TestBuild_KeepsEveryFormThatBinds(t *testing.T) {
	cat := build(t, BuildInput{
		Upstream: []Upstream{{Service: "elbv2", ResourceDir: "load_balancer",
			Kind: "LoadBalancer", Required: []string{"name"}}},
		Overrides: &Overrides{Resources: map[string]*ResourceOverride{
			"elbv2:load_balancer": {
				Why:                "v2 shapes only",
				ResourceTypeFilter: "elasticloadbalancing:loadbalancer",
				ARNTemplates: []string{
					"arn:${Partition}:elasticloadbalancing:${Region}:${Account}:loadbalancer/app/${LoadBalancerName}/${LoadBalancerId}",
					"arn:${Partition}:elasticloadbalancing:${Region}:${Account}:loadbalancer/net/${LoadBalancerName}/${LoadBalancerId}",
				},
			},
		}},
	})
	require.Len(t, cat.Resources, 1)
	require.Len(t, cat.Resources[0].Templates, 2, "every shape that binds must ship")
	for _, tmpl := range cat.Resources[0].Templates {
		assert.Equal(t, []metadata.Binding{{Key: "name", From: "${LoadBalancerName}"}}, tmpl.Bindings)
	}
}

// TestBuild_OptionalKeysBindPerForm: a key the controller reads but does not require is
// emitted from the shapes that carry it and simply absent from those that do not. The
// eventbridge rule's eventBusName narrows which bus the controller describes against.
func TestBuild_OptionalKeysBindPerForm(t *testing.T) {
	cat := build(t, BuildInput{
		Upstream: []Upstream{{Service: "eventbridge", ResourceDir: "rule", Kind: "Rule",
			Required: []string{"name"}, Optional: []string{"eventBusName"}}},
		Grammar: grammar("events", resource("rule",
			"arn:${Partition}:events:${Region}:${Account}:rule/${Name}",
			"arn:${Partition}:events:${Region}:${Account}:rule/${EventBusName}/${Name}")),
	})
	require.Len(t, cat.Resources, 1)
	tmpls := cat.Resources[0].Templates
	require.Len(t, tmpls, 2)
	assert.Equal(t, []string{"name"}, keysOf(tmpls[0].Bindings))
	assert.Equal(t, []string{"name", "eventBusName"}, keysOf(tmpls[1].Bindings))
	assert.Equal(t, "${EventBusName}", tmpls[1].Bindings[1].From)
}

// TestBuild_ARNPrimaryOverrideMismatch: an override whose arn_primary disagrees with what
// the controller declares would silently not apply, so it must refuse instead.
func TestBuild_ARNPrimaryOverrideMismatch(t *testing.T) {
	_, err := Build(BuildInput{
		Upstream: []Upstream{{Service: "sns", ResourceDir: "topic", Kind: "Topic",
			Required: []string{"name"}}},
		Overrides: &Overrides{Resources: map[string]*ResourceOverride{
			"sns:topic": {Why: "x", ResourceTypeFilter: "sns:topic",
				ARNTemplates: []string{arnPrimaryTemplate}, ARNPrimary: true},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "arn_primary")
}

// TestBuild_BindingOverrideKeyMustExist: an override on a key the controller never reads
// applies to nothing, and must be caught by name because an unread key does not gate
// support, so a typo on an optional one would otherwise pass silently.
func TestBuild_BindingOverrideKeyMustExist(t *testing.T) {
	for _, tc := range []struct {
		name string
		u    Upstream
	}{
		{"typo'd required key", Upstream{Service: "ec2", ResourceDir: "network_acl",
			Kind: "NetworkACL", Required: []string{"id"}}},
		{"typo'd optional key", Upstream{Service: "ec2", ResourceDir: "network_acl",
			Kind: "NetworkACL", Required: []string{"id"}, Optional: []string{"vpcID"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(BuildInput{
				Upstream: []Upstream{tc.u},
				Grammar: grammar("ec2", resource("network-acl",
					"arn:${Partition}:ec2:${Region}:${Account}:network-acl/${NaclId}")),
				Overrides: &Overrides{Bindings: map[string]*BindingOverride{
					"ec2:network_acl.identifier": {Why: "abbreviation", From: "${NaclId}"},
				}},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "does not read")
		})
	}
}

// TestBuild_BindingOverrideMustApply: a binding override exists to make its resource
// adoptable, so one whose resource still lands unsupported is a defect (a stale
// placeholder, a sibling key that no longer binds), not settled history.
func TestBuild_BindingOverrideMustApply(t *testing.T) {
	_, err := Build(BuildInput{
		// clusterName has no agreeing placeholder, so the override on id binds nothing.
		Upstream: []Upstream{{Service: "ec2", ResourceDir: "network_acl",
			Kind: "NetworkACL", Required: []string{"id", "clusterName"}}},
		Grammar: grammar("ec2", resource("network-acl",
			"arn:${Partition}:ec2:${Region}:${Account}:network-acl/${NaclId}")),
		Overrides: &Overrides{Bindings: map[string]*BindingOverride{
			"ec2:network_acl.id": {Why: "abbreviation", From: "${NaclId}"},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not make")
}

// TestBuild_BindingOverrideOnOptionalKey: an override on an optional key must reach the
// emitted bindings, since one that quietly never applied would look like a correct one.
func TestBuild_BindingOverrideOnOptionalKey(t *testing.T) {
	cat := build(t, BuildInput{
		Upstream: []Upstream{{Service: "ec2", ResourceDir: "network_acl",
			Kind: "NetworkACL", Required: []string{"id"}, Optional: []string{"vpcID"}}},
		Grammar: grammar("ec2", resource("network-acl",
			"arn:${Partition}:ec2:${Region}:${Account}:network-acl/${VpcRef}/${NaclId}")),
		Overrides: &Overrides{Bindings: map[string]*BindingOverride{
			"ec2:network_acl.id":    {Why: "abbreviation", From: "${NaclId}"},
			"ec2:network_acl.vpcID": {Why: "cryptic label", From: "${VpcRef}"},
		}},
	})
	require.Len(t, cat.Resources, 1)
	require.Len(t, cat.Resources[0].Templates, 1)
	assert.Equal(t, []metadata.Binding{
		{Key: "id", From: "${NaclId}"},
		{Key: "vpcID", From: "${VpcRef}"},
	}, cat.Resources[0].Templates[0].Bindings)
}

// TestBuild_DeterministicOutput: the catalog is committed, so regenerating with
// unchanged inputs must produce an identical file. Sorting both lists is what makes
// a real change stand out in review instead of being buried in reordering noise.
func TestBuild_DeterministicOutput(t *testing.T) {
	in := BuildInput{
		Upstream: []Upstream{
			{Service: "s3", ResourceDir: "bucket", Kind: "Bucket", Required: []string{"name"}},
			nodegroup(),
			{Service: "kms", ResourceDir: "grant", Kind: "Grant", Required: []string{"grantID"}},
		},
		Grammar: SvcRef{
			"eks": grammar("eks", resource("nodegroup",
				"arn:${Partition}:eks:${Region}:${Account}:nodegroup/${ClusterName}/${NodegroupName}/${UUID}"))["eks"],
			"s3": grammar("s3", resource("bucket", "arn:${Partition}:s3:::${BucketName}"))["s3"],
		},
		Overrides: &Overrides{},
	}
	first, err := Build(in)
	require.NoError(t, err)
	firstBytes, err := first.Marshal()
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		again, err := Build(in)
		require.NoError(t, err)
		againBytes, err := again.Marshal()
		require.NoError(t, err)
		require.Equal(t, string(firstBytes), string(againBytes), "run %d differed", i+2)
	}
	// Sorted by service then resource_dir, regardless of scan order.
	assert.Equal(t, "eks", first.Resources[0].Service)
	assert.Equal(t, "s3", first.Resources[1].Service)
}
