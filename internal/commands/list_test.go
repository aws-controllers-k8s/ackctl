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

package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runListWith invokes the list command with the package flags set, capturing stdout.
func runListWith(t *testing.T, service string, unsupported bool) (string, error) {
	t.Helper()
	prevService, prevUnsupported := listService, listShowUnsupported
	t.Cleanup(func() { listService, listShowUnsupported = prevService, prevUnsupported })
	listService, listShowUnsupported = service, unsupported

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	err := runList(cmd, nil)
	return buf.String(), err
}

// TestList_ServiceFilterIsCaseInsensitive covers both output paths. The --unsupported path
// once matched the service prefix case-sensitively, so --service EKS silently returned
// nothing while --service eks worked.
func TestList_ServiceFilterIsCaseInsensitive(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		lower, err := runListWith(t, "eks", unsupported)
		require.NoError(t, err)
		upper, err := runListWith(t, "EKS", unsupported)
		require.NoError(t, err)
		assert.Equal(t, lower, upper, "--unsupported=%v: case must not change the result", unsupported)
		assert.Greater(t, strings.Count(lower, "\n"), 1,
			"--unsupported=%v: expected rows, not just a header", unsupported)
	}
}

// TestList_UnknownServiceIsAnError stops an empty table from reading as "nothing here is
// adoptable" when the real problem is a typo.
func TestList_UnknownServiceIsAnError(t *testing.T) {
	_, err := runListWith(t, "notaservice", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown ACK service")

	_, err = runListWith(t, "notaservice", true)
	require.Error(t, err, "--unsupported must validate the service too")
}
