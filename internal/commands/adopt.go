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
	adoptDebug          bool
)

var adoptCmd = &cobra.Command{
	Use:   "adopt --service SERVICE --kind KIND --tag KEY=VALUE",
	Short: "Discover one AWS resource kind by tag and emit ACK adoption manifests",
	Long: `adopt queries the Resource Groups Tagging API for resources of a single
ACK kind matching the given tags, resolves each ARN into ACK adoption-fields,
and writes adoption Custom Resource manifests to stdout.

You must name the resource kind explicitly with --service and --kind. adopt
targets exactly one kind per run: this keeps the operation unambiguous and
prevents a broad tag selector from pulling in resource kinds you did not intend
to hand to ACK. Use 'ack list adoptable' to see the valid service/kind pairs.

THE FIRST RUN IS OBSERVE-ONLY. Adopting infrastructure ACK did not create
should not, by itself, let ACK change or destroy it, so --read-only defaults to
true and --deletion-policy defaults to retain. A too-broad selector or a
mis-mapped identifier then costs a CR to delete instead of a production
resource. Handing over management is a second, deliberate step, and the
adoption-set label makes it one command:

    kubectl annotate nodegroup -l ack.k8s.aws/adoption-set=platform-prod \
        services.k8s.aws/read-only=false --overwrite

--adoption-set labels the collection so it is selectable as a group. It
deliberately does NOT affect CR names, so one value can span several runs:
    kubectl get nodegroup -l ack.k8s.aws/adoption-set=platform-prod

CR names are derived from the AWS resource's own primary key plus a digest of
its ARN, e.g. prod-cluster-workers-7f3a9c1d. Because the name depends only on
the AWS resource's identity, re-adopting the same resource -- even under a
different --adoption-set -- regenerates the same name.

Nothing is applied to a cluster: manifests go to stdout, and the summary,
per-resource skips and --debug go to stderr, so the YAML pipes cleanly.

Apply with 'kubectl create', NOT 'kubectl apply'. create issues a POST, which
the API server rejects with AlreadyExists if a CR of that name already exists;
combined with identity-derived names, that is what stops the same AWS resource
being adopted twice. apply issues a patch, which would silently MUTATE an
existing CR -- stamping adoption annotations onto a CR that already manages a
different AWS resource. Note that create is per-object: if one name collides,
the other CRs are still created and only the conflicting one errors, so
re-running after new resources appear adopts exactly the delta.

Examples:
  # Review every EKS Nodegroup tagged Environment=prod (read-only, retained)
  ack adopt --service eks --kind Nodegroup --tag Environment=prod

  # Two tags (AND), piped straight into the cluster
  ack adopt --service rds --kind DBInstance \
      --tag team=platform --tag env=prod --adoption-set platform-prod \
      | kubectl create -f -

  # Hand full management to ACK at generation time
  ack adopt --service eks --kind Nodegroup --tag Environment=prod \
      --read-only=false --deletion-policy delete`,
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
	adoptCmd.Flags().BoolVar(&adoptDebug, "debug", false,
		"log discovery and resolution details to stderr (also ACK_DEBUG=1)")
	_ = adoptCmd.MarkFlagRequired("service")
	_ = adoptCmd.MarkFlagRequired("kind")
	_ = adoptCmd.MarkFlagRequired("tag")
}

// dns1123 is the character set Kubernetes allows in a label value, which
// --adoption-set is used as.
var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// maxLabelValueLen is the Kubernetes limit on a label value, which --adoption-set becomes.
const maxLabelValueLen = 63

