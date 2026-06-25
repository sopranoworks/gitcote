package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/shoka/pkg/uiws"
)

const (
	MsgNamespaceHealth  uiws.MessageType = "NAMESPACE_HEALTH"
	MsgCreateNamespace  uiws.MessageType = "CREATE_NAMESPACE"
	MsgDeleteNamespace  uiws.MessageType = "DELETE_NAMESPACE"
	MsgCreateProject    uiws.MessageType = "CREATE_PROJECT"
	MsgDeleteProject    uiws.MessageType = "DELETE_PROJECT"
	MsgRenameProject    uiws.MessageType = "RENAME_PROJECT"
	MsgRenameNamespace  uiws.MessageType = "RENAME_NAMESPACE"
	MsgMoveProject      uiws.MessageType = "MOVE_PROJECT"
	MsgNamespaceRecover uiws.MessageType = "NAMESPACE_RECOVER"
)

func nsDispatch(c *uiws.Client, gitStore *git.Store, msgType uiws.MessageType, payload json.RawMessage) bool {
	switch msgType {
	case MsgNamespaceHealth:
		handleNamespaceHealth(c, gitStore)
	case MsgCreateNamespace:
		handleCreateNamespace(c, gitStore, payload)
	case MsgDeleteNamespace:
		handleDeleteNamespace(c, gitStore, payload)
	case MsgCreateProject:
		handleCreateProject(c, gitStore, payload)
	case MsgDeleteProject:
		handleDeleteProject(c, gitStore, payload)
	case MsgRenameProject:
		handleRenameProject(c, gitStore, payload)
	case MsgRenameNamespace:
		handleRenameNamespace(c, gitStore, payload)
	case MsgMoveProject:
		handleMoveProject(c, gitStore, payload)
	case MsgNamespaceRecover:
		handleNamespaceRecover(c, payload)
	default:
		return false
	}
	return true
}

type projectHealth struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type namespaceHealth struct {
	Name     string          `json:"name"`
	Present  bool            `json:"present"`
	Healthy  bool            `json:"healthy"`
	Projects []projectHealth `json:"projects"`
}

type healthReport struct {
	Namespaces []namespaceHealth `json:"namespaces"`
}

func handleNamespaceHealth(c *uiws.Client, gitStore *git.Store) {
	entries, err := os.ReadDir(gitStore.BaseDir())
	if err != nil {
		c.SendResponse(MsgNamespaceHealth, healthReport{})
		return
	}

	var namespaces []namespaceHealth
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !git.IsValidName(e.Name()) {
			continue
		}
		ns := e.Name()
		projs, _ := gitStore.ListProjects(ns)
		var projects []projectHealth
		allHealthy := true
		for _, p := range projs {
			state := "healthy"
			headPath := filepath.Join(gitStore.BaseDir(), p.Namespace, p.Project, ".git", "HEAD")
			if _, serr := os.Stat(headPath); serr != nil {
				state = "corrupted"
				allHealthy = false
			} else if _, oerr := gitStore.OpenRepo(p.Namespace, p.Project); oerr != nil {
				state = "corrupted"
				allHealthy = false
			}
			projects = append(projects, projectHealth{Name: p.Project, State: state})
		}
		namespaces = append(namespaces, namespaceHealth{
			Name:     ns,
			Present:  true,
			Healthy:  allHealthy,
			Projects: projects,
		})
	}
	c.SendResponse(MsgNamespaceHealth, healthReport{Namespaces: namespaces})
}

func handleCreateNamespace(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p struct {
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	if !git.IsValidName(p.Namespace) {
		c.SendError("invalid namespace name")
		return
	}
	nsDir := filepath.Join(gitStore.BaseDir(), p.Namespace)
	if err := os.MkdirAll(nsDir, 0o755); err != nil {
		c.SendError(fmt.Sprintf("create namespace: %v", err))
		return
	}
	c.SendResponse(MsgCreateNamespace, map[string]string{"status": "ok"})
}

func handleDeleteNamespace(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p struct {
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	projs, _ := gitStore.ListProjects(p.Namespace)
	if len(projs) > 0 {
		c.SendError("namespace is not empty — delete its projects first")
		return
	}
	nsDir := filepath.Join(gitStore.BaseDir(), p.Namespace)
	if err := os.Remove(nsDir); err != nil {
		c.SendError(fmt.Sprintf("delete namespace: %v", err))
		return
	}
	c.SendResponse(MsgDeleteNamespace, map[string]string{"status": "ok"})
}

func handleCreateProject(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p struct {
		Namespace   string `json:"namespace"`
		ProjectName string `json:"projectName"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	if err := gitStore.CreateRepo(p.Namespace, p.ProjectName); err != nil {
		c.SendError(fmt.Sprintf("create project: %v", err))
		return
	}
	c.SendResponse(MsgCreateProject, map[string]string{"status": "ok"})
}

func handleDeleteProject(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p struct {
		Namespace   string `json:"namespace"`
		ProjectName string `json:"projectName"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	projPath, err := gitStore.ProjectPath(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	if err := os.RemoveAll(projPath); err != nil {
		c.SendError(fmt.Sprintf("delete project: %v", err))
		return
	}
	c.SendResponse(MsgDeleteProject, map[string]string{"status": "ok"})
}

func handleRenameProject(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p struct {
		Namespace      string `json:"namespace"`
		ProjectName    string `json:"projectName"`
		NewProjectName string `json:"newProjectName"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	if !git.IsValidName(p.NewProjectName) {
		c.SendError("invalid project name")
		return
	}
	oldPath, err := gitStore.ProjectPath(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	newPath := filepath.Join(gitStore.BaseDir(), p.Namespace, p.NewProjectName)
	if _, err := os.Stat(newPath); err == nil {
		c.SendError("target project already exists")
		return
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		c.SendError(fmt.Sprintf("rename project: %v", err))
		return
	}
	c.SendResponse(MsgRenameProject, map[string]string{"status": "ok"})
}

func handleRenameNamespace(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p struct {
		Namespace    string `json:"namespace"`
		NewNamespace string `json:"newNamespace"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	if !git.IsValidName(p.NewNamespace) {
		c.SendError("invalid namespace name")
		return
	}
	oldPath := filepath.Join(gitStore.BaseDir(), p.Namespace)
	newPath := filepath.Join(gitStore.BaseDir(), p.NewNamespace)
	if _, err := os.Stat(newPath); err == nil {
		c.SendError("target namespace already exists")
		return
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		c.SendError(fmt.Sprintf("rename namespace: %v", err))
		return
	}
	c.SendResponse(MsgRenameNamespace, map[string]string{"status": "ok"})
}

func handleMoveProject(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p struct {
		Namespace    string `json:"namespace"`
		ProjectName  string `json:"projectName"`
		NewNamespace string `json:"newNamespace"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	oldPath, err := gitStore.ProjectPath(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	newPath := filepath.Join(gitStore.BaseDir(), p.NewNamespace, p.ProjectName)
	if _, err := os.Stat(newPath); err == nil {
		c.SendError("target project already exists in destination namespace")
		return
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		c.SendError(fmt.Sprintf("move project: %v", err))
		return
	}
	c.SendResponse(MsgMoveProject, map[string]string{"status": "ok"})
}

func handleNamespaceRecover(c *uiws.Client, payload json.RawMessage) {
	c.SendError("recovery actions are not supported in GitYard")
}
