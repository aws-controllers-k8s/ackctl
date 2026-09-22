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

// Command ack-catalog-gen regenerates the adoption catalog ackctl embeds, from ACK
// controller releases and the AWS Service Reference API. Run it from the repo root:
//
//	ack-catalog-gen -controllers ~/go/src/github.com/aws-controllers-k8s
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/aws-controllers-k8s/ackctl/internal/gen"
)

var (
	overridesPath  = filepath.Join("metadata", "overrides.json")
	outPath        = filepath.Join("metadata", "adoption_metadata.json")
	provenancePath = filepath.Join("metadata", "provenance.json")
	grammarDir     = filepath.Join("metadata", "grammar")
)

func main() {
	controllers := flag.String("controllers", "",
		"directory holding the *-controller clones (required)")
	offline := flag.Bool("offline", false,
		"read the ARN grammar from "+grammarDir+" instead of the Service Reference API")
	flag.Parse()

	if err := run(*controllers, *offline); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(controllers string, offline bool) error {
	if controllers == "" {
		return fmt.Errorf("-controllers is required: point it at the directory holding " +
			"the *-controller clones")
	}

	overrides, err := gen.LoadOverrides(overridesPath)
	if err != nil {
		return err
	}

	scan, err := gen.Scan(gen.ScanOptions{Root: controllers})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "scanned %d resources from %d controller repos\n",
		len(scan.Resources), len(scan.Refs))
	reportSkipped(scan.Skipped)

	// Only the namespaces ACK has controllers for.
	namespaces := map[string]struct{}{}
	for _, u := range scan.Resources {
		namespaces[gen.Namespace(u.Service)] = struct{}{}
	}
	want := make([]string, 0, len(namespaces))
	for ns := range namespaces {
		want = append(want, ns)
	}
	sort.Strings(want)

	grammar, source, err := loadGrammar(want, offline)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "loaded ARN grammar for %d of %d services (%s)\n",
		len(grammar), len(want), source)

	catalog, err := gen.Build(gen.BuildInput{
		Upstream:  scan.Resources,
		Grammar:   grammar,
		Overrides: overrides,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "catalog: %s\n", catalog.Summary())

	rendered, err := catalog.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, rendered, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", outPath)

	if err := writeProvenance(provenancePath, scan.Refs); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", provenancePath)
	return nil
}

// loadGrammar fetches the ARN grammar, or reads the committed cache when -offline is set,
// defaulting to a fetch so an upstream revision lands as a cache diff beside the catalog.
func loadGrammar(namespaces []string, offline bool) (gen.SvcRef, string, error) {
	if offline {
		grammar, err := gen.LoadSvcRefCache(namespaces, grammarDir)
		return grammar, "from " + grammarDir, err
	}
	grammar, err := gen.FetchSvcRef(context.Background(), namespaces, grammarDir)
	return grammar, "from the Service Reference API", err
}

// reportSkipped lists repos that contributed nothing, so a coverage gap is always
// attributable rather than silent.
func reportSkipped(skipped map[string]string) {
	if len(skipped) == 0 {
		return
	}
	repos := make([]string, 0, len(skipped))
	for r := range skipped {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	fmt.Fprintf(os.Stderr, "skipped %d repo(s):\n", len(repos))
	for _, r := range repos {
		fmt.Fprintf(os.Stderr, "  %-34s %s\n", r, skipped[r])
	}
}

// writeProvenance records the release tag each repo's facts came from, so a catalog can
// be traced to exact controller sources.
func writeProvenance(path string, refs []gen.RepoRef) error {
	doc := struct {
		Comment string        `json:"_comment"`
		Repos   []gen.RepoRef `json:"repos"`
	}{
		Comment: "Controller releases the committed catalog was derived from. " +
			"Written by cmd/ack-catalog-gen. DO NOT EDIT BY HAND.",
		Repos: refs,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
