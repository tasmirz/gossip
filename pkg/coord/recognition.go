package coord

import (
	"errors"
	"net/url"
	pathpkg "path"
	"strings"
)

// NormalizeRepositoryRemote creates a credential-free suggestion key. It is
// used only to recognize repositories among projects the caller can access.
func NormalizeRepositoryRemote(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return "", errors.New("repository remote is required and must be at most 2048 bytes")
	}
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f || r == '\\' {
			return "", errors.New("repository remote contains invalid characters")
		}
	}
	var host, repoPath string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.Hostname() == "" {
			return "", errors.New("invalid repository remote")
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "ssh", "git":
		default:
			return "", errors.New("repository remote must use HTTP, HTTPS, SSH, or Git")
		}
		host = strings.ToLower(u.Host)
		repoPath = u.Path
	} else {
		// Accept Git's common scp form, while omitting the username from identity.
		colon := strings.IndexByte(raw, ':')
		if colon < 1 || strings.Contains(raw[:colon], "/") || colon == len(raw)-1 {
			return "", errors.New("repository remote must be an absolute Git URL")
		}
		endpoint := raw[:colon]
		if at := strings.LastIndexByte(endpoint, '@'); at >= 0 {
			endpoint = endpoint[at+1:]
		}
		host, repoPath = strings.ToLower(endpoint), raw[colon+1:]
	}
	if host == "" || strings.ContainsAny(host, "?#") || repoPath == "" || strings.ContainsAny(repoPath, "?#") {
		return "", errors.New("invalid repository remote")
	}
	for _, segment := range strings.Split(repoPath, "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("repository remote path cannot traverse directories")
		}
	}
	repoPath = strings.Trim(pathpkg.Clean("/"+strings.Trim(repoPath, "/")), "/")
	if strings.HasSuffix(strings.ToLower(repoPath), ".git") {
		repoPath = repoPath[:len(repoPath)-4]
	}
	if repoPath == "" {
		return "", errors.New("repository remote path is required")
	}
	return host + "/" + repoPath, nil
}
