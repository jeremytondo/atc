# AtelierCode Agent Instructions

## Source Control
Jujutsu (jj) Protocol: You are in a jj repository; strictly do not use git add/commit/stash/checkout. When a logical step passes tests, checkpoint your work by running jj describe -m "<msg>" followed by jj new. If you write code that breaks the build, immediately run jj undo to revert before trying again. To push a branch, use a jj bookmark and push it to the git remote. To create a PR push a branch and then create a PR in GitHub. Follow JJ best practices.

## Cockpit Server
This app is driven mostly by a remote server called Cockpit. The code for it can be found here:

https://github.com/jeremytondo/cockpit

It's running on the remote workstation defined in ~/.ssh/config. You can ssh into that machine to control it if needed.

