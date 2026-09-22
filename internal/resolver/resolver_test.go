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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/ackctl/metadata"
)

func newResolver(t *testing.T) (*Resolver, *metadata.Catalog) {
	t.Helper()
	catalog, err := metadata.Load()
	require.NoError(t, err)
	r, err := New(catalog)
	require.NoError(t, err)
	return r, catalog
}

// resolve is the test-side mirror of the CLI flow: look up the resource by the
// user-supplied service + kind, then decompose the ARN against it.
func resolve(t *testing.T, r *Resolver, c *metadata.Catalog, service, kind, arnStr string) (*Resolved, error) {
	t.Helper()
	res, ok := c.LookupByServiceKind(service, kind)
	require.True(t, ok, "resource %s/%s not in catalog", service, kind)
	return r.ResolveARNForResource(arnStr, res)
}

// TestResolveARN_RealARNs mirrors the design POC's real-ARN spot check: concrete,
// hand-verified ARNs must decompose into exactly the adoption-fields ACK expects.
// These are the cases most likely to break (multi-key, order-mismatch, colon
// separators, account-in-header).
func TestResolveARN_RealARNs(t *testing.T) {
	r, c := newResolver(t)

	cases := []struct {
		name    string
		service string
		kind    string
		arn     string
		fields  map[string]string
	}{
		{
			name:    "s3 bucket (flat ARN, single key)",
			service: "s3", kind: "Bucket",
			arn:    "arn:aws:s3:::my-prod-bucket",
			fields: map[string]string{"name": "my-prod-bucket"},
		},
		{
			name:    "ecr repository (stutter-strip, optional registryID from the header)",
			service: "ecr", kind: "Repository",
			arn:    "arn:aws:ecr:us-east-1:123456789012:repository/my-repo",
			fields: map[string]string{"name": "my-repo", "registryID": "123456789012"},
		},
		{
			name:    "ecr repository with a namespace (name contains '/')",
			service: "ecr", kind: "Repository",
			arn:    "arn:aws:ecr:us-east-1:123456789012:repository/team/my-repo",
			fields: map[string]string{"name": "team/my-repo", "registryID": "123456789012"},
		},
		{
			name:    "cloudwatch log group (name is a path)",
			service: "cloudwatchlogs", kind: "LogGroup",
			arn:    "arn:aws:logs:us-east-1:123456789012:log-group:/aws/lambda/my-fn",
			fields: map[string]string{"name": "/aws/lambda/my-fn"},
		},
		{
			name:    "cloudwatch alarm with '/' in the name (auto-created target tracking)",
			service: "cloudwatch", kind: "MetricAlarm",
			arn:    "arn:aws:cloudwatch:us-east-1:123456789012:alarm:TargetTracking-table/my-table-AlarmHigh-guid",
			fields: map[string]string{"name": "TargetTracking-table/my-table-AlarmHigh-guid"},
		},
		{
			name:    "rds db instance (colon-type, via override)",
			service: "rds", kind: "DBInstance",
			arn:    "arn:aws:rds:us-east-1:123456789012:db:my-db-instance",
			fields: map[string]string{"dbInstanceIdentifier": "my-db-instance"},
		},
		{
			name:    "eks nodegroup (multi-key hierarchical, trailing uuid ignored)",
			service: "eks", kind: "Nodegroup",
			arn:    "arn:aws:eks:us-west-2:123456789012:nodegroup/my-cluster/my-ng/a1b2c3d4-uuid",
			fields: map[string]string{"clusterName": "my-cluster", "name": "my-ng"},
		},
		{
			name:    "eks addon (multi-key hierarchical)",
			service: "eks", kind: "Addon",
			arn:    "arn:aws:eks:us-west-2:123456789012:addon/my-cluster/vpc-cni/abcd1234",
			fields: map[string]string{"clusterName": "my-cluster", "name": "vpc-cni"},
		},
		{
			name:    "bedrock agent runtime endpoint (ARN order != ACK key order)",
			service: "bedrockagentcorecontrol", kind: "AgentRuntimeEndpoint",
			arn:    "arn:aws:bedrock-agentcore:us-west-2:123456789012:runtime/rt-abc123/runtime-endpoint/prod",
			fields: map[string]string{"name": "prod", "agentRuntimeID": "rt-abc123"},
		},
		{
			name:    "quicksight dashboard (account from ARN header)",
			service: "quicksight", kind: "Dashboard",
			arn:    "arn:aws:quicksight:us-east-1:123456789012:dashboard/my-dash-id",
			fields: map[string]string{"awsAccountID": "123456789012", "id": "my-dash-id"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolve(t, r, c, tc.service, tc.kind, tc.arn)
			require.NoError(t, err)
			assert.Equal(t, tc.kind, got.Resource.Kind)
			assert.Equal(t, tc.fields, got.Fields)
		})
	}
}

