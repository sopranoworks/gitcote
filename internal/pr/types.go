// Package pr implements the pull request system for GitYard.
package pr

import "time"

// PRState represents the state of a pull request.
type PRState string

const (
	StateOpen     PRState = "open"
	StateApproved PRState = "approved"
	StateMerged   PRState = "merged"
	StateClosed   PRState = "closed"
)

// Mergeable represents the merge status of a pull request.
type Mergeable string

const (
	MergeableClean    Mergeable = "clean"
	MergeableConflict Mergeable = "conflict"
	MergeableUnknown  Mergeable = "unknown"
)

// PullRequest is the PR entity stored in bbolt.
type PullRequest struct {
	Number        uint32     `json:"number"`
	RepoNamespace string     `json:"repo_namespace"`
	RepoProject   string     `json:"repo_project"`
	Title         string     `json:"title"`
	Description   string     `json:"description"`
	SourceBranch  string     `json:"source_branch"`
	TargetBranch  string     `json:"target_branch"`
	Author        string     `json:"author"`
	State         PRState    `json:"state"`
	Mergeable     Mergeable  `json:"mergeable"`
	SourceCommit  string     `json:"source_commit"`
	TargetCommit  string     `json:"target_commit"`
	MergeCommit   string     `json:"merge_commit,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	MergedAt      *time.Time `json:"merged_at,omitempty"`
	ClosedAt      *time.Time `json:"closed_at,omitempty"`
	ApprovedBy    string     `json:"approved_by,omitempty"`
	ApprovedAt    *time.Time `json:"approved_at,omitempty"`
}