func runAdopt(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	if adoptDebug || os.Getenv("ACK_DEBUG") != "" {
		debuglog.Enable()
	}

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

	// The user names the kind, so there is no Kind inference or filter collision.
	target, ok := catalog.LookupByServiceKind(adoptService, adoptKind)
	if !ok {
		if reason, found := catalog.UnsupportedReason(adoptService, adoptKind); found {
			return fmt.Errorf("%s/%s cannot be adopted by tag: %s", adoptService, adoptKind, reason)
		}
		return fmt.Errorf("unknown or unsupported resource %s/%s (see 'ack list adoptable --service %s')",
			adoptService, adoptKind, adoptService)
	}
	if target.ResourceTypeFilter == "" {
		return fmt.Errorf("%s/%s has no Resource Groups Tagging API type filter; cannot discover it by tag",
			adoptService, adoptKind)
	}

	debuglog.Section("target")
	debuglog.Logf("service/kind:        %s/%s", adoptService, adoptKind)
	debuglog.Logf("resource type filter: %s", target.ResourceTypeFilter)
	debuglog.Logf("GVK:                 %s/%s %s", target.Group, target.Version, target.Kind)
	debuglog.Logf("ARN template:        %s", target.ARNTemplate)
	debuglog.Logf("identifier keys:     %s", strings.Join(bindingKeys(target), ", "))

	awsCfg, err := loadAWSConfig(ctx, adoptRegion)
	if err != nil {
		return err
	}
	// Fail rather than default a region, because it is written to every CR and picking
	// one would silently point the controller somewhere the user never named.
	if awsCfg.Region == "" {
		return fmt.Errorf("no AWS region resolved: pass --region, or set AWS_REGION, " +
			"AWS_DEFAULT_REGION, or a region in your active AWS profile.\n" +
			"adopt needs one to scope its tag query, and writes it to each CR as " +
			"services.k8s.aws/region so the controller looks in the same place")
	}

	debuglog.Section("aws")
	// Region is the most common reason a tag query finds nothing, so always show which
	// one was used and where it came from.
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
		// A malformed filter is a catalog defect the user cannot fix, so say so rather
		// than leak an SDK error. It is also the only filter problem the API reports.
		var unsupported *tagging.UnsupportedTypeError
		if errors.As(err, &unsupported) {
			return fmt.Errorf(
				"cannot query for %s/%s: %v.\n"+
					"This is a bug in ack's resource catalog, not in your command. "+
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
	// silently adopting the wrong resource, so refuse loudly rather than trust it.
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
					"this is a bug in ack name generation; please report it", name, prev, m.ARN)
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
	if resolved == 0 {
		return fmt.Errorf("matched %d resource(s) but resolved none, so there is nothing to "+
			"apply; see the skip reasons above", len(matches))
	}
	return nil
}

func bindingKeys(r metadata.Resource) []string {
	keys := make([]string, 0, len(r.Bindings))
	for _, b := range r.Bindings {
		keys = append(keys, b.Key)
	}
	return keys
}

// logCallerIdentity reports which account/identity the query will run as. Wrong
// profile is a common cause of an empty result, and it is invisible otherwise.
func logCallerIdentity(ctx context.Context, cfg aws.Config) {
	if !debuglog.Enabled() {
		return
	}
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		debuglog.Logf("caller: could not determine identity: %v", err)
		return
	}
	debuglog.Logf("caller: account=%s arn=%s", aws.ToString(out.Account), aws.ToString(out.Arn))
}

// explainNoMatches turns an empty result into a diagnosis, since "no matches" can mean
// a wrong region, no resources of the kind, or resources without the requested tags. It
// re-queries without tag filters to report which case this is.
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

	total, capped, sampleTags, err := tagClient.CountByType(ctx, target.ResourceTypeFilter, 25)
	if err != nil {
		fmt.Fprintf(w, "\ncould not check for untagged resources of this kind: %v\n", err)
		return
	}

	if total == 0 {
		fmt.Fprintf(w, "\nThe Tagging API reports no %s resources at all in this region/account.\n",
			target.ResourceTypeFilter)
		fmt.Fprintf(w, "Check that --region and your AWS credentials point where you expect,\n")
		fmt.Fprintf(w, "then confirm the resources exist there. Re-run with --debug for details.\n")
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
		fmt.Fprintf(w, "Tag keys present on those resources: %s\n", strings.Join(present, ", "))
		fmt.Fprintf(w, "Tag keys and values are case-sensitive.\n")
	} else {
		fmt.Fprintf(w, "Those resources have no tags at all.\n")
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

// parseTags turns each --tag occurrence into one filter. A bare KEY matches any value,
// KEY= matches only an empty value, and a value may contain spaces because occurrences are
// not split.
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