// TestResolveARN_Overrides exercises override-resolved resources on real ARNs: cryptic
// grammar labels, colon-separated types, extra placeholders and service aliases, where the
// generator validates that keys bind and these assert the values extract correctly.
func TestResolveARN_Overrides(t *testing.T) {
	r, c := newResolver(t)

	cases := []struct {
		name    string
		service string
		kind    string
		arn     string
		fields  map[string]string
	}{
		{"rds parameter group (cryptic 'pg' label)", "rds", "DBParameterGroup",
			"arn:aws:rds:us-east-1:111:pg:my-pg", map[string]string{"name": "my-pg"}},
		{"rds subnet group (cryptic 'subgrp' label)", "rds", "DBSubnetGroup",
			"arn:aws:rds:us-east-1:111:subgrp:my-subgrp", map[string]string{"name": "my-subgrp"}},
		{"rds cluster", "rds", "DBCluster",
			"arn:aws:rds:us-east-1:111:cluster:my-cluster", map[string]string{"dbClusterIdentifier": "my-cluster"}},
		{"mq broker (extra BrokerName placeholder ignored)", "mq", "Broker",
			"arn:aws:mq:us-east-1:111:broker:my-broker:b-1234", map[string]string{"brokerID": "b-1234"}},
		{"eventbridge rule (default-bus form)", "eventbridge", "Rule",
			"arn:aws:events:us-east-1:111:rule/my-rule", map[string]string{"name": "my-rule"}},
		{"eventbridge rule on a custom bus (two-segment form binds the bus too)", "eventbridge", "Rule",
			"arn:aws:events:us-east-1:111:rule/my-bus/my-rule",
			map[string]string{"name": "my-rule", "eventBusName": "my-bus"}},
		{"application load balancer (v2 three-segment form)", "elbv2", "LoadBalancer",
			"arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/app/my-alb/50dc6c495c0c9188",
			map[string]string{"name": "my-alb"}},
		{"network load balancer (v2 three-segment form)", "elbv2", "LoadBalancer",
			"arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/net/my-nlb/50dc6c495c0c9188",
			map[string]string{"name": "my-nlb"}},
		{"ecs task definition (family, revision ignored)", "ecs", "TaskDefinition",
			"arn:aws:ecs:us-east-1:111:task-definition/my-fam:3", map[string]string{"family": "my-fam"}},
		{"prometheus rule groups namespace (multi-key)", "prometheusservice", "RuleGroupsNamespace",
			"arn:aws:aps:us-east-1:111:rulegroupsnamespace/ws-abc/my-ns",
			map[string]string{"workspaceID": "ws-abc", "name": "my-ns"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolve(t, r, c, tc.service, tc.kind, tc.arn)
			require.NoError(t, err)
			assert.Equal(t, tc.fields, got.Fields)
		})
	}
}

// TestResolveARN_LeafIdentifier uses real ARNs whose segments differ visibly, since reading
// the wrong segment yields well-formed adoption-fields naming a real but different resource.
func TestResolveARN_LeafIdentifier(t *testing.T) {
	r, c := newResolver(t)

	cases := []struct {
		name    string
		service string
		kind    string
		arn     string
		fields  map[string]string
	}{
		{"organizations account reads the member account, not the ARN's management account",
			"organizations", "Account",
			"arn:aws:organizations::111122223333:account/o-exampleorg/222233334444",
			map[string]string{"accountID": "222233334444"}},
		{"s3vectors index reads the index, not its bucket", "s3vectors", "Index",
			"arn:aws:s3vectors:us-west-2:111:bucket/vectors-prod/index/embeddings",
			map[string]string{"name": "embeddings"}},
		{"s3files access point reads the access point, not its file system", "s3files", "AccessPoint",
			"arn:aws:s3files:us-west-2:111:file-system/fs-0abc/access-point/fsap-0def",
			map[string]string{"id": "fsap-0def"}},
		{"organizations OU reads the OU, not its organization, and keeps the ou- prefix",
			"organizations", "OrganizationalUnit",
			"arn:aws:organizations::111:ou/o-exampleorg/ou-ab12-cdef3456",
			map[string]string{"id": "ou-ab12-cdef3456"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolve(t, r, c, tc.service, tc.kind, tc.arn)
			require.NoError(t, err)
			assert.Equal(t, tc.fields, got.Fields)
		})
	}
}

