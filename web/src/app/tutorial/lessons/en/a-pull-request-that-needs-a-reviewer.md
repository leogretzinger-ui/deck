---
id: reviewer
title: A pull request that needs a reviewer who isn't me
group: explore
duration: 4 min
requires: [github]
---

# A pull request that needs a reviewer who isn't me

## Why a bot identity
advance_on: continue

GitHub won't let an account approve its own pull request. If every agent signs in as you, a review from another model can only be a comment. A separate bot identity, such as noise-review[bot], can approve.

## What the tutorial does
advance_on: continue

Approvals from a bot identity need the noise review GitHub app and its command line tool. The tutorial doesn't set these up, so the review you request next is a comment.

## Request a review
advance_on: continue
primary: Request review
action: request-review

Choose Request review, read the prompt it puts in the terminal, and press Return. The agent posts its findings on the pull request as a comment.
