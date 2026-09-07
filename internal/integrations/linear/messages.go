package linear

import (
	"fmt"
	"strings"

	"github.com/jeremytondo/atc/internal/api"
)

// What ATC says in Linear. Every text is honest about what ATC knows:
// nothing claims T3 started before T3 committed, nothing claims a stop
// happened before the evidence, and a missing link is said to be missing
// rather than invented. Markdown, as Linear renders activity bodies.

// composePrompt is the Thread's first prompt: a short preamble telling the
// agent where the request came from and how the conversation continues,
// then Linear's own promptContext — the issue, the thread, and the
// guidance, as Linear formatted them.
func composePrompt(identifier, url, promptContext string) string {
	where := "a Linear issue"
	if identifier != "" {
		where = "Linear issue " + identifier
	}
	if url != "" {
		where += " (" + url + ")"
	}
	return fmt.Sprintf("This request comes from a mention of @atc in %s. Answer the request in the most recent comment that mentions @atc. "+
		"Your final message is posted back to that Linear issue as the answer, so make it self-contained and readable on its own. "+
		"The person can continue this conversation from Linear: their follow-up messages, answers to your questions, and approval decisions reach you the same way, "+
		"and each of your final messages is posted back.\n\n%s",
		where, strings.TrimSpace(promptContext))
}

func acknowledgement() string {
	return fmt.Sprintf("ATC accepted this request and is starting a T3 Code conversation (%s, %s reasoning effort) in its configured project. "+
		"Links to the conversation follow once it is running; the answer is posted here when the run finishes, and you can continue the conversation from here.", profileModel, profileEffort)
}

func started(links *api.ThreadLinks) string {
	if links == nil {
		return "The T3 Code conversation is running. ATC has no link to it; open T3 Code to find it."
	}
	return "The T3 Code conversation is running. Reply here to continue it once the answer is posted; questions and approvals appear here as they come. To follow along in T3 Code:" + linkList(links)
}

func waitingForT3() string {
	return "T3 Code is not connected to ATC right now, so the conversation has not started. ATC starts it as soon as T3 Code is back; nothing else is needed from you."
}

func notTracked() string {
	return "ATC is not tracking a conversation for this session, so there is nothing to continue or stop here. Mention @atc in a new comment to start one."
}

func sessionOver(outcome string) string {
	return fmt.Sprintf("This session's conversation ended (%s) and cannot be continued from here. Mention @atc in a new comment to start a new one.", outcome)
}

func refusedNoContext() string {
	return "ATC found nothing to act on: Linear supplied no context for this mention. Mention @atc with a question in an issue comment."
}

func refusedNoMention() string {
	return "ATC only starts work from an explicit @atc mention with a question in an issue comment. Delegating the issue does not start a run in this prototype."
}

func startFailed(err error) string {
	return fmt.Sprintf("ATC could not start the T3 Code conversation: %v. Nothing is running. Mention @atc again once the cause is fixed.", err)
}

func startCancelled() string {
	return "Stopped before the conversation started: nothing was sent to T3 Code, and nothing will be. Mention @atc in a new comment to start again."
}

func uncertain() string {
	return "ATC could not confirm whether T3 Code accepted the conversation: T3 Code did not answer in time, or ATC restarted while starting it. ATC is watching the conversation it recorded and reports what it learns here; nothing is started again automatically."
}

func responseRejected(links *api.ThreadLinks) string {
	return "The run finished, but Linear refused the response ATC tried to post (it may be too long). Open the conversation in T3 Code to read it." + linkLines(links)
}

func turnReplaced(links *api.ThreadLinks) string {
	return "A newer turn replaced this one in T3 Code before ATC could deliver its answer, so that answer cannot be attributed here. Open the conversation in T3 Code to read it." + linkLines(links)
}

func threadGone() string {
	return "The conversation behind this session no longer exists in ATC, so nothing more can be delivered here. Mention @atc in a new comment to start a new one."
}

func threadDropped(links *api.ThreadLinks) string {
	return "T3 Code no longer reports the conversation behind this session, so ATC cannot deliver its answer here." + linkLines(links)
}

func responseMissing(links *api.ThreadLinks) string {
	return "The run finished, but ATC could not recover its final response from T3 Code. Open the conversation to read it." + linkLines(links)
}

func turnFailed(detail string, links *api.ThreadLinks) string {
	text := "The run failed in T3 Code"
	if detail != "" {
		text += ": " + detail
	}
	return text + ". Reply here to continue the conversation." + linkLines(links)
}

func turnInterrupted(links *api.ThreadLinks) string {
	return "The run was stopped before it finished, so there is no answer to post. Reply here to continue the conversation." + linkLines(links)
}

// Submissions.

func messageSent() string {
	return "Sent to the agent. Its answer is posted here when this turn finishes."
}

func replySent() string {
	return "Your reply was sent to the agent as the answer to its question; the agent reads it as written."
}

func choiceSent(label string) string {
	return fmt.Sprintf("Your choice (%s) was sent to the agent.", label)
}

func decisionSent(label string) string {
	return fmt.Sprintf("Your decision (%s) was delivered to the agent.", label)
}

func emptyMessage() string {
	return "That message was empty, so nothing was sent."
}

