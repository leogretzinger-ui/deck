// The directory picker is a navigator: rows cd into
// themselves, the directory being chosen is the one you stand in, and the
// primary is live whenever there is one. These pin that model — the path
// line, the Up control's two reasons to be live, the trailing chevron on
// every row, the sentence the empty box speaks — and the one state with no
// directory at all: the mount listing of allowed roots.
import assert from "node:assert/strict";
import test from "node:test";
import { directoryPickerHtml, type DirectoryPickerState } from "./directory-picker.ts";
import { ICON_PATHS } from "../../../ui/icons-data.ts";

function picker(over: Partial<DirectoryPickerState>): string {
  return directoryPickerHtml({
    open: true,
    loading: false,
    listing: null,
    error: "",
    fallbackError: null,
    newFolderOpen: false,
    newFolderName: "",
    mountRoots: 1,
    ...over,
  });
}

const rootTop = {
  current_path: "/workspace",
  parent_path: null,
  directories: [
    { name: "field-notes", path: "/workspace/field-notes" },
    { name: "deck", path: "/workspace/deck" },
  ],
};

// disabled reads the attribute on the tag carrying marker — the bare word,
// never a `disabled:` utility variant in the class list.
function disabled(html: string, marker: string): boolean {
  const at = html.indexOf(marker);
  assert.notEqual(at, -1, `${marker} is rendered`);
  const tag = html.slice(html.lastIndexOf("<", at), html.indexOf(">", at));
  return /\sdisabled(?=[\s>])/.test(tag);
}

test("standing in a directory: the path line names it and the primary is live", () => {
  const html = picker({ listing: rootTop });
  assert.ok(html.includes('data-directory-path-line class="min-w-0 flex-1 truncate text-text-primary">/workspace</span>'),
    "the path line prints the current path verbatim, tail-elided");
  assert.ok(!disabled(html, "data-directory-select"), "Choose this directory is live at a root's top");
  assert.ok(html.includes(">Choose this directory</button>"), "the primary names the current directory, not a selection");
  assert.ok(!disabled(html, "data-directory-new-folder"), "New directory creates inside the current directory");
  assert.ok(!html.includes("data-directory-roots"), "there is no Root control: Up is the one way back");
  assert.ok(!html.includes("Workspace</span>"), "no label sits above the list; the path line is the heading");
});

test("every row cd's into itself and carries a trailing chevron", () => {
  const html = picker({ listing: rootTop });
  const rows = html.split('data-directory-path="').slice(1);
  assert.equal(rows.length, 2);
  for (const row of rows) {
    const body = row.slice(0, row.indexOf("</button>"));
    assert.ok(body.includes(ICON_PATHS["chevron-right"]), "the row shows it navigates");
    assert.ok(!body.includes("bg-wash-pressed") && !body.includes("wash-selected"), "no row is ever selected");
  }
});

test("Up is live when the listing has a parent", () => {
  const html = picker({
    listing: { current_path: "/workspace/deck", parent_path: "/workspace", directories: [] },
  });
  assert.ok(!disabled(html, "data-directory-parent"));
});

test("Up at a root's top is dead with one root and live when the mount offers more", () => {
  assert.ok(disabled(picker({ listing: rootTop, mountRoots: 1 }), "data-directory-parent"),
    "a single root has nowhere above it");
  assert.ok(!disabled(picker({ listing: rootTop, mountRoots: 2 }), "data-directory-parent"),
    "with a second root, Up returns to the mount to choose it");
});

test("the mount listing is not a directory: no path line, nothing to choose or create in", () => {
  const html = picker({
    listing: { current_path: "", parent_path: null, directories: [{ name: "a", path: "/a" }, { name: "b", path: "/b" }] },
    mountRoots: 2,
  });
  assert.ok(!html.includes("data-directory-path-line"), "no path to print");
  assert.ok(disabled(html, "data-directory-select"));
  assert.ok(disabled(html, "data-directory-new-folder"));
  assert.equal(html.split('data-directory-path="').length - 1, 2, "the roots are the rows");
});

test("an empty directory says so in a sentence", () => {
  const html = picker({ listing: { current_path: "/workspace/deck", parent_path: "/workspace", directories: [] } });
  assert.ok(html.includes(">No subdirectories.</span>"), "full stop: the box is a statement, not a placeholder");
  assert.ok(!disabled(html, "data-directory-select"), "an empty directory is still choosable");
});

test("the new directory row draws the name you typed, escaped", () => {
  const html = picker({ listing: rootTop, newFolderOpen: true, newFolderName: 'late-"folder' });
  assert.ok(html.includes('data-directory-new-name value="late-&quot;folder"'), "a re-render keeps the typed name");
});
