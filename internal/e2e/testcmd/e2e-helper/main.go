//go:build e2e

// Command e2e-helper performs setup and verification for E2E tests.
//
// Usage:
//
//	e2e-helper --setup --base-dir=<dir> --ns=<ns> --proj=<proj> --agent-name=<name>
//	e2e-helper --setup --base-dir=<dir> --ns=<ns> --proj=<proj> --agent-name=<name> --auto-confirm=false --merger-agent=<name>
//	e2e-helper --setup-seed --base-dir=<dir> --ns=<ns> --proj=<proj> --seed-url=<url> --vault-password=<pw> --seed-merger-agent=<name>
//	e2e-helper --check --base-dir=<dir> --ns=<ns> --proj=<proj>
//	e2e-helper --check --base-dir=<dir> --ns=<ns> --proj=<proj> --expect-state=interrupted
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/gitcote/internal/pr"
	"github.com/sopranoworks/gitcote/internal/vault"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
)

func main() {
	setup := flag.Bool("setup", false, "run setup: create repo + set agent settings")
	setupSeed := flag.Bool("setup-seed", false, "run seed setup: vault + SSH key + seed config + seed event settings")
	check := flag.Bool("check", false, "run check: verify PR state")
	baseDir := flag.String("base-dir", "", "storage base directory")
	ns := flag.String("ns", "e2e", "namespace")
	proj := flag.String("proj", "fullflow", "project name")
	agentName := flag.String("agent-name", "mock_reviewer", "agent config name for reviewer")
	autoConfirm := flag.Bool("auto-confirm", true, "enable auto-confirm on approval")
	mergerAgent := flag.String("merger-agent", "", "agent config name for merge conflict resolution")
	expectState := flag.String("expect-state", "merged", "expected PR state for --check")
	seedURL := flag.String("seed-url", "", "seed repository URL (for --setup-seed)")
	vaultPassword := flag.String("vault-password", "e2e-test-password", "vault password (for --setup-seed)")
	seedMergerAgent := flag.String("seed-merger-agent", "", "agent for on_pull_conflict (for --setup-seed)")
	flag.Parse()

	if *baseDir == "" {
		fmt.Fprintln(os.Stderr, "e2e-helper: --base-dir is required")
		os.Exit(1)
	}

	if *setup {
		if err := runSetup(*baseDir, *ns, *proj, *agentName, *autoConfirm, *mergerAgent); err != nil {
			fmt.Fprintf(os.Stderr, "e2e-helper setup: %v\n", err)
			os.Exit(1)
		}
	} else if *setupSeed {
		if err := runSetupSeed(*baseDir, *ns, *proj, *seedURL, *vaultPassword, *seedMergerAgent); err != nil {
			fmt.Fprintf(os.Stderr, "e2e-helper setup-seed: %v\n", err)
			os.Exit(1)
		}
	} else if *check {
		if err := runCheck(*baseDir, *ns, *proj, *expectState); err != nil {
			fmt.Fprintf(os.Stderr, "e2e-helper check: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Fprintln(os.Stderr, "e2e-helper: specify --setup, --setup-seed, or --check")
		os.Exit(1)
	}
}

func runSetup(baseDir, ns, proj, agentName string, autoConfirm bool, mergerAgent string) error {
	gitStore := git.NewStore(baseDir)
	if err := gitStore.CreateRepo(ns, proj); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("create repo: %w", err)
		}
		fmt.Printf("setup: repo %s/%s already exists\n", ns, proj)
	} else {
		fmt.Printf("setup: created repo %s/%s\n", ns, proj)
	}

	intStore, err := integrity.Open(baseDir + "/repo_heads.db")
	if err != nil {
		return fmt.Errorf("open integrity store: %w", err)
	}
	defer intStore.Close()

	agentEnabled := true
	settings := &integrity.PREventSettings{
		OnCreated: &integrity.EventAction{
			AgentEnabled: &agentEnabled,
			AgentName:    agentName,
		},
		OnConfirmed: &integrity.ConfirmAction{
			AutoConfirm: &autoConfirm,
		},
	}

	if mergerAgent != "" {
		settings.OnMergeConflict = &integrity.EventAction{
			AgentEnabled: &agentEnabled,
			AgentName:    mergerAgent,
		}
	}

	if err := intStore.SetGlobalPREventSettings(settings); err != nil {
		return fmt.Errorf("set PR event settings: %w", err)
	}
	fmt.Printf("setup: PR event settings configured (reviewer=%s, auto_confirm=%v, merger=%s)\n", agentName, autoConfirm, mergerAgent)

	oauthSt, err := oauthstore.Open(baseDir + "/oauth.db")
	if err != nil {
		return fmt.Errorf("open oauth store: %w", err)
	}
	defer oauthSt.Close()
	fmt.Println("setup: oauth store initialized")

	return nil
}

