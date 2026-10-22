---
id: lesson-3
title: Your own code
group: course
duration: 8 min
requires: [github]
principles: [1, 2, 4]
---

# Your own code

## Bring your own code
advance_on: project_opened(source=github)
primary: Add project…
action: add-project

Import one of your own repositories. Pick one small enough that a single change is easy to check.

## Give it a brief
advance_on: task_exists
opens: tasks
primary: Add PRD… → Generate tasks
action: generate-tasks
primary_note: Uses your {agent} plan.
fallback: Add task…
fallback_action: add-task
fallback_primary_when: agent_cannot_generate_tasks

Write a short brief for a change you'd like, and Deck turns it into tasks. You can also add one task by hand. If nothing comes to mind, try: generate release notes for the last tag with relnotes.

## Give it a branch
advance_on: lane_session_attached
primary: Start a lane
action: start-lane

Choose Start a lane. Deck creates a branch for the task and opens a session on it.

## Ask for the change
advance_on: input_sent then session_busy
primary: Put it in the terminal
action: insert-prompt
target: lane
busy_text: {agent} is working.
prompt: |
  You're on a branch for one task: "{task.title}". {task.description} Make the change, keep it small, run the tests, and open a pull request when they pass. Tell me the pull request number.

Deck puts a prompt built from your task in the terminal. Read it, then press Return to send it.

## A reviewer that isn't the author
advance_on: review_seen
primary: Request review
action: request-review
note_when: no_review_app
note: This posts a comment, not a formal approval. For reviews that count as approvals, see the Explore chapter "A pull request that needs a reviewer who isn't me".

Choose Request review. A different agent starts on this branch, and you press Return to send it a prompt that asks it to review the pull request.

## Finish
advance_on: task_done
primary: Mark task done
action: mark-task-done

Merge the pull request, then mark the task done.

## See your release notes
advance_on: check_exited
when: starter_exists
primary: Put in a terminal
action: run-command
target: source
command: node {starter.relnotes}/relnotes.mjs
command_note: Runs relnotes from the practice project against this repository.

Run relnotes from lesson 1 on this repository to see the release notes for the commits since your last tag.

## done
next: all-lessons

Lesson 3 is done. You landed a change on your own repository, from its own branch, with a different model reviewing it. You're set up to work this way for real now.
