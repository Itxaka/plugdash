package plugins

import "testing"

func TestParseImage(t *testing.T) {
	cases := []struct {
		in           string
		wantRegistry string
		wantRepo     string
	}{
		{"nginx", "registry-1.docker.io", "library/nginx"},
		{"org/img", "registry-1.docker.io", "org/img"},
		{"ghcr.io/kairos-io/hadron", "ghcr.io", "kairos-io/hadron"},
		{"127.0.0.1:5000/myrepo", "127.0.0.1:5000", "myrepo"},
	}
	for _, c := range cases {
		got := parseImage(c.in)
		if got.registry != c.wantRegistry || got.repo != c.wantRepo {
			t.Errorf("parseImage(%q) = {registry:%q repo:%q}, want {registry:%q repo:%q}",
				c.in, got.registry, got.repo, c.wantRegistry, c.wantRepo)
		}
	}
}

func TestValidateImage(t *testing.T) {
	valid := []string{
		"nginx",
		"org/img",
		"ghcr.io/kairos-io/hadron",
		"registry.example.com:5000/team/app",
		"127.0.0.1:5000/myrepo",
	}
	for _, s := range valid {
		if err := ValidateImage(s); err != nil {
			t.Errorf("ValidateImage(%q) = %v, want nil", s, err)
		}
	}

	invalid := []string{
		"",                                  // empty
		"   ",                               // blank
		"docker pull ghcr.io/kairos/hadron", // pasted command (space)
		"ghcr.io/org/repo with space",       // stray space
		"https://ghcr.io/org/repo",          // URL
		"nginx:1.27",                        // tag in image
		"ghcr.io/org/repo:v1.0.0",           // tag in image with host
		"ghcr.io/org/repo@sha256:abc",       // digest
	}
	for _, s := range invalid {
		if err := ValidateImage(s); err == nil {
			t.Errorf("ValidateImage(%q) = nil, want error", s)
		}
	}
}
