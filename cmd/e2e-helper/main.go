// Command e2e-helper performs setup and verification for the E2E full-flow test.
//
// Usage:
//
//	e2e-helper --setup --base-dir=<dir> --ns=<ns> --proj=<proj> --agent-name=<name>
//	e2e-helper --check --base-dir=<dir> --ns=<ns> --proj=<proj>
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/gityard/internal/integrity"
	"github.com/sopranoworks/gityard/internal/pr"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
)

func main() {
	setup := flag.Bool("setup", false, "run setup: create repo + set agent settings")
	check := flag.Bool("check", false, "run check: verify PR merged")
	baseDir := flag.String("base-dir", "", "storage base directory")
	ns := flag.String("ns", "e2e", "namespace")
	proj := flag.String("proj", "fullflow", "project name")
	agentName := flag.String("agent-name", "mock_reviewer", "agent config name for reviewer")
	flag.Parse()

	if *baseDir == "" {
		fmt.Fprintln(os.Stderr, "e2e-helper: --base-dir is required")
		os.Exit(1)
	}

	if *setup {
		if err := runSetup(*baseDir, *ns, *proj, *agentName); err != nil {
			fmt.Fprintf(os.Stderr, "e2e-helper setup: %v\n", err)
			os.Exit(1)
		}
	} else if *check {
		if err := runCheck(*baseDir, *ns, *proj); err != nil {
			fmt.Fprintf(os.Stderr, "e2e-helper check: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Fprintln(os.Stderr, "e2e-helper: specify --setup or --check")
		os.Exit(1)
	}
}

func runSetup(baseDir, ns, proj, agentName string) error {
	gitStore := git.NewStore(baseDir)
	if err := gitStore.CreateRepo(ns, proj); err != nil {
		return fmt.Errorf("create repo: %w", err)
	}
	fmt.Printf("setup: created repo %s/%s\n", ns, proj)

	intStore, err := integrity.Open(baseDir + "/repo_heads.db")
	if err != nil {
		return fmt.Errorf("open integrity store: %w", err)
	}
	defer intStore.Close()

	agentEnabled := true
	autoConfirm := true
	if err := intStore.SetGlobalPREventSettings(&integrity.PREventSettings{
		OnCreated: &integrity.EventAction{
			AgentEnabled: &agentEnabled,
			AgentName:    agentName,
		},
		OnConfirmed: &integrity.ConfirmAction{
			AutoConfirm: &autoConfirm,
		},
	}); err != nil {
		return fmt.Errorf("set PR event settings: %w", err)
	}
	fmt.Printf("setup: PR event settings configured (agent=%s, auto_confirm=true)\n", agentName)

	oauthSt, err := oauthstore.Open(baseDir + "/oauth.db")
	if err != nil {
		return fmt.Errorf("open oauth store: %w", err)
	}
	defer oauthSt.Close()
	fmt.Println("setup: oauth store initialized")

	return nil
}

func runCheck(baseDir, ns, proj string) error {
	prStore, err := pr.Open(fmt.Sprintf("%s/%s/%s/prs.db", baseDir, ns, proj))
	if err != nil {
		return fmt.Errorf("open PR store: %w", err)
	}
	defer prStore.Close()

	prs, err := prStore.List("")
	if err != nil {
		return fmt.Errorf("list PRs: %w", err)
	}

	if len(prs) == 0 {
		return fmt.Errorf("no PRs found")
	}

	for _, p := range prs {
		fmt.Printf("check: PR #%d state=%s title=%q source=%s target=%s\n",
			p.Number, p.State, p.Title, p.SourceBranch, p.TargetBranch)
		if p.MergeCommit != "" {
			fmt.Printf("check: PR #%d merge_commit=%s\n", p.Number, p.MergeCommit)
		}
	}

	thePR := prs[0]
	if thePR.State != pr.StateMerged {
		return fmt.Errorf("PR #%d state=%q, want merged", thePR.Number, thePR.State)
	}
	if thePR.MergeCommit == "" {
		return fmt.Errorf("PR #%d merge_commit is empty", thePR.Number)
	}

	fmt.Printf("check: PASS — PR #%d merged (commit=%s)\n", thePR.Number, thePR.MergeCommit[:8])
	return nil
}
