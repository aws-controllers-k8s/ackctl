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
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	rgt "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	sts "github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"

	"github.com/aws-controllers-k8s/ackctl/internal/debuglog"
	"github.com/aws-controllers-k8s/ackctl/internal/emit"
	"github.com/aws-controllers-k8s/ackctl/internal/resolver"
	"github.com/aws-controllers-k8s/ackctl/internal/tagging"
	"github.com/aws-controllers-k8s/ackctl/metadata"
)

var (
	adoptTags           []string
	adoptReadOnly       bool
	adoptDeletionPolicy string
	adoptService        string
	adoptKind           string
	adoptAdoptionSet    string
	adoptNamespace      string
	adoptRegion         string
)

var adoptCmd = &cobra.Command{
	Use:   "adopt --service SERVICE --kind KIND --tag KEY=VALUE",
	Short: "Discover one AWS resource kind by tag and emit ACK adoption manifests",
	Long: `adopt finds the AWS resources of one ACK kind that carry the given tags, and
writes an adoption Custom Resource for each to stdout. Run 'ackctl list adoptable'
to see the kinds it can adopt.

Examples:
  ackctl adopt --service eks --kind Nodegroup --tag Environment=prod

  # create, not apply: create refuses a CR that already exists
  ackctl adopt --service rds --kind DBInstance --tag team=platform --tag env=prod \
      | kubectl create -f -`,
	Args: cobra.NoArgs,
	RunE: runAdopt,
}

func init() {
	adoptCmd.Flags().StringVar(&adoptService, "service", "",
		"ACK service of the resource to adopt (e.g. eks) [required]")
	adoptCmd.Flags().StringVar(&adoptKind, "kind", "",
		"ACK resource kind to adopt (e.g. Nodegroup) [required]")
	adoptCmd.Flags().StringArrayVar(&adoptTags, "tag", nil,
		"tag condition as KEY=VALUE, repeatable. All must match (AND). A bare KEY matches "+
			"any value, and KEY= matches only an empty value [required]")
	adoptCmd.Flags().StringVar(&adoptAdoptionSet, "adoption-set", "",
		"names the collection this adopts, applied as the ack.k8s.aws/adoption-set "+
			"label. Does NOT affect CR names (default: derived from the service, kind "+
			"and tag selector)")
	adoptCmd.Flags().BoolVar(&adoptReadOnly, "read-only", true,
		"emit services.k8s.aws/read-only, so ACK reports status but never mutates "+
			"the resource. Pass --read-only=false to let ACK manage it")
	adoptCmd.Flags().StringVar(&adoptDeletionPolicy, "deletion-policy",
		string(emit.DeletionPolicyRetain),
		"emit services.k8s.aws/deletion-policy: retain (deleting the CR leaves the "+
			"AWS resource) or delete")
	adoptCmd.Flags().StringVar(&adoptNamespace, "namespace", "",
		"namespace to set on emitted CRs (default: none, uses the apply-time namespace)")
	adoptCmd.Flags().StringVar(&adoptRegion, "region", "",
		"AWS region to query (default: AWS_REGION, AWS_DEFAULT_REGION, or the active profile)")
	_ = adoptCmd.MarkFlagRequired("service")
	_ = adoptCmd.MarkFlagRequired("kind")
	_ = adoptCmd.MarkFlagRequired("tag")
}

// --adoption-set becomes a Kubernetes label value, so it must satisfy both of these.
var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

const maxLabelValueLen = 63

// countSampleLimit bounds the tag keys sampled by the -v1 sweep, which reports a
// sample rather than an inventory.
const countSampleLimit = 25

