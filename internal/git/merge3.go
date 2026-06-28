package git

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/storer"
	"github.com/sergi/go-diff/diffmatchpatch"
)

type lineHunk struct {
	baseStart int
	baseLen   int
	newLines  []string
}

// mergeFileContent performs a line-level 3-way merge of a single file.
// Returns the merged content and whether a conflict was detected.
// Falls back to conservative (conflict) for binary files.
func mergeFileContent(baseContent, oursContent, theirsContent string) (string, bool) {
	if isBinary([]byte(baseContent)) || isBinary([]byte(oursContent)) || isBinary([]byte(theirsContent)) {
		return "", true
	}

	oursHunks := computeLineHunks(baseContent, oursContent)
	theirsHunks := computeLineHunks(baseContent, theirsContent)

	if anyHunkOverlap(oursHunks, theirsHunks) {
		return "", true
	}

	baseLines := splitIntoLines(baseContent)
	allHunks := append(oursHunks, theirsHunks...)
	merged := applyHunks(baseLines, allHunks)
	return strings.Join(merged, ""), false
}

// computeLineHunks computes the set of line-level hunks representing changes
// from baseText to modifiedText. Uses sergi/go-diff (Myers algorithm).
func computeLineHunks(baseText, modifiedText string) []lineHunk {
	if baseText == modifiedText {
		return nil
	}

	dmp := diffmatchpatch.New()
	baseRunes, modRunes, lineArray := dmp.DiffLinesToRunes(baseText, modifiedText)
	diffs := dmp.DiffMain(string(baseRunes), string(modRunes), false)

	var hunks []lineHunk
	baseLine := 0

	for i := 0; i < len(diffs); {
		d := diffs[i]
		if d.Type == diffmatchpatch.DiffEqual {
			baseLine += utf8.RuneCountInString(d.Text)
			i++
			continue
		}
		h := lineHunk{baseStart: baseLine}
		for i < len(diffs) && diffs[i].Type != diffmatchpatch.DiffEqual {
			n := utf8.RuneCountInString(diffs[i].Text)
			if diffs[i].Type == diffmatchpatch.DiffDelete {
				h.baseLen += n
				baseLine += n
			} else {
				for _, r := range diffs[i].Text {
					h.newLines = append(h.newLines, lineArray[r])
				}
			}
			i++
		}
		hunks = append(hunks, h)
	}

	return hunks
}

func anyHunkOverlap(a, b []lineHunk) bool {
	for _, ha := range a {
		for _, hb := range b {
			if hunksOverlap(ha, hb) {
				return true
			}
		}
	}
	return false
}

func hunksOverlap(a, b lineHunk) bool {
	aEnd := a.baseStart + a.baseLen
	bEnd := b.baseStart + b.baseLen
	if a.baseLen > 0 && b.baseLen > 0 {
		return a.baseStart < bEnd && b.baseStart < aEnd
	}
	if a.baseLen == 0 && b.baseLen == 0 {
		return a.baseStart == b.baseStart
	}
	// One is a pure insertion, the other is a modification.
	// Conflict only if the insertion is strictly inside the modified range.
	if a.baseLen == 0 {
		return a.baseStart > b.baseStart && a.baseStart < bEnd
	}
	return b.baseStart > a.baseStart && b.baseStart < aEnd
}

// applyHunks applies non-overlapping hunks to base lines, producing merged output.
// Hunks are applied bottom-to-top to avoid line-number shifts.
func applyHunks(baseLines []string, allHunks []lineHunk) []string {
	if len(allHunks) == 0 {
		return baseLines
	}
	sort.Slice(allHunks, func(i, j int) bool {
		if allHunks[i].baseStart != allHunks[j].baseStart {
			return allHunks[i].baseStart > allHunks[j].baseStart
		}
		return allHunks[i].baseLen > allHunks[j].baseLen
	})
	result := make([]string, len(baseLines))
	copy(result, baseLines)
	for _, h := range allHunks {
		var next []string
		next = append(next, result[:h.baseStart]...)
		next = append(next, h.newLines...)
		next = append(next, result[h.baseStart+h.baseLen:]...)
		result = next
	}
	return result
}

func splitIntoLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func storeBlob(s storer.EncodedObjectStorer, content []byte) (plumbing.Hash, error) {
	obj := s.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(int64(len(content)))
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(content); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.SetEncodedObject(obj)
}
