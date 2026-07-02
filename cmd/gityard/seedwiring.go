package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/gityard/internal/integrity"
	"github.com/sopranoworks/gityard/internal/vault"
	"github.com/sopranoworks/shoka/pkg/authz"
	"github.com/sopranoworks/shoka/pkg/uiws"
	"github.com/sopranoworks/shoka/pkg/userstore"
)

// Seed-related WebSocket message types.
const (
	MsgSeedConfigGet uiws.MessageType = "SEED_CONFIG_GET"
	MsgSeedConfigSet uiws.MessageType = "SEED_CONFIG_SET"
	MsgSeedKeyGen    uiws.MessageType = "SEED_KEY_GENERATE"
	MsgSeedKeyImport uiws.MessageType = "SEED_KEY_IMPORT"
	MsgSeedKeyList   uiws.MessageType = "SEED_KEY_LIST"
	MsgSeedKeyDelete uiws.MessageType = "SEED_KEY_DELETE"
	MsgSeedTest      uiws.MessageType = "SEED_TEST"
	MsgSeedPush      uiws.MessageType = "SEED_PUSH"
	MsgSeedPull      uiws.MessageType = "SEED_PULL"
	MsgSeedResume    uiws.MessageType = "SEED_RESUME"
	MsgSeedStatus    uiws.MessageType = "SEED_STATUS"
)

// SeedLevels maps seed message types to their authorization requirements.
var SeedLevels = map[uiws.MessageType]uiws.Op{
	MsgSeedConfigGet: {Level: authz.LevelRead, Global: false},
	MsgSeedConfigSet: {Level: authz.LevelAdmin, Global: false},
	MsgSeedKeyGen:    {Level: authz.LevelAdmin, Global: false},
	MsgSeedKeyImport: {Level: authz.LevelAdmin, Global: false},
	MsgSeedKeyList:   {Level: authz.LevelRead, Global: false},
	MsgSeedKeyDelete: {Level: authz.LevelAdmin, Global: false},
	MsgSeedTest:      {Level: authz.LevelAdmin, Global: false},
	MsgSeedPush:      {Level: authz.LevelWrite, Global: false},
	MsgSeedPull:      {Level: authz.LevelWrite, Global: false},
	MsgSeedResume:    {Level: authz.LevelAdmin, Global: true},
	MsgSeedStatus:    {Level: authz.LevelRead, Global: false},
}

type seedContext struct {
	gitStore   *git.Store
	vault      *vault.Vault
	userStore  *userstore.Store
	resumed    bool
	gityardURL string
}

func buildGityardCloneURL(sc *seedContext, namespace, project string) string {
	if sc.gityardURL == "" {
		return ""
	}
	return sc.gityardURL + "/" + namespace + "/" + project + ".git"
}

// seedDispatch handles SEED_* WebSocket messages. Returns true if the message was handled.
func seedDispatch(c *uiws.Client, sc *seedContext, ec *eventContext, msgType uiws.MessageType, payload json.RawMessage) bool {
	switch msgType {
	case MsgSeedConfigGet:
		handleSeedConfigGet(c, sc.gitStore, payload)
	case MsgSeedConfigSet:
		handleSeedConfigSet(c, sc.gitStore, payload)
	case MsgSeedKeyGen:
		handleSeedKeyGenerate(c, sc, payload)
	case MsgSeedKeyImport:
		handleSeedKeyImport(c, sc, payload)
	case MsgSeedKeyList:
		handleSeedKeyList(c, sc.vault, payload)
	case MsgSeedKeyDelete:
		handleSeedKeyDelete(c, sc, payload)
	case MsgSeedTest:
		handleSeedTest(c, sc, payload)
	case MsgSeedPush:
		handleSeedPushWS(c, sc, payload)
	case MsgSeedPull:
		handleSeedPullWS(c, sc, ec, payload)
	case MsgSeedResume:
		handleSeedResume(c, sc, payload)
	case MsgSeedStatus:
		handleSeedStatusWS(c, sc.gitStore, payload)
	default:
		return false
	}
	return true
}

type seedTargetPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
}

type seedConfigSetPayload struct {
	Namespace    string `json:"namespace"`
	ProjectName  string `json:"projectName"`
	SeedURL      string `json:"seedUrl"`
	KeyName      string `json:"keyName"`
	PushMode     string `json:"pushMode"`
	PushInterval string `json:"pushInterval,omitempty"`
}

