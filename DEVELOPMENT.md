# Development model

This repository is the curated public release mirror for Deck, part of
[Noisefloor](https://noisefloor.sh).

Deck is available under the [MIT license](LICENSE). Development takes
place in a private repository. Each public release is a curated source
snapshot; private development history and review discussions are not
mirrored.

This is a solo-maintained project. Pull requests are disabled. Issues are
open for bug reports and feedback, with no commitment to a response time,
fix or release date. Forks are permitted under the MIT license but are
not an upstream contribution channel. Report vulnerabilities privately as
described in [SECURITY.md](SECURITY.md).

## Status and support

The [backend code map](internal/README.md) describes package ownership and
local checks. The [browser frontend](web/) has its own development tools and
tests.

Deck is experimental. Interfaces, file formats and behavior may change
between releases. Only the latest tagged public release is supported;
there are no security backports to earlier releases and no service-level
agreement.

## Releases

Releases start with a curated snapshot of private `main` on this public
mirror. Prepare a draft GitHub Release with the release notes first, then
push its `vX.Y.Z` tag to the mirror. The tag workflow builds Linux amd64 and
arm64 binaries with the open font set and embedded web UI, then attaches
them and `SHA256SUMS` to the release. Existing notes and draft status are
preserved; publish the draft after checking the artifacts.

Release binaries are for native installs. Container users build the image
from the tagged source.

## Independent implementation

Deck is a from-scratch implementation. No source code, compiled code,
internal protocols, UI assets or tests are copied from other
agent-console projects. Where Deck's behavior overlaps with another
tool's, that behavior is specified from user stories, failure examples
and behavior-level acceptance criteria, and the implementation is written
independently.

## AI assistance

Coding agents assist with implementation and independent review. Doug
Doan directs the work, makes release decisions and is responsible for
what ships. Public releases require checks against their exact source and
artifacts; passing checks in the private development repository alone is
insufficient.
