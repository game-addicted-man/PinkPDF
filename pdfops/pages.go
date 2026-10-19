package pdfops

import (
	"fmt"
	"strconv"
	"strings"
)

// normalizeRanges unifies the many ways people type page ranges:
// "1~3", "1 - 3", "1–3", "1, 3", "1，3", "5-끝".
func normalizeRanges(s string) string {
	r := strings.NewReplacer(
		"~", "-", "∼", "-", "–", "-", "—", "-", "−", "-", "～", "-",
		"，", ",", "、", ",", ";", ",", "\n", ",", "\t", " ",
		"끝", "end", "마지막", "end", "END", "end", "End", "end",
		"쪽", "", // "1쪽-3쪽" means "1-3"
	)
	s = r.Replace(s)
	// remove spaces around dashes so "1 - 3" becomes "1-3"
	for strings.Contains(s, " -") || strings.Contains(s, "- ") {
		s = strings.ReplaceAll(s, " -", "-")
		s = strings.ReplaceAll(s, "- ", "-")
	}
	return s
}

// ParseRanges parses a page range expression into groups of page numbers.
// Each comma (or space) separated item becomes one group:
//
//	"1-3, 5, 8-"  ->  [[1 2 3] [5] [8 ... last]]
//
// Reverse ranges ("5-3") are allowed and keep their order.
func ParseRanges(expr string, pageCount int) ([][]int, error) {
	expr = normalizeRanges(expr)
	var groups [][]int
	for _, part := range strings.FieldsFunc(expr, func(r rune) bool { return r == ',' || r == ' ' }) {
		part = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(part, "쪽"), "p"))
		part = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(part, "p"), "P"))
		if part == "" {
			continue
		}
		from, thru, err := parseRangeItem(part, pageCount)
		if err != nil {
			return nil, err
		}
		var g []int
		if from <= thru {
			for p := from; p <= thru; p++ {
				g = append(g, p)
			}
		} else {
			for p := from; p >= thru; p-- {
				g = append(g, p)
			}
		}
		groups = append(groups, g)
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("페이지 범위를 입력해 주세요 (예: 1-3, 5, 8-)")
	}
	return groups, nil
}

func parseRangeItem(part string, pageCount int) (int, int, error) {
	num := func(s string, def int) (int, error) {
		s = strings.TrimSpace(s)
		if s == "" {
			return def, nil
		}
		if s == "end" {
			return pageCount, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("페이지 범위를 이해할 수 없습니다: %q", part)
		}
		if n < 1 || n > pageCount {
			return 0, fmt.Errorf("%d쪽은 없습니다 (전체 %d쪽)", n, pageCount)
		}
		return n, nil
	}
	if i := strings.Index(part, "-"); i >= 0 {
		if strings.Count(part, "-") > 1 {
			return 0, 0, fmt.Errorf("페이지 범위를 이해할 수 없습니다: %q", part)
		}
		from, err := num(part[:i], 1)
		if err != nil {
			return 0, 0, err
		}
		thru, err := num(part[i+1:], pageCount)
		if err != nil {
			return 0, 0, err
		}
		return from, thru, nil
	}
	n, err := num(part, 0)
	if err != nil {
		return 0, 0, err
	}
	return n, n, nil
}

// ParseSelection flattens a range expression into a single ordered page list
// without duplicates.
func ParseSelection(expr string, pageCount int) ([]int, error) {
	groups, err := ParseRanges(expr, pageCount)
	if err != nil {
		return nil, err
	}
	seen := make(map[int]bool)
	var pages []int
	for _, g := range groups {
		for _, p := range g {
			if !seen[p] {
				seen[p] = true
				pages = append(pages, p)
			}
		}
	}
	return pages, nil
}

// FormatPages renders a page list compactly: [1 2 3 5] -> "1-3,5".
func FormatPages(pages []int) string {
	var b strings.Builder
	for i := 0; i < len(pages); {
		j := i
		for j+1 < len(pages) && pages[j+1] == pages[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteString(",")
		}
		if j > i {
			fmt.Fprintf(&b, "%d-%d", pages[i], pages[j])
		} else {
			fmt.Fprintf(&b, "%d", pages[i])
		}
		i = j + 1
	}
	return b.String()
}
