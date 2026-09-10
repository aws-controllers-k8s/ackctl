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

//go:build e2e

package integration

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eNamespace must exist in the cluster: it is the namespace the manifests carry.
const e2eNamespace = "default"

// syncTimeout bounds the wait for the controller to adopt and reconcile.
const syncTimeout = 3 * time.Minute

// TestAdoptedBucketReachesSynced proves the product claim end to end: a manifest ack
// emitted, applied to a real cluster, brings the resource under ACK management.
//
// It also checks the two safety properties nothing else can -- that read-only adoption does
// not mutate the resource, and that a retained CR's deletion leaves AWS untouched.
func TestAdoptedBucketReachesSynced(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	requireCluster(t)
	h := newHarness(ctx, t)

	bucketARN, wantFields := createBucket(ctx, t, h)
	bucket := h.resourceName()
	t.Logf("created %s", bucketARN)
	h.waitForTagIndex(ctx, t, map[string]string{bucketARN: "s3/Bucket"})

	stdout, _ := h.adopt(t, "s3", "Bucket", "--namespace", e2eNamespace)
	crs := parseManifests(t, stdout)
	require.Len(t, crs, 1, "the run tag is unique, so exactly one bucket should have matched")
	name := crs[0].Metadata.Name
	require.Equal(t, wantFields["name"], bucket)

	// kubectl create, not apply: the README tells users to use create because it is what
	// makes a duplicate adoption fail instead of silently patching an existing CR.
	kubectlCreate(t, stdout)
	t.Cleanup(func() { kubectlDelete(t, "bucket", name) })

	waitForCondition(t, "bucket", name, "ACK.ResourceSynced", "True")
	t.Logf("%s reached ACK.ResourceSynced=True", name)
	// The runtime records adoption as an annotation, not a condition: ACK.Adopted is
	// declared in apis/core/v1alpha1 but never set, left over from the AdoptedResource CRD.
	assert.Equal(t, "true", kubectlGet(t, "bucket", name,
		`{.metadata.annotations['services\.k8s\.aws/adopted']}`),
		"the controller should have marked the resource adopted, which is what records that "+
			"ACK did not create it")

	assert.Equal(t, bucket, kubectlGet(t, "bucket", name, "{.spec.name}"),
		"spec.name should have been populated from the live bucket")

	assertNoACKTags(ctx, t, h, bucket)
	assertDuplicateAdoptionRefused(t, stdout)

	// deletion-policy: retain means the CR goes and the bucket stays.
	kubectlDelete(t, "bucket", name)
	requireBucketExists(ctx, t, h, bucket,
		"the CR was emitted with deletion-policy: retain, so deleting it must not delete the bucket")
}

// assertNoACKTags checks a read-only adoption left the resource alone: the reconciler skips
// EnsureTags for read-only resources, so ACK's own tag keys must be absent.
func assertNoACKTags(ctx context.Context, t *testing.T, h *harness, bucket string) {
	t.Helper()
	out, err := s3.NewFromConfig(h.cfg).GetBucketTagging(ctx, &s3.GetBucketTaggingInput{
		Bucket: aws.String(bucket),
	})
	require.NoError(t, err, "reading tags back from the bucket")

	var ackKeys []string
	for _, tag := range out.TagSet {
		if k := aws.ToString(tag.Key); strings.HasPrefix(k, "services.k8s.aws/") {
			ackKeys = append(ackKeys, k)
		}
	}
	assert.Empty(t, ackKeys,
		"read-only adoption added ACK's own tags to the bucket, so it is not observe-only: %v", ackKeys)
}

// assertDuplicateAdoptionRefused re-applies the same manifest, which must fail: identity-
// derived names are what stop two CRs managing one bucket.
func assertDuplicateAdoptionRefused(t *testing.T, manifest string) {
	t.Helper()
	cmd := exec.Command("kubectl", "create", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "a second create of the same manifest must be refused, not accepted")
	assert.Contains(t, string(out), "AlreadyExists",
		"expected the API server to reject the duplicate by name")
}

// requireCluster fails early, since every later failure would be a confusing consequence.
func requireCluster(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("kubectl", "get", "--raw", "/readyz").CombinedOutput(); err != nil {
		t.Fatalf("no reachable Kubernetes cluster (kubectl get --raw /readyz): %v\n%s", err, out)
	}
	if out, err := exec.Command("kubectl", "get", "crd", "buckets.s3.services.k8s.aws").CombinedOutput(); err != nil {
		t.Fatalf("the s3 controller's CRDs are not installed, so nothing can adopt a Bucket: %v\n%s", err, out)
	}
}

func kubectlCreate(t *testing.T, manifest string) {
	t.Helper()
	cmd := exec.Command("kubectl", "create", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "kubectl create failed:\n%s\nmanifest:\n%s", out, manifest)
	t.Logf("kubectl create: %s", strings.TrimSpace(string(out)))
}

func kubectlDelete(t *testing.T, kind, name string) {
	t.Helper()
	out, err := exec.Command("kubectl", "delete", kind, name,
		"-n", e2eNamespace, "--ignore-not-found", "--timeout=2m").CombinedOutput()
	if err != nil {
		t.Logf("kubectl delete %s/%s: %v\n%s", kind, name, err, out)
	}
}

func kubectlGet(t *testing.T, kind, name, jsonpath string) string {
	t.Helper()
	out, err := exec.Command("kubectl", "get", kind, name,
		"-n", e2eNamespace, "-o", "jsonpath="+jsonpath).Output()
	require.NoError(t, err, "kubectl get %s/%s -o jsonpath=%s", kind, name, jsonpath)
	return strings.TrimSpace(string(out))
}

// conditionStatus returns the status of one ACK condition, or "" when absent.
func conditionStatus(t *testing.T, kind, name, condType string) string {
	t.Helper()
	return kubectlGet(t, kind, name,
		`{.status.conditions[?(@.type=="`+condType+`")].status}`)
}

// waitForCondition polls until an ACK condition reaches the wanted status, reporting the
// resource's own message on timeout.
func waitForCondition(t *testing.T, kind, name, condType, want string) {
	t.Helper()
	deadline := time.Now().Add(syncTimeout)
	for {
		if got := conditionStatus(t, kind, name, condType); got == want {
			return
		}
		if time.Now().After(deadline) {
			msg := kubectlGet(t, kind, name,
				`{.status.conditions[?(@.type=="`+condType+`")].message}`)
			terminal := kubectlGet(t, kind, name,
				`{.status.conditions[?(@.type=="ACK.Terminal")].message}`)
			t.Fatalf("%s/%s did not reach %s=%s within %s\n  message:  %q\n  terminal: %q\n"+
				"The manifest was accepted, so the adoption-fields it carries are what the "+
				"controller could not resolve.", kind, name, condType, want, syncTimeout, msg, terminal)
		}
		time.Sleep(5 * time.Second)
	}
}

func requireBucketExists(ctx context.Context, t *testing.T, h *harness, bucket, msg string) {
	t.Helper()
	_, err := s3.NewFromConfig(h.cfg).HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	require.NoError(t, err, msg)
}
