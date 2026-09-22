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
	"fmt"
	"sort"
	"strings"
)

// AccountPlaceholder is the binding source for a key read from the ARN's account
// field rather than a path segment.
const AccountPlaceholder = "${Account}"

// Target describes the resource a binding is being computed for. The three names
// are what Agree uses to decide which qualifier words a placeholder may omit.
type Target struct {
	Kind        string
	ResourceDir string
	Service     string
}

// Bind assigns every required ACK key to a distinct ARN placeholder, solving it as exact
// matching over candidate sets rather than a greedy scan so a bare "name" key cannot claim
// the first plausible-looking placeholder.
//
// It refuses rather than guesses when a key has no agreeing placeholder, when no complete
// assignment exists, or when more than one does, and the caller records the refusal as
// unsupported.
func Bind(required, placeholders []string, hasAccount bool, t Target) (map[string]string, error) {
	out := map[string]string{}

	// Account-valued keys read the ARN envelope, so they never compete for a path
	// placeholder.
	var keys []string
	for _, k := range required {
		if IsAccountKey(k) && hasAccount {
			out[k] = AccountPlaceholder
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return out, nil
	}

	candidates := make([][]int, len(keys))
	for i, k := range keys {
		for j, p := range placeholders {
			if Agree(k, p, t.Kind, t.ResourceDir, t.Service) {
				candidates[i] = append(candidates[i], j)
			}
		}
		if len(candidates[i]) == 0 {
			return nil, fmt.Errorf("key %q matches no placeholder in [%s]",
				k, strings.Join(placeholders, " "))
		}
	}

	// Assign the most-constrained keys first: it prunes hardest and makes the
	// common single-candidate case immediate.
	order := make([]int, len(keys))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return len(candidates[order[a]]) < len(candidates[order[b]])
	})

	var found [][]int
	assign := make([]int, len(keys))
	taken := make([]bool, len(placeholders))
	var walk func(int)
	walk = func(depth int) {
		// Stop at two: one is a unique answer, two is already ambiguous, and
		// enumerating the rest tells us nothing more.
		if len(found) > 1 {
			return
		}
		if depth == len(order) {
			found = append(found, append([]int(nil), assign...))
			return
		}
		ki := order[depth]
		for _, pj := range candidates[ki] {
			if taken[pj] {
				continue
			}
			taken[pj] = true
			assign[ki] = pj
			walk(depth + 1)
			taken[pj] = false
		}
	}
	walk(0)

	switch len(found) {
	case 0:
		return nil, fmt.Errorf("keys [%s] have agreeing placeholders but no complete "+
			"assignment: two keys compete for one placeholder in [%s]",
			strings.Join(keys, " "), strings.Join(placeholders, " "))
	case 1:
		for i, k := range keys {
			out[k] = "${" + placeholders[found[0][i]] + "}"
		}
		return out, nil
	default:
		return nil, fmt.Errorf("keys [%s] bind ambiguously against [%s]: more than one "+
			"assignment agrees by name, so any choice risks adopting the wrong resource",
			strings.Join(keys, " "), strings.Join(placeholders, " "))
	}
}

// BindOptional assigns optional keys to placeholders the required assignment left
// unclaimed, updating claimed with what it takes. A key with no agreeing unclaimed
// placeholder, or more than one, is skipped rather than guessed.
func BindOptional(optional, placeholders []string, hasAccount bool, claimed map[string]bool, t Target) map[string]string {
	out := map[string]string{}
	for _, k := range optional {
		if IsAccountKey(k) {
			if hasAccount {
				out[k] = AccountPlaceholder
			}
			continue
		}
		match := ""
		ambiguous := false
		for _, p := range placeholders {
			if claimed[p] || !Agree(k, p, t.Kind, t.ResourceDir, t.Service) {
				continue
			}
			if match != "" {
				ambiguous = true
				break
			}
			match = p
		}
		if match == "" || ambiguous {
			continue
		}
		claimed[match] = true
		out[k] = "${" + match + "}"
	}
	return out
}
