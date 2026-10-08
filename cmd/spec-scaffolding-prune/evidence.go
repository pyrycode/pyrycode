package main

import (
	"fmt"
	"time"
)

const repository = "pyrycode/pyrycode"

type merge struct {
	Number      int    `json:"number"`
	Repository  string `json:"repository"`
	MergedAt    string `json:"merged_at"`
	Association string `json:"association"`
}
type issue struct {
	Number int     `json:"number"`
	State  string  `json:"state"`
	PRs    []merge `json:"prs"`
}
type snapshot struct {
	Repository  string  `json:"repository"`
	RetrievedAt string  `json:"retrieved_at"`
	Provenance  string  `json:"provenance"`
	Issues      []issue `json:"issues"`
}

func (s snapshot) validate() error {
	if s.Repository != repository || s.Provenance == "" {
		return fmt.Errorf("snapshot requires repository %s and provenance", repository)
	}
	if _, err := time.Parse(time.RFC3339, s.RetrievedAt); err != nil {
		return fmt.Errorf("snapshot retrieval time: %w", err)
	}
	return nil
}
func eligibility(n int, s snapshot) string {
	var matches []issue
	for _, v := range s.Issues {
		if v.Number == n {
			matches = append(matches, v)
		}
	}
	if len(matches) == 0 {
		return "missing issue evidence"
	}
	if len(matches) != 1 {
		return "contradictory duplicate issue evidence"
	}
	v := matches[0]
	if v.State == "OPEN" {
		return "open issue"
	}
	if v.State != "CLOSED" {
		return "unknown issue state"
	}
	if len(v.PRs) == 0 {
		return "closed without positive merge evidence"
	}
	retrieved, _ := time.Parse(time.RFC3339, s.RetrievedAt)
	seen := map[int]string{}
	for _, pr := range v.PRs {
		merged, err := time.Parse(time.RFC3339, pr.MergedAt)
		if pr.Number <= 0 || pr.Repository != repository || err != nil || pr.Association != "closingIssuesReferences" || merged.After(retrieved) || (seen[pr.Number] != "" && seen[pr.Number] != pr.MergedAt) {
			return "missing or contradictory PR evidence"
		}
		seen[pr.Number] = pr.MergedAt
	}
	return "eligible"
}
