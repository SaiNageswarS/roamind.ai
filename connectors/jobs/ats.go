package jobs

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// ATS is an applicant tracking system with a public posting API. Each one
// is in its own file; add one to systems to read its postings.
type ATS interface {
	// Match resolves u to a posting on this system, if it is one: the
	// posting's canonical URL and its address in the system's API.
	Match(u *url.URL) (Ref, bool)
	// Posting reads the posting at ref, which Match returned, through c;
	// its Source is ref's unless it sets one. It returns ErrGone when the
	// system no longer has the posting, and errNotHere when it turns out
	// not to be on this system.
	Posting(ctx context.Context, c *Client, ref Ref) (*Posting, error)
}

// systems are tried in order by Parse; the first to match a URL reads its
// posting. A URL none matches is read from its page.
var systems = []ATS{
	greenhouse{}, lever{}, ashby{}, workday{},
	// Common in Europe and Hong Kong.
	smartRecruiters{}, recruitee{}, personio{}, workable{}, oracle{},
	// Last: it matches any URL with a gh_jid, on a company's own domain.
	greenhouseHosted{},
}

// errNotHere is returned by an ATS for a matched URL that isn't one of its
// postings after all; its page is read instead.
var errNotHere = errors.New("not on this system")

// hostOf is u's host, lowercased, without its port.
func hostOf(u *url.URL) string { return strings.ToLower(u.Hostname()) }
