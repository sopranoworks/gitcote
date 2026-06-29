package git

import (
	"fmt"

	"github.com/sopranoworks/shoka/pkg/authz"
)

// AuthorizeGitZone checks whether scope grants at least the required level for
// git transport on namespace/project. Only git/-zoned grants and unzoned
// wildcard grants (super-user backward compat) are considered.
func AuthorizeGitZone(scope, namespace, project string, required authz.Level) error {
	if EffectiveGitLevel(scope, namespace, project) >= required {
		return nil
	}
	return fmt.Errorf("git access denied for %s/%s", namespace, project)
}

// EffectiveGitLevel returns the maximum git-transport permission level at
// (namespace, project). Only git/-zoned grants and unzoned wildcard grants
// are considered.
func EffectiveGitLevel(scope, namespace, project string) authz.Level {
	var max authz.Level
	for _, g := range authz.ParseScope(scope) {
		if gitZoneMatches(g, namespace, project) && g.Level > max {
			max = g.Level
		}
	}
	return max
}

func gitZoneMatches(g authz.Grant, namespace, project string) bool {
	if g.Zone == "" && g.Wildcard {
		return true
	}
	if g.Zone != "git" {
		return false
	}
	if g.Wildcard {
		return true
	}
	if namespace == "" {
		return true
	}
	if g.Namespace != namespace {
		return false
	}
	if g.Project == "" {
		return true
	}
	return g.Project == project
}
