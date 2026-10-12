package routing

import (
	"testing"

	"agent-host/internal/state"
)

func TestUnmappedResourceReferencesRequireSemanticRouting(t *testing.T) {
	projects := []state.Project{
		{ID: "cedar", Name: "Cedar", Aliases: []string{"cedar.example.invalid", "https://git.example.invalid/team/cedar"}},
		{ID: "relay", Name: "Relay", Aliases: []string{"relay.example.invalid:8443", "git@git.example.invalid:team/relay.git"}},
	}
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{"ordinary project task", "Check cedar startup", false},
		{"registered service", "cedar 请求 cedar.example.invalid 的接口", false},
		{"registered service path with project", "Check cedar at cedar.example.invalid/health", false},
		{"registered service path without exact project", "Check cedar.example.invalid/health", true},
		{"registered host URL", "Check cedar at https://CEDAR.example.invalid/status", false},
		{"bare service unknown", "cedar 请求 archive.example.invalid 的接口", true},
		{"unknown host with project label", "cedar 请求 cedar.other.invalid 的接口", true},
		{"subdomain not granted", "Check staging.cedar.example.invalid", true},
		{"parent domain not granted", "Check example.invalid", true},
		{"multiple references all checked", "Check cedar.example.invalid,archive.example.invalid", true},
		{"adjacent paths remain separate", "Check cedar.example.invalid/health,archive.example.invalid/status", true},
		{"adjacent URLs remain separate", "Check https://cedar.example.invalid/health;https://archive.example.invalid/status", true},
		{"registered repository", "Check https://git.example.invalid/team/cedar", false},
		{"registered repository suffix with project", "Check cedar at https://git.example.invalid/team/cedar.git", false},
		{"repository suffix without exact project", "Check https://git.example.invalid/team/cedar.git", true},
		{"repository sibling is unknown", "Check https://git.example.invalid/team/cedar-tools", true},
		{"repository path is not host permission", "Check https://git.example.invalid/team/unknown", true},
		{"repository on another host", "Check https://other.example.invalid/team/cedar", true},
		{"bare registered repository with project", "Check cedar at git.example.invalid/team/cedar", false},
		{"bare registered repository without exact project", "Check git.example.invalid/team/cedar", true},
		{"registered git remote", "Check git@git.example.invalid:team/relay.git", false},
		{"registered ssh remote with project", "Check relay at ssh://git@git.example.invalid/team/relay.git", false},
		{"registered ssh remote without exact project", "Check ssh://git@git.example.invalid/team/relay.git", true},
		{"unknown git remote", "cedar can reach git@git.example.invalid:team/unlisted.git", true},
		{"registered explicit port with project", "Check relay at relay.example.invalid:8443/status", false},
		{"registered explicit port without exact project", "Check relay.example.invalid:8443/status", true},
		{"known resource cannot hide another target", "Check cedar via https://relay.example.invalid:8443/status", true},
		{"both exact projects cover both resources", "Check cedar and relay via https://relay.example.invalid:8443/status", false},
		{"different port", "Check relay.example.invalid:9000", true},
		{"IP target", "Check cedar against 192.0.2.24:8080", true},
		{"localhost target", "Check cedar against localhost:8080", true},
		{"IPv6 URL target", "Check cedar against http://[2001:db8::7]", true},
		{"bare IPv6 target", "Check cedar against [2001:db8::7]:8443", true},
		{"source and config filenames", "Update cedar router.go, README.md and config.yaml", false},
		{"source path", "Update cedar internal/router.go", false},
		{"version number", "Update cedar to v2.3.4", false},
		{"email recipient", "Send cedar report to reader@example.invalid", false},
		{"punctuation", "Check cedar.example.invalid.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasUnmappedResourceReference(tc.text, projects); got != tc.want {
				t.Fatalf("hasUnmappedResourceReference(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestResourceCoverageRequiresFullCatalogIdentity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reference string
		alias     string
		want      bool
	}{
		{"project word is not hostname", "https://cedar.example.invalid", "cedar", false},
		{"project word is not repository", "https://git.example.invalid/team/cedar", "cedar", false},
		{"description is not authorization", "https://cedar.example.invalid", "Service at cedar.example.invalid", false},
		{"complete hostname", "https://cedar.example.invalid/path", "cedar.example.invalid", true},
		{"hostname cannot cover port", "https://cedar.example.invalid:8443/path", "cedar.example.invalid", false},
		{"complete repository", "git@git.example.invalid:team/cedar.git", "https://git.example.invalid/team/cedar", true},
		{"repository names are case sensitive", "https://git.example.invalid/team/Cedar", "https://git.example.invalid/team/cedar", false},
		{"single label URL explicit registration", "http://relay/health", "http://relay", true},
		{"single label ID insufficient", "http://relay/health", "relay", false},
		{"registered IP", "http://192.0.2.24/health", "192.0.2.24", true},
		{"registered IPv6 URL", "http://[2001:db8::7]/health", "http://[2001:db8::7]", true},
		{"registered bare IPv6 address", "http://[2001:db8::7]:8443/health", "[2001:db8::7]:8443", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resourceAliasCovers(tc.reference, tc.alias); got != tc.want {
				t.Fatalf("resourceAliasCovers(%q, %q) = %v, want %v", tc.reference, tc.alias, got, tc.want)
			}
		})
	}
}
