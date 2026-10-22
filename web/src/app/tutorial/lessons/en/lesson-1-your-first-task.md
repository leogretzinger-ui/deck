---
id: lesson-1
title: Your first task
group: course
duration: 8 min
requires: []
principles: [1, 6]
---

# Your first task

## Choose a coding agent
advance_on: agent_picked
primary: Choose an agent…
action: choose-agent
footer: Agents here can read everything under /workspace. [What that means](lesson:what-an-agent-can-reach)

Pick the agent you have a subscription for.

## Create the practice projects
advance_on: starter_created
primary: Create relnotes and start {agent}
action: create-starter
fallback_when: no_git_identity
fallback: Commits as [Set a Git identity…](action:git-identity)
fallback_action: git-identity

relnotes is a small practice project that turns a git log into release notes. Create it to begin.

## Sign in to {agent}
advance_on: session_signed_in
when: agent_needs_login

Sign in to {agent} in the page it opened in your browser, then come back. If the page ends on a localhost address that won't load, copy that address and paste it into Deck when it asks.

## Turn the PRD into tasks
advance_on: task_exists(requirement=1)
opens: tasks
primary: Add PRD… → Generate tasks
action: generate-tasks
generate_count: 2
primary_note: Uses your {agent} plan.
fallback: Add the first task by hand
fallback_action: add-task(requirement=1)
fallback_primary_when: agent_cannot_generate_tasks

Add the PRD that comes with relnotes. Deck turns it into two tasks.

## Ask {agent} to build it
advance_on: input_sent then session_busy
primary: Put it in the terminal
action: insert-prompt
target: relnotes
busy_text: {agent} is working. This usually takes a minute.
prompt: |
  Implement requirement 1 in PRD.md: print the Breaking changes section for commits whose body has a line starting with "BREAKING CHANGE:". Change only relnotes.mjs. Leave relnotes.test.mjs alone, run node --test relnotes.test.mjs, and tell me the result.

Deck puts a ready-made prompt for the first task in the terminal. Read it, then press Return to send it.

## See what changed
advance_on: file_opened(modified)
opens: files

Open a file {agent} changed. Changed files are marked Modified.

## Check that it works
advance_on: check_exited
primary: Put in a terminal
action: run-command
target: relnotes
command: node --test relnotes.test.mjs
pass_text: 2 tests passed
fail_text: 1 test failed. Look at the terminal, or ask {agent} to fix it.

Run the tests in a plain terminal to check the work.

## Read the diff and close the task
advance_on: task_done
command: git diff
primary: Put in a terminal
action: run-command
target: relnotes
fallback: Mark task done
fallback_action: mark-task-done

Read the diff in a terminal to see every line {agent} changed. When you're happy with it, mark the task done.

## done
next: lesson-2

Lesson 1 is done. relnotes now prints a Breaking changes section, with a test to prove it, and you made it happen.
