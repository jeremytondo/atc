# atc Idea

> **Archived:** This was the original broad product idea document. Current
> implementation work should use active specs under `docs/specs/` and ADRs under
> `docs/adr/`.
 
Ideation, planning, and AI coding orchestration platform. 
 
## Planning and Workflow First
Instead of starting with coding sessions, we put planning and workflow first. We leave the coing to the AI agent harnesses. Users define ideas and features, create and organize tasks/issues, and manage moving those through workflows that include research and definition, developing briefs and specs, coding, git management, etc.
 
## Coding
The app will eventually support coding, however it will be less of a hands on environment and more of of an orchestartor. It will allow handing off an item/task to an AI agent of choice, and that agent will do the work via their native harness. We still need to work out the details of how this works, but at the moment I’m thinking of possibly using ACP (Agent Client Protocol) to support this workflow. The one downside to this is that Claude Code subscriptions do not work with ACP due to their new pricing model. I don’t see this as a full coding agent harness though. A user would hand off the task to the agent. They could then monitor progress and respond to prompts. After coding is down we would supply a robust code review enfironment.
 
## Code Review
I’d like to eventually support a robust code review environment. We could use tools like [Diffs](https://diffs.com/) and [Trees](https://trees.software/) from the Pierre Computer Co. These seem like solid tools to faciliate exploring codebases and codebase diffs. I’d also like to eventually support a code review commenting feature that allows making a comment on a line or block of code and allowing an AI agent to take action on it.
 
## App Primitives
The app should make use of certain primitives which will be the building blocks for all of the organization and work the app does. 
 
* Project
* Item
* Environment
 
### Project
This is the top level organizational unit. It’s basically a name and some meta data. It’s tied to any number of items and can be optionally tied to an environment.
 
### Item
This is the bulk of where things are done in the app. This is essentially a markdown file that is tied to a project. It also contains a set of meta data to help organize. Some features I may want to eventually implement as part of an item:
 
- Tags - Used for flexible organization 
- History - The ability to keep track of changes with git style diffs and rollbacks. 
 
### Environment 
Environments are used to support local coding for a project. At a base level an environment is just a path to a local folder that contains a codebase. In the future we may be able to integrate this with something like Mise and/or containers to allow more control over the environment from within atc.
 
## Workflows
I have a particular workflow I like to use, but I think I’d like to keep things simple, provide the base level of features and allow flexibility to change up the workflow. In general though, here are examples of two common workflows I use.
 
### Planning Flow
 
Idea —> Brief —> Spec —> Tasks
 
### Building Flow
Start with a new task:
 
Branch —> Code —> PR —> Review —> Update —> Merge
 
### Greenfield
Coming up with new ideas for net new apps should be a supported workflow. I think the key here is that a project can be started without an environment being created. This allows doing some research and documentation up front before writing any code. In this flow you would create a project and then likely create an item within it that is tagged as an idea. This would contain markdown notes about the right shape of the idea. We research and define those ideas until we’re ready to create a brief, a spec, and start coding.
 
## CLI & API First
The idea here is not that the CLI would be the main UI, it won’t. However, if we have that integrated from the start, it gives AI agents a way to interact with the platform. The API then allows the real UIs like web and iOS to connect.
 
### Open Questions
- Could we create agent skills to connect directly to the api or would it be better to have a cli?
 
## Architecture 
The bulk of this will be a Go app. Mainly because that’s what I know and it fits well for this use case. 
 
- Core - The brains and core app functionality. 
- API - Wraps the core functionality. Needed to allow iOS applications 
- CLI - At a minimum this is needed to do things like start up and administer the app. Open question is if we also wrap core functionality and match the api to give ai agents cli based access. 
- Web - Eventual locally hosted web interface.
- IOS - Eventual iPhone and iPad apps that connect using the API over Tailscale.