type seedKeyGenPayload struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type seedKeyDeletePayload struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type seedResumePayload struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type seedPushPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
	Branch      string `json:"branch,omitempty"`
}

func handleSeedConfigGet(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p seedTargetPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	projPath, err := gitStore.ProjectPath(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgSeedConfigGet, map[string]interface{}{
		"seedUrl":      cfg.SeedURL,
		"keyName":      cfg.KeyName,
		"pushMode":     cfg.PushMode,
		"pushInterval": cfg.PushInterval,
		"syncStatus":   cfg.SyncStatus,
	})
}

func handleSeedConfigSet(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p seedConfigSetPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	projPath, err := gitStore.ProjectPath(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil {
		cfg = &git.SeedConfig{}
	}
	cfg.SeedURL = p.SeedURL
	cfg.KeyName = p.KeyName
	cfg.PushMode = p.PushMode
	cfg.PushInterval = p.PushInterval
	if err := git.SaveSeedConfig(projPath, cfg); err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgSeedConfigSet, map[string]string{"status": "ok"})
}

func handleSeedKeyGenerate(c *uiws.Client, sc *seedContext, payload json.RawMessage) {
	var p seedKeyGenPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	principal := c.Principal()
	createdBy := principal.Email
	if createdBy == "" {
		createdBy = principal.Name
	}
	pubKey, err := sc.vault.GenerateKey(p.Namespace, p.Name, createdBy)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgSeedKeyGen, map[string]string{"publicKey": pubKey})
}

type seedKeyImportPayload struct {
	Namespace     string `json:"namespace"`
	Name          string `json:"name"`
	PrivateKeyPEM string `json:"privateKeyPem"`
}

func handleSeedKeyImport(c *uiws.Client, sc *seedContext, payload json.RawMessage) {
	var p seedKeyImportPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	if p.PrivateKeyPEM == "" {
		c.SendError("privateKeyPem is required")
		return
	}
	principal := c.Principal()
	createdBy := principal.Email
	if createdBy == "" {
		createdBy = principal.Name
	}
	pubKey, fingerprint, err := sc.vault.ImportKey(p.Namespace, p.Name, createdBy, []byte(p.PrivateKeyPEM))
	if err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgSeedKeyImport, map[string]string{
		"publicKey":   pubKey,
		"fingerprint": fingerprint,
	})
}

func handleSeedKeyList(c *uiws.Client, v *vault.Vault, payload json.RawMessage) {
	var p struct {
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	keys, err := v.ListKeys(p.Namespace)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgSeedKeyList, map[string]interface{}{"keys": keys})
}

func handleSeedKeyDelete(c *uiws.Client, sc *seedContext, payload json.RawMessage) {
	var p seedKeyDeletePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	if err := sc.vault.DeleteKey(p.Namespace, p.Name); err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgSeedKeyDelete, map[string]string{"status": "ok"})
}

