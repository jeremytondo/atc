package linear

import (
	"fmt"
	"strings"

	"github.com/jeremytondo/atc/internal/api"
)

// What ATC says in Linear. Every text is honest about what ATC knows:
// nothing claims T3 started before T3 committed, nothing claims a stop
// happened, and a missing link is said to be missing rather than
// invented. Markdown, as Linear renders activity bodies.

// composePrompt is the Thread's first prompt: a short preamble telling the
// agent where the request came from and where its answer goes, then
// Linear's own promptContext — the issue, the thread, and the guidance,
// as Linear formatted them.
func composePrompt(identifier, url, promptContext string) string {
	where := "a Linear issue"
	if identifier != "" {
		where = "Linear issue " + identifier
	}
	if url != "" {
		where += " (" + url + ")"
	}
	return fmt.Sprintf("This request comes from a mention of @atc in %s. Answer the request in the most recent comment that mentions @atc. "+
		"Your final message is posted back to that Linear issue as the answer, so make it self-contained and readable on its own.\n\n%s",
		where, strings.TrimSpace(promptContext))
}

func acknowledgement() string {
	return fmt.Sprintf("ATC accepted this request and is starting a T3 Code conversation (%s, %s reasoning effort) in its configured project. "+
		"Links to the conversation follow once it is running; the answer is posted here when the run finishes.", profileModel, profileEffort)
}

func started(links *api.ThreadLinks) string {
	if links == nil {
		return "The T3 Code conversation is running. ATC has no link to it; open T3 Code to find it."
	}
	return "The T3 Code conversation is running. Open it to follow along, answer questions, approve actions, or stop the run:" + linkList(links)
}

func waitingForT3() string {
	return "T3 Code is not connected to ATC right now, so the conversation has not started. ATC starts it as soon as T3 Code is back; nothing else is needed from you."
}

func waiting(status api.ThreadStatus, links *api.ThreadLinks) string {
	what := "input"
	if status == api.ThreadWaitingForPermission {
		what = "an approval"
	}
	return fmt.Sprintf("The run is waiting for %s in T3 Code. ATC cannot relay that from Linear; open the conversation to continue it. ATC keeps watching and posts the answer here when the run finishes.%s", what, linkLines(links))
}

func followUpNotSupported(links *api.ThreadLinks) string {
	return "Follow-up messages from Linear are not supported yet, so this one was not sent to the agent. To continue the conversation, open it in T3 Code." +
		linkLines(links) + "\n\nTo start an independent run, mention @atc in a new comment."
}

func stopNotSupported(links *api.ThreadLinks) string {
	return "Stopping from Linear is not supported yet; the run has not been stopped. To stop it, open the conversation in T3 Code. ATC keeps watching and reports how the run ends." +
		linkLines(links)
}

func notTracked() string {
	return "ATC is not tracking a run for this session, so there is nothing to continue or stop here. Mention @atc in a new comment to start one."
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

func uncertain() string {
	return "ATC could not confirm whether T3 Code accepted the conversation: T3 Code did not answer in time, or ATC restarted while starting it. ATC is watching the conversation it recorded and reports what it learns here; nothing is started again automatically."
}

func responseRejected(links *api.ThreadLinks) string {
	return "The run finished, but Linear refused the response ATC tried to post (it may be too long). Open the conversation in T3 Code to read it." + linkLines(links)
}

func turnReplaced(links *api.ThreadLinks) string {
	return "A newer turn replaced the one started from this mention before ATC could deliver its answer, so that answer cannot be attributed here. Open the conversation in T3 Code to read it." + linkLines(links)
}

func threadGone() string {
	return "The conversation ATC started from this mention no longer exists in ATC, so its answer cannot be delivered here."
}

func threadDropped(links *api.ThreadLinks) string {
	return "T3 Code no longer reports the conversation started from this mention, so ATC cannot deliver its answer here." + linkLines(links)
}

func responseMissing(links *api.ThreadLinks) string {
	return "The run finished, but ATC could not recover its final response from T3 Code. Open the conversation to read it." + linkLines(links)
}

func turnFailed(detail string, links *api.ThreadLinks) string {
	text := "The run failed in T3 Code"
	if detail != "" {
		text += ": " + detail
	}
	return text + "." + linkLines(links)
}

func turnInterrupted(links *api.ThreadLinks) string {
	return "The run was stopped in T3 Code before it finished, so there is no answer to post." + linkLines(links)
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
