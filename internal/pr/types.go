// Package pr implements the pull request system for GitYard.
package pr

import "time"

// PRState represents the state of a pull request.
type PRState string

const (
	StateOpen          PRState = "open"
	StateApproved      PRState = "approved"
	StateRejected      PRState = "rejected"
	StateMerged        PRState = "merged"
	StateClosed        PRState = "closed"
	StateMergeConflict PRState = "merge_conflict"
	StateInterrupted   PRState = "interrupted"
)

// Mergeable represents the merge status of a pull request.
type Mergeable string

const (
	MergeableClean    Mergeable = "clean"
	MergeableConflict Mergeable = "conflict"
	MergeableUnknown  Mergeable = "unknown"
)

// InterruptInfo records why a PR entered the interrupted state.
type InterruptInfo struct {
	Reason    string    `json:"reason"`
	Detail    string    `json:"detail"`
	AgentName string    `json:"agent_name"`
	AgentRole string    `json:"agent_role"`
	At        time.Time `json:"at"`
}

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
	PreviousState PRState    `json:"previous_state,omitempty"`
	Mergeable     Mergeable  `json:"mergeable"`
	SourceCommit  string     `json:"source_commit"`
	TargetCommit  string     `json:"target_commit"`
	MergeCommit   string     `json:"merge_commit,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	MergedAt      *time.Time `json:"merged_at,omitempty"`
	ClosedAt      *time.Time `json:"closed_at,omitempty"`
	ApprovedBy          string         `json:"approved_by,omitempty"`
	ApprovedAt          *time.Time     `json:"approved_at,omitempty"`
	SourceBranchDeleted bool           `json:"source_branch_deleted,omitempty"`
	InterruptInfo       *InterruptInfo `json:"interrupt_info,omitempty"`
	OrderFiles          []string       `json:"order_files"`
	ResultFiles         []string       `json:"result_files"`
}
