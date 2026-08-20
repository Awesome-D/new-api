package dto

import (
	"bytes"
	"encoding/json"
)

// UnmarshalJSON accepts OpenAI-compatible Responses payloads whose created_at
// is emitted as a JSON float (for example 1786588600.0). The public struct keeps
// CreatedAt as int so existing conversion code and serialized output remain
// unchanged.
func (o *OpenAIResponsesResponse) UnmarshalJSON(data []byte) error {
	type responseAlias OpenAIResponsesResponse
	aux := struct {
		CreatedAt json.RawMessage `json:"created_at"`
		*responseAlias
	}{
		responseAlias: (*responseAlias)(o),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	createdAt := bytes.TrimSpace(aux.CreatedAt)
	if len(createdAt) == 0 || bytes.Equal(createdAt, []byte("null")) {
		o.CreatedAt = 0
		return nil
	}

	var value IntValue
	if err := json.Unmarshal(createdAt, &value); err != nil {
		return err
	}
	o.CreatedAt = int(value)
	return nil
}
