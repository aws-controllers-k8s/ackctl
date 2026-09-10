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

package tagging

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	rgt "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgttypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAPI struct {
	// responses maps the (single) requested type filter to the ARNs returned.
	byType map[string][]string
	calls  []rgt.GetResourcesInput
}

func (f *fakeAPI) GetResources(_ context.Context, in *rgt.GetResourcesInput, _ ...func(*rgt.Options)) (*rgt.GetResourcesOutput, error) {
	f.calls = append(f.calls, *in)
	var tf string
	if len(in.ResourceTypeFilters) == 1 {
		tf = in.ResourceTypeFilters[0]
	}
	var list []rgttypes.ResourceTagMapping
	for _, a := range f.byType[tf] {
		arn := a
		list = append(list, rgttypes.ResourceTagMapping{
			ResourceARN: &arn,
			Tags: []rgttypes.Tag{
				{Key: aws.String("Environment"), Value: aws.String("prod")},
			},
		})
	}
	return &rgt.GetResourcesOutput{ResourceTagMappingList: list}, nil
}

// TestFindByTagsPerType_LabelsMatches confirms each returned match carries the
// type filter it was found under, and that AND tag filters are passed through.
func TestFindByTagsPerType_LabelsMatches(t *testing.T) {
	f := &fakeAPI{byType: map[string][]string{
		"eks:nodegroup": {"arn:aws:eks:us-west-2:1:nodegroup/c/ng/uuid"},
		"s3:bucket":     {"arn:aws:s3:::b1", "arn:aws:s3:::b2"},
	}}
	c := New(f)

	matches, err := c.FindByTagsPerType(context.Background(),
		[]Filter{{Key: "Environment", Value: "prod"}},
		[]string{"eks:nodegroup", "s3:bucket"})
	require.NoError(t, err)
	require.Len(t, matches, 3)

	byARN := map[string]Match{}
	for _, m := range matches {
		byARN[m.ARN] = m
	}
	assert.Equal(t, "eks:nodegroup", byARN["arn:aws:eks:us-west-2:1:nodegroup/c/ng/uuid"].TypeFilter)
	assert.Equal(t, "s3:bucket", byARN["arn:aws:s3:::b1"].TypeFilter)
	assert.Equal(t, "s3:bucket", byARN["arn:aws:s3:::b2"].TypeFilter)

	// one query per type filter, each carrying the tag filter.
	require.Len(t, f.calls, 2)
	for _, call := range f.calls {
		require.Len(t, call.TagFilters, 1)
		assert.Equal(t, "Environment", *call.TagFilters[0].Key)
		assert.Equal(t, []string{"prod"}, call.TagFilters[0].Values)
	}
}

type erroringAPI struct{ err error }

func (e *erroringAPI) GetResources(context.Context, *rgt.GetResourcesInput, ...func(*rgt.Options)) (*rgt.GetResourcesOutput, error) {
	return nil, e.err
}

// TestFindByTags_MalformedType confirms an InvalidParameterException on a single-type
// query is classified as UnsupportedTypeError, so the CLI reports something actionable.
//
// The message says "malformed" deliberately, because GetResources rejects only a filter's
// syntax and never means the kind is unindexed.
func TestFindByTags_MalformedType(t *testing.T) {
	apiErr := &rgttypes.InvalidParameterException{}
	c := New(&erroringAPI{err: apiErr})

	_, err := c.FindByTags(context.Background(),
		[]Filter{{Key: "env", Value: "prod"}}, []string{"::::"})
	require.Error(t, err)

	var unsupported *UnsupportedTypeError
	require.ErrorAs(t, err, &unsupported)
	assert.Equal(t, "::::", unsupported.TypeFilter)
	assert.Contains(t, unsupported.Error(), "malformed")
	// the underlying SDK error stays reachable for debugging
	assert.ErrorIs(t, err, error(apiErr))
}

// TestFindByTags_OtherErrorsNotMisclassified ensures a generic failure (e.g.
// throttling, auth) is NOT reported as "unsupported type".
func TestFindByTags_OtherErrorsNotMisclassified(t *testing.T) {
	c := New(&erroringAPI{err: errors.New("AccessDeniedException: no tag:GetResources")})
	_, err := c.FindByTags(context.Background(),
		[]Filter{{Key: "env", Value: "prod"}}, []string{"s3:bucket"})
	require.Error(t, err)
	var unsupported *UnsupportedTypeError
	assert.False(t, errors.As(err, &unsupported), "generic error must not be classified as unsupported type")
}

// TestFindByTags_KeyExistence confirms a bare key (empty value) is sent with no
// Values, i.e. "match any value for this key".
func TestFindByTags_KeyExistence(t *testing.T) {
	f := &fakeAPI{byType: map[string][]string{"s3:bucket": {"arn:aws:s3:::b1"}}}
	c := New(f)
	_, err := c.FindByTags(context.Background(), []Filter{{Key: "Owner", Any: true}}, []string{"s3:bucket"})
	require.NoError(t, err)
	require.Len(t, f.calls, 1)
	require.Len(t, f.calls[0].TagFilters, 1)
	assert.Equal(t, "Owner", *f.calls[0].TagFilters[0].Key)
	assert.Nil(t, f.calls[0].TagFilters[0].Values)
}

// TestFindByTags_ExplicitEmptyValue is the wire-level half of the empty-value fix: an
// empty Value must be sent as Values:[""], which asks for that exact value, where a
// key-existence match sends no Values at all.
func TestFindByTags_ExplicitEmptyValue(t *testing.T) {
	f := &fakeAPI{}
	c := New(f)
	_, err := c.FindByTags(context.Background(),
		[]Filter{{Key: "env", Value: ""}}, []string{"s3:bucket"})
	require.NoError(t, err)
	require.Len(t, f.calls, 1)
	require.Len(t, f.calls[0].TagFilters, 1)
	assert.Equal(t, []string{""}, f.calls[0].TagFilters[0].Values,
		"KEY= must ask GetResources for an empty value, not for any value")
}

// pagingAPI hands back an unbounded stream of pages, standing in for an account with far
// more resources than the diagnostic needs to count.
type pagingAPI struct{ calls int }

func (p *pagingAPI) GetResources(_ context.Context, _ *rgt.GetResourcesInput, _ ...func(*rgt.Options)) (*rgt.GetResourcesOutput, error) {
	p.calls++
	next := "more"
	return &rgt.GetResourcesOutput{
		PaginationToken:        &next,
		ResourceTagMappingList: []rgttypes.ResourceTagMapping{{ResourceARN: aws.String("arn:aws:s3:::b")}},
	}, nil
}

// TestCountByType_StopsAtCap keeps the diagnostic from walking a large account. It runs
// only after a query already returned nothing, so an exact count is not worth the calls.
func TestCountByType_StopsAtCap(t *testing.T) {
	p := &pagingAPI{}
	total, capped, _, err := New(p).CountByType(context.Background(), "s3:bucket", 5)
	require.NoError(t, err)
	assert.True(t, capped, "counting must report that it stopped early")
	assert.Equal(t, countMaxPages, p.calls, "must stop after the page cap")
	assert.Equal(t, countMaxPages, total, "total is a lower bound once capped")
}
