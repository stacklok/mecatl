// Package lint holds the design-doc anti-drift checks.
package lint

import "strings"

// ReadingMapPage is one entry in the READING.md topic-branch table.
type ReadingMapPage struct {
	Page  string // architecture/*.md path relative to docs/
	Title string // as it appears in the table
}

// MissingLabel records a missing orientation label in an architecture page.
type MissingLabel struct {
	DocName string // the architecture/*.md basename
	Label   string // "What this covers:" / "Prerequisites:" / "Follow-on:"
}

func (m MissingLabel) Error() string {
	return m.DocName + ": missing orientation label \"" + m.Label + "\""
}

// CheckReadingMap validates the reading map integrity:
//  1. READING.md must list every human-authored architecture/*.md page
//     (except the generated mecatl.modelith.md).
//  2. Every such page must contain "What this covers:", "Prerequisites:",
//     and "Follow-on:".
//
// Both inputs are injected so this function imports no os.
func CheckReadingMap(readingMD string, archPages map[string]string) []MissingLabel {
	var problems []MissingLabel

	// 1. Gather architecture pages listed in READING.md.
	listed := listedArchPages(readingMD)

	// 2. Every human-authored page (except the generated modelith artifact)
	// must be in the map.
	listedSet := make(map[string]bool, len(listed))
	for _, p := range listed {
		listedSet[p.Page] = true
	}

	for name := range archPages {
		if isGeneratedPage(name) {
			continue
		}
		if !listedSet[name] {
			problems = append(problems, MissingLabel{
				DocName: name,
				Label:   "listed in docs/READING.md",
			})
		}
	}

	// 3. Every listed human-authored page must exist.
	for _, entry := range listed {
		if isGeneratedPage(entry.Page) {
			continue
		}
		if _, ok := archPages[entry.Page]; !ok {
			problems = append(problems, MissingLabel{
				DocName: entry.Page,
				Label:   "file exists in docs/architecture/",
			})
		}
	}

	// 4. Every human-authored architecture page must carry the orientation labels.
	for name, content := range archPages {
		if isGeneratedPage(name) {
			continue
		}
		for _, label := range []string{"What this covers:", "Prerequisites:", "Follow-on:"} {
			if !containsLine(content, label) {
				problems = append(problems, MissingLabel{
					DocName: name,
					Label:   label,
				})
			}
		}
	}

	return problems
}

// listedArchPages extracts architecture/*.md entries from READING.md.
// It scans markdown links of the form `[title](architecture/<name>.md)`.
func listedArchPages(md string) []ReadingMapPage {
	// Scan all markdown links of the form `[text](architecture/*.md)`; the
	// foundation spine and topic-branch table are both part of the map.
	var pages []ReadingMapPage
	// Simple scan: find lines with `](architecture/` and extract the name + title.
	lines := strings.Split(md, "\n")
	for _, line := range lines {
		// Match `[title](architecture/<name>.md)`
		start := strings.Index(line, "[")
		if start < 0 {
			continue
		}
		mid := strings.Index(line[start:], "](")
		if mid < 0 {
			continue
		}
		title := line[start+1 : start+mid]
		rest := line[start+mid+2:]
		end := strings.Index(rest, ")")
		if end < 0 {
			continue
		}
		link := rest[:end]
		if !strings.HasPrefix(link, "architecture/") || !strings.HasSuffix(link, ".md") {
			continue
		}
		name := link[len("architecture/"):]
		if isGeneratedPage(name) {
			continue
		}
		pages = append(pages, ReadingMapPage{Page: name, Title: title})
	}
	return pages
}

func isGeneratedPage(name string) bool {
	return name == "mecatl.modelith.md"
}

func containsLine(content, label string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, label) {
			return true
		}
	}
	return false
}
