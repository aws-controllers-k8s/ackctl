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

package naming

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBind_LeafNotParent pins the five entries the prototype got wrong, where a bare
// "name" or "id" key had to choose between a parent ARN segment and its own.
//
// The prototype took the first placeholder containing "name", producing well-formed
// adoption-fields that named a real but wrong resource.
func TestBind_LeafNotParent(t *testing.T) {
	for _, tc := range []struct {
		name         string
		required     []string
		placeholders []string
		target       Target
		want         map[string]string
	}{
		{
			name:         "ecs service binds its own name, not its cluster's",
			required:     []string{"name"},
			placeholders: []string{"ClusterName", "ServiceName"},
			target:       Target{Kind: "Service", ResourceDir: "service", Service: "ecs"},
			want:         map[string]string{"name": "${ServiceName}"},
		},
		{
			name:         "s3vectors index binds the index, not its bucket",
			required:     []string{"name"},
			placeholders: []string{"BucketName", "IndexName"},
			target:       Target{Kind: "Index", ResourceDir: "index", Service: "s3vectors"},
			want:         map[string]string{"name": "${IndexName}"},
		},
		{
			name:         "s3files access point binds the access point, not its file system",
			required:     []string{"id"},
			placeholders: []string{"FileSystemId", "AccessPointId"},
			target:       Target{Kind: "AccessPoint", ResourceDir: "access_point", Service: "s3files"},
			want:         map[string]string{"id": "${AccessPointId}"},
		},
		{
			name:         "organizations OU binds the OU, not its organization",
			required:     []string{"id"},
			placeholders: []string{"OrganizationId", "OrganizationalUnitId"},
			target:       Target{Kind: "OrganizationalUnit", ResourceDir: "organizational_unit", Service: "organizations"},
			want:         map[string]string{"id": "${OrganizationalUnitId}"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Bind(tc.required, tc.placeholders, false, tc.target)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestBind_RefusesWhenIdentifierAbsent is the s3tables:Table case, whose ARN is
// "bucket/${TableBucketName}/table/${TableID}" while ACK keys it by a name the ARN does
// not carry. The prototype bound it to the BUCKET's name, so refusing is the only correct
// answer.
func TestBind_RefusesWhenIdentifierAbsent(t *testing.T) {
	_, err := Bind([]string{"name"}, []string{"TableBucketName", "TableID"}, false,
		Target{Kind: "Table", ResourceDir: "table", Service: "s3tables"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matches no placeholder")
}

func TestBind_MultiKey(t *testing.T) {
	got, err := Bind(
		[]string{"clusterName", "name"},
		[]string{"ClusterName", "NodegroupName", "UUID"},
		false,
		Target{Kind: "Nodegroup", ResourceDir: "nodegroup", Service: "eks"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"clusterName": "${ClusterName}",
		"name":        "${NodegroupName}",
	}, got)
}

// TestBind_OrderIndependent: a binding must not depend on the order placeholders
// happen to appear in the ARN. wafv2:WebACL is the real case — its ARN is
// ${Scope}/webacl/${Name}/${Id} while ACK declares [name, id, scope].
func TestBind_OrderIndependent(t *testing.T) {
	target := Target{Kind: "WebACL", ResourceDir: "web_acl", Service: "wafv2"}
	want := map[string]string{"name": "${Name}", "id": "${Id}", "scope": "${Scope}"}

	got, err := Bind([]string{"name", "id", "scope"}, []string{"Scope", "Name", "Id"}, false, target)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	// Same answer with the keys declared in a different order.
	got, err = Bind([]string{"scope", "id", "name"}, []string{"Scope", "Name", "Id"}, false, target)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestBind_AccountKey: a key naming the AWS account reads the ARN envelope, so it
// never competes for a path placeholder. quicksight resources are the real case.
func TestBind_AccountKey(t *testing.T) {
	got, err := Bind([]string{"awsAccountID", "id"}, []string{"ResourceId"}, true,
		Target{Kind: "Dashboard", ResourceDir: "dashboard", Service: "quicksight"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"awsAccountID": AccountPlaceholder,
		"id":           "${ResourceId}",
	}, got)
}

// TestBind_AccountKeyNeedsAccountInARN: if the template has no account slot, an
// account key must fail rather than silently fall through to a path segment.
func TestBind_AccountKeyNeedsAccountInARN(t *testing.T) {
	_, err := Bind([]string{"awsAccountID"}, []string{"ResourceId"}, false,
		Target{Kind: "Dashboard", ResourceDir: "dashboard", Service: "quicksight"})
	require.Error(t, err)
}

// TestBind_RefusesCompetingKeys covers two keys agreeing with the same single
// placeholder, where binding one and dropping the other would emit a partial map. Both
// scenarios turn on "identifier", the only word Agree lets stand in for another.
func TestBind_RefusesCompetingKeys(t *testing.T) {
	_, err := Bind([]string{"name", "identifier"}, []string{"Name"}, false,
		Target{Kind: "Thing", ResourceDir: "thing", Service: "svc"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no complete assignment")
}

// TestBind_RefusesAmbiguous: two keys that each agree with both placeholders, so
// two different assignments are equally defensible. Choosing one would be a coin
// flip on which AWS resource the CR adopts, so Bind refuses.
func TestBind_RefusesAmbiguous(t *testing.T) {
	_, err := Bind([]string{"name", "identifier"}, []string{"Name", "Identifier"}, false,
		Target{Kind: "Thing", ResourceDir: "thing", Service: "svc"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguously")
}

// TestBind_DistinctNounsAreNotAmbiguous is the counterpart: name and id are
// specific, so [name id] against [Name Id] has exactly one assignment. An earlier
// version equated all head nouns and reported this real wafv2-shaped case as
// ambiguous, which would have pushed a working resource into unsupported.
func TestBind_DistinctNounsAreNotAmbiguous(t *testing.T) {
	got, err := Bind([]string{"name", "id"}, []string{"Name", "Id"}, false,
		Target{Kind: "Thing", ResourceDir: "thing", Service: "svc"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"name": "${Name}", "id": "${Id}"}, got)
}

func TestSplitWords(t *testing.T) {
	for in, want := range map[string][]string{
		"clusterName":          {"cluster", "name"},
		"NaclId":               {"nacl", "id"},
		"DBInstanceName":       {"db", "instance", "name"},
		"IamIdentityAccountID": {"iam", "identity", "account", "id"},
		"access_point":         {"access", "point"},
		"code signing config":  {"code", "signing", "config"},
		"type_":                {"type"},
		"ARN":                  {"arn"},
		"s3vectors":            {"s3vectors"},
	} {
		assert.Equal(t, want, SplitWords(in), "SplitWords(%q)", in)
	}
}

// TestAgree_RealConventionGaps pins the pairs that legitimately differ between
// ACK's and AWS's naming conventions. Each was a false positive from an earlier,
// whole-string version of this check; treating them as disagreements would push
// ~20 working resources into unsupported.
func TestAgree_RealConventionGaps(t *testing.T) {
	for _, tc := range []struct {
		key, placeholder, kind, dir, service string
	}{
		{"name", "RepositoryName", "Repository", "repository", "ecr"},
		{"name", "AlarmName", "MetricAlarm", "metric_alarm", "cloudwatch"},
		{"name", "RoleNameWithPath", "Role", "role", "iam"},
		{"name", "ParameterNameWithoutLeadingSlash", "Parameter", "parameter", "ssm"},
		{"name", "GroupFriendlyName", "AutoScalingGroup", "auto_scaling_group", "autoscaling"},
		{"name", "SubnetGroupName", "DBSubnetGroup", "db_subnet_group", "rds"},
		{"name", "ClusterParameterGroupName", "DBClusterParameterGroup", "db_cluster_parameter_group", "rds"},
		{"id", "PrefixListId", "ManagedPrefixList", "managed_prefix_list", "ec2"},
		{"id", "ResourceId", "Analysis", "analysis", "quicksight"},
		{"dbInstanceIdentifier", "DbInstanceName", "DBInstance", "db_instance", "rds"},
		{"domainName", "DomainName", "DomainName", "domain_name", "apigatewayv2"},
	} {
		assert.True(t, Agree(tc.key, tc.placeholder, tc.kind, tc.dir, tc.service),
			"%s should agree with ${%s} for %s", tc.key, tc.placeholder, tc.kind)
	}
}

// TestAgree_RejectsParentSegments is the other side: a qualifier the resource type
// does not imply means the placeholder belongs to something else.
func TestAgree_RejectsParentSegments(t *testing.T) {
	for _, tc := range []struct {
		key, placeholder, kind, dir, service string
	}{
		{"name", "ClusterName", "Service", "service", "ecs"},
		{"name", "BucketName", "Index", "index", "s3vectors"},
		{"name", "TableBucketName", "Table", "table", "s3tables"},
		{"id", "FileSystemId", "AccessPoint", "access_point", "s3files"},
		{"id", "OrganizationId", "OrganizationalUnit", "organizational_unit", "organizations"},
		{"clusterName", "NodegroupName", "Nodegroup", "nodegroup", "eks"},
	} {
		assert.False(t, Agree(tc.key, tc.placeholder, tc.kind, tc.dir, tc.service),
			"%s must NOT agree with ${%s} for %s", tc.key, tc.placeholder, tc.kind)
	}
}
