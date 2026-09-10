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

package metadata

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAccessorsReturnCopies stops a caller from reordering the catalog for everyone else.
// The list command sorts what Resources() hands back, which mutated the loaded catalog in
// place while it returned the internal slice.
func TestAccessorsReturnCopies(t *testing.T) {
	c, err := Load()
	require.NoError(t, err)

	first := c.Resources()[0]
	got := c.Resources()
	sort.Slice(got, func(i, j int) bool { return got[i].Kind > got[j].Kind })
	assert.Equal(t, first, c.Resources()[0], "sorting the returned slice must not reorder the catalog")

	firstUn := c.Unsupported()[0]
	gotUn := c.Unsupported()
	sort.Slice(gotUn, func(i, j int) bool { return gotUn[i].Resource > gotUn[j].Resource })
	assert.Equal(t, firstUn, c.Unsupported()[0])
}
