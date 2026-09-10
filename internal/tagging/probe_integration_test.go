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

// Probes the catalog's type filters against real AWS, testing the one claim the hermetic
// suite cannot: that a kind the catalog calls adoptable is one GetResources returns. Only a
// returned resource proves a filter good, since an empty result is indistinguishable from an
// account with none.
//
//	AWS_REGION=us-west-2 make test-probe
package tagging

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	rgt "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/ackctl/metadata"
)

// probeConcurrency keeps the sweep quick without tripping GetResources throttling,
// which is a few requests per second per account.
const probeConcurrency = 4

type verdict int

const (
	// unproven means the call succeeded and returned nothing, which says nothing
	// either way.
	unproven verdict = iota
	// proven means the call returned at least one resource, so the filter is indexed.
	proven
	// malformed means the API rejected the filter's syntax.
	malformed
	// inconclusive covers throttling, permissions and network failures.
	inconclusive
)

var verdictName = map[verdict]string{
	unproven: "unproven", proven: "proven",
	malformed: "MALFORMED", inconclusive: "inconclusive",
}

type probeResult struct {
	filter  string
	kinds   []string
	verdict verdict
	found   int
	err     error
}

// TestProbeTypeFilters sweeps every distinct type filter, failing only on a malformed one
// because the proven/unproven split depends on how populated the account is.
func TestProbeTypeFilters(t *testing.T) {
	catalog, err := metadata.Load()
	require.NoError(t, err)

	// Several kinds legitimately share one filter, as lambda:function covers Function,
	// Alias, Version and FunctionURLConfig.
	kindsByFilter := map[string][]string{}
	for _, r := range catalog.Resources() {
		kindsByFilter[r.ResourceTypeFilter] = append(
			kindsByFilter[r.ResourceTypeFilter], r.Service+"/"+r.Kind)
	}
	filters := make([]string, 0, len(kindsByFilter))
	for f := range kindsByFilter {
		filters = append(filters, f)
	}
	sort.Strings(filters)
	require.NotEmpty(t, filters)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	require.NoError(t, err, "the probe needs AWS credentials and a region")
	require.NotEmpty(t, cfg.Region,
		"no region resolved: set AWS_REGION. GetResources is regional, so the region "+
			"decides which resources exist to be found")
	api := rgt.NewFromConfig(cfg)
	t.Logf("probing %d distinct filters (%d kinds) in %s",
		len(filters), len(catalog.Resources()), cfg.Region)

	results := make([]probeResult, len(filters))
	var wg sync.WaitGroup
	sem := make(chan struct{}, probeConcurrency)
	for i, f := range filters {
		wg.Add(1)
		go func(i int, f string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = probeFilter(ctx, api, f, kindsByFilter[f])
		}(i, f)
	}
	wg.Wait()

	byVerdict := map[verdict][]probeResult{}
	for _, r := range results {
		byVerdict[r.verdict] = append(byVerdict[r.verdict], r)
	}
	t.Logf("proven %d, unproven %d, malformed %d, inconclusive %d",
		len(byVerdict[proven]), len(byVerdict[unproven]),
		len(byVerdict[malformed]), len(byVerdict[inconclusive]))

	for _, r := range byVerdict[proven] {
		t.Logf("proven   %-42s %d resource(s)", r.filter, r.found)
	}
	for _, r := range byVerdict[inconclusive] {
		t.Logf("INCONCLUSIVE %-42s %v", r.filter, r.err)
	}

	// This is the only assertable defect; everything else is a measurement.
	for _, r := range byVerdict[malformed] {
		t.Errorf("type filter %q is MALFORMED — the Tagging API rejected its syntax, so "+
			"the %d kind(s) claiming it can never be discovered: %s\n"+
			"    Correct the filter in metadata/overrides.json, or move those kinds to "+
			"the unsupported list so the CLI explains itself.",
			r.filter, len(r.kinds), strings.Join(r.kinds, ", "))
	}

	if len(byVerdict[unproven]) > 0 {
		t.Logf("%d filter(s) returned nothing in %s. That is not a failure: it means no "+
			"such resources exist here, OR the filter is silently wrong, and this API "+
			"cannot tell those apart. Probing an account with more resources, or more "+
			"regions, raises the proven count.",
			len(byVerdict[unproven]), cfg.Region)
	}

	if summary := os.Getenv("ACK_PROBE_SUMMARY"); summary != "" {
		require.NoError(t, writeSummary(summary, cfg.Region, results, kindsByFilter))
		t.Logf("wrote %s", summary)
	}
}

func probeFilter(ctx context.Context, api GetResourcesAPI, filter string, kinds []string) probeResult {
	res := probeResult{filter: filter, kinds: kinds}
	perPage := int32(50)
	out, err := api.GetResources(ctx, &rgt.GetResourcesInput{
		ResourceTypeFilters: []string{filter},
		ResourcesPerPage:    &perPage,
	})
	switch {
	case err != nil && isUnsupportedTypeErr(err):
		res.verdict, res.err = malformed, err
	case err != nil:
		res.verdict, res.err = inconclusive, err
	case len(out.ResourceTagMappingList) > 0:
		res.verdict, res.found = proven, len(out.ResourceTagMappingList)
	default:
		res.verdict = unproven
	}
	return res
}

// writeSummary records the sweep so a coverage claim can cite a measurement. Runs combine,
// because the proven set is a union.
func writeSummary(path, region string, results []probeResult, kindsByFilter map[string][]string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Tagging API type-filter probe\n\nRegion: %s\n\n", region)
	fmt.Fprintf(&b, "`unproven` means the filter returned nothing here, which does not\n")
	fmt.Fprintf(&b, "distinguish \"no such resources\" from \"filter is wrong\".\n\n")
	fmt.Fprintf(&b, "| Filter | Kinds | Verdict | Found |\n|---|---|---|---|\n")
	for _, r := range results {
		fmt.Fprintf(&b, "| `%s` | %d | %s | %d |\n",
			r.filter, len(kindsByFilter[r.filter]), verdictName[r.verdict], r.found)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// TestProbeDetectsAMalformedFilter is the control for the sweep's only assertion, since a
// dead oracle looks exactly like a clean result.
func TestProbeDetectsAMalformedFilter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	require.NoError(t, err)

	got := probeFilter(ctx, rgt.NewFromConfig(cfg), "::::", nil)
	require.Equal(t, malformed, got.verdict,
		"malformed-filter detection is broken, so the sweep's only assertion proves "+
			"nothing (got %s: %v)", verdictName[got.verdict], got.err)
}

// TestProbeCannotDetectAPlausibleWrongFilter pins the limitation so nobody reasons as though
// the probe validates filters in general.
func TestProbeCannotDetectAPlausibleWrongFilter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	api := rgt.NewFromConfig(cfg)

	for _, filter := range []string{"notaservice:notatype", "lambda:notatype", "lambda:code signing config"} {
		got := probeFilter(ctx, api, filter, nil)
		require.Equal(t, unproven, got.verdict,
			"%q: expected the API to accept a nonsense filter and return nothing. If this "+
				"now reports %s, AWS has started validating resource types and the sweep "+
				"can be strengthened to assert on every filter.",
			filter, verdictName[got.verdict])
	}
}
