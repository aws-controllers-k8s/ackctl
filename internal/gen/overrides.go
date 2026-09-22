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

package gen

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Overrides is the hand-maintained table of decisions automatic derivation cannot reach,
// committed so each is reviewable and regeneration cannot silently revert it.
type Overrides struct {
	// Resources supplies the type filter and ARN template for a resource whose grammar
	// could not be matched automatically. Keys still bind by the ordinary rule, so an
	// override says only which template to bind against.
	Resources map[string]*ResourceOverride `json:"resources"`

	// Unsupported forces a resource out of the supported set even when it binds
	// cleanly. Needed when the ARN is well-formed but not sufficient to identify
	// the resource: the documentdb kinds share rds:* ARN shapes with Aurora and
	// the distinguishing Engine field is not in the ARN.
	Unsupported map[string]*UnsupportedOverride `json:"unsupported"`

	// Bindings states one key's source directly, keyed "service:resource_dir.key", for
	// placeholders no name rule can reach or values needing literal text around them.
	//
	// It also waives the name-agreement check for that key, making this the one place a
	// mis-binding can enter the catalog unchecked, so each entry's "why" must name the
	// real ARN it was verified against.
	Bindings map[string]*BindingOverride `json:"bindings"`
}

type ResourceOverride struct {
	Why                string `json:"why"`
	ResourceTypeFilter string `json:"resource_type_filter"`
	// ARNTemplates lists every ARN shape the resource takes, spelling shared
	// placeholders identically so the same keys bind in each.
	ARNTemplates []string `json:"arn_templates"`
	ARNPrimary   bool     `json:"arn_primary,omitempty"`
}

type UnsupportedOverride struct {
	Why    string `json:"why"`
	Reason string `json:"reason"`
}

type BindingOverride struct {
	Why string `json:"why"`
	// From is a template over the ARN template's placeholders, the same form the
	// catalog's bindings use.
	From string `json:"from"`
}

// LoadOverrides reads and validates the table, requiring a non-empty "why" on every entry.
// An override without a stated reason cannot be reviewed or retired.
func LoadOverrides(path string) (*Overrides, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading overrides: %w", err)
	}
	var o Overrides
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	for key, r := range o.Resources {
		if r.Why == "" {
			return nil, fmt.Errorf("override %q has no \"why\"", key)
		}
		if r.ResourceTypeFilter == "" {
			return nil, fmt.Errorf("override %q has no resource_type_filter; without one "+
				"the CLI cannot scope its GetResources query", key)
		}
		if len(r.ARNTemplates) == 0 {
			return nil, fmt.Errorf("override %q has no arn_templates", key)
		}
		isSentinel := len(r.ARNTemplates) == 1 && r.ARNTemplates[0] == arnPrimaryTemplate
		if r.ARNPrimary != isSentinel {
			return nil, fmt.Errorf("override %q: arn_primary and arn_templates disagree "+
				"(an ARN-primary resource must use exactly the %q sentinel)", key, arnPrimaryTemplate)
		}
	}
	for key, u := range o.Unsupported {
		if u.Why == "" {
			return nil, fmt.Errorf("unsupported override %q has no \"why\"", key)
		}
		if u.Reason == "" {
			return nil, fmt.Errorf("unsupported override %q has no user-facing \"reason\"", key)
		}
	}
	for key, b := range o.Bindings {
		if b.Why == "" {
			return nil, fmt.Errorf("binding override %q has no \"why\"", key)
		}
		if b.From == "" {
			return nil, fmt.Errorf("binding override %q has no \"from\"", key)
		}
		if !strings.Contains(b.From, "${") {
			return nil, fmt.Errorf("binding override %q has a literal \"from\" (%q) with no "+
				"placeholder: it would emit the same identifier for every resource", key, b.From)
		}
	}
	return &o, nil
}