func runAdopt(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	deletionPolicy, err := emit.ParseDeletionPolicy(adoptDeletionPolicy)
	if err != nil {
		return err
	}
	tags, err := parseTags(adoptTags)
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		return fmt.Errorf("--tag must name at least one tag condition")
	}
	if adoptAdoptionSet != "" {
		if !dns1123.MatchString(adoptAdoptionSet) {
			return fmt.Errorf("invalid --adoption-set %q: must be lowercase alphanumeric or '-', "+
				"and start/end with alphanumeric (it is used as a label value)", adoptAdoptionSet)
		}
		if len(adoptAdoptionSet) > maxLabelValueLen {
			return fmt.Errorf("--adoption-set %q is %d characters; Kubernetes caps a label "+
				"value at %d, so the CRs would be rejected at apply time",
				adoptAdoptionSet, len(adoptAdoptionSet), maxLabelValueLen)
		}
	}

	catalog, err := metadata.Load()
	if err != nil {
		return err
	}
	debuglog.Logf("catalog: %d supported, %d unsupported resource(s)",
		len(catalog.Resources()), len(catalog.Unsupported()))
	res, err := resolver.New(catalog)
	if err != nil {
		return err
	}

	target, ok := catalog.LookupByServiceKind(adoptService, adoptKind)
	if !ok {
		if reason, found := catalog.UnsupportedReason(adoptService, adoptKind); found {
			return fmt.Errorf("%s/%s cannot be adopted by tag: %s", adoptService, adoptKind, reason)
		}
		return fmt.Errorf("unknown or unsupported resource %s/%s (see 'ackctl list adoptable --service %s')",
			adoptService, adoptKind, adoptService)
	}
	if target.ResourceTypeFilter == "" {
		return fmt.Errorf("%s/%s has no Resource Groups Tagging API type filter, so it cannot "+
			"be discovered by tag. This is a gap in ackctl's catalog, not in your command; "+
			"please report it", adoptService, adoptKind)
	}

	debuglog.Section("target")
	debuglog.Logf("service/kind:        %s/%s", adoptService, adoptKind)
	debuglog.Logf("resource type filter: %s", target.ResourceTypeFilter)
	debuglog.Logf("GVK:                 %s/%s %s", target.Group, target.Version, target.Kind)
	for _, tmpl := range target.Templates {
		debuglog.Logf("ARN template:        %s", tmpl.ARNTemplate)
	}
	debuglog.Logf("identifier keys:     %s", strings.Join(target.IdentifierKeys(), ", "))

	awsCfg, err := loadAWSConfig(ctx, adoptRegion)
	if err != nil {
		return err
	}
	// The region is written to every CR, so defaulting it would point the controller
	// somewhere the user never named.
	if awsCfg.Region == "" {
		return fmt.Errorf("no AWS region resolved: pass --region, or set AWS_REGION, " +
			"AWS_DEFAULT_REGION, or a region in your active AWS profile")
	}

	debuglog.Section("aws")
	// Show which region was used and where it came from.
	regionSource := "AWS config/environment"
	if adoptRegion != "" {
		regionSource = "--region flag"
	}
	debuglog.Logf("region:  %s  (from %s)", awsCfg.Region, regionSource)
	logCallerIdentity(ctx, awsCfg)

	adoptionSet := adoptAdoptionSet
	if adoptionSet == "" {
		adoptionSet = emit.DefaultAdoptionSet(adoptService, adoptKind, awsCfg.Region, tags)
		debuglog.Logf("adoption set: %s  (derived; pass --adoption-set to name it)", adoptionSet)
	}
	emitOpts := emit.Options{
		Policy:         emit.PolicyAdopt,
		ReadOnly:       adoptReadOnly,
		DeletionPolicy: deletionPolicy,
		AdoptionSet:    adoptionSet,
		Namespace:      adoptNamespace,
		Region:         awsCfg.Region,
	}

	tagClient := tagging.New(rgt.NewFromConfig(awsCfg))

	debuglog.Section("discover")

	matches, err := tagClient.FindByTags(ctx, tags, []string{target.ResourceTypeFilter})
	if err != nil {
		// A malformed filter is a catalog defect the user cannot fix.
		var unsupported *tagging.UnsupportedTypeError
		if errors.As(err, &unsupported) {
			return fmt.Errorf(
				"cannot query for %s/%s: %v.\n"+
					"This is a bug in ackctl's resource catalog, not in your command. "+
					"Please report it. In the meantime the resource can be adopted "+
					"individually with the services.k8s.aws/adoption-fields annotation:\n"+
					"  https://aws-controllers-k8s.github.io/community/docs/user-docs/adoption/",
				adoptService, adoptKind, unsupported)
		}
		return err
	}
	if len(matches) == 0 {
		explainNoMatches(ctx, tagClient, target, tags, awsCfg.Region)
		return nil
	}

	var crs []*emit.CR
	var resolved, skipped int
	// Naming should make this unreachable, but a duplicate name would mean one CR
	// adopting the wrong resource.
	seenNames := map[string]string{} // name -> ARN that claimed it
	for _, m := range matches {
		r, rerr := res.ResolveARNForResource(m.ARN, target)
		if rerr != nil {
			skipped++
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", m.ARN, rerr)
			continue
		}
		name := emit.DefaultName(r, m.ARN)
		debuglog.Logf("emit %s -> name %s", m.ARN, name)
		if prev, dup := seenNames[name]; dup {
			return fmt.Errorf(
				"name collision: %q would be generated for two different resources:\n  %s\n  %s\n"+
					"this is a bug in ackctl name generation; please report it", name, prev, m.ARN)
		}
		seenNames[name] = m.ARN

		cr, berr := emit.Build(r, name, emitOpts)
		if berr != nil {
			return fmt.Errorf("building the CR for %s: %w", m.ARN, berr)
		}
		crs = append(crs, cr)
		resolved++
	}

	sort.Slice(crs, func(i, j int) bool {
		return crs[i].Metadata.Name < crs[j].Metadata.Name
	})

	doc, err := emit.Document(crs)
	if err != nil {
		return err
	}
	fmt.Print(string(doc))

	fmt.Fprintf(os.Stderr, "\nresolved %d, skipped %d of %d matched resource(s)\n",
		resolved, skipped, len(matches))
	return adoptOutcome(resolved, skipped, len(matches))
}

