package tools

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	beginPatchMarker         = "*** Begin Patch"
	endPatchMarker           = "*** End Patch"
	addFileMarker            = "*** Add File: "
	deleteFileMarker         = "*** Delete File: "
	updateFileMarker         = "*** Update File: "
	moveToMarker             = "*** Move to: "
	eofMarker                = "*** End of File"
	changeContextMarker      = "@@ "
	emptyChangeContextMarker = "@@"
)

type patchHunk interface {
	sourcePath() string
	movePath() string
}

type addFileHunk struct {
	path     string
	contents string
}

func (h addFileHunk) sourcePath() string { return h.path }
func (h addFileHunk) movePath() string   { return "" }

type deleteFileHunk struct{ path string }

func (h deleteFileHunk) sourcePath() string { return h.path }
func (h deleteFileHunk) movePath() string   { return "" }

type updateFileHunk struct {
	path    string
	movedTo string
	chunks  []updateFileChunk
}

func (h updateFileHunk) sourcePath() string { return h.path }
func (h updateFileHunk) movePath() string   { return h.movedTo }

type updateFileChunk struct {
	changeContext string
	oldLines      []string
	newLines      []string
	endOfFile     bool
}

func parsePatch(patch string) ([]patchHunk, error) {
	lines := strings.Split(strings.TrimSpace(patch), "\n")
	inner, err := patchBodyLines(lines)
	if err != nil {
		return nil, err
	}
	if len(inner) < 2 {
		return nil, fmt.Errorf("invalid patch: The first line of the patch must be '%s'", beginPatchMarker)
	}
	remaining := inner[1 : len(inner)-1]
	lineNumber := 2
	var hunks []patchHunk
	for len(remaining) > 0 {
		hunk, consumed, err := parseOneHunk(remaining, lineNumber)
		if err != nil {
			return nil, err
		}
		hunks = append(hunks, hunk)
		lineNumber += consumed
		remaining = remaining[consumed:]
	}
	if len(hunks) == 0 {
		return nil, fmt.Errorf("No files were modified.")
	}
	return hunks, nil
}

func patchBodyLines(lines []string) ([]string, error) {
	if err := checkPatchBoundaries(lines); err == nil {
		return lines, nil
	} else if inner, innerErr := unwrapHeredoc(lines); innerErr == nil {
		if err := checkPatchBoundaries(inner); err != nil {
			return nil, err
		}
		return inner, nil
	} else {
		return nil, err
	}
}

func checkPatchBoundaries(lines []string) error {
	if len(lines) == 0 {
		return fmt.Errorf("invalid patch: The first line of the patch must be '%s'", beginPatchMarker)
	}
	if strings.TrimSpace(lines[0]) != beginPatchMarker {
		return fmt.Errorf("invalid patch: The first line of the patch must be '%s'", beginPatchMarker)
	}
	if strings.TrimSpace(lines[len(lines)-1]) != endPatchMarker {
		return fmt.Errorf("invalid patch: The last line of the patch must be '%s'", endPatchMarker)
	}
	return nil
}

func unwrapHeredoc(lines []string) ([]string, error) {
	if len(lines) < 4 {
		return nil, fmt.Errorf("invalid patch: The first line of the patch must be '%s'", beginPatchMarker)
	}
	first := strings.TrimSpace(lines[0])
	last := strings.TrimSpace(lines[len(lines)-1])
	if (first == "<<EOF" || first == "<<'EOF'" || first == `<<"EOF"`) && strings.HasSuffix(last, "EOF") {
		return lines[1 : len(lines)-1], nil
	}
	return nil, fmt.Errorf("invalid patch: The first line of the patch must be '%s'", beginPatchMarker)
}

