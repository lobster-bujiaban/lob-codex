package protocol

import "encoding/json"

// ResponseItem is the model-visible conversation item shared by session history
// and the Responses API. This stage implements Codex's message subset; later
// stages add reasoning and tool-call variants without changing history ownership.
type ResponseItem struct {
	Type             string        `json:"type"`
	ID               string        `json:"id,omitempty"`
	Role             string        `json:"role,omitempty"`
	Content          []ContentItem `json:"content,omitempty"`
	CallID           string        `json:"call_id,omitempty"`
	Name             string        `json:"name,omitempty"`
	Arguments        string        `json:"arguments,omitempty"`
	Output           string        `json:"output,omitempty"`
	Summary          []ContentItem `json:"summary,omitempty"`
	EncryptedContent string        `json:"encrypted_content,omitempty"`
}

// MarshalJSON keeps the Responses API's required empty summary array on
// reasoning items. omitempty is correct for every other item type, but an
// upstream reasoning item may legitimately arrive without summary entries.
func (item ResponseItem) MarshalJSON() ([]byte, error) {
	type responseItemAlias ResponseItem
	if item.Type != "reasoning" {
		return json.Marshal(responseItemAlias(item))
	}
	summary := item.Summary
	if summary == nil {
		summary = []ContentItem{}
	}
	return json.Marshal(struct {
		responseItemAlias
		Summary []ContentItem `json:"summary"`
	}{responseItemAlias: responseItemAlias(item), Summary: summary})
}

// NewFunctionCallOutput creates the model-visible result paired with one call.
func NewFunctionCallOutput(callID, output string) ResponseItem {
	return ResponseItem{Type: "function_call_output", CallID: callID, Output: output}
}

// ContentItem is one typed piece of message content.
type ContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// NewUserMessage converts accepted turn text into a model-visible user item.
func NewUserMessage(text string) ResponseItem {
	return NewUserMessageWithImages(text, nil)
}

// NewUserMessageWithImages converts text and clipboard images into one ordered
// user message, matching Codex's UserInput to ContentItem conversion boundary.
func NewUserMessageWithImages(text string, imageURLs []string) ResponseItem {
	content := make([]ContentItem, 0, 1+len(imageURLs))
	if text != "" {
		content = append(content, ContentItem{Type: "input_text", Text: text})
	}
	for _, imageURL := range imageURLs {
		content = append(content, ContentItem{Type: "input_image", ImageURL: imageURL})
	}
	return ResponseItem{
		Type:    "message",
		Role:    "user",
		Content: content,
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

func NewDeveloperMessage(text string) ResponseItem {
	return ResponseItem{Type: "message", Role: "developer", Content: []ContentItem{{Type: "input_text", Text: text}}}
}

// Text returns the concatenated text content carried by an item.
func (item ResponseItem) Text() string {
	text := ""
	for _, content := range item.Content {
		text += content.Text
	}
	return text
}
