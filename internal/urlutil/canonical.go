package urlutil

import (
	"fmt"
	"net/url"
	"strings"
)

// Canonicalize parses a raw URL string and returns its canonical form.
// The canonicalization rules are:
// 1. Scheme and host are lowercased.
// 2. Default ports (80 for http, 443 for https) are stripped.
// 3. The URL fragment (#...) is removed.
// 4. A trailing slash is removed, unless it's the root path.
// Returns an error if the URL is not a valid absolute HTTP/HTTPS URL.
func Canonicalize(rawURL string) (string, error) {
	// Parse the URL
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse url: %w", err)
	}

	// Must be an absolute URL with an HTTP or HTTPS scheme
	if !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("url must be an absolute http or https url")
	}

	// Rule 1: Scheme & Host to Lowercase
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)

	// Rule 2: Strip Default Ports
	// The url.URL struct's Host field includes the port, so we need to check it.
	// The Hostname() method returns the host without the port.
	if (u.Scheme == "http" && strings.HasSuffix(u.Host, ":80")) ||
		(u.Scheme == "https" && strings.HasSuffix(u.Host, ":443")) {
		u.Host = u.Hostname()
	}

	// Rule 3: Remove Fragments
	u.Fragment = ""

	// Rule 4: Trim Trailing Slash (unless it's the root)
	if len(u.Path) > 1 && strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}

	return u.String(), nil
}