// TestCatalogExcludesUnresolvableKinds covers the kinds withheld from the supported set,
// so a regenerated catalog that re-admits one fails here.
//
// Each would adopt the wrong resource or nothing at all; overrides.json records why per kind.
func TestCatalogExcludesUnresolvableKinds(t *testing.T) {
	c, err := metadata.Load()
	require.NoError(t, err)

	for _, tc := range []struct{ service, kind string }{
		{"ec2", "FlowLog"},
		{"s3tables", "Table"},
		{"documentdb", "DBCluster"},
		{"documentdb", "DBInstance"},
		{"documentdb", "DBSubnetGroup"},
		{"wafv2", "IPSet"},
		{"wafv2", "WebACL"},
		{"wafv2", "RuleGroup"},
		{"lambda", "Version"},
		{"lambda", "Alias"},
		{"lambda", "FunctionURLConfig"},
		{"iam", "Role"},
		{"iam", "ServiceLinkedRole"},
		{"iam", "User"},
		{"iam", "Group"},
		{"iam", "InstanceProfile"},
		{"kafka", "ServerlessCluster"},
		{"prometheusservice", "AlertManagerDefinition"},
		{"prometheusservice", "LoggingConfiguration"},
		{"ecs", "Service"},
		{"apigatewayv2", "API"},
		{"apigatewayv2", "Route"},
		{"apigatewayv2", "Integration"},
		{"apigatewayv2", "Authorizer"},
		{"apigatewayv2", "Deployment"},
		{"apigatewayv2", "APIMapping"},
		{"eventbridge", "Archive"},
		{"sagemaker", "App"},
	} {
		t.Run(tc.service+"/"+tc.kind, func(t *testing.T) {
			_, ok := c.LookupByServiceKind(tc.service, tc.kind)
			assert.False(t, ok, "must not be supported: adopting it emits a CR for the wrong resource")

			reason, found := c.UnsupportedReason(tc.service, tc.kind)
			assert.True(t, found, "must be listed as unsupported so 'list adoptable --unsupported' explains it")
			assert.NotEmpty(t, reason)
		})
	}
}

// TestResolveARN_ARNPrimary verifies ARN-primary resources (flat ARN, no
// derivable type label) pass the ARN through verbatim as the "arn" field.
func TestResolveARN_ARNPrimary(t *testing.T) {
	r, c := newResolver(t)
	arnStr := "arn:aws:sns:us-east-1:123456789012:my-topic"
	got, err := resolve(t, r, c, "sns", "Topic", arnStr)
	require.NoError(t, err)
	assert.Equal(t, "Topic", got.Resource.Kind)
	assert.Equal(t, map[string]string{"arn": arnStr}, got.Fields)
}

// TestResolveARN_Refuses is the core safety property: an ARN the resolver cannot
// confidently decompose against its named resource must error, never return a
// partial/guessed map.
func TestResolveARN_Refuses(t *testing.T) {
	r, c := newResolver(t)

	t.Run("garbage is not an ARN", func(t *testing.T) {
		_, err := resolve(t, r, c, "s3", "Bucket", "not-an-arn")
		require.Error(t, err)
	})

	t.Run("ARN of the wrong shape for the named resource", func(t *testing.T) {
		// a bucket ARN resolved as an eks Nodegroup must NOT produce a partial
		// map — it must refuse.
		_, err := resolve(t, r, c, "eks", "Nodegroup", "arn:aws:s3:::just-a-bucket")
		require.Error(t, err)
		_, ok := err.(*UnresolvableError)
		assert.True(t, ok, "want UnresolvableError, got %T", err)
	})

	t.Run("qualified ARN carrying an extra colon segment", func(t *testing.T) {
		// Read greedily these yield "/aws/lambda/my-fn:*" and "my-fn:PROD", names no log
		// group or function has, so the CR would adopt nothing.
		for _, tc := range []struct{ service, kind, arn string }{
			{"cloudwatchlogs", "LogGroup",
				"arn:aws:logs:us-east-1:123456789012:log-group:/aws/lambda/my-fn:*"},
			{"lambda", "Function",
				"arn:aws:lambda:us-east-1:123456789012:function:my-fn:PROD"},
		} {
			_, err := resolve(t, r, c, tc.service, tc.kind, tc.arn)
			require.Error(t, err, tc.arn)
			_, ok := err.(*UnresolvableError)
			assert.True(t, ok, "want UnresolvableError for %s, got %T", tc.arn, err)
		}
	})

	t.Run("classic ELB matches no elbv2 shape", func(t *testing.T) {
		// The shared elasticloadbalancing:loadbalancer type filter returns classic
		// ELBs too; the elbv2 controller cannot manage one, so it must be skipped
		// rather than emitted as a v2 CR that adopts a same-named ALB.
		_, err := resolve(t, r, c, "elbv2", "LoadBalancer",
			"arn:aws:elasticloadbalancing:us-east-1:111:loadbalancer/my-classic-elb")
		require.Error(t, err)
		_, ok := err.(*UnresolvableError)
		assert.True(t, ok, "want UnresolvableError, got %T", err)
	})
}

// TestResolveARN_NeverPartial asserts that when resolution succeeds, EVERY
// declared identifier key is present and non-empty — never a partial map.
func TestResolveARN_NeverPartial(t *testing.T) {
	r, c := newResolver(t)
	got, err := resolve(t, r, c, "eks", "Nodegroup", "arn:aws:eks:us-west-2:123456789012:nodegroup/c/ng/uuid")
	require.NoError(t, err)
	for _, b := range got.Bindings {
		v, ok := got.Fields[b.Key]
		assert.True(t, ok, "key %q missing", b.Key)
		assert.NotEmpty(t, v, "key %q empty", b.Key)
	}
}
