# AtelierCode Agent Instructions

## Source Control
Jujutsu (jj) Protocol: You are in a jj repository; strictly do not use git add/commit/stash/checkout. When a logical step passes tests, checkpoint your work by running jj describe -m "<msg>" followed by jj new. If you write code that breaks the build, immediately run jj undo to revert before trying again.
