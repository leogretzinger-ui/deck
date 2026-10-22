# relnotes

A command-line tool that writes release notes from a git log.

## Who it is for

Anyone who tags releases and has to write "what changed" by hand. The person runs it in a repository, pastes the output into the release, and edits from there.

## How it is used

```
node relnotes.mjs v1.2.0..HEAD
```

It reads the commit subjects and bodies in that range and prints Markdown to standard output. With no range it uses the commits since the most recent tag.

## What it prints

A heading per commit type, in this order: Breaking changes, Features, Fixes, Other. Under each heading, one bullet per commit: the scope in parentheses when there is one, then the description. Commits whose subject does not follow the conventional commit form go under Other with the subject as written. Empty sections are left out.

Example:

```
## Breaking changes
- (api) drop v1 endpoints

## Features
- (cli) accept a range argument

## Fixes
- handle an empty log
```

## How it parses commits

relnotes does not parse commit subjects itself. It imports `ccparse`, a separate project, and pins the version it expects in `ccparse.pin`. The pinned version is updated deliberately, after the new version is tagged.

## Requirements

There are two requirements and they are two tasks. The second depends on the first.

1. **Print the Breaking changes section.** A commit is breaking when its body contains a line starting with `BREAKING CHANGE:`. Those commits appear under Breaking changes, and nowhere else, with the text after `BREAKING CHANGE:` as the bullet. Today the section is never printed. `relnotes.test.mjs` has a failing test for this.

2. **Treat the `!` marker as breaking.** A subject such as `feat(api)!: drop v1` is breaking even when the body says nothing. These commits appear under Breaking changes with the subject's description as the bullet. Today they are listed as plain features. One task covers finding the cause and fixing it. This depends on requirement 1. `breaking-marker.test.mjs` has a failing test for this; the cause may not be in this project.

## Out of scope

Reading GitHub, linking to pull requests, or editing the notes after they are printed.
