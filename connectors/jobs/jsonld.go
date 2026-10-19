package jobs

import (
	"encoding/json"
	"html"
	"regexp"
	"slices"
	"strings"
)

// The JobPosting a page describes itself with, in JSON-LD, for search
// engines' job listings (schema.org). Careers sites without an API of
// their own (Teamtailor, SuccessFactors, many job boards) carry it.

var ldRe = regexp.MustCompile(`(?is)<script[^>]*type=["']?application/ld\+json["']?[^>]*>(.*?)</script>`)

// ldPosting is the page's JobPosting, or nil.
func ldPosting(page string) *Posting {
	for _, m := range ldRe.FindAllStringSubmatch(page, -1) {
		var v any
		if json.Unmarshal([]byte(strings.TrimSpace(m[1])), &v) != nil {
			continue
		}
		if jp := findJobPosting(v); jp != nil {
			return jp
		}
	}
	return nil
}

// findJobPosting looks for a JobPosting in a JSON-LD value: the value
// itself, an array of them or an @graph.
func findJobPosting(v any) *Posting {
	switch v := v.(type) {
	case []any:
		for _, x := range v {
			if p := findJobPosting(x); p != nil {
				return p
			}
		}
	case map[string]any:
		if isType(v["@type"], "JobPosting") {
			p := &Posting{
				Title:    str(v["title"]),
				Company:  str(field(v["hiringOrganization"], "name")),
				Location: ldLocation(v["jobLocation"]),
				Posted:   isoDate(str(v["datePosted"])),
				// Often HTML, escaped once more.
				Description: Text(html.UnescapeString(str(v["description"]))),
			}
			if p.Description != "" {
				return p
			}
		}
		if g, ok := v["@graph"]; ok {
			return findJobPosting(g)
		}
	}
	return nil
}

// ldLocation is a jobLocation's places, as "city, country".
func ldLocation(v any) string {
	places, ok := v.([]any)
	if !ok {
		places = []any{v}
	}
	var out []string
	for _, pl := range places {
		addr := field(pl, "address")
		if s, ok := addr.(string); ok {
			out = append(out, s)
			continue
		}
		var parts []string
		for _, k := range []string{"addressLocality", "addressRegion", "addressCountry"} {
			s := str(field(addr, k))
			if s == "" {
				s = str(field(field(addr, k), "name"))
			}
			if s != "" && !slices.Contains(parts, s) {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			out = append(out, strings.Join(parts, ", "))
		}
	}
	return strings.Join(out, "; ")
}

func isType(t any, want string) bool {
	switch t := t.(type) {
	case string:
		return t == want
	case []any:
		for _, x := range t {
			if x == want {
				return true
			}
		}
	}
	return false
}

func field(v any, k string) any {
	if m, ok := v.(map[string]any); ok {
		return m[k]
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
