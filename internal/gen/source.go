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
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

// source reads a controller repo's files at one revision, defaulting to the highest
// stable tag via `git show` so a contributor's working tree is never touched. Reading
// tags also stops a stale local clone from silently shrinking the catalog.
type source interface {
	// listDirs names the immediate subdirectories of a repo-relative path.
	listDirs(dir string) ([]string, error)
	// readFile reads a repo-relative file.
	readFile(name string) ([]byte, error)
}

func openSource(repoPath string) (source, RepoRef, error) {
	if _, err := git(repoPath, "rev-parse", "HEAD"); err != nil {
		return nil, RepoRef{}, fmt.Errorf("not a readable git checkout: %w", err)
	}
	tag, err := highestStableTag(repoPath)
	if err != nil {
		return nil, RepoRef{}, err
	}
	tagCommit, err := git(repoPath, "rev-parse", tag+"^{commit}")
	if err != nil {
		return nil, RepoRef{}, fmt.Errorf("resolving %s: %w", tag, err)
	}
	return &tagSource{root: repoPath, tag: tag}, RepoRef{Tag: tag, Commit: tagCommit}, nil
}

// tagSource reads blobs at a tag without disturbing the working tree.
type tagSource struct {
	root string
	tag  string
}

func (t *tagSource) listDirs(dir string) ([]string, error) {
	// -d lists tree entries only, so this yields directories and nothing else.
	out, err := git(t.root, "ls-tree", "-d", "--name-only", t.tag+":"+dir)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			dirs = append(dirs, path.Base(line))
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

func (t *tagSource) readFile(name string) ([]byte, error) {
	out, err := gitBytes(t.root, "show", t.tag+":"+name)
	if err != nil {
		return nil, fmt.Errorf("%s:%s: %w", t.tag, name, err)
	}
	return out, nil
}

// highestStableTag returns the greatest final-release tag at or above minTag, excluding
// pre-releases. Sorting is numeric because v1.10.0 sorts below v1.9.0 lexically.
func highestStableTag(repoPath string) (string, error) {
	out, err := git(repoPath, "tag", "--list")
	if err != nil {
		return "", fmt.Errorf("listing tags: %w", err)
	}
	var best string
	var bestV [3]int
	for _, line := range strings.Split(out, "\n") {
		tag := strings.TrimSpace(line)
		v, ok := parseStableSemver(tag)
		if !ok || !atLeast(v, minTag) {
			continue
		}
		if best == "" || atLeast(v, bestV) {
			best, bestV = tag, v
		}
	}
	if best == "" {
		return "", fmt.Errorf("no stable release tag >= v%d.%d.%d "+
			"(unreleased, or the clone has no tags fetched — try `git fetch --tags`)",
			minTag[0], minTag[1], minTag[2])
	}
	return best, nil
}

// parseStableSemver accepts only a final vX.Y.Z; a pre-release or build suffix is
// deliberately rejected.
func parseStableSemver(tag string) ([3]int, bool) {
	m := stableSemverRE.FindStringSubmatch(tag)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func atLeast(v, floor [3]int) bool {
	for i := 0; i < 3; i++ {
		if v[i] != floor[i] {
			return v[i] > floor[i]
		}
	}
	return true
}
