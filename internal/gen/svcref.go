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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws-controllers-k8s/ackctl/internal/naming"
)

// placeholderRE matches an ARN-template placeholder like ${ClusterName}.
var placeholderRE = regexp.MustCompile(`\$\{([A-Za-z0-9]+)\}`)

// SvcRefIndexURL lists every service the AWS Service Reference API publishes,
// each with the URL of its own document. It is the machine-readable feed behind
// the Service Authorization Reference docs: first-party, unauthenticated, and the
// source of every ARN template in the catalog.
const SvcRefIndexURL = "https://servicereference.us-east-1.amazonaws.com/"

// serviceAliases maps an ACK service name to the AWS service namespace where the two
// differ, such as eventbridge to events and prometheusservice to aps. Both the Service
// Reference API and the Tagging API's type filter are keyed by namespace.
var serviceAliases = map[string]string{
	"acmpca":                  "acm-pca",
	"apigatewayv2":            "apigateway",
	"applicationautoscaling":  "application-autoscaling",
	"bedrockagent":            "bedrock",
	"bedrockagentcorecontrol": "bedrock-agentcore",
	"cloudwatchlogs":          "logs",
	"cognitoidentityprovider": "cognito-idp",
	"documentdb":              "rds",
	"ecrpublic":               "ecr-public",
	"efs":                     "elasticfilesystem",
	"elbv2":                   "elasticloadbalancing",
	"emrcontainers":           "emr-containers",
	"eventbridge":             "events",
	"keyspaces":               "cassandra",
	"mwaa":                    "airflow",
	"networkfirewall":         "network-firewall",
	"opensearchserverless":    "aoss",
	"opensearchservice":       "es",
	"prometheusservice":       "aps",
	"recyclebin":              "rbin",
	"s3control":               "s3",
	"sfn":                     "states",
	"stepfunctions":           "states",
	"cloudwatchevents":        "events",
}

func Namespace(ackService string) string {
	if ns, ok := serviceAliases[ackService]; ok {
		return ns
	}
	return ackService
}

type svcRefIndexEntry struct {
	Service  string `json:"service"`
	URL      string `json:"url"`
	Modified int64  `json:"modified"`
}

// svcRefDoc is one service's document. Only Resources is used; Actions and
// ConditionKeys are ignored.
type svcRefDoc struct {
	Name      string `json:"Name"`
	Resources []struct {
		Name       string   `json:"Name"`
		ARNFormats []string `json:"ARNFormats"`
	} `json:"Resources"`
}

// ARNForm is one ARN shape a resource type can take, with placeholders split into the
// path identifiers a binding may use. A resource can publish several forms, so the
// generator tries each and takes the first that binds every key.
type ARNForm struct {
	Template string
	// PathPlaceholders excludes ${Partition}, ${Region} and ${Account}: those are
	// envelope slots, not resource identifiers. Leaving them in the pool is how a
	// key can bind to a segment that is really the account or the region.
	PathPlaceholders []string
	HasAccount       bool
}

type SvcRefResource struct {
	// Type is the resource-type label as AWS spells it ("nodegroup"), which is
	// also the second half of the Tagging API filter.
	Type  string
	Forms []ARNForm
}

type SvcRefService struct {
	Namespace string
	Resources []SvcRefResource
}

type SvcRef map[string]*SvcRefService

// LoadSvcRefCache reads the ARN grammar from a cache FetchSvcRef wrote earlier, so a
// regeneration reproduces from the committed documents with no network, and treats a
// namespace with no cached document as missing just as an unpublished one.
func LoadSvcRefCache(namespaces []string, cacheDir string) (SvcRef, error) {
	if cacheDir == "" {
		return nil, fmt.Errorf("offline regeneration needs a grammar cache directory")
	}
	out := SvcRef{}
	var missing []string
	for _, ns := range namespaces {
		b, err := os.ReadFile(filepath.Join(cacheDir, ns+".json"))
		if err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, ns)
				continue
			}
			return nil, fmt.Errorf("reading cached grammar for %s: %w", ns, err)
		}
		var doc svcRefDoc
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("parsing cached grammar for %s: %w", ns, err)
		}
		out[ns] = parseSvcRefDoc(ns, &doc)
	}
	reportMissing(missing)
	return out, nil
}

