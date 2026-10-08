// Command spec-scaffolding-prune previews expired scaffolding removal using a
// reusable GitHub snapshot. Run from the repository root; -apply enables writes.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const specsDir = "docs/specs/architecture"

type approval struct {
	Document string `json:"document"`
	SHA256   string `json:"sha256"`
	Heading  int    `json:"heading"`
}
type section struct {
	Heading int    `json:"heading"`
	Title   string `json:"title"`
	Start   int    `json:"start_byte"`
	End     int    `json:"end_byte"`
	Action  string `json:"action"`
	Reason  string `json:"reason"`
}
type document struct {
	Document string    `json:"document"`
	SHA256   string    `json:"sha256"`
	Ticket   int       `json:"ticket"`
	Reason   string    `json:"reason"`
	Sections []section `json:"sections"`
}
type report struct {
	Checkout                 string     `json:"checkout_commit"`
	Evidence                 snapshot   `json:"evidence"`
	EvidenceSHA256           string     `json:"evidence_sha256"`
	ScannedDocuments         int        `json:"scanned_documents"`
	EligibleDocuments        int        `json:"eligible_documents"`
	ProposedRemovalSections  int        `json:"proposed_removal_sections"`
	DeferredQuestionSections int        `json:"deferred_question_sections"`
	Documents                []document `json:"documents"`
	ApprovalDiagnostics      []string   `json:"approval_diagnostics"`
}

func hash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func decode(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing data in %s", path)
	}
	return nil
}
func main() {
	evidence := flag.String("evidence", "", "verified snapshot JSON (required for preview/apply)")
	approvals := flag.String("approvals", "", "JSON array of document/hash/heading approvals")
	apply := flag.Bool("apply", false, "write exactly the removals described by stdout preview")
	flag.Parse()
	err := func() error {
		if flag.NArg() != 0 {
			return fmt.Errorf("unexpected positional arguments")
		}
		if *evidence == "" {
			return fmt.Errorf("-evidence is required (verified GitHub metadata snapshot)")
		}
		var s snapshot
		var a []approval
		if err := decode(*evidence, &s); err != nil {
			return err
		}
		if *approvals != "" {
			if err := decode(*approvals, &a); err != nil {
				return err
			}
		}
		return run(".", s, a, *apply, os.Stdout)
	}()
	if err != nil {
		fmt.Fprintf(os.Stderr, "spec-scaffolding-prune: %v\n", err)
		os.Exit(1)
	}
}
func run(root string, s snapshot, approvals []approval, apply bool, out io.Writer) error {
	if err := s.validate(); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(root, specsDir))
	if err != nil {
		return fmt.Errorf("discover specs: %w", err)
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		return err
	}
	result := report{Evidence: s, EvidenceSHA256: hash(encoded)}
	if commit, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output(); err == nil {
		result.Checkout = strings.TrimSpace(string(commit))
	}
	originals := map[string][]byte{}
	matched := make([]bool, len(approvals))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := specsDir + "/" + entry.Name()
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("nonregular spec %s", path)
		}
		b, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		originals[path] = b
		ticket, reason, headings := parse(entry.Name(), b)
		if reason == "" {
			reason = eligibility(ticket, s)
		}
		doc := document{Document: path, SHA256: hash(b), Ticket: ticket, Reason: reason}
		result.ScannedDocuments++
		if reason == "eligible" {
			result.EligibleDocuments++
		}
		for _, h := range headings {
			title := strings.ToLower(h.title)
			if title != "files to read first" && title != "open questions" {
				continue
			}
			sec := section{Heading: h.ordinal, Title: h.title, Start: h.start, End: h.end, Action: "retain", Reason: reason}
			count := 0
			for i, a := range approvals {
				if a.Document == path && a.SHA256 == doc.SHA256 && a.Heading == h.ordinal && title == "open questions" {
					matched[i] = true
					count++
				}
			}
			if reason == "eligible" {
				sec.Reason = "unapproved nontrivial question"
				if title == "files to read first" {
					sec.Action, sec.Reason = "remove", "expired reading list"
				} else if trivial(b[h.body:h.end]) {
					sec.Action, sec.Reason = "remove", "whole-body resolution literal"
				} else if count == 1 {
					sec.Action, sec.Reason = "remove", "content-bound individual approval"
				}
			}
			doc.Sections = append(doc.Sections, sec)
		}
		// Removal ranges cannot overlap a retained question section.
		for i := range doc.Sections {
			for _, q := range doc.Sections {
				if doc.Sections[i].Action == "remove" && q.Action == "retain" && strings.EqualFold(q.Title, "open questions") && q.Start < doc.Sections[i].End && q.End > doc.Sections[i].Start {
					doc.Sections[i].Action = "retain"
					doc.Sections[i].Reason = "overlaps retained question section"
				}
			}
		}
		for _, sec := range doc.Sections {
			if sec.Action == "remove" {
				result.ProposedRemovalSections++
			} else if strings.EqualFold(sec.Title, "open questions") {
				result.DeferredQuestionSections++
			}
		}
		result.Documents = append(result.Documents, doc)
	}
	for i, a := range approvals {
		count := 0
		for _, other := range approvals {
			if a == other {
				count++
			}
		}
		if !matched[i] || count != 1 {
			result.ApprovalDiagnostics = append(result.ApprovalDiagnostics, fmt.Sprintf("%s sha256=%s heading=%d: stale, unmatched or ambiguous approval", a.Document, a.SHA256, a.Heading))
		}
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return fmt.Errorf("write preview: %w", err)
	}
	if !apply {
		return nil
	}
	for _, doc := range result.Documents {
		b := originals[doc.Document]
		var next []byte
		position := 0
		for _, sec := range doc.Sections {
			if sec.Action == "remove" && sec.Start >= position {
				next = append(next, b[position:sec.Start]...)
				position = sec.End
			}
		}
		next = append(next, b[position:]...)
		if !bytes.Equal(next, b) {
			if err := replace(filepath.Join(root, doc.Document), b, next); err != nil {
				return fmt.Errorf("apply %s: %w", doc.Document, err)
			}
		}
	}
	return nil
}
func replace(path string, old, next []byte) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("spec became nonregular")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, old) {
		return fmt.Errorf("spec changed since preview")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".scaffolding-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // best-effort cleanup after rename or failure
	defer f.Close()           // best-effort cleanup on early errors
	if err = f.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if _, err = f.Write(next); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
