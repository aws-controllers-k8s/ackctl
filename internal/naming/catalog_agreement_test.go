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

package naming

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/ackctl/metadata"
)

// placeholderRE matches an ARN-template placeholder like ${ClusterName}.
var placeholderRE = regexp.MustCompile(`\$\{([A-Za-z0-9]+)\}`)

// TestCatalogBindingsAgreeWithKeyNames judges every shipped binding against the ACK
// identifier key's own name, so swapping clusterName and name on eks:Nodegroup leaves
// clusterName reading ${NodegroupName}, which no normalization makes agree.
//
// It shares Agree with the generator rather than reimplementing it, making it a regression
// guard against a stale or hand-edited catalog rather than an independent check on the
// rule itself.
func TestCatalogBindingsAgreeWithKeyNames(t *testing.T) {
	catalog, err := metadata.Load()
	require.NoError(t, err)
	exempt := loadNameAgreementExemptions(t)

	for _, r := range catalog.Resources() {
		t.Run(r.Service+"/"+r.Kind, func(t *testing.T) {
			for _, tmpl := range r.Templates {
				for _, b := range tmpl.Bindings {
					placeholder, single := singlePlaceholder(b.From)
					if !single {
						continue // composed: no single placeholder to agree with
					}
					switch strings.ToLower(placeholder) {
					case "account", "region", "partition", "arn":
						continue // reads the ARN envelope, not a path segment
					}
					if exempt[r.Service+":"+r.ResourceDir+"."+b.Key] {
						continue
					}
					assert.True(t, Agree(b.Key, placeholder, r.Kind, r.ResourceDir, r.Service),
						"binding %q reads ${%s}, and the two do not agree by name\n"+
							"  kind:     %s\n  template: %s\n"+
							"a key bound to the wrong placeholder adopts the wrong AWS resource; "+
							"if this binding is deliberate, it needs an entry in overrides.json",
						b.Key, placeholder, r.Kind, tmpl.ARNTemplate)
				}
			}
		})
	}
}

// loadNameAgreementExemptions reads the bindings that are correct but cannot be shown
// correct by name, keyed "service:resource_dir.key". They live in overrides.json so each
// is reviewed as a deliberate claim rather than hidden in a slice here.
func loadNameAgreementExemptions(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "metadata", "overrides.json"))
	require.NoError(t, err, "overrides.json must be readable; it records why each exemption is safe")

	var doc struct {
		Bindings map[string]struct {
			Why string `json:"why"`
		} `json:"bindings"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	out := make(map[string]bool, len(doc.Bindings))
	for k, v := range doc.Bindings {
		assert.NotEmpty(t, v.Why, "binding override %q must say why it is correct", k)
		out[k] = true
	}
	return out
}

// singlePlaceholder reports the placeholder name when From is exactly one placeholder and
// nothing else, such as "${ClusterName}".
func singlePlaceholder(from string) (string, bool) {
	m := placeholderRE.FindStringSubmatch(from)
	if m == nil || m[0] != from {
		return "", false
	}
	return m[1], true
}
