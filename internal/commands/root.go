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

// Package commands implements the ack commands.
package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ack",
	Short: "Command-line tools for AWS Controllers for Kubernetes (ACK)",
	Long: `ack is a command-line companion for AWS Controllers for Kubernetes.

Available features:

  adopt          Bring existing AWS resources under ACK management by tag,
                 emitting adoption Custom Resources so you do not have to
                 hand-write adoption-fields annotations.
  list adoptable Show which ACK resource kinds can be adopted by tag, the
                 identifiers each needs, and why the rest cannot.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// listCmd groups the read-only catalog views, which make no AWS calls.
var listCmd = &cobra.Command{
	Use:   "list",
	Short: "Show what ack knows about ACK resources",
	Long: `list reports on the resource catalog embedded in this binary. All of its
subcommands are read-only and make no AWS API calls, so they work offline and
without credentials.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
}

// versionCmd exists because `ack version` is what users type, while cobra only wires up
// the --version flag.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), rootCmd.Version)
		return err
	},
}

// SetVersion records build metadata, injected at link time from main.
func SetVersion(version, commit string) {
	rootCmd.Version = version + " (" + commit + ")"
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func init() {
	// The catalog views live under "list" rather than under "adopt": what they
	// report (identifier keys, type filters, why a kind is unreachable) is read
	// by someone deciding whether and how to adopt, before running adopt at all.
	// Nesting them under the action they inform made them hard to find.
	listCmd.AddCommand(adoptableCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(adoptCmd)
	rootCmd.AddCommand(versionCmd)
}
