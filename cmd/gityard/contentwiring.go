package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/shoka/pkg/authz"
	"github.com/sopranoworks/shoka/pkg/uiws"
)

const (
	MsgGetProjects uiws.MessageType = "GET_PROJECTS"
	MsgGetTree     uiws.MessageType = "GET_TREE"
	MsgReadFile    uiws.MessageType = "READ_FILE"
	MsgGetHistory  uiws.MessageType = "GET_HISTORY"
	MsgGetFileAt   uiws.MessageType = "GET_FILE_AT"
	MsgGetDiff     uiws.MessageType = "GET_DIFF"
	MsgSearchFiles uiws.MessageType = "SEARCH_FILES"
)

var ContentLevels = map[uiws.MessageType]uiws.Op{
	MsgGetProjects: {Level: authz.LevelRead, Global: true},
	MsgGetTree:     {Level: authz.LevelRead, Global: false},
	MsgReadFile:    {Level: authz.LevelRead, Global: false},
	MsgGetHistory:  {Level: authz.LevelRead, Global: false},
	MsgGetFileAt:   {Level: authz.LevelRead, Global: false},
	MsgGetDiff:     {Level: authz.LevelRead, Global: false},
	MsgSearchFiles: {Level: authz.LevelRead, Global: false},
}

func contentDispatch(c *uiws.Client, gitStore *git.Store, msgType uiws.MessageType, payload json.RawMessage) bool {
	switch msgType {
	case MsgGetProjects:
		handleGetProjects(c, gitStore)
	case MsgGetTree:
		handleGetTree(c, gitStore, payload)
	case MsgReadFile:
		handleReadFile(c, gitStore, payload)
	case MsgGetHistory:
		handleGetHistory(c, gitStore, payload)
	case MsgGetFileAt:
		handleGetFileAt(c, gitStore, payload)
	case MsgGetDiff:
		handleGetDiff(c, gitStore, payload)
	case MsgSearchFiles:
		handleSearchFiles(c, gitStore, payload)
	default:
		return false
	}
	return true
}

type contentProjectPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
}

type contentFilePayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
	Path        string `json:"path"`
}

type contentFileAtPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
	Path        string `json:"path"`
	Hash        string `json:"hash"`
}

type contentDiffPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
	Path        string `json:"path"`
	FromHash    string `json:"fromHash"`
	ToHash      string `json:"toHash"`
}

type contentSearchPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
	Query       string `json:"query"`
}

// --- GET_PROJECTS ---

type projectInfoWS struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	State     string `json:"state"`
}

func handleGetProjects(c *uiws.Client, gitStore *git.Store) {
	projects, err := gitStore.ListProjects("")
	if err != nil {
		c.SendError(err.Error())
		return
	}
	result := make([]projectInfoWS, len(projects))
	for i, p := range projects {
		result[i] = projectInfoWS{Namespace: p.Namespace, Name: p.Project, State: "healthy"}
	}
	c.SendResponse(MsgGetProjects, result)
}

// --- GET_TREE ---

type fileNodeWS struct {
	Name     string       `json:"name"`
	Path     string       `json:"path"`
	IsDir    bool         `json:"isDir"`
	Children []fileNodeWS `json:"children,omitempty"`
}

