# Project files API

The routes behind the Files panel and the agent assets list. Every route
needs a signed-in user and answers 404 for a project that is not
registered before it looks at any path. Paths are relative to the project
root, use forward slashes, and are opened beneath the root without following
a symbolic link out of it.

| Route | What it does |
| --- | --- |
| `GET /api/projects/{id}/files?path=` | List one folder (at most 2,000 entries). |
| `GET /api/projects/{id}/files/events?path=` | Stream `files-changed` when that folder's listing changes, checked every 2 seconds. |
| `GET /api/projects/{id}/files/view?path=` | Return a UTF-8 text file of at most 1 MiB. |
| `GET /api/projects/{id}/files/image?path=` | Return an image of at most 16 MiB for inline display. |
| `GET /api/projects/{id}/files/download?path=` | Download a file. |
| `POST /api/projects/{id}/files?path=` | Upload one file of at most 32 MiB. Replacing a file needs `replace=1` and the version the conflict reported. |
| `GET /api/projects/{id}/assets` | List the files agents wrote in the five agent locations. |
| `GET /api/projects/{id}/assets/events` | Stream `assets-changed` when that list changes, checked every 5 seconds. |
| `POST /api/projects/{id}/assets/delete` | Delete selected files from the agent locations. |

Delete exists only for agent assets. Nothing else in the project can be
deleted through Deck.

## Agent assets

Agents write files for you into five folders under the project's
`.deck/` directory. This is Deck's convention:

| Folder | Shown as |
| --- | --- |
| `.deck/assets/` | Assets folder — files agents export for you |
| `.deck/build/` | Build scratch |
| `.deck/visual-diff/` | Visual-diff captures |
| `.deck/runs/` | Run receipts |
| `.deck/sessions/` | Session scratch |

Agents learn where files for you go in three ways:

- When you register or import a project, Deck adds a short marked block to
  its `AGENTS.md` (creating the file if there is none) saying that
  screenshots, exports, reports and other files for you go in
  `.deck/assets/`. A file that already has the block is left alone, and
  nothing else in it is changed.
- Every session starts with `NOISEDECK_ASSETS_DIR=.deck/assets`, relative to
  the project root.
- The image installs a `deck-assets` skill with the same instruction into
  each agent CLI's skills folder at start-up.

### Listing

`GET /assets` returns every location in that order, even one that does not
exist (it has no entries):

```json
{
  "locations": [
    {
      "id": "assets", "name": "Assets folder", "path": ".deck/assets/", "truncated": false,
      "entries": [
        { "path": ".deck/assets/home.png", "name": "home.png", "kind": "file",
          "size": 412000, "mtime": "2026-10-18T17:00:00Z", "version": "1-…", "locked": null }
      ]
    }
  ],
  "scanned": "2026-10-18T17:02:00Z",
  "preview_max_bytes": 16777216
}
```

- Each location is walked to a depth of 8 folders and lists at most 500
  entries. When the walk stops at either bound, `truncated` is true and the
  list is not the whole folder.
- `name` is the path inside the location, so a nested file reads as
  `actual/home.png`. Entries are newest first.
- Folders are listed where they sit, with `kind: "dir"` and
  `locked: "Folder — view only"`. Only a file with `locked: null` can be
  deleted.
- Symbolic links and special files are never listed, and the walk never
  follows a link.
- `version` is an opaque identity of the file, the same one an upload
  conflict reports. It changes when the file is replaced or rewritten.
- `preview_max_bytes` (16 MiB) is the largest file the client previews
  inline; it is the `/files/image` bound.

### Delete

`POST /assets/delete` takes up to 500 files, each with the version the
listing reported:

```json
{ "files": [ { "path": ".deck/assets/home.png", "version": "1-…" } ] }
```

More than 500 files, or a file without a path or version, is refused whole
with 400. Otherwise the answer is 200 with one result per file, in request
order. Each file is handled on its own:

| Outcome | Reason | When |
| --- | --- | --- |
| `deleted` | | The file was removed. |
| `skipped` | Outside the agent locations | The path is not inside one of the five folders, or reaches it through a link. |
| `skipped` | Not a regular file | A folder (even an empty one), a link, or a special file. |
| `skipped` | Changed on disk after you selected it — modified HH:MM | The version no longer matches. The time is the server's local time. |
| `skipped` | Already gone | Nothing holds the path any more. |
| `skipped` | The workspace refused: permission denied | The filesystem refused the delete. |
| `skipped` | The workspace could not delete it | Any other failure. |

Delete is an ordinary unlink, like `rm` or a file manager's delete. The
server checks that the name still holds a regular file whose version matches
the selection, then removes it. A change that lands in the instant between
that check and the removal is not detected.

A file the client sent but never heard back about is the client's to report
as unconfirmed; the server has no such outcome. The server logs each
outcome.
