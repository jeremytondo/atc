package api

import (
	"bytes"
	"encoding/json"
)

// Optional is the three-state field every PATCH body uses (ATC-295), the
// JSON Merge Patch reading of RFC 7386: a field omitted from the body is
// unchanged, a value replaces, and an explicit null clears. Go pointers
// with omitempty cannot tell null from omitted — encoding/json decodes
// both to nil — so presence is carried explicitly, and a body field
// declares itself `json:"name,omitzero"` so an unset Optional is omitted
// when a client marshals the params.
//
// On the wire an Optional[T] is exactly a nullable T; the server
// registers it as such in the OpenAPI document, and fields that refuse
// null say so with a nullable:"false" tag.
type Optional[T any] struct {
	// Set reports that the field was present in the body; Value is then
	// the value, or nil for an explicit null.
	Set   bool
	Value *T
}

// Some is an Optional carrying a value.
func Some[T any](value T) Optional[T] {
	return Optional[T]{Set: true, Value: &value}
}

// Clear is an Optional carrying an explicit null.
func Clear[T any]() Optional[T] {
	return Optional[T]{Set: true}
}

// Null reports an explicit null.
func (o Optional[T]) Null() bool { return o.Set && o.Value == nil }

// IsZero reports an unset Optional, so omitzero omits it.
func (o Optional[T]) IsZero() bool { return !o.Set }

func (o Optional[T]) MarshalJSON() ([]byte, error) {
	if o.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*o.Value)
}

// UnmarshalJSON is invoked for present fields only — encoding/json hands
// Unmarshalers the literal null — so presence is exactly "was called".
func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	o.Value = nil
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.Value = &value
	return nil
}
