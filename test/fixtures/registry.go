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

//go:build integration

// Package fixtures provisions the real AWS resources the integration suite adopts, and
// records which adoptable kinds have no fixture and why.
package fixtures

import (
	"context"
	"testing"

	"github.com/aws-controllers-k8s/ackctl/test/harness"
)

// Fixtures provisions the real AWS resources the suites adopt. It embeds the harness and
// caches the prerequisites several fixtures share, each created on first use.
type Fixtures struct {
	*harness.Harness

	vpcID     string
	fsID      string
	apiID     string
	subnetIDs []string
}

func New(h *harness.Harness) *Fixtures { return &Fixtures{Harness: h} }

// createBucket adapts the harness fixture, which the e2e suite also uses, to CreateFunc.
func createBucket(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	return harness.CreateBucket(ctx, t, f.Harness)
}

type SkipReason string

const (
	SkipCost   SkipReason = "cost"
	SkipPrereq SkipReason = "prerequisite"
	SkipSlow   SkipReason = "slow"
	SkipRegion SkipReason = "region"
)

// CreateFunc provisions one real resource and returns its ARN plus the adoption-fields ACK
// requires, both read back from the AWS response. It registers its own teardown.
type CreateFunc func(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string)

// KindEntry is one catalog kind's e2e coverage: a fixture, or a recorded reason there is none.
type KindEntry struct {
	Service string
	Kind    string
	Create  CreateFunc
	Skip    SkipReason
	Why     string
}

