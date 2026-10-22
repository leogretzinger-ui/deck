# ccparse

A small library that parses one conventional commit subject.

## Who it is for

Tools that read git history and need to know a commit's type, scope and whether it is a breaking change. `relnotes` is the first user.

## How it is used

```js
import { parse } from "./index.mjs";
parse("feat(api)!: drop v1");
// { type: "feat", scope: "api", breaking: true, description: "drop v1" }
```

`parse` takes the subject line as a string and returns an object. It never throws. A subject that does not follow the form returns `{ type: null, scope: null, breaking: false, description: <the subject unchanged> }`.

## The form

```
<type>(<scope>)!: <description>
```

The scope and the `!` are optional. Type is one or more lowercase letters. Scope is anything but a closing parenthesis. The colon is followed by one space. Leading and trailing whitespace on the subject is ignored.

## Requirements

1. Parse the type, optional scope and description. Done, with tests.

2. **Set `breaking` to true when the subject has `!` before the colon**, with or without a scope: `feat!: x` and `feat(api)!: x` are both breaking. Today the `!` is dropped and `breaking` is always false. There is no test for this yet; add one.

## Releases

Each change to the parsing rules is a new version in `package.json` and a git tag with the same number. Users pin a version and update it on purpose.

## Out of scope

Parsing the commit body. Bodies are the caller's business; `relnotes` looks for `BREAKING CHANGE:` itself.
