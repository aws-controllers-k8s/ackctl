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

//go:build integration || e2e

// Package integration drives the built ack binary against real AWS, which is the only way to
// test that a kind the catalog calls adoptable can actually be adopted. It CREATES AND
// DELETES real resources, all free of charge and tagged with a run-unique value.
package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	rgt "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgttypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// runTagKey is the tag every fixture carries, with a value unique per run so the
// selector cannot match anything this run did not create.
const runTagKey = "ack-integration-run"

// resourcePrefix marks created resources as test scaffolding by name as well as by
// tag, so anything left behind by a killed run is recognisable.
const resourcePrefix = "ack-it-"

// tagIndexTimeout bounds the wait for a freshly tagged resource to become visible
// to GetResources, which is an eventually consistent index.
const tagIndexTimeout = 6 * time.Minute

type harness struct {
	cfg       aws.Config
	region    string
	partition string
	account   string
	bin       string
	runID     string
	rgt       *rgt.Client
}

func newHarness(ctx context.Context, t *testing.T) *harness {
	t.Helper()

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	require.NoError(t, err, "this test needs AWS credentials")
	require.NotEmpty(t, cfg.Region,
		"no region resolved: set AWS_REGION. GetResources is regional, and the region "+
			"is written to every emitted CR")

	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err, "could not identify the calling account")
	callerARN, err := arn.Parse(aws.ToString(ident.Arn))
	require.NoError(t, err)

	h := &harness{
		cfg:       cfg,
		region:    cfg.Region,
		partition: callerARN.Partition,
		account:   aws.ToString(ident.Account),
		runID:     randomID(t),
		rgt:       rgt.NewFromConfig(cfg),
	}
	h.bin = buildBinary(t)

	t.Logf("region %s, run %s=%s", h.region, runTagKey, h.runID)
	return h
}

// randomID is random rather than clock-derived so two runs starting in the same
// second cannot share a tag value and tear down each other's resources.
func randomID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

// buildBinary compiles the real CLI, so flag parsing, region resolution and the stdout/stderr
// split stay in scope.
func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ack")
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/ack")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building ack failed:\n%s", out)
	return bin
}

func (h *harness) resourceName() string { return resourcePrefix + h.runID }

func (h *harness) runTag() string { return runTagKey + "=" + h.runID }

// adopt runs `ack adopt` for one kind and returns stdout and stderr separately.
func (h *harness) adopt(t *testing.T, service, kind string, extra ...string) (string, string) {
	t.Helper()
	args := append([]string{
		"adopt",
		"--service", service,
		"--kind", kind,
		"--tag", h.runTag(),
		"--region", h.region,
	}, extra...)

	var stdout, stderr strings.Builder
	cmd := exec.Command(h.bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	require.NoError(t, err, "ack %s failed\nstdout:\n%s\nstderr:\n%s",
		strings.Join(args, " "), stdout.String(), stderr.String())
	return stdout.String(), stderr.String()
}

// waitForTagIndex blocks until every ARN is visible to GetResources under this
// run's tag. A timeout is a real finding: it means a kind the catalog calls
// adoptable is not returned by the Tagging API at all.
func (h *harness) waitForTagIndex(ctx context.Context, t *testing.T, want map[string]string) {
	t.Helper()
	deadline := time.Now().Add(tagIndexTimeout)
	pending := map[string]string{}
	for a, label := range want {
		pending[a] = label
	}

	for attempt := 1; ; attempt++ {
		found, err := h.indexedARNs(ctx)
		require.NoError(t, err, "querying the Tagging API failed")
		for a := range pending {
			if found[a] {
				delete(pending, a)
			}
		}
		if len(pending) == 0 {
			t.Logf("all %d resource(s) indexed after %d poll(s)", len(want), attempt)
			return
		}
		if time.Now().After(deadline) {
			var missing []string
			for a, label := range pending {
				missing = append(missing, fmt.Sprintf("%s (%s)", label, a))
			}
			t.Fatalf("after %s, %d resource(s) never appeared in the Tagging API:\n  %s\n"+
				"They exist and are tagged, so either indexing is unusually slow, or the "+
				"Tagging API does not index these kinds at all — in which case the catalog "+
				"should not offer them for tag-based adoption.",
				tagIndexTimeout, len(pending), strings.Join(missing, "\n  "))
		}
		time.Sleep(10 * time.Second)
	}
}

func (h *harness) indexedARNs(ctx context.Context) (map[string]bool, error) {
	found := map[string]bool{}
	p := rgt.NewGetResourcesPaginator(h.rgt, &rgt.GetResourcesInput{
		TagFilters: []rgttypes.TagFilter{{
			Key:    aws.String(runTagKey),
			Values: []string{h.runID},
		}},
	})
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, m := range out.ResourceTagMappingList {
			found[aws.ToString(m.ResourceARN)] = true
		}
	}
	return found, nil
}

// manifest mirrors the emitted CR. It is declared here rather than imported so the
// assertions do not share a definition with the code that produced the output.
type manifest struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec map[string]interface{} `json:"spec"`
}

// docSeparator splits a YAML stream on its document markers. Annotation values hold
// JSON on a single line, so no value can contain a line that is bare "---".
var docSeparator = regexp.MustCompile(`(?m)^---$`)

// dns1123Subdomain is the name format the Kubernetes API server enforces, so an
// emitted name that violates it fails at apply time rather than here.
var dns1123Subdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// cleanupErr reports a teardown failure loudly without failing the test, since
// turning it into a failure would mask the result the test actually measured.
func cleanupErr(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		fmt.Fprintf(os.Stderr, "LEAKED: could not delete %s: %v\n", what, err)
		t.Logf("LEAKED %s: %v", what, err)
	}
}

func parseManifests(t *testing.T, stdout string) []manifest {
	t.Helper()
	var out []manifest
	for _, doc := range docSeparator.Split(stdout, -1) {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var cr manifest
		require.NoError(t, yaml.Unmarshal([]byte(doc), &cr),
			"emitted document is not valid YAML:\n%s", doc)
		out = append(out, cr)
	}
	return out
}

// createBucket makes an empty bucket, whose ARN carries neither region nor account.
func createBucket(ctx context.Context, t *testing.T, h *harness) (string, map[string]string) {
	name := h.resourceName()
	c := s3.NewFromConfig(h.cfg)

	in := &s3.CreateBucketInput{Bucket: &name}
	if h.region != "us-east-1" {
		in.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(h.region),
		}
	}
	_, err := c.CreateBucket(ctx, in)
	require.NoError(t, err, "creating bucket %s", name)
	t.Cleanup(func() {
		_, derr := c.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: &name})
		cleanupErr(t, derr, "s3 bucket "+name)
	})

	_, err = c.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket: &name,
		Tagging: &s3types.Tagging{TagSet: []s3types.Tag{{
			Key:   aws.String(runTagKey),
			Value: aws.String(h.runID),
		}}},
	})
	require.NoError(t, err, "tagging bucket %s", name)

	// S3 returns no ARN and its form is fixed, so the partition comes from the caller.
	return "arn:" + h.partition + ":s3:::" + name, map[string]string{"name": name}
}