// kinds covers every adoptable kind, enforced by TestEveryCatalogKindHasAnEntry.
var Kinds = []KindEntry{
	{"acm", "AcmeDomainValidation", nil, SkipPrereq, "needs a public domain under your control"},
	{"acm", "AcmeEndpoint", nil, SkipPrereq, "needs a private CA and a domain under your control"},
	{"acm", "Certificate", nil, SkipPrereq, "needs a public domain under your control for validation"},
	{"acmpca", "CertificateAuthority", nil, SkipCost, "a private CA costs about USD 400 per month"},
	{"apigateway", "APIKey", nil, SkipPrereq, "an API key carries no tags the Tagging API indexes"},
	{"apigateway", "RestAPI", nil, SkipPrereq, "REST API tags are indexed under a different type than the catalog claims"},
	{"apigateway", "VPCLink", nil, SkipPrereq, "needs a network load balancer"},
	{"apigatewayv2", "DomainName", nil, SkipPrereq, "needs an ACM certificate for a domain under your control"},
	{"apigatewayv2", "Stage", createStage, "", ""},
	{"apigatewayv2", "VPCLink", nil, SkipPrereq, "needs subnets and security groups, and takes minutes to become available"},
	{"athena", "DataCatalog", nil, SkipPrereq, "needs a Glue catalog or a Lambda metadata function"},
	{"athena", "WorkGroup", createWorkGroup, "", ""},
	{"autoscaling", "AutoScalingGroup", nil, SkipPrereq, "needs a launch template and a subnet with capacity"},
	{"backup", "BackupPlan", nil, SkipPrereq, "needs a backup vault and a resource selection"},
	{"backup", "BackupVault", createBackupVault, "", ""},
	{"bedrockagentcorecontrol", "APIKeyCredentialProvider", nil, SkipPrereq, "needs Bedrock AgentCore prerequisites (roles, images or network config)"},
	{"bedrockagentcorecontrol", "AgentRuntime", nil, SkipPrereq, "needs Bedrock AgentCore prerequisites (roles, images or network config)"},
	{"bedrockagentcorecontrol", "AgentRuntimeEndpoint", nil, SkipPrereq, "needs Bedrock AgentCore prerequisites (roles, images or network config)"},
	{"bedrockagentcorecontrol", "Browser", nil, SkipPrereq, "needs Bedrock AgentCore prerequisites (roles, images or network config)"},
	{"bedrockagentcorecontrol", "BrowserProfile", nil, SkipPrereq, "needs Bedrock AgentCore prerequisites (roles, images or network config)"},
	{"bedrockagentcorecontrol", "CodeInterpreter", nil, SkipPrereq, "needs Bedrock AgentCore prerequisites (roles, images or network config)"},
	{"bedrockagentcorecontrol", "Gateway", nil, SkipPrereq, "needs Bedrock AgentCore prerequisites (roles, images or network config)"},
	{"bedrockagentcorecontrol", "Harness", nil, SkipPrereq, "needs Bedrock AgentCore prerequisites (roles, images or network config)"},
	{"bedrockagentcorecontrol", "HarnessEndpoint", nil, SkipPrereq, "needs Bedrock AgentCore prerequisites (roles, images or network config)"},
	{"bedrockagentcorecontrol", "Memory", nil, SkipPrereq, "needs Bedrock AgentCore prerequisites (roles, images or network config)"},
	{"bedrockagentcorecontrol", "Policy", nil, SkipPrereq, "needs Bedrock AgentCore prerequisites (roles, images or network config)"},
	{"bedrockagentcorecontrol", "PolicyEngine", nil, SkipPrereq, "needs Bedrock AgentCore prerequisites (roles, images or network config)"},
	{"bedrockagentcorecontrol", "WorkloadIdentity", nil, SkipPrereq, "needs Bedrock AgentCore prerequisites (roles, images or network config)"},
	{"cloudfront", "CachePolicy", nil, SkipPrereq, "CloudFront is global and its policies carry no tags"},
	{"cloudfront", "ConnectionGroup", nil, SkipPrereq, "needs a multi-tenant distribution"},
	{"cloudfront", "Distribution", nil, SkipCost, "a distribution bills per request and per GB"},
	{"cloudfront", "DistributionTenant", nil, SkipPrereq, "needs a multi-tenant distribution"},
	{"cloudfront", "Function", nil, SkipPrereq, "CloudFront is global and its functions carry no tags"},
	{"cloudfront", "OriginAccessControl", nil, SkipPrereq, "CloudFront is global and its access controls carry no tags"},
	{"cloudfront", "OriginRequestPolicy", nil, SkipPrereq, "CloudFront is global and its policies carry no tags"},
	{"cloudfront", "ResponseHeadersPolicy", nil, SkipPrereq, "CloudFront is global and its policies carry no tags"},
	{"cloudfront", "VPCOrigin", nil, SkipPrereq, "needs a VPC resource and a distribution"},
	{"cloudtrail", "EventDataStore", nil, SkipCost, "an event data store bills for ingested events"},
	{"cloudtrail", "Trail", nil, SkipPrereq, "needs an S3 bucket with a CloudTrail bucket policy"},
	{"cloudwatch", "Dashboard", nil, SkipRegion, "a dashboard ARN carries no region and the Tagging API indexes it only in us-east-1, measured: tagged and visible there within 20s, never in us-west-2"},
	{"cloudwatch", "MetricAlarm", createMetricAlarm, "", ""},
	{"cloudwatch", "MetricStream", nil, SkipPrereq, "needs a Firehose delivery stream and a role"},
	{"cloudwatchlogs", "LogGroup", createLogGroup, "", ""},
	{"codeartifact", "Domain", nil, SkipPrereq, "needs a KMS key for domain encryption"},
	{"cognitoidentityprovider", "UserPool", nil, SkipPrereq, "a user pool carries no tags the Tagging API indexes"},
	{"dsql", "Cluster", nil, SkipCost, "a DSQL cluster bills per hour"},
	{"dynamodb", "Backup", nil, SkipPrereq, "needs an existing table"},
	{"dynamodb", "GlobalTable", nil, SkipPrereq, "needs replicas in more than one region"},
	{"dynamodb", "Table", createDynamoTable, "", ""},
	{"ec2", "CapacityReservation", nil, SkipCost, "a capacity reservation bills for reserved capacity whether used or not"},
	{"ec2", "DHCPOptions", createDHCPOptions, "", ""},
	{"ec2", "EgressOnlyInternetGateway", createEgressOnlyInternetGateway, "", ""},
	{"ec2", "ElasticIPAddress", nil, SkipCost, "an unassociated elastic IP bills per hour"},
	{"ec2", "Instance", nil, SkipCost, "an instance bills per hour"},
	{"ec2", "InternetGateway", createInternetGateway, "", ""},
	{"ec2", "LaunchTemplate", createLaunchTemplate, "", ""},
	{"ec2", "ManagedPrefixList", createManagedPrefixList, "", ""},
	{"ec2", "NATGateway", nil, SkipCost, "a NAT gateway bills per hour"},
	{"ec2", "NetworkACL", createNetworkACL, "", ""},
	{"ec2", "RouteTable", createRouteTable, "", ""},
	{"ec2", "SecurityGroup", createSecurityGroup, "", ""},
	{"ec2", "Subnet", createSubnet, "", ""},
	{"ec2", "TransitGateway", nil, SkipCost, "a transit gateway bills per hour and per attachment"},
	{"ec2", "VPC", createVPC, "", ""},
	{"ec2", "VPCEndpoint", nil, SkipPrereq, "needs a route table and a service name"},
	{"ec2", "VPCEndpointServiceConfiguration", nil, SkipPrereq, "needs a network load balancer"},
	{"ec2", "VPCPeeringConnection", nil, SkipPrereq, "needs a second VPC and an accepter"},
	{"ecr", "Repository", createECRRepository, "", ""},
	{"ecrpublic", "Repository", nil, SkipPrereq, "public ECR is available only in us-east-1"},
	{"ecs", "CapacityProvider", nil, SkipPrereq, "needs an auto scaling group"},
	{"ecs", "Cluster", createECSCluster, "", ""},
	{"ecs", "TaskDefinition", createTaskDefinition, "", ""},
	{"efs", "AccessPoint", createEFSAccessPoint, "", ""},
	{"efs", "FileSystem", createFileSystem, "", ""},
	{"eks", "Addon", nil, SkipPrereq, "needs an EKS cluster"},
	{"eks", "Capability", nil, SkipPrereq, "needs an EKS cluster"},
	{"eks", "Cluster", nil, SkipCost, "an EKS cluster bills about USD 73 per month"},
	{"eks", "FargateProfile", nil, SkipPrereq, "needs an EKS cluster"},
	{"eks", "IdentityProviderConfig", nil, SkipPrereq, "needs an EKS cluster and an OIDC provider"},
	{"eks", "Nodegroup", nil, SkipPrereq, "needs an EKS cluster"},
	{"elasticache", "CacheCluster", nil, SkipCost, "a cache cluster bills per node hour"},
	{"elasticache", "CacheParameterGroup", createCacheParameterGroup, "", ""},
	{"elasticache", "CacheSubnetGroup", createCacheSubnetGroup, "", ""},
	{"elasticache", "ReplicationGroup", nil, SkipCost, "a replication group bills per node hour"},
	{"elasticache", "ServerlessCache", nil, SkipCost, "a serverless cache bills for stored data and requests"},
	{"elasticache", "ServerlessCacheSnapshot", nil, SkipPrereq, "needs a serverless cache"},
	{"elasticache", "Snapshot", nil, SkipPrereq, "needs a running cache cluster"},
	{"elasticache", "User", nil, SkipPrereq, "needs RBAC-enabled engine settings"},
	{"elasticache", "UserGroup", nil, SkipPrereq, "needs at least one ElastiCache user"},
	{"elbv2", "LoadBalancer", nil, SkipCost, "a load balancer bills per hour"},
	{"elbv2", "TargetGroup", createTargetGroup, "", ""},
	{"emrcontainers", "JobRun", nil, SkipPrereq, "needs a virtual cluster and a job execution role"},
	{"emrcontainers", "VirtualCluster", nil, SkipPrereq, "needs an EKS cluster with a namespace and access entry"},
	{"eventbridge", "Endpoint", nil, SkipPrereq, "needs event buses in two regions and a health check"},
	{"eventbridge", "EventBus", createEventBus, "", ""},
	{"eventbridge", "Rule", createRule, "", ""},
	{"firehose", "DeliveryStream", nil, SkipPrereq, "needs a destination and a delivery role"},
	{"glue", "Job", nil, SkipPrereq, "needs a Glue service role and a script in S3"},
	{"iam", "Policy", nil, SkipRegion, "IAM is global and the Tagging API indexes it only in us-east-1"},
	{"kafka", "Cluster", nil, SkipCost, "an MSK cluster bills per broker hour"},
	{"kafka", "Configuration", nil, SkipSlow, "an MSK configuration carries no tags the Tagging API indexes"},
	{"kafka", "VPCConnection", nil, SkipPrereq, "needs an MSK cluster"},
	{"keyspaces", "Keyspace", nil, SkipPrereq, "a keyspace carries no tags the Tagging API indexes"},
	{"keyspaces", "Table", nil, SkipCost, "a Keyspaces table bills for storage and requests"},
	{"kinesis", "Stream", nil, SkipCost, "a stream bills per shard hour"},
	{"kms", "Key", nil, SkipCost, "a customer managed key costs USD 1 per month"},
	{"lambda", "CodeSigningConfig", nil, SkipPrereq, "needs an AWS Signer signing profile"},
	{"lambda", "EventSourceMapping", nil, SkipPrereq, "needs a function and a stream or queue source"},
	{"lambda", "Function", nil, SkipPrereq, "needs an execution role and a deployment package"},
	{"lambda", "LayerVersion", nil, SkipPrereq, "needs a layer archive in S3 or inline zip content"},
	{"memorydb", "ACL", createMemoryDBACL, "", ""},
	{"memorydb", "Cluster", nil, SkipCost, "a MemoryDB cluster bills per node hour"},
	{"memorydb", "ParameterGroup", createMemoryDBParameterGroup, "", ""},
	{"memorydb", "Snapshot", nil, SkipPrereq, "needs a running MemoryDB cluster"},
	{"memorydb", "SubnetGroup", createMemoryDBSubnetGroup, "", ""},
	{"memorydb", "User", nil, SkipPrereq, "needs an authentication mode and a MemoryDB ACL"},
	{"mq", "Broker", nil, SkipCost, "a broker bills per hour"},
	{"mwaa", "Environment", nil, SkipCost, "an MWAA environment costs several hundred USD per month"},
	{"networkfirewall", "Firewall", nil, SkipCost, "a firewall bills per hour and per GB"},
	{"networkfirewall", "FirewallPolicy", nil, SkipCost, "a policy requires rule groups, which bill for reserved capacity"},
	{"opensearchserverless", "Collection", nil, SkipCost, "a collection bills for OCU capacity"},
	{"opensearchservice", "Domain", nil, SkipCost, "a domain bills per instance hour"},
	{"organizations", "Account", nil, SkipPrereq, "needs an AWS Organization and creates a real member account"},
	{"organizations", "OrganizationalUnit", nil, SkipPrereq, "needs an AWS Organization"},
	{"pipes", "Pipe", nil, SkipPrereq, "needs a source, a target and a role"},
	{"prometheusservice", "RuleGroupsNamespace", nil, SkipPrereq, "needs a Prometheus workspace"},
	{"prometheusservice", "Workspace", nil, SkipCost, "a workspace bills for ingested and stored metrics"},
	{"quicksight", "Analysis", nil, SkipPrereq, "needs a QuickSight subscription"},
	{"quicksight", "Dashboard", nil, SkipPrereq, "needs a QuickSight subscription"},
	{"quicksight", "DataSet", nil, SkipPrereq, "needs a QuickSight subscription and a data source"},
	{"quicksight", "DataSource", nil, SkipPrereq, "needs a QuickSight subscription"},
	{"ram", "Permission", nil, SkipPrereq, "needs a resource type that supports customer managed permissions"},
	{"rds", "DBCluster", nil, SkipCost, "a DB cluster bills per instance hour"},
	{"rds", "DBClusterEndpoint", nil, SkipPrereq, "needs a DB cluster"},
	{"rds", "DBClusterParameterGroup", createDBClusterParameterGroup, "", ""},
	{"rds", "DBClusterSnapshot", nil, SkipPrereq, "needs a DB cluster"},
	{"rds", "DBInstance", nil, SkipCost, "a DB instance bills per hour"},
	{"rds", "DBParameterGroup", createDBParameterGroup, "", ""},
	{"rds", "DBSnapshot", nil, SkipPrereq, "needs a DB instance"},
	{"rds", "DBSubnetGroup", createDBSubnetGroup, "", ""},
	{"rds", "GlobalCluster", nil, SkipCost, "a global cluster requires a billable regional cluster"},
	{"recyclebin", "Rule", createRecycleBinRule, "", ""},
	{"route53", "HealthCheck", nil, SkipCost, "a health check costs USD 0.50 per month"},
	{"route53", "HostedZone", nil, SkipCost, "a hosted zone costs USD 0.50 per month"},
	{"route53resolver", "ResolverEndpoint", nil, SkipCost, "a resolver endpoint bills per IP per hour"},
	{"route53resolver", "ResolverQueryLogConfig", nil, SkipPrereq, "needs a log destination"},
	{"route53resolver", "ResolverRule", nil, SkipPrereq, "needs a resolver endpoint"},
	{"s3", "Bucket", createBucket, "", ""},
	{"s3control", "AccessPoint", nil, SkipPrereq, "needs a bucket policy granting access point creation"},
	{"s3files", "AccessPoint", nil, SkipPrereq, "needs an S3 file system"},
	{"s3files", "FileSystem", nil, SkipPrereq, "S3 file systems are not available in every region"},
	{"s3tables", "TableBucket", nil, SkipPrereq, "table buckets are not available in every region"},
	{"s3vectors", "Index", nil, SkipPrereq, "needs a vector bucket"},
	{"s3vectors", "VectorBucket", nil, SkipPrereq, "vector buckets are not available in every region"},
	{"sagemaker", "DataQualityJobDefinition", nil, SkipSlow, "needs an endpoint and a baseline job"},
	{"sagemaker", "Domain", nil, SkipCost, "a domain provisions billable EFS storage"},
	{"sagemaker", "Endpoint", nil, SkipCost, "an endpoint bills per instance hour"},
	{"sagemaker", "EndpointConfig", nil, SkipSlow, "needs a model"},
	{"sagemaker", "FeatureGroup", nil, SkipSlow, "a feature group takes minutes to become active"},
	{"sagemaker", "HyperParameterTuningJob", nil, SkipSlow, "runs billable compute"},
	{"sagemaker", "InferenceComponent", nil, SkipSlow, "needs an endpoint"},
	{"sagemaker", "LabelingJob", nil, SkipSlow, "needs a human task workforce"},
	{"sagemaker", "Model", nil, SkipSlow, "needs an inference image and an execution role"},
	{"sagemaker", "ModelBiasJobDefinition", nil, SkipSlow, "needs an endpoint and a baseline job"},
	{"sagemaker", "ModelExplainabilityJobDefinition", nil, SkipSlow, "needs an endpoint and a baseline job"},
	{"sagemaker", "ModelPackage", nil, SkipSlow, "needs a model package group and an inference specification"},
	{"sagemaker", "ModelPackageGroup", nil, SkipSlow, "needs a model package to be discoverable"},
	{"sagemaker", "ModelQualityJobDefinition", nil, SkipSlow, "needs an endpoint and a baseline job"},
	{"sagemaker", "MonitoringSchedule", nil, SkipSlow, "needs an endpoint and a job definition"},
	{"sagemaker", "NotebookInstance", nil, SkipCost, "a notebook instance bills per hour"},
	{"sagemaker", "NotebookInstanceLifecycleConfig", nil, SkipSlow, "a lifecycle config carries no tags"},
	{"sagemaker", "Pipeline", nil, SkipSlow, "needs an execution role and a pipeline definition"},
	{"sagemaker", "PipelineExecution", nil, SkipSlow, "needs a pipeline"},
	{"sagemaker", "ProcessingJob", nil, SkipSlow, "runs billable compute"},
	{"sagemaker", "Project", nil, SkipSlow, "needs a Service Catalog portfolio"},
	{"sagemaker", "Space", nil, SkipSlow, "needs a domain"},
	{"sagemaker", "TrainingJob", nil, SkipSlow, "runs billable compute"},
	{"sagemaker", "TransformJob", nil, SkipSlow, "runs billable compute"},
	{"sagemaker", "UserProfile", nil, SkipSlow, "needs a domain"},
	{"ses", "ConfigurationSet", nil, SkipPrereq, "needs a verified sending identity to be useful"},
	{"sfn", "Activity", createActivity, "", ""},
	{"sfn", "StateMachine", nil, SkipPrereq, "needs an execution role"},
	{"sfn", "StateMachineAlias", nil, SkipPrereq, "needs a state machine with a published version"},
	{"sns", "Topic", createTopic, "", ""},
	{"ssm", "Document", createDocument, "", ""},
	{"ssm", "Parameter", createParameter, "", ""},
	{"ssm", "ResourceDataSync", nil, SkipPrereq, "needs an S3 bucket with a Systems Manager bucket policy"},
}
