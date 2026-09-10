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

package integration

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	apitypes "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture provisions one real AWS resource and states what ACK must receive for it.
type fixture struct {
	service string
	kind    string
	group   string
	version string
	// create returns the resource's ARN and the adoption-fields ACK requires, both
	// taken from the AWS response. It registers its own teardown.
	create func(ctx context.Context, t *testing.T, h *harness) (string, map[string]string)
}

// fixtures covers one instance of each structurally distinct ARN template rather
// than one per service: an empty ARN envelope (s3), an ARN-primary resource that
// skips template matching (sns), and a nested multi-key template whose AWS
// namespace differs from the ACK service name (apigatewayv2).
var fixtures = []fixture{
	{
		service: "s3", kind: "Bucket",
		group: "s3.services.k8s.aws", version: "v1alpha1",
		create: createBucket,
	},
	{
		service: "sns", kind: "Topic",
		group: "sns.services.k8s.aws", version: "v1alpha1",
		create: createTopic,
	},
	{
		service: "apigatewayv2", kind: "Stage",
		group: "apigatewayv2.services.k8s.aws", version: "v1alpha1",
		create: createStage,
	},
}

// selectedFixtures narrows the fixture set to ACK_TEST_SERVICES when it is set, which is
// how a run limited to one service skips the fixtures it cannot create.
func selectedFixtures(t *testing.T) []fixture {
	want := os.Getenv("ACK_TEST_SERVICES")
	if want == "" {
		return fixtures
	}
	allowed := map[string]bool{}
	for _, s := range strings.Split(want, ",") {
		allowed[strings.TrimSpace(strings.ToLower(s))] = true
	}
	var out []fixture
	for _, f := range fixtures {
		if allowed[strings.ToLower(f.service)] {
			out = append(out, f)
		}
	}
	require.NotEmpty(t, out, "ACK_TEST_SERVICES=%q selected no fixtures", want)
	t.Logf("ACK_TEST_SERVICES=%s selected %d of %d fixtures", want, len(out), len(fixtures))
	return out
}

// TestAdoptEmitsUsableManifests is the end-to-end case: real resources in, correct
// adoption manifests out. Every fixture is created before anything is asserted,
// because the wait for the Tagging API to index them dominates the runtime.
func TestAdoptEmitsUsableManifests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	h := newHarness(ctx, t)
	selected := selectedFixtures(t)

	want := map[string]string{}
	fields := map[string]map[string]string{}
	for _, f := range selected {
		label := f.service + "/" + f.kind
		arnStr, wantFields := f.create(ctx, t, h)
		t.Logf("created %s", label)
		want[arnStr] = label
		fields[label] = wantFields
	}

	h.waitForTagIndex(ctx, t, want)

	for _, f := range selected {
		label := f.service + "/" + f.kind
		t.Run(label, func(t *testing.T) {
			stdout, stderr := h.adopt(t, f.service, f.kind)
			assertOneManifest(t, f, h, stdout, stderr, fields[label])
		})
	}
}

func assertOneManifest(
	t *testing.T,
	f fixture,
	h *harness,
	stdout, stderr string,
	wantFields map[string]string,
) {
	t.Helper()

	crs := parseManifests(t, stdout)
	require.Len(t, crs, 1,
		"the run tag is unique to this test, so exactly one resource should have matched\nstderr:\n%s", stderr)
	cr := crs[0]

	assert.Equal(t, f.group+"/"+f.version, cr.APIVersion)
	assert.Equal(t, f.kind, cr.Kind)
	assert.Empty(t, cr.Spec, "adoption manifests carry no spec; the controller fills it in")

	ann := cr.Metadata.Annotations
	assert.Equal(t, "adopt", ann["services.k8s.aws/adoption-policy"])
	assert.Equal(t, "true", ann["services.k8s.aws/read-only"],
		"the first run must be observe-only")
	assert.Equal(t, "retain", ann["services.k8s.aws/deletion-policy"],
		"deleting an adoption CR must not delete infrastructure ACK did not create")

	// The region must come from the query, not the ARN, because an s3 or iam ARN has
	// an empty region slot.
	assert.Equal(t, h.region, ann["services.k8s.aws/region"])

	var got map[string]string
	require.NoError(t, json.Unmarshal([]byte(ann["services.k8s.aws/adoption-fields"]), &got),
		"adoption-fields must be a JSON object; ACK parses it")
	assert.Equal(t, wantFields, got,
		"adoption-fields do not match what the AWS API reported for this resource, so "+
			"the controller would look up the wrong resource or fail to find it")

	assert.Regexp(t, dns1123Subdomain, cr.Metadata.Name,
		"the API server rejects names outside this format")
	assert.LessOrEqual(t, len(cr.Metadata.Name), 253)
	assert.NotEmpty(t, cr.Metadata.Labels["ack.k8s.aws/adoption-set"],
		"an unnamed set still needs a label, or the collection cannot be selected later")

	// stdout must be pipeable into kubectl, so the run summary belongs on stderr.
	assert.NotContains(t, stdout, "resolved ",
		"the summary leaked into stdout, which would corrupt the YAML stream")
	assert.Contains(t, stderr, "resolved 1")
}

