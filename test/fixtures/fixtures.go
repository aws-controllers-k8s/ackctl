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

package fixtures

import (
	"context"
	"testing"

	"github.com/aws-controllers-k8s/ackctl/test/harness"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	apitypes "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	athenatypes "github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/aws/aws-sdk-go-v2/service/backup"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/memorydb"
	mdbtypes "github.com/aws/aws-sdk-go-v2/service/memorydb/types"
	"github.com/aws/aws-sdk-go-v2/service/rbin"
	rbintypes "github.com/aws/aws-sdk-go-v2/service/rbin/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfntypes "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/require"
)

func createTopic(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := sns.NewFromConfig(f.Cfg)
	out, err := c.CreateTopic(ctx, &sns.CreateTopicInput{
		Name: &name,
		Tags: []snstypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating topic %s", name)
	topicARN := aws.ToString(out.TopicArn)
	t.Cleanup(func() {
		_, derr := c.DeleteTopic(context.Background(), &sns.DeleteTopicInput{TopicArn: &topicARN})
		harness.CleanupErr(t, derr, "sns topic "+topicARN)
	})
	return topicARN, map[string]string{"arn": topicARN}
}

// sharedAPI creates one HTTP API the run reuses, since stage, route and integration hang off it.
func (f *Fixtures) sharedAPI(ctx context.Context, t *testing.T) string {
	t.Helper()
	if f.apiID != "" {
		return f.apiID
	}
	c := apigatewayv2.NewFromConfig(f.Cfg)
	out, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name:         aws.String(f.ResourceName() + "-shared"),
		ProtocolType: apitypes.ProtocolTypeHttp,
		Tags:         map[string]string{harness.SharedTagKey: f.RunID},
	})
	require.NoError(t, err, "creating the shared HTTP API")
	id := aws.ToString(out.ApiId)
	t.Cleanup(func() {
		_, derr := c.DeleteApi(context.Background(), &apigatewayv2.DeleteApiInput{ApiId: aws.String(id)})
		harness.CleanupErr(t, derr, "apigatewayv2 api "+id)
	})
	f.apiID = id
	return id
}

func createStage(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	c := apigatewayv2.NewFromConfig(f.Cfg)
	apiID := f.sharedAPI(ctx, t)
	stageName := "probe"
	_, err := c.CreateStage(ctx, &apigatewayv2.CreateStageInput{
		ApiId:     aws.String(apiID),
		StageName: aws.String(stageName),
		Tags:      map[string]string{harness.RunTagKey: f.RunID},
	})
	require.NoError(t, err, "creating stage on %s", apiID)
	return "arn:" + f.Partition + ":apigateway:" + f.Region + "::/apis/" + apiID + "/stages/" + stageName,
		map[string]string{"apiID": apiID, "stageName": stageName}
}

func createLogGroup(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := cloudwatchlogs.NewFromConfig(f.Cfg)
	_, err := c.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(name),
		Tags:         map[string]string{harness.RunTagKey: f.RunID},
	})
	require.NoError(t, err, "creating log group")
	t.Cleanup(func() {
		_, derr := c.DeleteLogGroup(context.Background(),
			&cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(name)})
		harness.CleanupErr(t, derr, "log group "+name)
	})
	return "arn:" + f.Partition + ":logs:" + f.Region + ":" + f.Account + ":log-group:" + name,
		map[string]string{"name": name}
}

