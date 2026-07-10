# Atelier Code Agent Instructions

## Core Priorities
- Performance
- Reliability
- Simplicity
- User Experience

If a tradeoff is required, choose correctness and robustness over short-term convenience.

## Maintainability

Long term maintainability is a core priority. If you add new functionality, first check if there is shared logic that can be extracted to a separate module. Duplicate logic across multiple files is a code smell and should be avoided. Don't be afraid to change existing code. Don't take shortcuts by just adding local logic to solve a problem.

## Source Control

Jujutsu (jj) Protocol: You are in a jj repository; strictly do not use git
add/commit/stash/checkout. When a logical step passes tests, checkpoint your
work by running `jj describe -m "<msg>"` followed by `jj new`. If you write
code that breaks the build, immediately run `jj undo` to revert before trying
again. To push a branch, use a jj bookmark and push it to the git remote. To
create a PR push a branch and then create a PR in GitHub. Follow JJ best
practices.

## Reference Apps 

Use the follwing apps as references and inspiration of similar projects. Can be used for design and UX inspiration as well as code and architecture ideas.

T3Code (https://github.com/pingdotgg/t3code/): Great user expereience and similar feature set.
Codex Desktop App (https://chatgpt.com/codex): Greate user experience
AGTerm (https://github.com/umputun/agterm): Great LibGhostty app with a lot of similar features.

## Code Style

- Always strive for simplicity. This is not a complex enterprise app.
- Code readability is critical. Code should be easily understandable by
  developers coming into the project.
- Developer ergonoics is important. It should be easy for developers to work with and test the codebase.

## Model Selection

When starting sub agents or running workflows, be smart about which agents to choose in order to save on token cost. Use agents like Opus, Sonnet, Terra, or Luna when it makes sense. Always review and check their work.

