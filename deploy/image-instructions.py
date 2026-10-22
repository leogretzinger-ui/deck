#!/usr/bin/env python3
"""Print the instruction graph one Dockerfile target is built from.

A stage graph hides what a target is made of. Docker builds one by running
its base stage's instructions and then its own — and, wherever the target
copies from another stage, by building that stage too. This prints all of
it as one ordered list: the parser directives, the file's global scope, then
each stage in build order, each introduced by its own FROM line, with
comments dropped and line continuations joined.

What the stream covers, so that a reordering or a restructuring cannot hide
a changed instruction inside it:

  * the FROM ancestry, each stage's base named as written, digest included;
  * every stage reached by `COPY --from=<stage>` or `RUN --mount=…,from=<stage>`,
    transitively — a build stage that produces a binary the target copies is
    part of that build even though nothing inherits from it;
  * global ARG defaults, which are what a FROM line interpolates;
  * the `# syntax` parser directive, which selects the frontend that
    interprets the whole file.

A stage is emitted once, before the first stage that needs it. External
references (`COPY --from=nginx:latest`) are not stages and are left as the
instruction text that names them.

WHAT EQUAL STREAMS DO AND DO NOT MEAN. Two equal streams mean the two files
describe the same instruction graph for that target. They do not mean the
two builds produce the same image, and no reading of a Dockerfile could
establish that: the bytes `COPY` takes from the build context, what
`.dockerignore` withholds from it, build args and secrets supplied on the
command line, every remote input a `RUN` fetches from a registry or a URL,
and whatever a base image tag resolves to are all outside the file. Image
content is established by building both variants and probing them, which is
what the release-acceptance workflow does.

Instruction-graph equivalence is nevertheless the exact evidence for a
claim like "these instructions moved between stages without changing", which
is what a variant split is.

    deploy/image-instructions.py Dockerfile runtime

The deploy suite uses it to pin the variant graph; a reviewer can use it to
diff a restructured Dockerfile against the previous one:

    git show origin/main:Dockerfile >/tmp/before
    diff <(deploy/image-instructions.py /tmp/before runtime) \
         <(deploy/image-instructions.py Dockerfile runtime)
"""

import re
import sys

FROM_RE = re.compile(r"^FROM\s+(\S+)(?:\s+AS\s+(\S+))?$", re.IGNORECASE)
# `COPY --from=x`, and the `from=x` inside a RUN mount. Both name a stage
# when the value matches one, and an external image or nothing when it does
# not — so over-matching here costs nothing and under-matching hides a
# dependency.
COPY_FROM_RE = re.compile(r"(?:^|[\s,])-{0,2}from=([^\s,]+)")
DIRECTIVE_RE = re.compile(r"^#\s*(syntax|escape|check)\s*=", re.IGNORECASE)


def parser_directives(path):
    """The directive comments at the top of the file, in order.

    A parser directive selects or configures the frontend that reads the
    whole Dockerfile, so a change to one can change every instruction's
    meaning. Docker only honours them before any other line, and the first
    blank line or ordinary comment ends the section.
    """
    found = []
    with open(path, encoding="utf-8") as handle:
        for raw in handle:
            line = raw.strip()
            if DIRECTIVE_RE.match(line):
                found.append(re.sub(r"\s+", " ", line))
                continue
            break
    return found


def instructions(path):
    """Yield one joined, whitespace-normalized instruction per element."""
    out = []
    buffered = None
    with open(path, encoding="utf-8") as handle:
        for raw in handle:
            line = raw.rstrip("\n")
            if buffered is None:
                if not line.strip() or line.lstrip().startswith("#"):
                    continue
                buffered = line
            else:
                buffered += "\n" + line
            if buffered.rstrip().endswith("\\"):
                continue
            joined = re.sub(r"\\\n\s*", " ", buffered)
            out.append(re.sub(r"[ \t]+", " ", joined).strip())
            buffered = None
    if buffered is not None:
        out.append(re.sub(r"[ \t]+", " ", buffered).strip())
    return out


def stages(path):
    """(global instructions, {stage: (base, FROM line, [instructions])}).

    Instructions before the first FROM are the file's global scope. In
    practice they are ARG declarations, and their defaults are what a FROM
    line interpolates, so `ARG BASE=alpine` decides the base of
    `FROM ${BASE}` as surely as writing it out would.
    """
    found = {}
    globals_ = []
    current = None
    for instruction in instructions(path):
        match = FROM_RE.match(instruction)
        if match:
            name = match.group(2) or match.group(1)
            current = (match.group(1), instruction, [])
            found[name] = current
            continue
        if current is None:
            globals_.append(instruction)
            continue
        current[2].append(instruction)
    return globals_, found


def copied_stages(block, known):
    """The stages one stage's instructions build from, in first-use order."""
    seen = []
    for instruction in block:
        for name in COPY_FROM_RE.findall(instruction):
            if name in known and name not in seen:
                seen.append(name)
    return seen


def flatten(path, target):
    """Everything Docker builds to produce `target`, dependencies first."""
    globals_, found = stages(path)
    if target not in found:
        raise SystemExit(f"no such stage: {target}")

    out = parser_directives(path) + list(globals_)
    emitted = set()

    def emit(name, pending):
        # `pending` is the set of stages currently being emitted further up
        # the recursion. A Dockerfile cannot legally contain a cycle, but a
        # tool that reads one must not hang on it.
        if name in emitted or name in pending or name not in found:
            return
        base, from_line, body = found[name]
        pending = pending | {name}
        emit(base, pending)
        emitted.add(name)
        block = [from_line] + body
        for dependency in copied_stages(block, found):
            emit(dependency, pending)
        out.extend(block)

    emit(target, frozenset())
    return out


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("usage: image-instructions.py <Dockerfile> <target>")
    for line in flatten(sys.argv[1], sys.argv[2]):
        print(line)
