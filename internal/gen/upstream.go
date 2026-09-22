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

// Package gen builds the adoption catalog that the CLI embeds. Stage 1, in this file,
// reads each controller's own generated PopulateResourceFromAnnotation from a local
// directory, which is the definition of what adoption-fields must contain.
package gen

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Upstream struct {
	Service     string
	ResourceDir string
	// Required is the ordered list of adoption-fields keys the controller
	// refuses to adopt without. Order is declaration order, which is what CR
	// name generation walks.
	Required []string
	// Optional keys are read by the controller if present but not demanded.
	Optional []string
	// ARNPrimary is true when the sole required key is "arn" — the resource's
	// identifier simply is its ARN.
	ARNPrimary bool
	Group      string
	Kind       string
	Version    string
}

// Key is the "service:resource_dir" catalog key, e.g. "eks:nodegroup".
func (u Upstream) Key() string { return u.Service + ":" + u.ResourceDir }

// RepoRef records which checkout one repository's facts came from, so a
// generated catalog can be traced back to exact controller sources.
type RepoRef struct {
	Repo   string `json:"repo"`
	Tag    string `json:"tag,omitempty"`
	Commit string `json:"commit"`
}

// minTag is the lowest controller release the catalog will describe. Repos below
// it are treated as unreleased: shipping metadata for a resource no user can run
// would overstate coverage.
var minTag = [3]int{0, 1, 0}

// stableSemverRE matches a final release tag. Pre-release and build suffixes
// (v1.2.3-rc1) are deliberately rejected as non-final.
var stableSemverRE = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

type ScanOptions struct {
	// Root is the directory holding the controller clones, e.g. the parent of
	// eks-controller, s3-controller, ...
	Root string
}

type ScanResult struct {
	Resources []Upstream
	Refs      []RepoRef
	// Skipped records repos that were deliberately not read, with the reason,
	// so a coverage gap is always attributable.
	Skipped map[string]string
}

func Scan(opts ScanOptions) (*ScanResult, error) {
	entries, err := os.ReadDir(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("reading controller root %s: %w", opts.Root, err)
	}

	out := &ScanResult{Skipped: map[string]string{}}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), "-controller") {
			continue
		}
		repo := e.Name()
		repoPath := filepath.Join(opts.Root, repo)
		service := strings.TrimSuffix(repo, "-controller")

		src, ref, err := openSource(repoPath)
		if err != nil {
			out.Skipped[repo] = err.Error()
			continue
		}

		// Unlike an openSource refusal, a scan failure is a defect: recorded as a skip it
		// would drop the whole controller from both the supported and unsupported sets.
		resources, err := scanRepo(src, service)
		if err != nil {
			return nil, fmt.Errorf("scanning %s: %w", repo, err)
		}
		if len(resources) == 0 {
			out.Skipped[repo] = "no resources with PopulateResourceFromAnnotation"
			continue
		}
		ref.Repo = repo
		out.Refs = append(out.Refs, ref)
		out.Resources = append(out.Resources, resources...)
	}

	sort.Slice(out.Resources, func(i, j int) bool {
		if out.Resources[i].Service != out.Resources[j].Service {
			return out.Resources[i].Service < out.Resources[j].Service
		}
		return out.Resources[i].ResourceDir < out.Resources[j].ResourceDir
	})
	sort.Slice(out.Refs, func(i, j int) bool { return out.Refs[i].Repo < out.Refs[j].Repo })
	return out, nil
}

// scanRepo reads pkg/resource/<dir>/{resource,descriptor}.go for one controller.
func scanRepo(src source, service string) ([]Upstream, error) {
	dirs, err := src.listDirs("pkg/resource")
	if err != nil {
		// A repo with no pkg/resource is not an error: controller-bootstrap and
		// friends live alongside real controllers.
		return nil, nil
	}

	var out []Upstream
	for _, name := range dirs {
		dir := "pkg/resource/" + name

		resourceGo, err := src.readFile(dir + "/resource.go")
		if err != nil {
			continue // no resource.go: nothing to read
		}
		required, optional, hasFunc, err := parseAdoptionFields(dir+"/resource.go", resourceGo)
		if err != nil {
			// A parse failure is not a missing contract; skipping it would silently
			// shrink the catalog.
			return nil, fmt.Errorf("%s/%s: parsing resource.go: %w", service, name, err)
		}
		if !hasFunc {
			// No PopulateResourceFromAnnotation: this resource predates adoption
			// support. Skipped, and not a catalog gap — there is simply no
			// adoption contract to read.
			continue
		}
		descriptorGo, err := src.readFile(dir + "/descriptor.go")
		if err != nil {
			return nil, fmt.Errorf("%s/%s: reading descriptor.go: %w", service, name, err)
		}
		group, kind, version, err := parseDescriptor(dir+"/descriptor.go", descriptorGo)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: reading GVK: %w", service, name, err)
		}

		out = append(out, Upstream{
			Service:     service,
			ResourceDir: name,
			Required:    required,
			Optional:    optional,
			ARNPrimary:  len(required) == 1 && required[0] == "arn",
			Group:       group,
			Kind:        kind,
			Version:     version,
		})
	}
	return out, nil
}

