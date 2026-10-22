import assert from "node:assert/strict";
import { afterEach, beforeEach, test } from "node:test";

import { applyFace, currentFace, FACES } from "./face.ts";

interface Stub {
  store: Map<string, string>;
  root: { dataset: Record<string, string | undefined> };
}

let stub: Stub;

function install(search: string, seeded?: string): void {
  stub = { store: new Map(), root: { dataset: {} } };
  if (seeded !== undefined) stub.store.set("noisedeck.face", seeded);
  const g = globalThis as Record<string, unknown>;
  g.localStorage = {
    getItem: (k: string) => stub.store.get(k) ?? null,
    setItem: (k: string, v: string) => void stub.store.set(k, v),
    removeItem: (k: string) => void stub.store.delete(k),
  };
  g.location = { search };
  g.document = {
    documentElement: {
      get dataset() {
        return stub.root.dataset;
      },
    },
  };
}

beforeEach(() => install(""));

afterEach(() => {
  const g = globalThis as Record<string, unknown>;
  delete g.localStorage;
  delete g.location;
  delete g.document;
});

test("no selection leaves the licensed face, which owns the ramp", () => {
  assert.equal(currentFace(), null);
  applyFace(null);
  assert.equal(stub.root.dataset.face, undefined);
});

test("the query parameter selects a face and persists it", () => {
  install("?face=inter");
  assert.equal(currentFace(), "inter");
  assert.equal(stub.store.get("noisedeck.face"), "inter");
});

test("a stored face is honoured on a later load", () => {
  install("", "figtree");
  assert.equal(currentFace(), "figtree");
});

test("the query parameter beats the store", () => {
  install("?face=work-sans", "inter");
  assert.equal(currentFace(), "work-sans");
  assert.equal(stub.store.get("noisedeck.face"), "work-sans");
});

test("an empty parameter clears the selection and the store", () => {
  install("?face=", "inter");
  assert.equal(currentFace(), null);
  assert.equal(stub.store.has("noisedeck.face"), false);
});

// A value no rule matches must not reach the document: the scale would keep
// the licensed trims while the attribute claimed a face nothing declares.
test("an unknown face is refused, from the parameter and from the store", () => {
  install("?face=comic-sans");
  assert.equal(currentFace(), null);
  install("", "helvetica");
  assert.equal(currentFace(), null);
});

test("applying a face writes the attribute the stylesheet scopes on", () => {
  applyFace("public-sans");
  assert.equal(stub.root.dataset.face, "public-sans");
  applyFace(null);
  assert.equal(stub.root.dataset.face, undefined);
});

test("every advertised face is scoped in the stylesheet", async () => {
  const css = await import("node:fs/promises").then((fs) =>
    fs.readFile(new URL("../styles/app.css", import.meta.url), "utf8"),
  );
  for (const face of FACES) {
    assert.ok(
      css.includes(`:root[data-face="${face}"]`),
      `face ${face} is selectable but no rule declares it`,
    );
  }
});
