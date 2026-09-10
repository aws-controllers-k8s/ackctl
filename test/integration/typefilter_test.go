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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	rgt "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/ackctl/internal/resolver"
	"github.com/aws-controllers-k8s/ackctl/metadata"
)

// maxPagesPerQuery bounds an untagged sweep, since finding one resource of a kind is
// enough to judge its filter.
const maxPagesPerQuery = 5

// crossCheckConcurrency keeps the sweep quick without tripping GetResources
// throttling, which is a few requests per second per account.
const crossCheckConcurrency = 4

// TestTypeFiltersReturnWhatTheServiceIndexes proves type filters wrong using resources
// that already exist in the account, which the sweep in internal/tagging cannot do.
//
// It queries the bare SERVICE filter, attributes each returned ARN to the kind whose
// template matches it, and requires that kind's own filter to return something too, so a
// kind with no resources in the account stays unproven.
func TestTypeFiltersReturnWhatTheServiceIndexes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	require.NoError(t, err, "this test needs AWS credentials")
	require.NotEmpty(t, cfg.Region, "no region resolved: set AWS_REGION")
	api := rgt.NewFromConfig(cfg)

	catalog, err := metadata.Load()
	require.NoError(t, err)
	res, err := resolver.New(catalog)
	require.NoError(t, err)

	// Group by the AWS namespace in the filter, not the ACK service name, because
	// they differ (prometheusservice -> aps, eventbridge -> events).
	byNamespace := map[string][]metadata.Resource{}
	for _, r := range catalog.Resources() {
		if r.ResourceTypeFilter == "" {
			continue
		}
		ns, _, _ := strings.Cut(r.ResourceTypeFilter, ":")
		byNamespace[ns] = append(byNamespace[ns], r)
	}
	namespaces := sortedKeys(byNamespace)
	t.Logf("cross-checking %d kinds across %d AWS namespaces in %s",
		len(catalog.Resources()), len(namespaces), cfg.Region)

	inventory := make([]map[string]bool, len(namespaces))
	runConcurrently(len(namespaces), func(i int) {
		arns, qerr := queryARNs(ctx, api, namespaces[i])
		if qerr != nil {
			t.Logf("namespace %-24s no inventory: %v", namespaces[i], qerr)
			return
		}
		inventory[i] = arns
	})

	claims := map[string][]string{}
	kindOf := map[string]metadata.Resource{}
	for i, ns := range namespaces {
		for arnStr := range inventory[i] {
			var matched []metadata.Resource
			for _, r := range byNamespace[ns] {
				// An ARN-primary kind accepts any ARN by definition, so it can never
				// be attributed this way and never contributes evidence.
				if r.ARNPrimary {
					continue
				}
				if _, rerr := res.ResolveARNForResource(arnStr, r); rerr == nil {
					matched = append(matched, r)
				}
			}
			// Two templates matching one ARN means neither can be held responsible.
			if len(matched) != 1 {
				continue
			}
			key := matched[0].Service + "/" + matched[0].Kind
			claims[key] = append(claims[key], arnStr)
			kindOf[key] = matched[0]
		}
	}

	keys := sortedKeys(claims)
	verdicts := make([]string, len(keys))
	runConcurrently(len(keys), func(i int) {
		r := kindOf[keys[i]]
		got, qerr := queryARNs(ctx, api, r.ResourceTypeFilter)
		switch {
		case qerr != nil:
			verdicts[i] = "inconclusive: " + qerr.Error()
		case len(got) == 0:
			verdicts[i] = "WRONG"
		default:
			verdicts[i] = "ok"
		}
	})

	var wrong, ok, inconclusive int
	for i, key := range keys {
		switch {
		case verdicts[i] == "ok":
			ok++
		case verdicts[i] == "WRONG":
			wrong++
			r := kindOf[key]
			t.Errorf("type filter %q for %s returns NOTHING, but %d resource(s) of that "+
				"kind are indexed under the bare %q filter, e.g.\n    %s\n"+
				"The filter is wrong: users get \"no resources matched\" for resources that "+
				"exist. Correct it in metadata/overrides.json, or if the service is only "+
				"indexed at service granularity, use the bare namespace as the filter.",
				r.ResourceTypeFilter, key, len(claims[key]),
				namespaceOf(r.ResourceTypeFilter), claims[key][0])
		default:
			inconclusive++
			t.Logf("inconclusive %-36s %s", key, verdicts[i])
		}
	}
	t.Logf("cross-check: %d filters confirmed, %d WRONG, %d inconclusive, "+
		"%d kinds had no resources in this account",
		ok, wrong, inconclusive, len(catalog.Resources())-len(keys))
}

// queryARNs returns the ARNs a single filter yields, untagged and page-capped.
func queryARNs(ctx context.Context, api *rgt.Client, filter string) (map[string]bool, error) {
	perPage := int32(100)
	p := rgt.NewGetResourcesPaginator(api, &rgt.GetResourcesInput{
		ResourceTypeFilters: []string{filter},
		ResourcesPerPage:    &perPage,
	})
	out := map[string]bool{}
	for page := 0; p.HasMorePages() && page < maxPagesPerQuery; page++ {
		res, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, m := range res.ResourceTagMappingList {
			out[aws.ToString(m.ResourceARN)] = true
		}
	}
	return out, nil
}

func namespaceOf(filter string) string {
	ns, _, _ := strings.Cut(filter, ":")
	return ns
}

func runConcurrently(n int, fn func(i int)) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, crossCheckConcurrency)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
