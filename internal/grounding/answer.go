package grounding

import (
	"errors"
	"regexp"
	"strings"
)

var (
	errNoCitation       = errors.New("answer does not cite any retrieved source")
	errUnknownCitation  = errors.New("answer contains a citation outside the retrieval allowlist")
	chunkPattern        = regexp.MustCompile(`\b(?:doc-[A-Za-z0-9_-]+|chunk-[A-Za-z0-9_-]+)\b`)
	markdownFilePattern = regexp.MustCompile(`[A-Za-z0-9_.-]+\.md`)
)

func ValidateAnswer(answer string, observation Observation) error {
	if !observation.Usable {
		return errors.New("retrieval evidence is not usable")
	}
	allowedChunks := make(map[string]struct{}, len(observation.Items))
	allowedFiles := make(map[string]struct{}, len(observation.Items))
	for _, item := range observation.Items {
		allowedChunks[item.Citation.ChunkID] = struct{}{}
		allowedFiles[item.Citation.FileName] = struct{}{}
	}
	seenAllowedChunk := false
	seenAllowedFile := false
	for _, chunk := range chunkPattern.FindAllString(answer, -1) {
		if _, ok := allowedChunks[chunk]; !ok {
			return errUnknownCitation
		}
		seenAllowedChunk = true
	}
	for _, fileName := range markdownFilePattern.FindAllString(answer, -1) {
		if _, ok := allowedFiles[fileName]; !ok {
			return errUnknownCitation
		}
		seenAllowedFile = true
	}
	if !seenAllowedChunk || !seenAllowedFile {
		return errNoCitation
	}
	return nil
}

func NeedsNoteRetrieval(prompt string) bool {
	normalized := strings.ToLower(strings.TrimSpace(prompt))
	for _, marker := range []string{
		"之前", "以前", "记录", "笔记", "我曾", "我说过", "我提到过", "我的偏好",
		"previous note", "my note", "my record", "i mentioned", "i said before",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