func handleSeedTest(c *uiws.Client, sc *seedContext, payload json.RawMessage) {
	var p seedTargetPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	projPath, err := sc.gitStore.ProjectPath(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil {
		c.SendResponse(MsgSeedTest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	if cfg.SeedURL == "" {
		c.SendResponse(MsgSeedTest, map[string]interface{}{"success": false, "error": "no seed URL configured"})
		return
	}
	if cfg.KeyName == "" {
		c.SendResponse(MsgSeedTest, map[string]interface{}{"success": false, "error": "no key configured"})
		return
	}
	pemData, err := sc.vault.DecryptPrivateKey(p.Namespace, cfg.KeyName)
	if err != nil {
		c.SendResponse(MsgSeedTest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	if err := git.TestSeedConnection(cfg.SeedURL, pemData); err != nil {
		c.SendResponse(MsgSeedTest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	c.SendResponse(MsgSeedTest, map[string]interface{}{"success": true})
}

func handleSeedPushWS(c *uiws.Client, sc *seedContext, payload json.RawMessage) {
	var p seedPushPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	err := executeSeedPush(sc, p.Namespace, p.ProjectName, p.Branch)
	if err != nil {
		c.SendResponse(MsgSeedPush, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	c.SendResponse(MsgSeedPush, map[string]interface{}{"success": true})
}

func handleSeedPullWS(c *uiws.Client, sc *seedContext, ec *eventContext, payload json.RawMessage) {
	var p seedTargetPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	result := executeSeedPull(sc, ec, p.Namespace, p.ProjectName, "")
	c.SendResponse(MsgSeedPull, result)
}

func executeSeedPull(sc *seedContext, ec *eventContext, namespace, project, branch string) map[string]interface{} {
	if sc.vault.State() != vault.VaultUnlocked {
		return map[string]interface{}{"success": false, "error": "vault is locked — resume required"}
	}
	if branch == "" {
		branch = "main"
	}
	projPath, err := sc.gitStore.ProjectPath(namespace, project)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	if cfg.SeedURL == "" {
		return map[string]interface{}{"success": false, "error": "no seed URL configured"}
	}
	if cfg.KeyName == "" {
		return map[string]interface{}{"success": false, "error": "no key configured"}
	}
	pemData, err := sc.vault.DecryptPrivateKey(namespace, cfg.KeyName)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	repo, err := sc.gitStore.OpenRepo(namespace, project)
	if err != nil {
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("open repo: %v", err)}
	}

	seedHash, err := git.FetchSeedRef(repo, cfg.SeedURL, branch, pemData)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	localHash, err := git.ResolveBranch(repo, branch)
	if err != nil {
		if err2 := git.SetBranchRef(repo, branch, seedHash); err2 != nil {
			return map[string]interface{}{"success": false, "error": fmt.Sprintf("create branch: %v", err2)}
		}
		recordHeadHash(sc.gitStore, namespace, project)
		return map[string]interface{}{"success": true, "status": "fast-forward", "message": "initial pull from seed"}
	}

	mr, err := git.SeedMerge(repo, localHash, seedHash)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}

	switch mr.Status {
	case "up-to-date":
		return map[string]interface{}{"success": true, "status": "up-to-date", "message": "already up to date"}
	case "fast-forward", "auto-merged":
		if err := git.SetBranchRef(repo, branch, mr.MergedHash); err != nil {
			return map[string]interface{}{"success": false, "error": fmt.Sprintf("update ref: %v", err)}
		}
		recordHeadHash(sc.gitStore, namespace, project)
		return map[string]interface{}{"success": true, "status": mr.Status, "message": mr.Status + " completed"}
	case "conflict":
		var conflicts []conflictInfoWire
		var conflictPaths []string
		for _, c := range mr.Conflicts {
			conflicts = append(conflicts, conflictInfoWire{Path: c.Path, Type: c.Type})
			conflictPaths = append(conflictPaths, c.Path)
		}
		resp := map[string]interface{}{
			"success":   false,
			"status":    "conflict",
			"conflicts": conflicts,
		}
		gityardURL := buildGityardCloneURL(sc, namespace, project)
		tempDir, terr := git.CreateSeedTempClone(cfg.SeedURL, pemData, gityardURL)
		if terr == nil {
			resp["temp_clone"] = tempDir
			resp["instructions"] = "Resolve conflicts in the temp clone, then: git push gityard HEAD:main"
			if headStore != nil {
				_ = headStore.AddTempClone(integrity.TempCloneRecord{
					Namespace: namespace, Project: project,
					Path: tempDir, CreatedAt: time.Now().UTC().Format(time.RFC3339),
				})
			}
			if ec != nil {
				go onSeedPullConflict(ec, namespace, project, tempDir, conflictPaths)
			}
		}
		return resp
	}
	return map[string]interface{}{"success": false, "error": "unexpected merge status"}
}

func handleSeedResume(c *uiws.Client, sc *seedContext, payload json.RawMessage) {
	var p seedResumePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	if p.Email == "" || p.Password == "" {
		c.SendError("email and password are required")
		return
	}
	u, err := sc.userStore.GetUser(p.Email)
	if err != nil {
		c.SendError("invalid credentials")
		return
	}
	ok, err := userstore.VerifyPassword(p.Password, u.PasswordHash)
	if err != nil || !ok {
		c.SendError("invalid credentials")
		return
	}
	if !u.IsAdmin() {
		c.SendError("super-user credentials required")
		return
	}
	if err := sc.vault.Unlock(p.Password); err != nil {
		c.SendError(fmt.Sprintf("vault unlock failed: %v", err))
		return
	}
	sc.resumed = true
	c.SendResponse(MsgSeedResume, map[string]string{"status": "ok", "vault": "unlocked"})
}

func handleSeedStatusWS(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p seedTargetPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	projPath, err := gitStore.ProjectPath(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgSeedStatus, map[string]interface{}{
		"seedUrl":    cfg.SeedURL,
		"keyName":    cfg.KeyName,
		"pushMode":   cfg.PushMode,
		"syncStatus": cfg.SyncStatus,
	})
}

// SeedPushResult holds the outcome of a push attempt with merge support.
type SeedPushResult struct {
	Success      bool
	Status       string // "pushed", "auto-merged", "conflict"
	Message      string
	Conflicts    []conflictInfoWire
	TempCloneDir string
}

func executeSeedPush(sc *seedContext, namespace, projectName, branch string) error {
	r := executeSeedPushWithMerge(sc, namespace, projectName, branch)
	if !r.Success {
		return fmt.Errorf("%s", r.Message)
	}
	return nil
}

func executeSeedPushWithMerge(sc *seedContext, namespace, projectName, branch string) SeedPushResult {
	if sc.vault.State() != vault.VaultUnlocked {
		return SeedPushResult{Message: "vault is locked — resume required"}
	}
	if branch == "" {
		branch = "main"
	}
	projPath, err := sc.gitStore.ProjectPath(namespace, projectName)
	if err != nil {
		return SeedPushResult{Message: err.Error()}
	}
	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil {
		return SeedPushResult{Message: err.Error()}
	}
	if cfg.SeedURL == "" {
		return SeedPushResult{Message: "no seed URL configured"}
	}
	if cfg.KeyName == "" {
		return SeedPushResult{Message: "no key configured"}
	}
	pemData, err := sc.vault.DecryptPrivateKey(namespace, cfg.KeyName)
	if err != nil {
		return SeedPushResult{Message: fmt.Sprintf("decrypt key: %v", err)}
	}
	repo, err := sc.gitStore.OpenRepo(namespace, projectName)
	if err != nil {
		return SeedPushResult{Message: fmt.Sprintf("open repo: %v", err)}
	}

	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		pushErr := git.PushToSeed(repo, cfg.SeedURL, branch, pemData)
		if pushErr == nil {
			now := time.Now()
			_ = git.UpdateSeedStatus(projPath, &git.SeedSyncStatus{
				State: git.SeedStateActive, LastPushAt: &now, LastResult: "ok",
			})
			status := "pushed"
			if attempt > 0 {
				status = "auto-merged"
			}
			return SeedPushResult{Success: true, Status: status, Message: status + " successfully"}
		}

		// Push failed — try fetch + merge + retry.
		seedHash, fetchErr := git.FetchSeedRef(repo, cfg.SeedURL, branch, pemData)
		if fetchErr != nil {
			now := time.Now()
			_ = git.UpdateSeedStatus(projPath, &git.SeedSyncStatus{
				State: git.SeedStateError, LastPushAt: &now, LastResult: pushErr.Error(),
			})
			return SeedPushResult{Message: pushErr.Error()}
		}

		localHash, _ := git.ResolveBranch(repo, branch)
		mr, mergeErr := git.SeedMerge(repo, localHash, seedHash)
		if mergeErr != nil {
			return SeedPushResult{Message: fmt.Sprintf("merge: %v", mergeErr)}
		}

		switch mr.Status {
		case "up-to-date":
			return SeedPushResult{Success: true, Status: "up-to-date", Message: "already up to date"}
		case "conflict":
			var conflicts []conflictInfoWire
			for _, c := range mr.Conflicts {
				conflicts = append(conflicts, conflictInfoWire{Path: c.Path, Type: c.Type})
			}
			result := SeedPushResult{Status: "conflict", Message: "push conflicts", Conflicts: conflicts}
			gityardURL := buildGityardCloneURL(sc, namespace, projectName)
			tempDir, terr := git.CreateSeedTempClone(cfg.SeedURL, pemData, gityardURL)
			if terr == nil {
				result.TempCloneDir = tempDir
				if headStore != nil {
					_ = headStore.AddTempClone(integrity.TempCloneRecord{
						Namespace: namespace, Project: projectName,
						Path: tempDir, CreatedAt: time.Now().UTC().Format(time.RFC3339),
					})
				}
			}
			now := time.Now()
			_ = git.UpdateSeedStatus(projPath, &git.SeedSyncStatus{
				State: git.SeedStateError, LastPushAt: &now, LastResult: "conflict",
			})
			return result
		case "fast-forward", "auto-merged":
			_ = git.SetBranchRef(repo, branch, mr.MergedHash)
			recordHeadHash(sc.gitStore, namespace, projectName)
			// Retry push with merged commit.
		}
	}

	return SeedPushResult{Message: "push failed after retries"}
}

// registerSeedTools registers seed-related MCP tools.
func registerSeedTools(mcpServer *mcp.Server, gitStore *git.Store, v *vault.Vault, gityardURL string, ec *eventContext) {
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "push_to_seed",
		Description: "Push a branch to the configured seed repository via SSH. Auto-merges if seed has diverged cleanly; reports conflicts otherwise.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in pushToSeedInput) (*mcp.CallToolResult, pushToSeedOutput, error) {
		sc := &seedContext{gitStore: gitStore, vault: v, gityardURL: gityardURL}
		r := executeSeedPushWithMerge(sc, in.Namespace, in.ProjectName, in.Branch)
		return nil, pushToSeedOutput{
			Success:      r.Success,
			Message:      r.Message,
			Status:       r.Status,
			Conflicts:    r.Conflicts,
			TempClone:    r.TempCloneDir,
			Instructions: tempCloneInstructions(r),
		}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "pull_from_seed",
		Description: "Pull from the configured seed repository via SSH. Auto-merges if branches have diverged cleanly; reports conflicts otherwise.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in pullFromSeedInput) (*mcp.CallToolResult, pullFromSeedOutput, error) {
		sc := &seedContext{gitStore: gitStore, vault: v, gityardURL: gityardURL}
		r := executeSeedPull(sc, ec, in.Namespace, in.ProjectName, in.Branch)
		success, _ := r["success"].(bool)
		msg, _ := r["message"].(string)
		if msg == "" {
			if errStr, ok := r["error"].(string); ok {
				msg = errStr
			}
		}
		out := pullFromSeedOutput{Success: success, Message: msg}
		if status, ok := r["status"].(string); ok {
			out.Status = status
		}
		if conflicts, ok := r["conflicts"].([]conflictInfoWire); ok {
			out.Conflicts = conflicts
		}
		if tc, ok := r["temp_clone"].(string); ok {
			out.TempClone = tc
		}
		if inst, ok := r["instructions"].(string); ok {
			out.Instructions = inst
		}
		return nil, out, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "get_seed_status",
		Description: "Get seed sync status for a project.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in getSeedStatusInput) (*mcp.CallToolResult, getSeedStatusOutput, error) {
		projPath, err := gitStore.ProjectPath(in.Namespace, in.ProjectName)
		if err != nil {
			return nil, getSeedStatusOutput{}, err
		}
		cfg, err := git.LoadSeedConfig(projPath)
		if err != nil {
			return nil, getSeedStatusOutput{}, err
		}
		var status *git.SeedSyncStatus
		if cfg.SyncStatus != nil {
			status = cfg.SyncStatus
		}
		vaultState := "locked"
		if v.State() == vault.VaultUnlocked {
			vaultState = "unlocked"
		}
		return nil, getSeedStatusOutput{
			SeedURL:    cfg.SeedURL,
			KeyName:    cfg.KeyName,
			PushMode:   cfg.PushMode,
			VaultState: vaultState,
			SyncStatus: status,
		}, nil
	})
}

type pushToSeedInput struct {
	Namespace   string `json:"namespace" jsonschema:"required,the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Branch      string `json:"branch,omitempty" jsonschema:"branch to push (default: main)"`
}

type pushToSeedOutput struct {
	Success      bool               `json:"success"`
	Message      string             `json:"message"`
	Status       string             `json:"status,omitempty"`
	Conflicts    []conflictInfoWire `json:"conflicts,omitempty"`
	TempClone    string             `json:"temp_clone,omitempty"`
	Instructions string             `json:"instructions,omitempty"`
}

type pullFromSeedInput struct {
	Namespace   string `json:"namespace" jsonschema:"required,the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Branch      string `json:"branch,omitempty" jsonschema:"branch to pull (default: main)"`
}

type pullFromSeedOutput struct {
	Success      bool               `json:"success"`
	Message      string             `json:"message"`
	Status       string             `json:"status,omitempty"`
	Conflicts    []conflictInfoWire `json:"conflicts,omitempty"`
	TempClone    string             `json:"temp_clone,omitempty"`
	Instructions string             `json:"instructions,omitempty"`
}

func tempCloneInstructions(r SeedPushResult) string {
	if r.TempCloneDir != "" {
		return "Resolve conflicts in the temp clone, then: git push gityard HEAD:main"
	}
	return ""
}

type getSeedStatusInput struct {
	Namespace   string `json:"namespace" jsonschema:"required,the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
}

type getSeedStatusOutput struct {
	SeedURL    string              `json:"seed_url"`
	KeyName    string              `json:"key_name"`
	PushMode   string              `json:"push_mode"`
	VaultState string              `json:"vault_state"`
	SyncStatus *git.SeedSyncStatus `json:"sync_status,omitempty"`
}

// startSeedScheduler starts a background goroutine that periodically pushes
// projects configured with push_mode=periodic to their seed repositories.
func startSeedScheduler(ctx context.Context, sc *seedContext, logger *slog.Logger) {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runPeriodicPush(sc, logger)
			}
		}
	}()
}

func runPeriodicPush(sc *seedContext, logger *slog.Logger) {
	if sc.vault.State() != vault.VaultUnlocked || !sc.resumed {
		return
	}

	projects, err := sc.gitStore.ListProjects("")
	if err != nil {
		logger.Warn("seed scheduler: list projects", "error", err)
		return
	}

	for _, p := range projects {
		projPath, err := sc.gitStore.ProjectPath(p.Namespace, p.Project)
		if err != nil {
			continue
		}
		cfg, err := git.LoadSeedConfig(projPath)
		if err != nil || cfg.PushMode != git.PushModePeriodic {
			continue
		}
		if cfg.SeedURL == "" || cfg.KeyName == "" {
			continue
		}

		if cfg.PushInterval != "" {
			dur, perr := time.ParseDuration(cfg.PushInterval)
			if perr == nil && dur > 5*time.Minute {
				markerPath := filepath.Join(projPath, ".seed_last_push")
				if info, serr := os.Stat(markerPath); serr == nil {
					if time.Since(info.ModTime()) < dur {
						continue
					}
				}
			}
		}

		if err := executeSeedPush(sc, p.Namespace, p.Project, ""); err != nil {
			logger.Warn("seed scheduler: push failed",
				"namespace", p.Namespace, "project", p.Project, "error", err)
			continue
		}
		markerPath := filepath.Join(projPath, ".seed_last_push")
		_ = os.WriteFile(markerPath, []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
		logger.Info("seed scheduler: push succeeded",
			"namespace", p.Namespace, "project", p.Project)
	}
}

// triggerOnMergePush checks the project's seed config and pushes if on-merge mode is active.
func triggerOnMergePush(sc *seedContext, namespace, project, branch string) {
	if sc.vault.State() != vault.VaultUnlocked || !sc.resumed {
		return
	}
	projPath, err := sc.gitStore.ProjectPath(namespace, project)
	if err != nil {
		return
	}
	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil || cfg.PushMode != git.PushModeOnMerge {
		return
	}
	if cfg.SeedURL == "" || cfg.KeyName == "" {
		return
	}
	if err := executeSeedPush(sc, namespace, project, branch); err != nil {
		slog.Default().Warn("on-merge push: push failed",
			"namespace", namespace, "project", project, "branch", branch, "error", err)
		return
	}
	slog.Default().Info("on-merge push: succeeded",
		"namespace", namespace, "project", project, "branch", branch)
}

// startTempCloneCleanup periodically removes temp clones older than 24 hours.
func startTempCloneCleanup(ctx context.Context, hs *integrity.Store, logger *slog.Logger) {
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanupTempClones(hs, logger)
			}
		}
	}()
}

func cleanupTempClones(hs *integrity.Store, logger *slog.Logger) {
	if hs == nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)

	recs, err := hs.ListTempClones()
	if err == nil {
		for _, rec := range recs {
			created, perr := time.Parse(time.RFC3339, rec.CreatedAt)
			if perr != nil || created.Before(cutoff) {
				if err := os.RemoveAll(rec.Path); err == nil || os.IsNotExist(err) {
					_ = hs.RemoveTempClone(rec.Path)
					logger.Info("cleaned up temp clone", "path", rec.Path, "namespace", rec.Namespace, "project", rec.Project)
				}
			}
		}
	}

	workdirs, err := hs.ListAgentWorkdirs()
	if err == nil {
		for _, rec := range workdirs {
			if rec.Status == "running" {
				continue
			}
			created, perr := time.Parse(time.RFC3339, rec.CreatedAt)
			if perr != nil || created.Before(cutoff) {
				if err := os.RemoveAll(rec.Path); err == nil || os.IsNotExist(err) {
					_ = hs.RemoveAgentWorkdir(rec.Path)
					logger.Info("cleaned up agent workdir", "path", rec.Path, "agent", rec.AgentName, "status", rec.Status)
				}
			}
		}
	}
}