func createMetricAlarm(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := cloudwatch.NewFromConfig(f.Cfg)
	_, err := c.PutMetricAlarm(ctx, &cloudwatch.PutMetricAlarmInput{
		AlarmName:          aws.String(name),
		ComparisonOperator: cwtypes.ComparisonOperatorGreaterThanThreshold,
		EvaluationPeriods:  aws.Int32(1),
		MetricName:         aws.String("ackctl-probe"),
		Namespace:          aws.String("ackctl/integration"),
		Period:             aws.Int32(60),
		Statistic:          cwtypes.StatisticSum,
		Threshold:          aws.Float64(1),
		Tags:               []cwtypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating metric alarm")
	t.Cleanup(func() {
		_, derr := c.DeleteAlarms(context.Background(), &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{name}})
		harness.CleanupErr(t, derr, "cloudwatch alarm "+name)
	})
	return "arn:" + f.Partition + ":cloudwatch:" + f.Region + ":" + f.Account + ":alarm:" + name,
		map[string]string{"name": name}
}

func createECRRepository(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := ecr.NewFromConfig(f.Cfg)
	out, err := c.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName: aws.String(name),
		Tags:           []ecrtypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating ECR repository")
	t.Cleanup(func() {
		_, derr := c.DeleteRepository(context.Background(),
			&ecr.DeleteRepositoryInput{RepositoryName: aws.String(name), Force: true})
		harness.CleanupErr(t, derr, "ecr repository "+name)
	})
	return aws.ToString(out.Repository.RepositoryArn), map[string]string{
		"name":       name,
		"registryID": aws.ToString(out.Repository.RegistryId),
	}
}

func createDynamoTable(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := dynamodb.NewFromConfig(f.Cfg)
	out, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(name),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash},
		},
		Tags: []ddbtypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating DynamoDB table")
	t.Cleanup(func() {
		_, derr := c.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(name)})
		harness.CleanupErr(t, derr, "dynamodb table "+name)
	})
	return aws.ToString(out.TableDescription.TableArn), map[string]string{"tableName": name}
}

func createECSCluster(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := ecs.NewFromConfig(f.Cfg)
	out, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String(name),
		Tags:        []ecstypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating ECS cluster")
	t.Cleanup(func() {
		_, derr := c.DeleteCluster(context.Background(), &ecs.DeleteClusterInput{Cluster: aws.String(name)})
		harness.CleanupErr(t, derr, "ecs cluster "+name)
	})
	return aws.ToString(out.Cluster.ClusterArn), map[string]string{"name": name}
}

func createTaskDefinition(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	family := f.ResourceName()
	c := ecs.NewFromConfig(f.Cfg)
	out, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String(family),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:      aws.String("probe"),
			Image:     aws.String("public.ecr.aws/docker/library/busybox:latest"),
			Essential: aws.Bool(true),
			Memory:    aws.Int32(128),
		}},
		Tags: []ecstypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "registering task definition")
	arnStr := aws.ToString(out.TaskDefinition.TaskDefinitionArn)
	t.Cleanup(func() {
		_, derr := c.DeregisterTaskDefinition(context.Background(),
			&ecs.DeregisterTaskDefinitionInput{TaskDefinition: aws.String(arnStr)})
		harness.CleanupErr(t, derr, "ecs task definition "+arnStr)
	})
	return arnStr, map[string]string{"family": family}
}

// sharedFileSystem creates one EFS file system the run reuses, since the access point needs it.
func (f *Fixtures) sharedFileSystem(ctx context.Context, t *testing.T) string {
	t.Helper()
	if f.fsID != "" {
		return f.fsID
	}
	c := efs.NewFromConfig(f.Cfg)
	out, err := c.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken: aws.String(f.ResourceName() + "-shared"),
		Tags:          f.efsTagsWithKey(harness.SharedTagKey),
	})
	require.NoError(t, err, "creating the shared EFS file system")
	id := aws.ToString(out.FileSystemId)
	t.Cleanup(func() {
		_, derr := c.DeleteFileSystem(context.Background(),
			&efs.DeleteFileSystemInput{FileSystemId: aws.String(id)})
		harness.CleanupErr(t, derr, "efs file system "+id)
	})
	f.waitForFileSystem(ctx, t, id)
	f.fsID = id
	return id
}

