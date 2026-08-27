package protocol

// ResponseItem is the model-visible conversation item shared by session history
// and the Responses API. This stage implements Codex's message subset; later
// stages add reasoning and tool-call variants without changing history ownership.
type ResponseItem struct {
	Type    string        `json:"type"`
	Role    string        `json:"role"`
	Content []ContentItem `json:"content"`
}

// ContentItem is one typed piece of message content.
type ContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// NewUserMessage converts accepted turn text into a model-visible user item.
func NewUserMessage(text string) ResponseItem {
	return ResponseItem{
		Type: "message",
		Role: "user",
		Content: []ContentItem{{
			Type: "input_text",
			Text: text,
		}},
	}
}

// NewAssistantMessage creates the completed assistant item recorded after sampling.
func NewAssistantMessage(text string) ResponseItem {
	return ResponseItem{
		Type: "message",
		Role: "assistant",
		Content: []ContentItem{{
			Type: "output_text",
			Text: text,
		}},
	}
}

// Text returns the concatenated text content carried by an item.
func (item ResponseItem) Text() string {
	text := ""
	for _, content := range item.Content {
		text += content.Text
	}
	return text
}
