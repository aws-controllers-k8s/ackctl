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

// Package tagging wraps the AWS Resource Groups Tagging API GetResources call
// used to discover resource ARNs by tag.
package tagging

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	rgt "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgttypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"

	"github.com/aws-controllers-k8s/ackctl/internal/debuglog"
)

// UnsupportedTypeError means GetResources rejected a resource-type filter as
// malformed, which is the only filter problem it reports. A filter that is
// well-formed but wrong is accepted and returns nothing, indistinguishable from the
// kind having no resources.
type UnsupportedTypeError struct {
	TypeFilter string
	Err        error
}

func (e *UnsupportedTypeError) Error() string {
	return fmt.Sprintf("the Resource Groups Tagging API rejected the resource type filter %q as malformed", e.TypeFilter)
}

func (e *UnsupportedTypeError) Unwrap() error { return e.Err }

// isUnsupportedTypeErr reports whether GetResources rejected the filter itself rather
// than failing transiently. In practice it fires only for malformed syntax.
func isUnsupportedTypeErr(err error) bool {
	var invalid *rgttypes.InvalidParameterException
	return errors.As(err, &invalid)
}

// GetResourcesAPI is the subset of the Tagging API this package needs, so it can
// be faked in tests without a live client.
type GetResourcesAPI interface {
	GetResources(ctx context.Context, in *rgt.GetResourcesInput, optFns ...func(*rgt.Options)) (*rgt.GetResourcesOutput, error)
}

// Filter is one tag condition. Any means "this key with any value", which is distinct from
// Value being empty: AWS permits an empty tag value, so both must be expressible.
type Filter struct {
	Key   string
	Value string
	Any   bool
}

type Match struct {
	ARN  string
	Tags map[string]string
	// TypeFilter is the resource-type filter this match was found under, or empty
	// when the query was not scoped by type.
	TypeFilter string
}

type Client struct {
	api GetResourcesAPI
}

func New(api GetResourcesAPI) *Client {
	return &Client{api: api}
}

// FindByTags returns every resource matching ALL of the supplied filters, optionally scoped
// to the given resource-type filters, paging through all results.
func (c *Client) FindByTags(ctx context.Context, tags []Filter, typeFilters []string) ([]Match, error) {
	filters := make([]rgttypes.TagFilter, 0, len(tags))
	for _, t := range tags {
		f := rgttypes.TagFilter{Key: strPtr(t.Key)}
		if !t.Any {
			f.Values = []string{t.Value}
		}
		filters = append(filters, f)
	}

	in := &rgt.GetResourcesInput{TagFilters: filters}
	if len(typeFilters) > 0 {
		in.ResourceTypeFilters = typeFilters
	}

	debuglog.Logf("GetResources request: ResourceTypeFilters=%s TagFilters=%s",
		formatTypeFilters(typeFilters), formatTagFilters(filters))

	var out []Match
	pageNum := 0
	p := rgt.NewGetResourcesPaginator(c.api, in)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		pageNum++
		if err != nil {
			debuglog.Logf("GetResources page %d failed: %v", pageNum, err)
			if len(typeFilters) == 1 && isUnsupportedTypeErr(err) {
				return nil, &UnsupportedTypeError{TypeFilter: typeFilters[0], Err: err}
			}
			return nil, fmt.Errorf("GetResources: %w", err)
		}
		debuglog.Logf("GetResources page %d: %d resource(s)", pageNum, len(page.ResourceTagMappingList))
		for _, m := range page.ResourceTagMappingList {
			if m.ResourceARN == nil {
				debuglog.Logf("  skip: entry with nil ResourceARN")
				continue
			}
			tagMap := make(map[string]string, len(m.Tags))
			for _, t := range m.Tags {
				if t.Key != nil {
					tagMap[*t.Key] = deref(t.Value)
				}
			}
			debuglog.Logf("  match: %s tags=%s", *m.ResourceARN, formatTags(tagMap))
			out = append(out, Match{ARN: *m.ResourceARN, Tags: tagMap})
		}
	}
	debuglog.Logf("GetResources returned %d resource(s) across %d page(s)", len(out), pageNum)
	return out, nil
}

// countMaxPages caps the diagnostic sweep. It runs only after a query already returned
// nothing, so walking a large account to produce an exact count is not worth the calls.
const countMaxPages = 10

// CountByType reports how many resources of the given type exist in the account and
// region, ignoring tag filters. It is what makes an empty tag-filtered result
// interpretable. capped reports that counting stopped early, so total is a lower bound.
func (c *Client) CountByType(ctx context.Context, typeFilter string, sampleLimit int) (total int, capped bool, sampleTags []map[string]string, err error) {
	in := &rgt.GetResourcesInput{ResourceTypeFilters: []string{typeFilter}}
	p := rgt.NewGetResourcesPaginator(c.api, in)
	for pages := 0; p.HasMorePages(); pages++ {
		if pages == countMaxPages {
			return total, true, sampleTags, nil
		}
		page, perr := p.NextPage(ctx)
		if perr != nil {
			return total, false, sampleTags, perr
		}
		for _, m := range page.ResourceTagMappingList {
			if m.ResourceARN == nil {
				continue
			}
			total++
			if len(sampleTags) < sampleLimit {
				tagMap := make(map[string]string, len(m.Tags))
				for _, t := range m.Tags {
					if t.Key != nil {
						tagMap[*t.Key] = deref(t.Value)
					}
				}
				sampleTags = append(sampleTags, tagMap)
			}
		}
	}
	return total, false, sampleTags, nil
}

// FindByTagsPerType queries each resource-type filter separately so every returned
// Match is labeled with the filter it was found under, letting the resolver decompose
// each ARN against a known Kind instead of guessing. It costs more API calls than one
// batched query, which the correctness win justifies.
func (c *Client) FindByTagsPerType(ctx context.Context, tags []Filter, typeFilters []string) ([]Match, error) {
	if len(typeFilters) == 0 {
		return c.FindByTags(ctx, tags, nil)
	}
	var out []Match
	for _, tf := range typeFilters {
		debuglog.Logf("querying resource type %q", tf)
		ms, err := c.FindByTags(ctx, tags, []string{tf})
		if err != nil {
			return nil, fmt.Errorf("type %s: %w", tf, err)
		}
		for i := range ms {
			ms[i].TypeFilter = tf
		}
		out = append(out, ms...)
	}
	return out, nil
}

func formatTypeFilters(tf []string) string {
	if len(tf) == 0 {
		return "(none: querying ALL resource types)"
	}
	return "[" + strings.Join(tf, " ") + "]"
}

// formatTagFilters renders tag filters the way the API sees them, so a user can spot a
// case mismatch or an unintended key-existence match.
func formatTagFilters(filters []rgttypes.TagFilter) string {
	if len(filters) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(filters))
	for _, f := range filters {
		key := deref(f.Key)
		if len(f.Values) == 0 {
			parts = append(parts, fmt.Sprintf("%s=<any value>", key))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", key, strings.Join(f.Values, "|")))
	}
	return "[" + strings.Join(parts, " AND ") + "]"
}

func formatTags(tags map[string]string) string {
	if len(tags) == 0 {
		return "(none)"
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+tags[k])
	}
	return "{" + strings.Join(parts, " ") + "}"
}

func strPtr(s string) *string { return &s }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
