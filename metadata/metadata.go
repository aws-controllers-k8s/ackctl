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

// Package metadata holds the embedded adoption metadata mapping each ACK-supported AWS
// resource to what is needed to find it by tag and decompose its ARN into ACK identifier
// fields. It is generated offline and embedded so the CLI is hermetic.
package metadata

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed adoption_metadata.json
var rawMetadata []byte

// Binding maps one ACK identifier key to where its value comes from in the resource's ARN,
// in the order that matches ACK's required-key declaration and drives CR naming.
//
// From is a template over the ARN template's placeholders, which may also use ${Partition},
// ${Region}, ${Account} from the envelope and ${ARN} for the whole ARN.
type Binding struct {
	Key  string `json:"key"`
	From string `json:"from"`
}

type Resource struct {
	Service     string `json:"service"`
	ResourceDir string `json:"resource_dir"`
	Kind        string `json:"kind"`
	Group       string `json:"group"`
	Version     string `json:"version"`
	// ResourceTypeFilter is the Tagging API resource-type filter, scoped to the AWS
	// service namespace rather than the ACK service name.
	ResourceTypeFilter string `json:"resource_type_filter"`
	// ARNTemplate is the AWS ARN grammar with ${Placeholder} slots, or the sentinel
	// "<arn>" when the primary key is the ARN itself.
	ARNTemplate string    `json:"arn_template"`
	ARNPrimary  bool      `json:"arn_primary"`
	Bindings    []Binding `json:"bindings"`
}

// GroupVersion returns the CR apiVersion string (e.g. "eks.services.k8s.aws/v1alpha1").
func (r Resource) GroupVersion() string {
	return fmt.Sprintf("%s/%s", r.Group, r.Version)
}

type Unsupported struct {
	Resource string `json:"resource"`
	Kind     string `json:"kind,omitempty"`
	Reason   string `json:"reason"`
}

type document struct {
	Resources   []Resource    `json:"resources"`
	Unsupported []Unsupported `json:"unsupported"`
}

type Catalog struct {
	resources   []Resource
	unsupported []Unsupported
}

func Load() (*Catalog, error) {
	var doc document
	if err := json.Unmarshal(rawMetadata, &doc); err != nil {
		return nil, fmt.Errorf("parsing embedded adoption metadata: %w", err)
	}
	return &Catalog{resources: doc.Resources, unsupported: doc.Unsupported}, nil
}

// Resources returns a copy, so a caller that sorts the slice cannot reorder the catalog
// underneath everything else.
func (c *Catalog) Resources() []Resource {
	out := make([]Resource, len(c.resources))
	copy(out, c.resources)
	return out
}

// Unsupported returns a copy, for the same reason as Resources.
func (c *Catalog) Unsupported() []Unsupported {
	out := make([]Unsupported, len(c.unsupported))
	copy(out, c.unsupported)
	return out
}

// LookupByServiceKind returns the supported resource for an ACK service and Kind, both
// matched case-insensitively.
func (c *Catalog) LookupByServiceKind(service, kind string) (Resource, bool) {
	for _, r := range c.resources {
		if strings.EqualFold(r.Service, service) && strings.EqualFold(r.Kind, kind) {
			return r, true
		}
	}
	return Resource{}, false
}

// UnsupportedReason returns the recorded reason a resource cannot be adopted by tag,
// matching against either the recorded Kind or the "service:resource_dir" key.
func (c *Catalog) UnsupportedReason(service, kind string) (string, bool) {
	wantKey := strings.ToLower(service + ":" + kind)
	for _, u := range c.unsupported {
		if u.Kind != "" && strings.EqualFold(u.Kind, kind) &&
			strings.HasPrefix(strings.ToLower(u.Resource), strings.ToLower(service)+":") {
			return u.Reason, true
		}
		if strings.ToLower(u.Resource) == wantKey {
			return u.Reason, true
		}
	}
	return "", false
}
