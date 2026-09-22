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

// Package resolver turns an AWS resource ARN into the map of ACK adoption-fields that
// adoption-by-annotation expects. It returns a complete map or an error, never a partial one.
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

// headerPlaceholders name the slots read from the parsed ARN envelope, not the resource path.
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
	// Bindings is the matched template's binding list, which CR naming walks in order.
	Bindings []metadata.Binding
	// Resource is the matched metadata entry (carries GVK for CR emission).
	Resource metadata.Resource
}

type compiledTemplate struct {
	res  metadata.Resource
	tmpl metadata.Template
	re   *regexp.Regexp
	// pathPlaceholders lists the non-header placeholder names in template order, which
	// is also capture-group order.
	pathPlaceholders []string
	literalLen       int
}

type Resolver struct {
	// compiled groups each resource's templates by resource key, most specific first.
	compiled map[string][]compiledTemplate
}

func resourceKey(r metadata.Resource) string { return r.Service + ":" + r.ResourceDir }

func New(catalog *metadata.Catalog) (*Resolver, error) {
	r := &Resolver{compiled: map[string][]compiledTemplate{}}
	for _, res := range catalog.Resources() {
		if res.ARNPrimary {
			// The ARN itself is the identifier, handled in ResolveARNForResource.
			continue
		}
		key := resourceKey(res)
		for _, tmpl := range res.Templates {
			ct, err := compileTemplate(res, tmpl)
			if err != nil {
				return nil, fmt.Errorf("compiling ARN template for %s: %w", key, err)
			}
			r.compiled[key] = append(r.compiled[key], ct)
		}
		group := r.compiled[key]
		sort.SliceStable(group, func(i, j int) bool {
			if len(group[i].pathPlaceholders) != len(group[j].pathPlaceholders) {
				return len(group[i].pathPlaceholders) > len(group[j].pathPlaceholders)
			}
			return group[i].literalLen > group[j].literalLen
		})
	}
	return r, nil
}

// compileTemplate converts an ARN template into an anchored regex whose capture groups
// are the non-header placeholders. Each value excludes the ARN delimiters ':' and '/',
// except a template-ending placeholder, which admits '/' so names containing one (log
// groups, namespaced ECR repos) still resolve but never ':', so a qualified ARN carrying
// an extra colon segment is refused rather than read as a name no resource has.
func compileTemplate(res metadata.Resource, tmpl metadata.Template) (compiledTemplate, error) {
	text := tmpl.ARNTemplate
	var b strings.Builder
	b.WriteString("^")
	var pathPlaceholders []string
	literalLen := len(text)
	last := 0
	matches := placeholderRE.FindAllStringSubmatchIndex(text, -1)
	for _, m := range matches {
		// literal text before this placeholder
		b.WriteString(regexp.QuoteMeta(text[last:m[0]]))
		literalLen -= m[1] - m[0]
		name := text[m[2]:m[3]]
		trailing := m[1] == len(text)
		if _, isHeader := headerPlaceholders[strings.ToLower(name)]; isHeader {
			// header slots still consume a segment, but not '/' or ':'
			b.WriteString(`[^:/]*`)
		} else {
			pathPlaceholders = append(pathPlaceholders, name)
			if trailing {
				b.WriteString(`([^:]+)`)
			} else {
				b.WriteString(`([^:/]+)`)
			}
		}
		last = m[1]
	}
	b.WriteString(regexp.QuoteMeta(text[last:]))
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return compiledTemplate{}, err
	}
	return compiledTemplate{
		res: res, tmpl: tmpl, re: re,
		pathPlaceholders: pathPlaceholders, literalLen: literalLen,
	}, nil
}

// ResolveARNForResource decomposes an ARN against an already-chosen resource, trying the
// resource's templates most-specific first. An ARN that matches none of them is an error.
func (r *Resolver) ResolveARNForResource(arnStr string, res metadata.Resource) (*Resolved, error) {
	parsed, err := arn.Parse(arnStr)
	if err != nil {
		return nil, fmt.Errorf("not a valid ARN %q: %w", arnStr, err)
	}
	if res.ARNPrimary {
		debuglog.Logf("resolve %s: ARN-primary, adoption-fields = {arn: <the ARN>}", arnStr)
		return &Resolved{
			Fields:   map[string]string{"arn": arnStr},
			Bindings: []metadata.Binding{{Key: "arn", From: "${ARN}"}},
			Resource: res,
		}, nil
	}
	group := r.compiled[resourceKey(res)]
	if len(group) == 0 {
		debuglog.Logf("resolve %s: no compiled template for %s", arnStr, resourceKey(res))
		return nil, &UnresolvableError{ARN: arnStr,
			Reason: fmt.Sprintf("no template compiled for %s", resourceKey(res))}
	}
	debuglog.Logf("resolve %s", arnStr)
	for _, ct := range group {
		debuglog.Logf("  template: %s", ct.tmpl.ARNTemplate)
		debuglog.Logf("  regex:    %s", ct.re.String())
		fields, ok := ct.extract(arnStr, parsed)
		if !ok {
			debuglog.Logf("  no match — ARN shape differs from this template")
			continue
		}
		debuglog.Logf("  RESULT:   adoption-fields = %s", formatFields(fields))
		return &Resolved{Fields: fields, Bindings: ct.tmpl.Bindings, Resource: res}, nil
	}
	debuglog.Logf("  RESULT:   no match — ARN shape differs from all %d template(s)", len(group))
	return nil, &UnresolvableError{ARN: arnStr,
		Reason: fmt.Sprintf("ARN did not match any of the %d %s template(s)",
			len(group), resourceKey(res))}
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
	fields := make(map[string]string, len(ct.tmpl.Bindings))
	for _, b := range ct.tmpl.Bindings {
		v, ok := renderFrom(b.From, phValues, parsed, arnStr)
		if !ok {
			// A missing identifier would point the CR at the wrong resource.
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
