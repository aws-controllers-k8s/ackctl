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

// Package naming decides whether an ACK identifier key and an AWS ARN placeholder refer
// to the same thing. A wrong binding is well-formed but names a different real resource,
// so agreement is word-level rather than a substring test.
package naming

import "strings"

// fillerWords describe the form of a value rather than which value it is, so
// ${RoleNameWithPath} still names the role.
var fillerWords = map[string]bool{
	"with": true, "without": true, "leading": true, "slash": true,
	"path": true, "friendly": true, "resource": true,
}

// interchangeable reports whether two bare head nouns may stand in for one another.
// Only "identifier" is: "name" and "id" can both exist on one resource as different values.
func interchangeable(a, b string) bool {
	if a == b {
		return true
	}
	nouns := map[string]bool{"name": true, "id": true, "identifier": true}
	return nouns[a] && nouns[b] && (a == "identifier" || b == "identifier")
}

// accountKeys are ACK keys whose value is the ARN's account field, not a path segment.
var accountKeys = map[string]bool{
	"awsaccountid": true, "accountid": true, "owneraccountid": true,
	"registryid": true, "domainowner": true,
}

func IsAccountKey(key string) bool {
	return accountKeys[strings.Join(SplitWords(key), "")]
}

// Agree reports whether an ACK identifier key and an ARN placeholder name refer to the same
// segment, by reducing both to the words carrying identity and requiring an exact match.
func Agree(key, placeholder, kind, resourceDir, service string) bool {
	implied := map[string]bool{}
	for _, src := range []string{kind, resourceDir, service} {
		for _, w := range SplitWords(src) {
			implied[w] = true
		}
	}

	reduce := func(s string) []string {
		var out []string
		for _, w := range SplitWords(s) {
			if fillerWords[w] || implied[w] {
				continue
			}
			out = append(out, w)
		}
		return out
	}

	k, p := reduce(key), reduce(placeholder)
	if len(k) == 0 && len(p) == 0 {
		return true
	}
	if len(k) == 1 && len(p) == 1 && interchangeable(k[0], p[0]) {
		return true
	}
	return len(k) > 0 && equal(k, p)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SplitWords lowercases and splits an identifier on case transitions, underscores, hyphens
// and spaces, treating a run of capitals as one acronym.
func SplitWords(s string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = nil
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == ' ':
			flush()
		case isUpper(r):
			prevLower := i > 0 && !isUpper(runes[i-1]) && !isSep(runes[i-1])
			acronymEnd := i > 0 && isUpper(runes[i-1]) && i+1 < len(runes) && isLower(runes[i+1])
			if prevLower || acronymEnd {
				flush()
			}
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }
func isSep(r rune) bool   { return r == '_' || r == '-' || r == ' ' }
