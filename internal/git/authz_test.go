package git_test

import (
	"testing"

	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/shoka/pkg/authz"
)

func TestAuthorizeGitZone(t *testing.T) {
	cases := []struct {
		name    string
		scope   string
		ns, proj string
		level   authz.Level
		wantErr bool
	}{
		{"git-zoned rw allows read", "git/namespace:ns/proj:rw", "ns", "proj", authz.LevelRead, false},
		{"git-zoned rw allows write", "git/namespace:ns/proj:rw", "ns", "proj", authz.LevelWrite, false},
		{"git-zoned r denies write", "git/namespace:ns/proj:r", "ns", "proj", authz.LevelWrite, true},
		{"git-zoned r allows read", "git/namespace:ns/proj:r", "ns", "proj", authz.LevelRead, false},
		{"unzoned rw denied git", "namespace:ns/proj:rw", "ns", "proj", authz.LevelRead, true},
		{"unzoned admin denied git", "namespace:ns/proj:admin", "ns", "proj", authz.LevelRead, true},
		{"super-user allowed git", "*", "ns", "proj", authz.LevelAdmin, false},
		{"super-user admin allowed git", "*:admin", "ns", "proj", authz.LevelAdmin, false},
		{"wrong namespace denied", "git/namespace:other/proj:rw", "ns", "proj", authz.LevelRead, true},
		{"wrong project denied", "git/namespace:ns/other:rw", "ns", "proj", authz.LevelRead, true},
		{"namespace-wide git grant", "git/namespace:ns:rw", "ns", "proj", authz.LevelWrite, false},
		{"git wildcard", "git/*:admin", "ns", "proj", authz.LevelAdmin, false},
		{"mixed scope git+mcp", "namespace:ns/proj:rw,git/namespace:ns/proj:rw", "ns", "proj", authz.LevelWrite, false},
		{"mixed scope mcp-only half denied", "namespace:ns/proj:rw,namespace:other/proj:rw", "ns", "proj", authz.LevelRead, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := git.AuthorizeGitZone(c.scope, c.ns, c.proj, c.level)
			if c.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestEffectiveGitLevel(t *testing.T) {
	cases := []struct {
		name     string
		scope    string
		ns, proj string
		want     authz.Level
	}{
		{"git-zoned rw", "git/namespace:ns/proj:rw", "ns", "proj", authz.LevelWrite},
		{"git-zoned r", "git/namespace:ns/proj:r", "ns", "proj", authz.LevelRead},
		{"git-zoned admin", "git/namespace:ns/proj:admin", "ns", "proj", authz.LevelAdmin},
		{"unzoned rw returns none", "namespace:ns/proj:rw", "ns", "proj", authz.LevelNone},
		{"super-user", "*", "ns", "proj", authz.LevelAdmin},
		{"mixed picks git zone", "namespace:ns/proj:r,git/namespace:ns/proj:rw", "ns", "proj", authz.LevelWrite},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := git.EffectiveGitLevel(c.scope, c.ns, c.proj)
			if got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
