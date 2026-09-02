package appserver

import (
	"context"
	"encoding/json"
)

// ModelListParams is the app-server model/list request. The fields are pointers
// in the protocol because omitted and false/null have distinct meanings; the
// bridge only needs the default picker list and asks for a bounded first page.
type ModelListParams struct {
	Cursor        *string `json:"cursor,omitempty"`
	IncludeHidden *bool   `json:"includeHidden,omitempty"`
	Limit         *uint32 `json:"limit,omitempty"`
}

type ReasoningEffortOption struct {
	ReasoningEffort string `json:"reasoningEffort"`
	Description     string `json:"description"`
}

// ModelInfo mirrors only the model/list fields needed by the phone. Unknown
// fields from newer Codex versions are intentionally ignored by encoding/json.
type ModelInfo struct {
	ID                        string                  `json:"id"`
	Model                     string                  `json:"model"`
	DisplayName               string                  `json:"displayName"`
	Description               string                  `json:"description"`
	Hidden                    bool                    `json:"hidden"`
	IsDefault                 bool                    `json:"isDefault"`
	SupportedReasoningEfforts []ReasoningEffortOption `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    string                  `json:"defaultReasoningEffort"`
}

type ModelListResult struct {
	Data       []ModelInfo `json:"data"`
	NextCursor *string     `json:"nextCursor"`
}

// ModelList returns the models currently exposed by this Codex app-server.
// Pagination is followed defensively so a future desktop release can expose
// more than one page without requiring another phone update.
func (c *Client) ModelList(ctx context.Context) ([]ModelInfo, error) {
	var all []ModelInfo
	var cursor *string
	includeHidden := false
	limit := uint32(100)
	for page := 0; page < 20; page++ {
		res, err := c.Call(ctx, "model/list", ModelListParams{
			Cursor: cursor, IncludeHidden: &includeHidden, Limit: &limit,
		})
		if err != nil {
			return nil, err
		}
		var out ModelListResult
		if err := json.Unmarshal(res, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Data...)
		if out.NextCursor == nil || *out.NextCursor == "" {
			return all, nil
		}
		cursor = out.NextCursor
	}
	return all, nil
}