func ambiguousChoice() string {
	return "Several open options share that text, so ATC could not tell which one you chose. Pick it from the options above, or reply in your own words."
}

func submissionRefused(what string, err error) string {
	return fmt.Sprintf("%s was not sent: %v.", what, err)
}

func replyStale(err error) string {
	return fmt.Sprintf("Your reply was addressed to a question that is no longer open (%v), so it was not sent as an answer — and not as a new message either. Send it again if it is still meant for the agent.", err)
}

func decisionStale(err error) string {
	return fmt.Sprintf("That decision was not delivered: %v. Nothing was approved or declined by it.", err)
}

func decisionUncertain(err error) string {
	return fmt.Sprintf("ATC could not confirm whether your decision reached the agent before the request closed (%v). Check the conversation in T3 Code if it matters.", err)
}

func stopSent() string {
	return "The stop was sent to T3 Code. ATC reports what it confirms: the work stopped, or already finished."
}

func stopOutcome(stop api.ThreadStop) string {
	detail := ""
	if stop.Detail != "" {
		detail = " (" + stop.Detail + ")"
	}
	switch stop.State {
	case api.StopStopped:
		return "Stopped: T3 Code confirmed the work was cut short" + detail + ". The conversation is kept; reply here to continue it."
	case api.StopFinished:
		return "Nothing was stopped: the work had already finished by the time the stop applied" + detail + ". Reply here to continue the conversation."
	case api.StopFailed:
		return "The stop failed: T3 Code refused it" + detail + ". The work was not stopped."
	}
	return "The stop is still being confirmed" + detail + "."
}

// Requests.

func approvalAsk(approval api.ThreadApproval) string {
	var b strings.Builder
	b.WriteString("The agent asks for approval")
	if approval.Kind != "" && approval.Kind != api.ApprovalUnknown {
		b.WriteString(" (" + strings.ReplaceAll(string(approval.Kind), "_", " ") + ")")
	}
	b.WriteString(": ")
	b.WriteString(strings.TrimSpace(approval.Summary))
	if detail := strings.TrimSpace(approval.Detail); detail != "" {
		b.WriteString("\n\n```\n" + detail + "\n```")
	}
	if approval.AppName != "" {
		b.WriteString("\n\nApp: " + approval.AppName)
	}
	if len(approval.Options) == 0 {
		b.WriteString("\n\nThe decisions it offers are none ATC can relay; open the conversation in T3 Code to decide.")
		return b.String()
	}
	b.WriteString("\n\nChoose one of the options. Only a choice decides it: a text reply is sent to the agent as a message and approves nothing.")
	return b.String()
}

func inputAsk(request api.ThreadInputRequest, links *api.ThreadLinks) string {
	if request.Unanswerable != "" {
		return "The agent asked something ATC cannot relay from here (" + request.Unanswerable + "). Open the conversation in T3 Code to answer." + linkLines(links)
	}
	var b strings.Builder
	if len(request.Questions) == 1 {
		b.WriteString("The agent asks:\n\n")
	} else {
		fmt.Fprintf(&b, "The agent asks %d questions:\n\n", len(request.Questions))
	}
	for i, q := range request.Questions {
		if len(request.Questions) > 1 {
			fmt.Fprintf(&b, "%d. ", i+1)
		}
		if q.Header != "" {
			b.WriteString("**" + q.Header + "**: ")
		}
		b.WriteString(strings.TrimSpace(q.Text))
		b.WriteString("\n")
		for _, o := range q.Options {
			b.WriteString("   - " + o.Label)
			if o.Description != "" {
				b.WriteString(" — " + o.Description)
			}
			b.WriteString("\n")
		}
		if q.AllowsMultiple {
			b.WriteString("   (several choices allowed)\n")
		}
	}
	b.WriteString("\nPick an option, or reply in your own words: your text goes to the agent as written, and the agent reads it against its questions.")
	return b.String()
}

func requestClosed(kind string, request api.ThreadInputRequest, decided string) string {
	switch {
	case kind == kindApproval && decided != "":
		return "The approval request was decided from here (" + decided + ")."
	case kind == kindApproval:
		return "The approval request was resolved elsewhere — in T3 Code, or by a stop — so it no longer takes a decision from here."
	case request.Resolution == api.InputResolvedByAnswer:
		return "The agent received your answer to its question."
	case request.Resolution == api.InputResolvedByStop:
		return "The question was closed by the stop."
	case request.Answer != nil && request.Answer.State == api.InputAnswerSuperseded:
		return "The question was resolved elsewhere before your answer reached it (" + request.Answer.Detail + "), so your answer was set aside."
	}
	return "The question was resolved elsewhere — in T3 Code — so it no longer takes an answer from here."
}

func answerFailed(detail string) string {
	return "The agent's question is still open: T3 Code could not deliver your answer (" + detail + "). Answer again, or reply in your own words."
}

// linkLines appends the links as a paragraph, or says there are none.
func linkLines(links *api.ThreadLinks) string {
	if links == nil {
		return "\n\nATC has no link to the conversation yet."
	}
	return "\n\nOpen in T3 Code:" + linkList(links)
}

func linkList(links *api.ThreadLinks) string {
	return fmt.Sprintf("\n- Web: %s\n- Desktop: %s", links.Web, links.App)
}
