package linear

import (
	"encoding/json"
	"strings"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/store"
)

// Presented requests: what a session was offered, kept durably so a
// selection is matched against the exact options ATC put in front of
// the person — the ATC request, the question, and the choice or decision
// each stands for. Linear hands a selected option back as an ordinary
// prompt activity; ATC recognizes an option's value (the token it
// minted) on any request it ever presented, so a selection on a request
// since closed keeps its target and is refused as stale rather than
// re-routed. A question's choice is also recognized by its label while
// the question is open — typing the choice is choosing it — but a
// decision never is: only the exact value decides.

// presented is a request row as stored.
type presented = store.LinearRequest

// option is one selectable option as offered: what Linear shows and
// returns, and what it stands for.
type option struct {
	Label string `json:"label"`
	Value string `json:"value"`
	// QuestionID and Choice name a question's choice; Decision an
	// approval's decision.
	QuestionID string `json:"questionId,omitempty"`
	Choice     string `json:"choice,omitempty"`
	Decision   string `json:"decision,omitempty"`
}

// target is what a selection stands for: the request, and its option's
// question and choice, or decision.
type target struct {
	questionID string
	choice     string
	decision   string
}

func (o option) target() target {
	return target{questionID: o.QuestionID, choice: o.Choice, decision: o.Decision}
}

// approvalOptions are the decisions an approval request offers, each
// under a value naming the exact request and decision.
func approvalOptions(approval api.ThreadApproval) []option {
	options := make([]option, 0, len(approval.Options))
	for _, o := range approval.Options {
		label := o.Label
		if label == "" {
			label = string(o.Decision)
		}
		options = append(options, option{Label: label, Value: approval.ID + "/" + string(o.Decision), Decision: string(o.Decision)})
	}
	return options
}

// inputOptions are the choices an input request's questions offer, each
// under a value naming the exact request, question, and choice; with
// several questions the label names the question too.
func inputOptions(request api.ThreadInputRequest) []option {
	var options []option
	for _, q := range request.Questions {
		for _, o := range q.Options {
			label := o.Label
			if label == "" {
				label = o.Value
			}
			if len(request.Questions) > 1 {
				prefix := q.Header
				if prefix == "" {
					prefix = q.ID
				}
				label = prefix + ": " + label
			}
			options = append(options, option{Label: label, Value: request.ID + "/" + q.ID + "/" + o.Value, QuestionID: q.ID, Choice: o.Value})
		}
	}
	return options
}

func encodeOptions(options []option) string {
	data, _ := json.Marshal(options)
	return string(data)
}

func decodeOptions(data string) []option {
	var options []option
	_ = json.Unmarshal([]byte(data), &options)
	return options
}

// selectOptions is the select signal's view of the options.
func selectOptions(options []option) []selectOption {
	out := make([]selectOption, 0, len(options))
	for _, o := range options {
		out = append(out, selectOption{Label: o.Label, Value: o.Value})
	}
	return out
}

// match finds the option a prompt's text selects: by value on any
// request presented, open or closed; by label on an open question's
// choice, when exactly one carries it. Ambiguous reports a label
// several open choices share; found is false for a text that is
// neither, which is the person's own words.
func match(requests []presented, text string) (requestID string, t target, found, ambiguous bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", target{}, false, false
	}
	var labelled []presented
	var byLabel []target
	for _, request := range requests {
		for _, o := range decodeOptions(request.Options) {
			if o.Value == text {
				return request.ID, o.target(), true, false
			}
			if request.State == requestOpen && request.Kind == kindInput && o.Label == text {
				labelled = append(labelled, request)
				byLabel = append(byLabel, o.target())
			}
		}
	}
	switch len(byLabel) {
	case 0:
		return "", target{}, false, false
	case 1:
		return labelled[0].ID, byLabel[0], true, false
	default:
		return "", target{}, false, true
	}
}

// label is the label an option was offered under, for reporting a
// choice back; the choice or decision itself when no longer on record.
func label(requests []presented, requestID string, t target) string {
	for _, request := range requests {
		if request.ID != requestID {
			continue
		}
		for _, o := range decodeOptions(request.Options) {
			if o.target() == t {
				return o.Label
			}
		}
	}
	if t.decision != "" {
		return t.decision
	}
	return t.choice
}
