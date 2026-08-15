package lint

import (
	"regexp"
	"sort"
	"strconv"
)

var adrFilename = regexp.MustCompile(`^([0-9]+)-(.+)\.md$`)

// ADRNumber parses the numeric prefix from a numbered ADR basename.
func ADRNumber(name string) (int, bool) {
	match := adrFilename.FindStringSubmatch(name)
	if match == nil {
		return 0, false
	}
	number, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return number, true
}

// ADRNumberDuplicate identifies files that share one numeric ADR prefix.
type ADRNumberDuplicate struct {
	Number int
	Files  []string
}

// CheckADRNumberUniqueness returns every duplicate number in numeric order.
// Filenames within each duplicate are sorted for deterministic diagnostics.
func CheckADRNumberUniqueness(names []string) []ADRNumberDuplicate {
	byNumber := make(map[int][]string)
	for _, name := range names {
		number, ok := ADRNumber(name)
		if !ok {
			continue
		}
		byNumber[number] = append(byNumber[number], name)
	}

	numbers := make([]int, 0, len(byNumber))
	for number, files := range byNumber {
		if len(files) > 1 {
			numbers = append(numbers, number)
		}
	}
	sort.Ints(numbers)

	var duplicates []ADRNumberDuplicate
	for _, number := range numbers {
		files := byNumber[number]
		sort.Strings(files)
		duplicates = append(duplicates, ADRNumberDuplicate{Number: number, Files: files})
	}
	return duplicates
}