func runSetupSeed(baseDir, ns, proj, seedURL, vaultPassword, seedMergerAgent string) error {
	gitStore := git.NewStore(baseDir)
	if err := gitStore.CreateRepo(ns, proj); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("create repo: %w", err)
		}
		fmt.Printf("setup-seed: repo %s/%s already exists\n", ns, proj)
	} else {
		fmt.Printf("setup-seed: created repo %s/%s\n", ns, proj)
	}

	v, err := vault.Open(filepath.Join(baseDir, "keys.db"))
	if err != nil {
		return fmt.Errorf("open vault: %w", err)
	}
	defer v.Close()

	if err := v.Unlock(vaultPassword); err != nil {
		return fmt.Errorf("unlock vault: %w", err)
	}
	fmt.Println("setup-seed: vault unlocked")

	keyName := "e2e-seed-key"
	pubKey, err := v.GenerateKey(ns, keyName, "e2e-helper")
	if err != nil {
		fmt.Printf("setup-seed: key %q may already exist, continuing\n", keyName)
	} else {
		fmt.Printf("setup-seed: generated SSH key %q (pub: %s...)\n", keyName, pubKey[:40])
	}

	projPath, err := gitStore.ProjectPath(ns, proj)
	if err != nil {
		return fmt.Errorf("project path: %w", err)
	}

	cfg := &git.SeedConfig{
		SeedURL:  seedURL,
		KeyName:  keyName,
		PushMode: "disabled",
	}
	if err := git.SaveSeedConfig(projPath, cfg); err != nil {
		return fmt.Errorf("save seed config: %w", err)
	}
	fmt.Printf("setup-seed: seed config saved (url=%s, key=%s)\n", seedURL, keyName)

	intStore, err := integrity.Open(filepath.Join(baseDir, "repo_heads.db"))
	if err != nil {
		return fmt.Errorf("open integrity store: %w", err)
	}
	defer intStore.Close()

	if seedMergerAgent != "" {
		agentEnabled := true
		seedSettings := &integrity.SeedEventSettings{
			OnPullConflict: &integrity.EventAction{
				AgentEnabled: &agentEnabled,
				AgentName:    seedMergerAgent,
			},
			OnPushConflict: &integrity.EventAction{
				AgentEnabled: &agentEnabled,
				AgentName:    seedMergerAgent,
			},
		}
		if err := intStore.SetGlobalSeedEventSettings(seedSettings); err != nil {
			return fmt.Errorf("set seed event settings: %w", err)
		}
		fmt.Printf("setup-seed: seed event settings configured (merger=%s)\n", seedMergerAgent)
	}

	oauthSt, err := oauthstore.Open(filepath.Join(baseDir, "oauth.db"))
	if err != nil {
		return fmt.Errorf("open oauth store: %w", err)
	}
	defer oauthSt.Close()
	fmt.Println("setup-seed: oauth store initialized")

	return nil
}

func runCheck(baseDir, ns, proj, expectState string) error {
	prStore, err := pr.Open(fmt.Sprintf("%s/%s/%s.prs.db", baseDir, ns, proj))
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
		if p.InterruptInfo != nil {
			info, _ := json.Marshal(p.InterruptInfo)
			fmt.Printf("check: PR #%d interrupt_info=%s\n", p.Number, string(info))
		}
	}

	thePR := prs[0]
	expected := pr.PRState(expectState)
	if thePR.State != expected {
		return fmt.Errorf("PR #%d state=%q, want %s", thePR.Number, thePR.State, expectState)
	}

	switch expected {
	case pr.StateMerged:
		if thePR.MergeCommit == "" {
			return fmt.Errorf("PR #%d merge_commit is empty", thePR.Number)
		}
		fmt.Printf("check: PASS — PR #%d merged (commit=%s)\n", thePR.Number, thePR.MergeCommit[:8])
	case pr.StateInterrupted:
		if thePR.InterruptInfo == nil {
			return fmt.Errorf("PR #%d interrupt_info is nil", thePR.Number)
		}
		fmt.Printf("check: PASS — PR #%d interrupted (reason=%s, agent=%s)\n",
			thePR.Number, thePR.InterruptInfo.Reason, thePR.InterruptInfo.AgentName)
	default:
		fmt.Printf("check: PASS — PR #%d state=%s\n", thePR.Number, thePR.State)
	}
	return nil
}