// parseAdoptionFields extracts the ordered adoption-fields keys from a resource.go and
// splits them into required and optional, treating a key as required if ANY read of it is
// guarded.
//
// Controllers spell the same contract two ways — a guarded `if !ok` after the read, and a
// read in the if's own init that refuses from the else branch — so only top-level
// statements and their if-init are inspected, keeping a key read inside a branch unhoisted.
func parseAdoptionFields(name string, src []byte) (required, optional []string, hasFunc bool, err error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, nil, false, err
	}

	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Name.Name == "PopulateResourceFromAnnotation" && fd.Body != nil {
			fn = fd
			break
		}
	}
	if fn == nil {
		return nil, nil, false, nil
	}

	var order []string
	isRequired := map[string]bool{}
	observe := func(key string, req bool) {
		if _, seen := isRequired[key]; !seen {
			order = append(order, key)
		}
		isRequired[key] = isRequired[key] || req
	}

	stmts := fn.Body.List
	for i, stmt := range stmts {
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			key, ok := fieldsKeyRead(s)
			if !ok {
				continue
			}
			var next ast.Stmt
			if i+1 < len(stmts) {
				next = stmts[i+1]
			}
			observe(key, guardsRequired(next, okVarOf(s)))
		case *ast.IfStmt:
			// The read lives in the if's own init: `if v, ok := fields[k]; ok`.
			init, ok := s.Init.(*ast.AssignStmt)
			if !ok {
				continue
			}
			key, ok := fieldsKeyRead(init)
			if !ok {
				continue
			}
			observe(key, guardsRequired(s, okVarOf(init)))
		}
	}

	for _, key := range order {
		if isRequired[key] {
			required = append(required, key)
		} else {
			optional = append(optional, key)
		}
	}
	return required, optional, true, nil
}

// fieldsKeyRead reports the map key when assign reads fields["key"].
func fieldsKeyRead(assign *ast.AssignStmt) (string, bool) {
	for _, rhs := range assign.Rhs {
		idx, ok := rhs.(*ast.IndexExpr)
		if !ok {
			continue
		}
		ident, ok := idx.X.(*ast.Ident)
		if !ok || ident.Name != "fields" {
			continue
		}
		lit, ok := idx.Index.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		key, err := strconv.Unquote(lit.Value)
		if err != nil {
			continue
		}
		return key, true
	}
	return "", false
}

// okVarOf names the comma-ok variable of a `v, ok := fields[k]` read, which is the last
// name assigned. Controllers do not consistently call it "ok" — eks uses "f0ok" — so the
// guard must be matched against whatever the read bound rather than a fixed name.
func okVarOf(assign *ast.AssignStmt) string {
	if len(assign.Lhs) == 0 {
		return ""
	}
	ident, ok := assign.Lhs[len(assign.Lhs)-1].(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name
}

// guardsRequired reports whether ifStmt rejects the absence of the key that bound okVar,
// accepting `if !ok { return ...required field missing... }` or an else branch returning an
// error. The negated form needs the marker because `if !ok` is also how a controller
// offering a CHOICE of identifiers falls through to its alternative.
func guardsRequired(stmt ast.Stmt, okVar string) bool {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Body == nil || okVar == "" {
		return false
	}
	switch cond := ifStmt.Cond.(type) {
	case *ast.UnaryExpr:
		if cond.Op != token.NOT {
			return false
		}
		if ident, ok := cond.X.(*ast.Ident); !ok || ident.Name != okVar {
			return false
		}
		return mentionsRequiredFieldMissing(ifStmt.Body)
	case *ast.Ident:
		if cond.Name != okVar {
			return false
		}
		return returnsError(ifStmt.Else)
	}
	return false
}

// mentionsRequiredFieldMissing looks for codegen's marker string anywhere in a
// block, so the specific error constructor used does not matter.
func mentionsRequiredFieldMissing(block *ast.BlockStmt) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if ok && lit.Kind == token.STRING && strings.Contains(lit.Value, "required field missing") {
			found = true
			return false
		}
		return true
	})
	return found
}

// returnsError reports whether a branch returns a non-nil value, i.e. refuses.
func returnsError(branch ast.Stmt) bool {
	if branch == nil {
		return false
	}
	found := false
	ast.Inspect(branch, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			return true
		}
		for _, r := range ret.Results {
			if ident, isIdent := r.(*ast.Ident); isIdent && ident.Name == "nil" {
				continue
			}
			found = true
			return false
		}
		return true
	})
	return found
}

// parseDescriptor reads Group, Kind and Version out of a descriptor.go. They
// appear as struct-literal fields; Version defaults to v1alpha1 when absent,
// matching ACK's own default.
func parseDescriptor(name string, src []byte) (group, kind, version string, err error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return "", "", "", err
	}
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		name, ok := kv.Key.(*ast.Ident)
		if !ok {
			return true
		}
		lit, ok := kv.Value.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v, uerr := strconv.Unquote(lit.Value)
		if uerr != nil {
			return true
		}
		switch name.Name {
		case "Group":
			if group == "" {
				group = v
			}
		case "Kind":
			if kind == "" {
				kind = v
			}
		case "Version":
			if version == "" {
				version = v
			}
		}
		return true
	})
	if group == "" || kind == "" {
		return "", "", "", fmt.Errorf("no Group/Kind found in %s", name)
	}
	if version == "" {
		version = "v1alpha1"
	}
	return group, kind, version, nil
}

func git(repoPath string, args ...string) (string, error) {
	out, err := gitBytes(repoPath, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitBytes runs one git command, folding git's stderr into the error so a failure says
// more than "exit status 128".
func gitBytes(repoPath string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("git %s: %w: %s",
				strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
