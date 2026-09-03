package api

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The three states survive a round trip: omitted stays unset (and is
// omitted again on marshal), null is set-and-null, a value is set.
func TestOptionalMergePatchStates(t *testing.T) {
	type body struct {
		Title   Optional[string] `json:"title,omitzero"`
		Project Optional[string] `json:"project,omitzero"`
		Flag    Optional[bool]   `json:"flag,omitzero"`
	}
	var got body
	if err := json.Unmarshal([]byte(`{"project":null,"flag":true}`), &got); err != nil {
		t.Fatal(err)
	}
	want := body{Project: Clear[string](), Flag: Some(true)}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("decoded (-want +got):\n%s", diff)
	}
	if got.Title.Set || !got.Project.Null() || got.Flag.Null() || !*got.Flag.Value {
		t.Errorf("state accessors: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"project":null,"flag":true}` {
		t.Errorf("encoded = %s; want the unset field omitted and null preserved", encoded)
	}
	if encoded, _ := json.Marshal(body{Title: Some("x")}); string(encoded) != `{"title":"x"}` {
		t.Errorf("encoded value = %s", encoded)
	}
}
