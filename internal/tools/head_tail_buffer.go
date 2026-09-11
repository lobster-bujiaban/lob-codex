package tools

import "fmt"

// headTailBuffer preserves a stable prefix and newest suffix while omitting
// bytes from the middle, matching Codex unified exec output collection.
type headTailBuffer struct {
	maxBytes     int
	head         []byte
	tail         []byte
	omittedBytes int
}

func newHeadTailBuffer(maxBytes int) *headTailBuffer {
	return &headTailBuffer{maxBytes: max(maxBytes, 0)}
}

func (buffer *headTailBuffer) pushChunk(chunk []byte) {
	headBudget := buffer.maxBytes / 2
	tailBudget := buffer.maxBytes - headBudget
	remainingHead := max(headBudget-len(buffer.head), 0)
	headLength := min(len(chunk), remainingHead)
	buffer.head = append(buffer.head, chunk[:headLength]...)
	chunk = chunk[headLength:]

	remainingTail := max(tailBudget-len(buffer.tail), 0)
	excess := max(len(chunk)-remainingTail, 0)
	buffer.omittedBytes += excess
	if excess < len(buffer.tail) {
		buffer.tail = append(buffer.tail[:0], buffer.tail[excess:]...)
	} else {
		skip := excess - len(buffer.tail)
		buffer.tail = buffer.tail[:0]
		chunk = chunk[skip:]
	}
	buffer.tail = append(buffer.tail, chunk...)
}

func (buffer *headTailBuffer) totalBytes() int {
	return len(buffer.head) + len(buffer.tail) + buffer.omittedBytes
}

func (buffer *headTailBuffer) bytesWithOmissionMarker() []byte {
	if buffer.omittedBytes == 0 {
		result := append([]byte(nil), buffer.head...)
		return append(result, buffer.tail...)
	}
	marker := fmt.Sprintf("\n... %d bytes omitted ...\n", buffer.omittedBytes)
	result := make([]byte, 0, len(buffer.head)+len(marker)+len(buffer.tail))
	result = append(result, buffer.head...)
	result = append(result, marker...)
	return append(result, buffer.tail...)
}

func (buffer *headTailBuffer) withLimit(maxBytes int) *headTailBuffer {
	if maxBytes >= len(buffer.head)+len(buffer.tail) {
		return buffer
	}
	limited := newHeadTailBuffer(maxBytes)
	limited.pushChunk(buffer.head)
	limited.pushChunk(buffer.tail)
	limited.omittedBytes += buffer.omittedBytes
	return limited
}
