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

package commands

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/aws-controllers-k8s/ackctl/metadata"
)

var (
	listShowUnsupported bool
	listService         string
)

var adoptableCmd = &cobra.Command{
	Use:   "adoptable",
	Short: "Show which ACK resource kinds can be adopted by tag",
	Long: `adoptable lists the ACK resource kinds ack can adopt by tag, the
Resource Groups Tagging API type filter used to discover each, and the identifier
keys that kind's adoption-fields must carry — the fields adopt populates for you.

Use --unsupported to list the kinds that cannot be adopted by tag, each with the
reason, so a missing kind is explained rather than silently absent.

Reads the catalog embedded in this binary: no AWS calls, no credentials needed.`,
	Args: cobra.NoArgs,
	RunE: runList,
}

func init() {
	adoptableCmd.Flags().BoolVar(&listShowUnsupported, "unsupported", false,
		"list kinds that cannot be adopted by tag, with the reason")
	adoptableCmd.Flags().StringVar(&listService, "service", "",
		"limit to a single ACK service (e.g. eks)")
}

// serviceOf returns the ACK service from an unsupported entry's "service:resource_dir" key.
func serviceOf(resourceKey string) string {
	svc, _, _ := strings.Cut(resourceKey, ":")
	return svc
}

// knownServices lists every service the catalog mentions, supported or not, so an unknown
// --service is an error rather than empty output that reads as "nothing is adoptable".
func knownServices(c *metadata.Catalog) map[string]struct{} {
	out := map[string]struct{}{}
	for _, r := range c.Resources() {
		out[strings.ToLower(r.Service)] = struct{}{}
	}
	for _, u := range c.Unsupported() {
		out[strings.ToLower(serviceOf(u.Resource))] = struct{}{}
	}
	return out
}

func runList(cmd *cobra.Command, _ []string) error {
	catalog, err := metadata.Load()
	if err != nil {
		return err
	}

	if listService != "" {
		if _, ok := knownServices(catalog)[strings.ToLower(listService)]; !ok {
			return fmt.Errorf("unknown ACK service %q; run without --service to see them all",
				listService)
		}
	}

	if listShowUnsupported {
		un := catalog.Unsupported()
		sort.Slice(un, func(i, j int) bool { return un[i].Resource < un[j].Resource })
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "RESOURCE\tKIND\tREASON")
		for _, u := range un {
			if listService != "" && !strings.EqualFold(serviceOf(u.Resource), listService) {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", u.Resource, u.Kind, u.Reason)
		}
		return w.Flush()
	}

	res := catalog.Resources()
	sort.Slice(res, func(i, j int) bool {
		if res[i].Service != res[j].Service {
			return res[i].Service < res[j].Service
		}
		return res[i].Kind < res[j].Kind
	})
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SERVICE\tKIND\tTYPE FILTER\tIDENTIFIER KEYS")
	for _, r := range res {
		if listService != "" && !strings.EqualFold(r.Service, listService) {
			continue
		}
		keys := make([]string, 0, len(r.Bindings))
		for _, b := range r.Bindings {
			keys = append(keys, b.Key)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Service, r.Kind, r.ResourceTypeFilter, strings.Join(keys, ","))
	}
	return w.Flush()
}