// waitForFileSystem blocks until the file system is available, which CreateAccessPoint requires.
func (f *Fixtures) waitForFileSystem(ctx context.Context, t *testing.T, id string) {
	t.Helper()
	c := efs.NewFromConfig(f.Cfg)
	deadline := time.Now().Add(5 * time.Minute)
	for {
		out, err := c.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{FileSystemId: aws.String(id)})
		require.NoError(t, err, "describing file system %s", id)
		require.Len(t, out.FileSystems, 1)
		if out.FileSystems[0].LifeCycleState == efstypes.LifeCycleStateAvailable {
			return
		}
		require.False(t, time.Now().After(deadline),
			"file system %s never became available (last state %s)", id, out.FileSystems[0].LifeCycleState)
		time.Sleep(5 * time.Second)
	}
}

func createFileSystem(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	c := efs.NewFromConfig(f.Cfg)
	out, err := c.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
		CreationToken: aws.String(f.ResourceName()),
		Tags:          f.efsTags(),
	})
	require.NoError(t, err, "creating EFS file system")
	id := aws.ToString(out.FileSystemId)
	t.Cleanup(func() {
		_, derr := c.DeleteFileSystem(context.Background(),
			&efs.DeleteFileSystemInput{FileSystemId: aws.String(id)})
		harness.CleanupErr(t, derr, "efs file system "+id)
	})
	return aws.ToString(out.FileSystemArn), map[string]string{"fileSystemID": id}
}

func createEFSAccessPoint(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	c := efs.NewFromConfig(f.Cfg)
	out, err := c.CreateAccessPoint(ctx, &efs.CreateAccessPointInput{
		FileSystemId: aws.String(f.sharedFileSystem(ctx, t)),
		Tags:         f.efsTags(),
	})
	require.NoError(t, err, "creating EFS access point")
	id := aws.ToString(out.AccessPointId)
	t.Cleanup(func() {
		_, derr := c.DeleteAccessPoint(context.Background(),
			&efs.DeleteAccessPointInput{AccessPointId: aws.String(id)})
		harness.CleanupErr(t, derr, "efs access point "+id)
	})
	return aws.ToString(out.AccessPointArn), map[string]string{"accessPointID": id}
}

