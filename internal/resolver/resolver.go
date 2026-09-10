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

// Package resolver turns a concrete AWS resource ARN into the map of ACK
// adoption-fields that adoption-by-annotation expects. Its safety property is that it
// either returns the correct identifier map or refuses, never a plausible-but-wrong one.
package resolver

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"

	"github.com/aws-controllers-k8s/ackctl/internal/debuglog"
	"github.com/aws-controllers-k8s/ackctl/metadata"
)

// placeholderRE matches an ARN-template placeholder like ${ClusterName}.
var placeholderRE = regexp.MustCompile(`\$\{([A-Za-z0-9]+)\}`)

// headerPlaceholders name the slots that come from the ARN envelope rather than the
// resource path, so their values are read from the parsed ARN.
var headerPlaceholders = map[string]struct{}{
	"partition": {},
	"region":    {},
	"account":   {},
}

// wholeARNPlaceholder is the name a binding uses to mean the entire matched ARN.
const wholeARNPlaceholder = "arn"

type Resolved struct {
	// Fields is the adoption-fields map handed to the CR annotation.
	Fields map[string]string
	// Resource is the matched metadata entry (carries GVK for CR emission).
	Resource metadata.Resource
}

type compiledTemplate struct {
	res metadata.Resource
	re  *regexp.Regexp
	// pathPlaceholders lists the non-header placeholder names in template order, which
	// is also capture-group order.
	pathPlaceholders []string
}

type Resolver struct {
	compiled []compiledTemplate
}

func New(catalog *metadata.Catalog) (*Resolver, error) {
	r := &Resolver{}
	for _, res := range catalog.Resources() {
		if res.ARNPrimary {
			// The ARN itself is the identifier, handled in ResolveARNForResource.
			continue
		}
		ct, err := compileTemplate(res)
		if err != nil {
			return nil, fmt.Errorf("compiling ARN template for %s/%s: %w", res.Service, res.ResourceDir, err)
		}
		r.compiled = append(r.compiled, ct)
	}
	return r, nil
}

// compileTemplate converts an ARN template into an anchored regex whose capture groups
// are the non-header placeholders. Each value excludes the ARN delimiters ':' and '/' so
// path-style templates decompose segment by segment.
func compileTemplate(res metadata.Resource) (compiledTemplate, error) {
	tmpl := res.ARNTemplate
	var b strings.Builder
	b.WriteString("^")
	var pathPlaceholders []string
	last := 0
	for _, m := range placeholderRE.FindAllStringSubmatchIndex(tmpl, -1) {
		// literal text before this placeholder
		b.WriteString(regexp.QuoteMeta(tmpl[last:m[0]]))
		name := tmpl[m[2]:m[3]]
		if _, isHeader := headerPlaceholders[strings.ToLower(name)]; isHeader {
			// header slots still consume a segment, but not '/' or ':'
			b.WriteString(`[^:/]*`)
		} else {
			pathPlaceholders = append(pathPlaceholders, name)
			b.WriteString(`([^:/]+)`)
		}
		last = m[1]
	}
	b.WriteString(regexp.QuoteMeta(tmpl[last:]))
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return compiledTemplate{}, err
	}
	return compiledTemplate{res: res, re: re, pathPlaceholders: pathPlaceholders}, nil
}

// ResolveARNForResource decomposes an ARN against an already-chosen resource, which is
// the sole entry point because the CLI requires the user to name service and kind. A
// non-matching ARN fails rather than yielding a partial or guessed map.
func (r *Resolver) ResolveARNForResource(arnStr string, res metadata.Resource) (*Resolved, error) {
	parsed, err := arn.Parse(arnStr)
	if err != nil {
		return nil, fmt.Errorf("not a valid ARN %q: %w", arnStr, err)
	}
	if res.ARNPrimary {
		debuglog.Logf("resolve %s: ARN-primary, adoption-fields = {arn: <the ARN>}", arnStr)
		return &Resolved{Fields: map[string]string{"arn": arnStr}, Resource: res}, nil
	}
	for _, ct := range r.compiled {
		if ct.res.Service == res.Service && ct.res.ResourceDir == res.ResourceDir {
			debuglog.Logf("resolve %s", arnStr)
			debuglog.Logf("  template: %s", ct.res.ARNTemplate)
			debuglog.Logf("  regex:    %s", ct.re.String())
			fields, ok := ct.extract(arnStr, parsed)
			if !ok {
				debuglog.Logf("  RESULT:   no match — ARN shape differs from the template")
				return nil, &UnresolvableError{ARN: arnStr,
					Reason: fmt.Sprintf("ARN did not match the %s/%s template %q",
						res.Service, res.ResourceDir, ct.res.ARNTemplate)}
			}
			debuglog.Logf("  RESULT:   adoption-fields = %s", formatFields(fields))
			return &Resolved{Fields: fields, Resource: res}, nil
		}
	}
	debuglog.Logf("resolve %s: no compiled template for %s/%s", arnStr, res.Service, res.ResourceDir)
	return nil, &UnresolvableError{ARN: arnStr,
		Reason: fmt.Sprintf("no template compiled for %s/%s", res.Service, res.ResourceDir)}
}

func formatFields(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+fields[k])
	}
	return "{" + strings.Join(parts, " ") + "}"
}

func (ct compiledTemplate) extract(arnStr string, parsed arn.ARN) (map[string]string, bool) {
	m := ct.re.FindStringSubmatch(arnStr)
	if m == nil {
		return nil, false
	}
	// capture group i (1-based) -> pathPlaceholders[i-1]
	phValues := make(map[string]string, len(ct.pathPlaceholders))
	for i, name := range ct.pathPlaceholders {
		phValues[strings.ToLower(name)] = m[i+1]
	}
	fields := make(map[string]string, len(ct.res.Bindings))
	for _, b := range ct.res.Bindings {
		v, ok := renderFrom(b.From, phValues, parsed, arnStr)
		if !ok {
			// A missing identifier would point the CR at the wrong resource, so treat
			// it as a non-match rather than emit a partial map.
			return nil, false
		}
		fields[b.Key] = v
	}
	return fields, true
}

// renderFrom resolves a binding's From template against one matched ARN, where the
// template may name one placeholder, the whole ARN, or several segments spelled out. It
// reports false if a referenced placeholder is absent or the rendered value is empty.
func renderFrom(
	from string,
	phValues map[string]string,
	parsed arn.ARN,
	arnStr string,
) (string, bool) {
	ok := true
	out := placeholderRE.ReplaceAllStringFunc(from, func(match string) string {
		name := strings.ToLower(placeholderRE.FindStringSubmatch(match)[1])
		switch name {
		case wholeARNPlaceholder:
			return arnStr
		case "partition":
			return parsed.Partition
		case "region":
			// Legitimately empty for global resources such as iam and route53.
			return parsed.Region
		case "account":
			if parsed.AccountID == "" {
				ok = false
			}
			return parsed.AccountID
		default:
			v, found := phValues[name]
			if !found {
				ok = false
			}
			return v
		}
	})
	if !ok || out == "" {
		return "", false
	}
	return out, true
}

type UnresolvableError struct {
	ARN    string
	Reason string
}

func (e *UnresolvableError) Error() string {
	return fmt.Sprintf("cannot resolve ARN %q: %s", e.ARN, e.Reason)
}
