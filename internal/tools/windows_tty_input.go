package tools

// windowsTtyInputNormalizer matches Codex WindowsTtyInputNormalizer:
// LF and split CRLF become one Enter (\r), backspace becomes DEL, other bytes pass through.
type windowsTtyInputNormalizer struct {
	previousWasCR bool
}

func (normalizer *windowsTtyInputNormalizer) normalize(data []byte) []byte {
	normalized := make([]byte, 0, len(data))
	for _, b := range data {
		switch b {
		case '\x08':
			normalized = append(normalized, '\x7f')
		case '\n':
			if !normalizer.previousWasCR {
				normalized = append(normalized, '\r')
			}
		default:
			normalized = append(normalized, b)
		}
		normalizer.previousWasCR = b == '\r'
	}
	return normalized
}