func handleGetTree(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p contentProjectPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	repo, err := gitStore.OpenRepo(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	hash, err := git.ResolveRef(repo, "")
	if err != nil {
		c.SendResponse(MsgGetTree, []fileNodeWS{})
		return
	}
	commit, err := repo.CommitObject(hash)
	if err != nil {
		c.SendResponse(MsgGetTree, []fileNodeWS{})
		return
	}
	tree, err := commit.Tree()
	if err != nil {
		c.SendResponse(MsgGetTree, []fileNodeWS{})
		return
	}
	nodes := buildTree(tree, "")
	c.SendResponse(MsgGetTree, nodes)
}

func buildTree(tree *object.Tree, prefix string) []fileNodeWS {
	var nodes []fileNodeWS
	for _, e := range tree.Entries {
		path := e.Name
		if prefix != "" {
			path = prefix + "/" + e.Name
		}
		node := fileNodeWS{Name: e.Name, Path: path}
		if e.Mode == filemode.Dir {
			node.IsDir = true
			sub, err := tree.Tree(e.Name)
			if err == nil {
				node.Children = buildTree(sub, path)
			}
		}
		nodes = append(nodes, node)
	}
	return nodes
}

// --- READ_FILE ---

func handleReadFile(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p contentFilePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	repo, err := gitStore.OpenRepo(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	hash, err := git.ResolveRef(repo, "")
	if err != nil {
		c.SendError(err.Error())
		return
	}
	content, _, err := git.ReadFileContent(repo, hash, p.Path)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	h := sha256.Sum256([]byte(content))
	etag := hex.EncodeToString(h[:])
	c.SendResponse(MsgReadFile, map[string]string{
		"path":    p.Path,
		"content": content,
		"etag":    etag,
	})
}

// --- GET_HISTORY ---

type historyCommitWS struct {
	Hash       string `json:"hash"`
	Subject    string `json:"subject"`
	Committer  string `json:"committer"`
	CommitDate string `json:"commitDate"`
}

func handleGetHistory(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p contentFilePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	repo, err := gitStore.OpenRepo(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	hash, err := git.ResolveRef(repo, "")
	if err != nil {
		c.SendError(err.Error())
		return
	}
	entries, err := git.GetLog(repo, hash, p.Path, 100)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	commits := make([]historyCommitWS, len(entries))
	for i, e := range entries {
		commits[i] = historyCommitWS{
			Hash:       e.SHA,
			Subject:    e.Message,
			Committer:  e.Author,
			CommitDate: e.Date,
		}
	}
	c.SendResponse(MsgGetHistory, map[string]interface{}{
		"commits": commits,
	})
}

// --- GET_FILE_AT ---

func handleGetFileAt(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p contentFileAtPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	repo, err := gitStore.OpenRepo(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	commitHash := plumbing.NewHash(p.Hash)
	content, _, err := git.ReadFileContent(repo, commitHash, p.Path)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgGetFileAt, map[string]string{
		"path":    p.Path,
		"hash":    p.Hash,
		"content": content,
	})
}

// --- GET_DIFF ---

type diffLineWS struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

type diffHunkWS struct {
	OldStart int          `json:"oldStart"`
	OldLines int          `json:"oldLines"`
	NewStart int          `json:"newStart"`
	NewLines int          `json:"newLines"`
	Lines    []diffLineWS `json:"lines"`
}

func handleGetDiff(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p contentDiffPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	repo, err := gitStore.OpenRepo(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}

	fromHash := plumbing.NewHash(p.FromHash)
	toHash := plumbing.NewHash(p.ToHash)

	fromContent, fromBin, fromErr := git.ReadFileContent(repo, fromHash, p.Path)
	toContent, toBin, toErr := git.ReadFileContent(repo, toHash, p.Path)

	status := "modified"
	if fromErr != nil && toErr == nil {
		status = "added"
	} else if fromErr == nil && toErr != nil {
		status = "deleted"
	}

	binary := fromBin || toBin
	if fromErr != nil {
		fromContent = ""
	}
	if toErr != nil {
		toContent = ""
	}

	var hunks []diffHunkWS
	if !binary {
		hunks = computeLineDiff(fromContent, toContent)
	}

	c.SendResponse(MsgGetDiff, map[string]interface{}{
		"path":     p.Path,
		"fromHash": p.FromHash,
		"toHash":   p.ToHash,
		"status":   status,
		"binary":   binary,
		"hunks":    hunks,
	})
}

func computeLineDiff(from, to string) []diffHunkWS {
	fromLines := splitLines(from)
	toLines := splitLines(to)

	if len(fromLines) == 0 && len(toLines) == 0 {
		return nil
	}

	var lines []diffLineWS
	maxOld, maxNew := len(fromLines), len(toLines)

	i, j := 0, 0
	for i < maxOld && j < maxNew {
		if fromLines[i] == toLines[j] {
			lines = append(lines, diffLineWS{Type: "context", Content: fromLines[i]})
			i++
			j++
		} else {
			lines = append(lines, diffLineWS{Type: "remove", Content: fromLines[i]})
			lines = append(lines, diffLineWS{Type: "add", Content: toLines[j]})
			i++
			j++
		}
	}
	for ; i < maxOld; i++ {
		lines = append(lines, diffLineWS{Type: "remove", Content: fromLines[i]})
	}
	for ; j < maxNew; j++ {
		lines = append(lines, diffLineWS{Type: "add", Content: toLines[j]})
	}

	if len(lines) == 0 {
		return nil
	}

	return []diffHunkWS{{
		OldStart: 1,
		OldLines: maxOld,
		NewStart: 1,
		NewLines: maxNew,
		Lines:    lines,
	}}
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// --- SEARCH_FILES ---

type searchMatchWS struct {
	Path    string `json:"path"`
	Matches int    `json:"matches"`
}

func handleSearchFiles(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p contentSearchPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	repo, err := gitStore.OpenRepo(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	hash, err := git.ResolveRef(repo, "")
	if err != nil {
		c.SendResponse(MsgSearchFiles, map[string]interface{}{"results": []searchMatchWS{}})
		return
	}
	commit, err := repo.CommitObject(hash)
	if err != nil {
		c.SendResponse(MsgSearchFiles, map[string]interface{}{"results": []searchMatchWS{}})
		return
	}
	tree, err := commit.Tree()
	if err != nil {
		c.SendResponse(MsgSearchFiles, map[string]interface{}{"results": []searchMatchWS{}})
		return
	}

	query := strings.ToLower(p.Query)
	var results []searchMatchWS
	_ = tree.Files().ForEach(func(f *object.File) error {
		if strings.Contains(strings.ToLower(f.Name), query) {
			results = append(results, searchMatchWS{Path: f.Name, Matches: 1})
		}
		return nil
	})
	c.SendResponse(MsgSearchFiles, map[string]interface{}{"results": results})
}