// FetchSvcRef downloads the ARN grammar for the given namespaces, which is only the
// ones ACK has controllers for rather than all 455 the index publishes.
//
// cacheDir, when non-empty, holds the raw documents so LoadSvcRefCache can reproduce this
// grammar offline and an AWS template revision shows up as a diff in the cache.
func FetchSvcRef(ctx context.Context, namespaces []string, cacheDir string) (SvcRef, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	index, err := fetchJSON[[]svcRefIndexEntry](ctx, client, SvcRefIndexURL)
	if err != nil {
		return nil, fmt.Errorf("fetching Service Reference index: %w", err)
	}
	byName := make(map[string]svcRefIndexEntry, len(*index))
	for _, e := range *index {
		byName[e.Service] = e
	}

	want := map[string]struct{}{}
	for _, ns := range namespaces {
		want[ns] = struct{}{}
	}

	out := SvcRef{}
	var missing []string
	for ns := range want {
		entry, ok := byName[ns]
		if !ok {
			// AWS has not published grammar for this service yet. Not an error:
			// its resources are recorded unsupported with that reason.
			missing = append(missing, ns)
			continue
		}
		doc, err := fetchJSON[svcRefDoc](ctx, client, entry.URL)
		if err != nil {
			return nil, fmt.Errorf("fetching grammar for %s: %w", ns, err)
		}
		if cacheDir != "" {
			if err := writeCache(cacheDir, ns, doc); err != nil {
				return nil, err
			}
		}
		out[ns] = parseSvcRefDoc(ns, doc)
	}
	reportMissing(missing)
	return out, nil
}

// reportMissing names the services with no grammar, whose resources land in the catalog's
// unsupported set with that reason.
func reportMissing(missing []string) {
	if len(missing) == 0 {
		return
	}
	sort.Strings(missing)
	fmt.Fprintf(os.Stderr, "no published ARN grammar for: %s\n", strings.Join(missing, ", "))
}

func parseSvcRefDoc(ns string, doc *svcRefDoc) *SvcRefService {
	svc := &SvcRefService{Namespace: ns}
	for _, r := range doc.Resources {
		res := SvcRefResource{Type: r.Name}
		for _, tmpl := range r.ARNFormats {
			tmpl = strings.TrimSpace(tmpl)
			if tmpl == "" || foreignService(tmpl, ns) {
				continue
			}
			res.Forms = append(res.Forms, newARNForm(tmpl))
		}
		if len(res.Forms) > 0 {
			svc.Resources = append(svc.Resources, res)
		}
	}
	sort.Slice(svc.Resources, func(i, j int) bool { return svc.Resources[i].Type < svc.Resources[j].Type })
	return svc
}

// foreignService reports whether an ARN template belongs to a different service than the
// document it came from — Service Reference docs list every resource a service's actions
// touch, e.g. kms templates inside the events document.
func foreignService(tmpl, ns string) bool {
	parts := strings.SplitN(tmpl, ":", 4)
	if len(parts) < 3 || strings.Contains(parts[2], "${") {
		return false // no literal service field to judge
	}
	return parts[2] != ns
}

func newARNForm(tmpl string) ARNForm {
	form := ARNForm{Template: tmpl}
	for _, m := range placeholderRE.FindAllStringSubmatch(tmpl, -1) {
		switch strings.ToLower(m[1]) {
		case "partition", "region":
			// envelope, never an identifier
		case "account":
			form.HasAccount = true
		default:
			form.PathPlaceholders = append(form.PathPlaceholders, m[1])
		}
	}
	return form
}

// PickResource chooses the AWS resource type whose label matches an ACK resource
// directory, by exact equality after normalization or a whole-word trailing match where
// the longest label wins. Anything ambiguous returns false and becomes a visible
// unsupported entry.
func (s *SvcRefService) PickResource(resourceDir string) (SvcRefResource, bool) {
	words := naming.SplitWords(resourceDir)
	target := strings.Join(words, "")

	var exact []SvcRefResource
	for _, r := range s.Resources {
		if strings.Join(naming.SplitWords(r.Type), "") == target {
			exact = append(exact, r)
		}
	}
	if len(exact) == 1 {
		return exact[0], true
	}
	if len(exact) > 1 {
		return SvcRefResource{}, false
	}

	var best SvcRefResource
	bestLen := 0
	tie := false
	for _, r := range s.Resources {
		label := strings.Join(naming.SplitWords(r.Type), "")
		if label == "" {
			continue
		}
		for i := range words {
			if strings.Join(words[i:], "") != label {
				continue
			}
			switch {
			case len(label) > bestLen:
				best, bestLen, tie = r, len(label), false
			case len(label) == bestLen:
				tie = true
			}
			break
		}
	}
	if bestLen == 0 || tie {
		return SvcRefResource{}, false
	}
	return best, true
}

func fetchJSON[T any](ctx context.Context, client *http.Client, url string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", url, err)
	}
	return &out, nil
}

func writeCache(dir, ns string, doc *svcRefDoc) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Re-marshalled rather than stored verbatim, so the cache is stable under
	// upstream whitespace churn and diffs only on real grammar changes.
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ns+".json"), append(b, '\n'), 0o644)
}