func createEventBus(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := eventbridge.NewFromConfig(f.Cfg)
	out, err := c.CreateEventBus(ctx, &eventbridge.CreateEventBusInput{
		Name: aws.String(name),
		Tags: []ebtypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating event bus")
	t.Cleanup(func() {
		_, derr := c.DeleteEventBus(context.Background(), &eventbridge.DeleteEventBusInput{Name: aws.String(name)})
		harness.CleanupErr(t, derr, "event bus "+name)
	})
	return aws.ToString(out.EventBusArn), map[string]string{"name": name}
}

// createRule uses the DEFAULT bus, which is the only form the catalog's rule template matches.
func createRule(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := eventbridge.NewFromConfig(f.Cfg)
	out, err := c.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String(name),
		EventPattern: aws.String(`{"source":["ackctl.integration"]}`),
		Tags:         []ebtypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating rule")
	t.Cleanup(func() {
		_, derr := c.DeleteRule(context.Background(), &eventbridge.DeleteRuleInput{Name: aws.String(name)})
		harness.CleanupErr(t, derr, "event rule "+name)
	})
	return aws.ToString(out.RuleArn), map[string]string{"name": name}
}

func createActivity(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := sfn.NewFromConfig(f.Cfg)
	out, err := c.CreateActivity(ctx, &sfn.CreateActivityInput{
		Name: aws.String(name),
		Tags: []sfntypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating activity")
	arnStr := aws.ToString(out.ActivityArn)
	t.Cleanup(func() {
		_, derr := c.DeleteActivity(context.Background(), &sfn.DeleteActivityInput{ActivityArn: aws.String(arnStr)})
		harness.CleanupErr(t, derr, "sfn activity "+arnStr)
	})
	return arnStr, map[string]string{"arn": arnStr}
}

func createParameter(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := ssm.NewFromConfig(f.Cfg)
	_, err := c.PutParameter(ctx, &ssm.PutParameterInput{
		Name:  aws.String(name),
		Value: aws.String("ackctl"),
		Type:  ssmtypes.ParameterTypeString,
		Tags:  []ssmtypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating parameter")
	t.Cleanup(func() {
		_, derr := c.DeleteParameter(context.Background(), &ssm.DeleteParameterInput{Name: aws.String(name)})
		harness.CleanupErr(t, derr, "ssm parameter "+name)
	})
	return "arn:" + f.Partition + ":ssm:" + f.Region + ":" + f.Account + ":parameter/" + name,
		map[string]string{"name": name}
}

func createDocument(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := ssm.NewFromConfig(f.Cfg)
	_, err := c.CreateDocument(ctx, &ssm.CreateDocumentInput{
		Name:           aws.String(name),
		DocumentType:   ssmtypes.DocumentTypeCommand,
		DocumentFormat: ssmtypes.DocumentFormatYaml,
		Content: aws.String("schemaVersion: '2.2'\ndescription: ackctl probe\nmainSteps:\n" +
			"  - action: aws:runShellScript\n    name: probe\n    inputs:\n      runCommand:\n        - 'true'\n"),
		Tags: []ssmtypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating document")
	t.Cleanup(func() {
		_, derr := c.DeleteDocument(context.Background(), &ssm.DeleteDocumentInput{Name: aws.String(name)})
		harness.CleanupErr(t, derr, "ssm document "+name)
	})
	return "arn:" + f.Partition + ":ssm:" + f.Region + ":" + f.Account + ":document/" + name,
		map[string]string{"name": name}
}

func createDBSubnetGroup(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := rds.NewFromConfig(f.Cfg)
	out, err := c.CreateDBSubnetGroup(ctx, &rds.CreateDBSubnetGroupInput{
		DBSubnetGroupName:        aws.String(name),
		DBSubnetGroupDescription: aws.String("ackctl integration test"),
		SubnetIds:                f.twoSubnets(ctx, t),
		Tags:                     []rdstypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating DB subnet group")
	t.Cleanup(func() {
		_, derr := c.DeleteDBSubnetGroup(context.Background(),
			&rds.DeleteDBSubnetGroupInput{DBSubnetGroupName: aws.String(name)})
		harness.CleanupErr(t, derr, "rds db subnet group "+name)
	})
	return aws.ToString(out.DBSubnetGroup.DBSubnetGroupArn), map[string]string{"name": name}
}

func createDBParameterGroup(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := rds.NewFromConfig(f.Cfg)
	out, err := c.CreateDBParameterGroup(ctx, &rds.CreateDBParameterGroupInput{
		DBParameterGroupName:   aws.String(name),
		DBParameterGroupFamily: aws.String("postgres16"),
		Description:            aws.String("ackctl integration test"),
		Tags:                   []rdstypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating DB parameter group")
	t.Cleanup(func() {
		_, derr := c.DeleteDBParameterGroup(context.Background(),
			&rds.DeleteDBParameterGroupInput{DBParameterGroupName: aws.String(name)})
		harness.CleanupErr(t, derr, "rds db parameter group "+name)
	})
	return aws.ToString(out.DBParameterGroup.DBParameterGroupArn), map[string]string{"name": name}
}

func createDBClusterParameterGroup(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := rds.NewFromConfig(f.Cfg)
	out, err := c.CreateDBClusterParameterGroup(ctx, &rds.CreateDBClusterParameterGroupInput{
		DBClusterParameterGroupName: aws.String(name),
		DBParameterGroupFamily:      aws.String("aurora-postgresql16"),
		Description:                 aws.String("ackctl integration test"),
		Tags:                        []rdstypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating DB cluster parameter group")
	t.Cleanup(func() {
		_, derr := c.DeleteDBClusterParameterGroup(context.Background(),
			&rds.DeleteDBClusterParameterGroupInput{DBClusterParameterGroupName: aws.String(name)})
		harness.CleanupErr(t, derr, "rds db cluster parameter group "+name)
	})
	return aws.ToString(out.DBClusterParameterGroup.DBClusterParameterGroupArn),
		map[string]string{"name": name}
}

func createCacheSubnetGroup(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := elasticache.NewFromConfig(f.Cfg)
	out, err := c.CreateCacheSubnetGroup(ctx, &elasticache.CreateCacheSubnetGroupInput{
		CacheSubnetGroupName:        aws.String(name),
		CacheSubnetGroupDescription: aws.String("ackctl integration test"),
		SubnetIds:                   f.twoSubnets(ctx, t),
		Tags:                        []ectypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating cache subnet group")
	t.Cleanup(func() {
		_, derr := c.DeleteCacheSubnetGroup(context.Background(),
			&elasticache.DeleteCacheSubnetGroupInput{CacheSubnetGroupName: aws.String(name)})
		harness.CleanupErr(t, derr, "elasticache cache subnet group "+name)
	})
	return aws.ToString(out.CacheSubnetGroup.ARN), map[string]string{"cacheSubnetGroupName": name}
}

func createCacheParameterGroup(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := elasticache.NewFromConfig(f.Cfg)
	out, err := c.CreateCacheParameterGroup(ctx, &elasticache.CreateCacheParameterGroupInput{
		CacheParameterGroupName:   aws.String(name),
		CacheParameterGroupFamily: aws.String("redis7"),
		Description:               aws.String("ackctl integration test"),
		Tags:                      []ectypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating cache parameter group")
	t.Cleanup(func() {
		_, derr := c.DeleteCacheParameterGroup(context.Background(),
			&elasticache.DeleteCacheParameterGroupInput{CacheParameterGroupName: aws.String(name)})
		harness.CleanupErr(t, derr, "elasticache cache parameter group "+name)
	})
	return aws.ToString(out.CacheParameterGroup.ARN), map[string]string{"cacheParameterGroupName": name}
}

func createMemoryDBParameterGroup(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := memorydb.NewFromConfig(f.Cfg)
	out, err := c.CreateParameterGroup(ctx, &memorydb.CreateParameterGroupInput{
		ParameterGroupName: aws.String(name),
		Family:             aws.String("memorydb_redis7"),
		Tags:               []mdbtypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating MemoryDB parameter group")
	t.Cleanup(func() {
		_, derr := c.DeleteParameterGroup(context.Background(),
			&memorydb.DeleteParameterGroupInput{ParameterGroupName: aws.String(name)})
		harness.CleanupErr(t, derr, "memorydb parameter group "+name)
	})
	return aws.ToString(out.ParameterGroup.ARN), map[string]string{"name": name}
}

func createMemoryDBSubnetGroup(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := memorydb.NewFromConfig(f.Cfg)
	out, err := c.CreateSubnetGroup(ctx, &memorydb.CreateSubnetGroupInput{
		SubnetGroupName: aws.String(name),
		SubnetIds:       f.twoSubnets(ctx, t),
		Tags:            []mdbtypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating MemoryDB subnet group")
	t.Cleanup(func() {
		_, derr := c.DeleteSubnetGroup(context.Background(),
			&memorydb.DeleteSubnetGroupInput{SubnetGroupName: aws.String(name)})
		harness.CleanupErr(t, derr, "memorydb subnet group "+name)
	})
	return aws.ToString(out.SubnetGroup.ARN), map[string]string{"name": name}
}

func createMemoryDBACL(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := memorydb.NewFromConfig(f.Cfg)
	out, err := c.CreateACL(ctx, &memorydb.CreateACLInput{
		ACLName: aws.String(name),
		Tags:    []mdbtypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating MemoryDB ACL")
	t.Cleanup(func() {
		_, derr := c.DeleteACL(context.Background(), &memorydb.DeleteACLInput{ACLName: aws.String(name)})
		harness.CleanupErr(t, derr, "memorydb acl "+name)
	})
	return aws.ToString(out.ACL.ARN), map[string]string{"name": name}
}

func createWorkGroup(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := athena.NewFromConfig(f.Cfg)
	_, err := c.CreateWorkGroup(ctx, &athena.CreateWorkGroupInput{
		Name: aws.String(name),
		Tags: []athenatypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating Athena workgroup")
	t.Cleanup(func() {
		_, derr := c.DeleteWorkGroup(context.Background(), &athena.DeleteWorkGroupInput{WorkGroup: aws.String(name)})
		harness.CleanupErr(t, derr, "athena workgroup "+name)
	})
	return "arn:" + f.Partition + ":athena:" + f.Region + ":" + f.Account + ":workgroup/" + name,
		map[string]string{"name": name}
}

func createBackupVault(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := backup.NewFromConfig(f.Cfg)
	out, err := c.CreateBackupVault(ctx, &backup.CreateBackupVaultInput{
		BackupVaultName: aws.String(name),
		BackupVaultTags: map[string]string{harness.RunTagKey: f.RunID},
	})
	require.NoError(t, err, "creating backup vault")
	t.Cleanup(func() {
		_, derr := c.DeleteBackupVault(context.Background(),
			&backup.DeleteBackupVaultInput{BackupVaultName: aws.String(name)})
		harness.CleanupErr(t, derr, "backup vault "+name)
	})
	return aws.ToString(out.BackupVaultArn), map[string]string{"name": name}
}

func createRecycleBinRule(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	c := rbin.NewFromConfig(f.Cfg)
	out, err := c.CreateRule(ctx, &rbin.CreateRuleInput{
		Description:  aws.String("ackctl integration test"),
		ResourceType: rbintypes.ResourceTypeEbsSnapshot,
		RetentionPeriod: &rbintypes.RetentionPeriod{
			RetentionPeriodUnit:  rbintypes.RetentionPeriodUnitDays,
			RetentionPeriodValue: aws.Int32(1),
		},
		Tags: []rbintypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating recycle bin rule")
	id := aws.ToString(out.Identifier)
	t.Cleanup(func() {
		_, derr := c.DeleteRule(context.Background(), &rbin.DeleteRuleInput{Identifier: aws.String(id)})
		harness.CleanupErr(t, derr, "recycle bin rule "+id)
	})
	return "arn:" + f.Partition + ":rbin:" + f.Region + ":" + f.Account + ":rule/" + id,
		map[string]string{"identifier": id}
}

func createTargetGroup(ctx context.Context, t *testing.T, f *Fixtures) (string, map[string]string) {
	name := f.ResourceName()
	c := elbv2.NewFromConfig(f.Cfg)
	out, err := c.CreateTargetGroup(ctx, &elbv2.CreateTargetGroupInput{
		Name:       aws.String(name),
		Protocol:   elbtypes.ProtocolEnumHttp,
		Port:       aws.Int32(80),
		VpcId:      aws.String(f.sharedVPC(ctx, t)),
		TargetType: elbtypes.TargetTypeEnumInstance,
		Tags:       []elbtypes.Tag{{Key: aws.String(harness.RunTagKey), Value: aws.String(f.RunID)}},
	})
	require.NoError(t, err, "creating target group")
	arnStr := aws.ToString(out.TargetGroups[0].TargetGroupArn)
	t.Cleanup(func() {
		_, derr := c.DeleteTargetGroup(context.Background(),
			&elbv2.DeleteTargetGroupInput{TargetGroupArn: aws.String(arnStr)})
		harness.CleanupErr(t, derr, "elbv2 target group "+arnStr)
	})
	return arnStr, map[string]string{"name": name}
}

func (f *Fixtures) efsTags() []efstypes.Tag { return f.efsTagsWithKey(harness.RunTagKey) }

func (f *Fixtures) efsTagsWithKey(key string) []efstypes.Tag {
	return []efstypes.Tag{{Key: aws.String(key), Value: aws.String(f.RunID)}}
}