// TestAdoptIsDeterministic pins the property that makes re-running safe: identical
// runs produce identical YAML, so `kubectl create` rejects a second adoption of the
// same resource by name instead of creating a duplicate CR.
func TestAdoptIsDeterministic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	h := newHarness(ctx, t)

	f := selectedFixtures(t)[0]
	arnStr, _ := f.create(ctx, t, h)
	h.waitForTagIndex(ctx, t, map[string]string{arnStr: f.service + "/" + f.kind})

	first, _ := h.adopt(t, f.service, f.kind)
	second, _ := h.adopt(t, f.service, f.kind)
	require.Equal(t, first, second,
		"two identical runs produced different YAML; re-running would create a second CR "+
			"for the same AWS resource")
	require.NotEmpty(t, first)
}

// TestAdoptExplainsAnEmptyResult checks the diagnosis path, which is the outcome
// users hit most often. A bare "no matches" cannot distinguish a wrong region from a
// wrong tag, so the message has to say which.
func TestAdoptExplainsAnEmptyResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	h := newHarness(ctx, t)

	stdout, stderr := h.adopt(t, "s3", "Bucket")
	assert.Empty(t, strings.TrimSpace(stdout),
		"an empty result must emit no YAML at all, or a pipe into kubectl would apply nothing "+
			"while looking like it worked")
	assert.Contains(t, stderr, "no s3/Bucket resources matched")
	assert.Contains(t, stderr, h.region, "the region searched is the most common cause")
	assert.Contains(t, stderr, "s3:bucket", "the type filter used should be visible")
}

// createTopic makes an SNS topic, whose ACK identifier is its ARN.
func createTopic(ctx context.Context, t *testing.T, h *harness) (string, map[string]string) {
	name := h.resourceName()
	c := sns.NewFromConfig(h.cfg)

	out, err := c.CreateTopic(ctx, &sns.CreateTopicInput{
		Name: &name,
		Tags: []snstypes.Tag{{Key: aws.String(runTagKey), Value: aws.String(h.runID)}},
	})
	require.NoError(t, err, "creating topic %s", name)
	topicARN := aws.ToString(out.TopicArn)
	t.Cleanup(func() {
		_, derr := c.DeleteTopic(context.Background(), &sns.DeleteTopicInput{TopicArn: &topicARN})
		cleanupErr(t, derr, "sns topic "+topicARN)
	})

	return topicARN, map[string]string{"arn": topicARN}
}

// createStage makes an HTTP API and a stage on it, giving two nested identifiers
// under an AWS namespace ("apigateway") that is not the ACK service name.
func createStage(ctx context.Context, t *testing.T, h *harness) (string, map[string]string) {
	name := h.resourceName()
	c := apigatewayv2.NewFromConfig(h.cfg)

	api, err := c.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name:         &name,
		ProtocolType: apitypes.ProtocolTypeHttp,
		Tags:         map[string]string{runTagKey: h.runID},
	})
	require.NoError(t, err, "creating API %s", name)
	apiID := aws.ToString(api.ApiId)
	t.Cleanup(func() {
		// Deleting the API deletes its stages, so this is the only teardown needed.
		_, derr := c.DeleteApi(context.Background(), &apigatewayv2.DeleteApiInput{ApiId: &apiID})
		cleanupErr(t, derr, "apigatewayv2 api "+apiID)
	})

	stageName := "probe"
	_, err = c.CreateStage(ctx, &apigatewayv2.CreateStageInput{
		ApiId:     &apiID,
		StageName: &stageName,
		Tags:      map[string]string{runTagKey: h.runID},
	})
	require.NoError(t, err, "creating stage on %s", apiID)

	// CreateStage returns no ARN, and the form is fixed by the grammar.
	return "arn:" + h.partition + ":apigateway:" + h.region + "::/apis/" + apiID + "/stages/" + stageName,
		map[string]string{"apiID": apiID, "stageName": stageName}
}