// adoptOutcome decides the exit status. A skipped resource matched the selector but
// produced no CR, so any skip is a failure rather than a line in the log.
func adoptOutcome(resolved, skipped, matched int) error {
	if skipped == 0 {
		return nil
	}
	if resolved == 0 {
		return fmt.Errorf("matched %d resource(s) but resolved none, so there is nothing to "+
			"apply; see the skip reasons above", matched)
	}
	return fmt.Errorf("skipped %d of %d matched resource(s), so the manifests above are "+
		"incomplete; see the skip reasons above", skipped, matched)
}

// logCallerIdentity reports which account and identity the query runs as.
func logCallerIdentity(ctx context.Context, cfg aws.Config) {
	if !debuglog.V(debuglog.LevelDiagnose) {
		return
	}
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		debuglog.Diagf("caller: could not determine identity: %v", err)
		return
	}
	debuglog.Diagf("caller: account=%s arn=%s", aws.ToString(out.Account), aws.ToString(out.Arn))
}

// explainNoMatches re-queries without tag filters to tell apart a wrong region, no
// resources of the kind, and resources that are missing the requested tags.
func explainNoMatches(
	ctx context.Context,
	tagClient *tagging.Client,
	target metadata.Resource,
	tags []tagging.Filter,
	region string,
) {
	w := os.Stderr
	fmt.Fprintf(w, "no %s/%s resources matched the given tags\n", adoptService, adoptKind)
	fmt.Fprintf(w, "  region:      %s\n", region)
	fmt.Fprintf(w, "  type filter: %s\n", target.ResourceTypeFilter)
	fmt.Fprintf(w, "  tag filters: %s (all must match)\n", formatTagSelector(tags))

	// Telling a wrong region apart from a missing tag needs a second, untagged sweep, so
	// it is opt-in rather than a cost every empty result pays.
	if !debuglog.V(debuglog.LevelDiagnose) {
		fmt.Fprintf(w, "\nRe-run with -v1 to sweep the region for resources of this kind.\n")
		return
	}

	total, capped, sampleTags, err := tagClient.CountByType(ctx, target.ResourceTypeFilter, countSampleLimit)
	if err != nil {
		fmt.Fprintf(w, "\ncould not check for untagged resources of this kind: %v\n", err)
		return
	}

	if total == 0 {
		fmt.Fprintf(w, "\nThe Tagging API reports no %s resources at all in this region/account.\n",
			target.ResourceTypeFilter)
		fmt.Fprintf(w, "Check that --region and your AWS credentials point where you expect,\n")
		fmt.Fprintf(w, "then confirm the resources exist there.\n")
		return
	}

	atLeast := ""
	if capped {
		atLeast = "at least "
	}
	fmt.Fprintf(w, "\n%s%d %s resource(s) exist here, but none carry all the requested tags.\n",
		atLeast, total, target.ResourceTypeFilter)
	present := collectTagKeys(sampleTags)
	if len(present) > 0 {
		fmt.Fprintf(w, "Tag keys on the first %d sampled: %s\n",
			len(sampleTags), strings.Join(present, ", "))
		fmt.Fprintf(w, "Tag keys and values are case-sensitive.\n")
	} else {
		fmt.Fprintf(w, "The %d sampled resource(s) have no tags at all.\n", len(sampleTags))
	}
}

func collectTagKeys(samples []map[string]string) []string {
	seen := map[string]struct{}{}
	for _, m := range samples {
		for k := range m {
			seen[k] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func formatTagSelector(tags []tagging.Filter) string {
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		switch {
		case t.Any:
			parts = append(parts, t.Key+"=<any value>")
		case t.Value == "":
			parts = append(parts, t.Key+"=<empty>")
		default:
			parts = append(parts, t.Key+"="+t.Value)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, " AND ")
}

// parseTags turns each --tag occurrence into one filter, taking the occurrence whole so a
// value may contain spaces.
func parseTags(raw []string) ([]tagging.Filter, error) {
	var out []tagging.Filter
	seen := map[string]struct{}{}
	for _, pair := range raw {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			return nil, fmt.Errorf("empty --tag value")
		}
		k, v, found := strings.Cut(pair, "=")
		if k == "" {
			return nil, fmt.Errorf("invalid --tag %q: empty key", pair)
		}
		if _, dup := seen[k]; dup {
			return nil, fmt.Errorf("tag key %q given twice: all conditions are ANDed, so a "+
				"key cannot usefully have two values", k)
		}
		seen[k] = struct{}{}
		out = append(out, tagging.Filter{Key: k, Value: v, Any: !found})
	}
	return out, nil
}

func loadAWSConfig(ctx context.Context, region string) (aws.Config, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	return awsconfig.LoadDefaultConfig(ctx, opts...)
}