func parseOneHunk(lines []string, lineNumber int) (patchHunk, int, error) {
	first := strings.TrimSpace(lines[0])
	if path, ok := strings.CutPrefix(first, addFileMarker); ok {
		contents := strings.Builder{}
		parsed := 1
		for _, line := range lines[1:] {
			added, ok := strings.CutPrefix(line, "+")
			if !ok {
				break
			}
			contents.WriteString(added)
			contents.WriteByte('\n')
			parsed++
		}
		return addFileHunk{path: path, contents: contents.String()}, parsed, nil
	}
	if path, ok := strings.CutPrefix(first, deleteFileMarker); ok {
		return deleteFileHunk{path: path}, 1, nil
	}
	if path, ok := strings.CutPrefix(first, updateFileMarker); ok {
		remaining := lines[1:]
		parsed := 1
		movedTo := ""
		if len(remaining) > 0 {
			if dest, ok := strings.CutPrefix(remaining[0], moveToMarker); ok {
				movedTo = dest
				remaining = remaining[1:]
				parsed++
			}
		}
		var chunks []updateFileChunk
		for len(remaining) > 0 {
			if strings.TrimSpace(remaining[0]) == "" {
				parsed++
				remaining = remaining[1:]
				continue
			}
			if strings.HasPrefix(remaining[0], "***") {
				break
			}
			chunk, consumed, err := parseUpdateChunk(remaining, lineNumber+parsed, len(chunks) == 0)
			if err != nil {
				return nil, 0, err
			}
			chunks = append(chunks, chunk)
			parsed += consumed
			remaining = remaining[consumed:]
		}
		if len(chunks) == 0 {
			return nil, 0, fmt.Errorf("invalid hunk at line %d, Update file hunk for path '%s' is empty", lineNumber, path)
		}
		return updateFileHunk{path: path, movedTo: movedTo, chunks: chunks}, parsed, nil
	}
	return nil, 0, fmt.Errorf("invalid hunk at line %d, '%s' is not a valid hunk header. Valid hunk headers: '*** Add File: {path}', '*** Delete File: {path}', '*** Update File: {path}'", lineNumber, first)
}

func parseUpdateChunk(lines []string, lineNumber int, allowMissingContext bool) (updateFileChunk, int, error) {
	if len(lines) == 0 {
		return updateFileChunk{}, 0, fmt.Errorf("invalid hunk at line %d, Update hunk does not contain any lines", lineNumber)
	}
	context := ""
	start := 0
	switch {
	case lines[0] == emptyChangeContextMarker:
		start = 1
	case strings.HasPrefix(lines[0], changeContextMarker):
		context = strings.TrimPrefix(lines[0], changeContextMarker)
		start = 1
	default:
		if !allowMissingContext {
			return updateFileChunk{}, 0, fmt.Errorf("invalid hunk at line %d, Expected update hunk to start with a @@ context marker, got: '%s'", lineNumber, lines[0])
		}
	}
	if start >= len(lines) {
		return updateFileChunk{}, 0, fmt.Errorf("invalid hunk at line %d, Update hunk does not contain any lines", lineNumber+1)
	}
	chunk := updateFileChunk{changeContext: context}
	parsed := 0
	for _, line := range lines[start:] {
		if line == eofMarker {
			if parsed == 0 {
				return updateFileChunk{}, 0, fmt.Errorf("invalid hunk at line %d, Update hunk does not contain any lines", lineNumber+1)
			}
			chunk.endOfFile = true
			parsed++
			break
		}
		if line == "" {
			chunk.oldLines = append(chunk.oldLines, "")
			chunk.newLines = append(chunk.newLines, "")
			parsed++
			continue
		}
		prefix, size := utf8.DecodeRuneInString(line)
		rest := line[size:]
		switch prefix {
		case ' ':
			chunk.oldLines = append(chunk.oldLines, rest)
			chunk.newLines = append(chunk.newLines, rest)
		case '+':
			chunk.newLines = append(chunk.newLines, rest)
		case '-':
			chunk.oldLines = append(chunk.oldLines, rest)
		default:
			if parsed == 0 {
				return updateFileChunk{}, 0, fmt.Errorf("invalid hunk at line %d, Unexpected line found in update hunk: '%s'. Every line should start with ' ' (context line), '+' (added line), or '-' (removed line)", lineNumber+1, line)
			}
			return chunk, parsed + start, nil
		}
		parsed++
	}
	return chunk, parsed + start, nil
}

func seekSequence(lines, pattern []string, start int, eof bool) (int, bool) {
	if len(pattern) == 0 {
		return start, true
	}
	if len(pattern) > len(lines) {
		return 0, false
	}
	searchStart := start
	if eof && len(lines) >= len(pattern) {
		searchStart = len(lines) - len(pattern)
	}
	end := len(lines) - len(pattern)
	for i := searchStart; i <= end; i++ {
		if sliceEqual(lines[i:i+len(pattern)], pattern) {
			return i, true
		}
	}
	for i := searchStart; i <= end; i++ {
		if sliceEqualFunc(lines[i:i+len(pattern)], pattern, func(left, right string) bool {
			return strings.TrimRightFunc(left, unicode.IsSpace) == strings.TrimRightFunc(right, unicode.IsSpace)
		}) {
			return i, true
		}
	}
	for i := searchStart; i <= end; i++ {
		if sliceEqualFunc(lines[i:i+len(pattern)], pattern, func(left, right string) bool {
			return strings.TrimSpace(left) == strings.TrimSpace(right)
		}) {
			return i, true
		}
	}
	for i := searchStart; i <= end; i++ {
		if sliceEqualFunc(lines[i:i+len(pattern)], pattern, func(left, right string) bool {
			return normalizePatchPunctuation(left) == normalizePatchPunctuation(right)
		}) {
			return i, true
		}
	}
	return 0, false
}

func sliceEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func sliceEqualFunc(left, right []string, equal func(string, string) bool) bool {
	for i := range left {
		if !equal(left[i], right[i]) {
			return false
		}
	}
	return true
}

func normalizePatchPunctuation(value string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(value) {
		switch r {
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
			builder.WriteByte('-')
		case '\u2018', '\u2019', '\u201A', '\u201B':
			builder.WriteByte('\'')
		case '\u201C', '\u201D', '\u201E', '\u201F':
			builder.WriteByte('"')
		case '\u00A0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F', '\u205F', '\u3000':
			builder.WriteByte(' ')
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func deriveUpdatedContents(path, original string, chunks []updateFileChunk) (string, error) {
	originalLines := strings.Split(original, "\n")
	if len(originalLines) > 0 && originalLines[len(originalLines)-1] == "" {
		originalLines = originalLines[:len(originalLines)-1]
	}
	replacements, err := computeReplacements(originalLines, path, chunks)
	if err != nil {
		return "", err
	}
	updated := applyReplacements(originalLines, replacements)
	if len(updated) == 0 || updated[len(updated)-1] != "" {
		updated = append(updated, "")
	}
	return strings.Join(updated, "\n"), nil
}

func computeReplacements(originalLines []string, path string, chunks []updateFileChunk) ([]replacement, error) {
	var replacements []replacement
	lineIndex := 0
	for _, chunk := range chunks {
		if chunk.changeContext != "" {
			idx, ok := seekSequence(originalLines, []string{chunk.changeContext}, lineIndex, false)
			if !ok {
				return nil, fmt.Errorf("Failed to find context '%s' in %s", chunk.changeContext, path)
			}
			lineIndex = idx + 1
		}
		if len(chunk.oldLines) == 0 {
			insertion := len(originalLines)
			if len(originalLines) > 0 && originalLines[len(originalLines)-1] == "" {
				insertion = len(originalLines) - 1
			}
			replacements = append(replacements, replacement{start: insertion, oldLen: 0, newLines: append([]string(nil), chunk.newLines...)})
			continue
		}
		pattern := chunk.oldLines
		newSlice := chunk.newLines
		start, ok := seekSequence(originalLines, pattern, lineIndex, chunk.endOfFile)
		if !ok && len(pattern) > 0 && pattern[len(pattern)-1] == "" {
			pattern = pattern[:len(pattern)-1]
			if len(newSlice) > 0 && newSlice[len(newSlice)-1] == "" {
				newSlice = newSlice[:len(newSlice)-1]
			}
			start, ok = seekSequence(originalLines, pattern, lineIndex, chunk.endOfFile)
		}
		if !ok {
			return nil, fmt.Errorf("Failed to find expected lines in %s:\n%s", path, strings.Join(chunk.oldLines, "\n"))
		}
		replacements = append(replacements, replacement{start: start, oldLen: len(pattern), newLines: append([]string(nil), newSlice...)})
		lineIndex = start + len(pattern)
	}
	return replacements, nil
}

type replacement struct {
	start    int
	oldLen   int
	newLines []string
}

func applyReplacements(lines []string, replacements []replacement) []string {
	updated := append([]string(nil), lines...)
	for i := len(replacements) - 1; i >= 0; i-- {
		item := replacements[i]
		start := item.start
		for removed := 0; removed < item.oldLen; removed++ {
			if start < len(updated) {
				updated = append(updated[:start], updated[start+1:]...)
			}
		}
		tail := append([]string(nil), updated[start:]...)
		updated = append(updated[:start], item.newLines...)
		updated = append(updated, tail...)
	}
	return updated
}

func joinUnderWorkspace(root, cwd, name string) (string, error) {
	path := name
	if !filepath.IsAbs(path) {
		base := cwd
		if base == "" {
			base = root
		}
		path = filepath.Join(base, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve patch path: %w", err)
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("patch path escapes the workspace root: %s", name)
	}
	for _, protected := range []string{".git", ".codex"} {
		if relative == protected || strings.HasPrefix(relative, protected+string(filepath.Separator)) {
			return "", fmt.Errorf("patch path is protected: %s", relative)
		}
	}
	return absolute, nil
}
