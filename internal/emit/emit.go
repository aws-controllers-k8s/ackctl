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

// Package emit renders adoption Custom Resource manifests from resolved ARNs.
package emit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/aws-controllers-k8s/ackctl/internal/resolver"
	"github.com/aws-controllers-k8s/ackctl/internal/tagging"
)

// Annotation keys ACK's adoption-by-annotation flow reads, mirroring the constants in
// aws-controllers-k8s/runtime apis/core/v1alpha1/annotations.go.
const (
	annotationAdoptionPolicy = "services.k8s.aws/adoption-policy"
	annotationAdoptionFields = "services.k8s.aws/adoption-fields"
	annotationReadOnly       = "services.k8s.aws/read-only"
	annotationDeletionPolicy = "services.k8s.aws/deletion-policy"
	annotationRegion         = "services.k8s.aws/region"
)

// labelAdoptionSet is added by ackctl itself and not read by the ACK runtime. It makes one
// adopted collection selectable as a group.
const labelAdoptionSet = "ack.k8s.aws/adoption-set"

type Policy string

// PolicyAdopt has the controller find the resource by its adoption-fields and populate
// spec from the live resource.
const PolicyAdopt Policy = "adopt"

// DeletionPolicy is what ACK does to the AWS resource when its CR is deleted.
type DeletionPolicy string

const (
	// DeletionPolicyRetain means deleting the CR removes only the CR.
	DeletionPolicyRetain DeletionPolicy = "retain"
	// DeletionPolicyDelete means deleting the CR deletes the AWS resource.
	DeletionPolicyDelete DeletionPolicy = "delete"
)

func ParseDeletionPolicy(s string) (DeletionPolicy, error) {
	switch DeletionPolicy(s) {
	case DeletionPolicyRetain, DeletionPolicyDelete:
		return DeletionPolicy(s), nil
	default:
		return "", fmt.Errorf("invalid deletion policy %q (want retain or delete)", s)
	}
}

// CR is the Custom Resource we emit. Spec is empty; the controller fills it from the live
// AWS resource.
type CR struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Metadata   crMetadata             `json:"metadata"`
	Spec       map[string]interface{} `json:"spec,omitempty"`
}

type crMetadata struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations"`
}

type Options struct {
	Policy         Policy
	ReadOnly       bool
	DeletionPolicy DeletionPolicy
	// AdoptionSet labels the collection this CR belongs to, and never affects Name.
	AdoptionSet string
	Namespace   string
	// Region is written to services.k8s.aws/region, which is how the controller decides
	// where to look. It must be set, because ARNs for global-form resources such as s3 and
	// iam leave the region slot empty, so it cannot be recovered from the ARN.
	Region string
}

// Build assembles a CR from a resolved ARN. Region comes from the tag query, since many
// ARNs leave the region slot empty for resources that are still regional.
func Build(res *resolver.Resolved, name string, opts Options) (*CR, error) {
	if opts.Region == "" {
		return nil, fmt.Errorf("region is required: it becomes services.k8s.aws/region")
	}
	fieldsJSON, err := json.Marshal(res.Fields)
	if err != nil {
		return nil, fmt.Errorf("marshaling adoption-fields: %w", err)
	}

	ann := map[string]string{
		annotationAdoptionPolicy: string(opts.Policy),
		annotationAdoptionFields: string(fieldsJSON),
		annotationReadOnly:       strconv.FormatBool(opts.ReadOnly),
		annotationDeletionPolicy: string(opts.DeletionPolicy),
		annotationRegion:         opts.Region,
	}
	var labels map[string]string
	if opts.AdoptionSet != "" {
		labels = map[string]string{labelAdoptionSet: opts.AdoptionSet}
	}

	return &CR{
		APIVersion: res.Resource.GroupVersion(),
		Kind:       res.Resource.Kind,
		Metadata: crMetadata{
			Name:        name,
			Namespace:   opts.Namespace,
			Labels:      labels,
			Annotations: ann,
		},
	}, nil
}

// DefaultAdoptionSet names the collection when --adoption-set is omitted, as
// "<service>-<kind>-<digest>". The digest covers the run's selector, so the same query
// always produces the same name.
func DefaultAdoptionSet(service, kind, region string, tags []tagging.Filter) string {
	sorted := make([]tagging.Filter, len(tags))
	copy(sorted, tags)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })

	h := sha256.New()
	writeField := func(s string) {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	writeField(service)
	writeField(kind)
	writeField(region)
	for _, t := range sorted {
		writeField(t.Key)
		// The any-value marker is hashed too, so `env` and `env=` get different sets.
		if t.Any {
			writeField("*")
			continue
		}
		writeField("=" + t.Value)
	}
	digest := hex.EncodeToString(h.Sum(nil))[:8]
	return sanitizeDNS(service+"-"+kind) + "-" + digest
}

func (cr *CR) YAML() ([]byte, error) {
	return yaml.Marshal(cr)
}

// Document renders a multi-CR YAML stream. Every document gets a leading "---", including
// the first, so the output of several runs can be concatenated.
func Document(crs []*CR) ([]byte, error) {
	var b strings.Builder
	for _, cr := range crs {
		b.WriteString("---\n")
		y, err := cr.YAML()
		if err != nil {
			return nil, err
		}
		b.Write(y)
	}
	return []byte(b.String()), nil
}

var nonDNS = regexp.MustCompile(`[^a-z0-9-]+`)

// maxNameLen is the DNS-1123 subdomain limit Kubernetes enforces on object names.
const maxNameLen = 253

func arnDigest(arnStr string) string {
	sum := sha256.Sum256([]byte(arnStr))
	return hex.EncodeToString(sum[:])[:8]
}

// DefaultName derives a DNS-1123-safe name as "<identifier values joined>-<arn digest>".
// It depends only on the resource's identity, not on the adoption set, so re-adopting the
// same resource hits AlreadyExists instead of creating a second CR.
func DefaultName(res *resolver.Resolved, arnStr string) string {
	// Every declared binding, in the matched template's order: dropping one would rename
	// the CRs of resources an earlier release already adopted.
	var parts []string
	for _, b := range res.Bindings {
		if v := res.Fields[b.Key]; v != "" {
			parts = append(parts, v)
		}
	}
	// ARN-primary resources bind only "arn", so name them after the ARN's tail.
	if res.Resource.ARNPrimary && len(parts) == 1 {
		parts[0] = arnTail(parts[0])
	}
	// Unreachable with the embedded catalog, which has no entry without bindings, and the
	// resolver rejects a binding that renders empty. Kept because DefaultName cannot fail.
	if len(parts) == 0 {
		parts = []string{arnTail(arnStr)}
	}

	digest := arnDigest(arnStr)
	ident := sanitizeDNS(strings.Join(parts, "-"))

	// Reserve room for the digest suffix, then truncate the readable part.
	if max := maxNameLen - len(digest) - 1; len(ident) > max {
		ident = strings.Trim(ident[:max], "-")
	}
	if ident == "" {
		return digest
	}
	return ident + "-" + digest
}

func arnTail(arnStr string) string {
	if i := strings.LastIndexAny(arnStr, "/:"); i >= 0 {
		return arnStr[i+1:]
	}
	return arnStr
}

func sanitizeDNS(s string) string {
	s = strings.ToLower(s)
	s = nonDNS.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
