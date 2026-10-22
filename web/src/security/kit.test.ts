import { test } from "node:test";
import assert from "node:assert/strict";
import { recoveryKit, serializeRecoveryKit, importRecoveryKit } from "./kit.ts";
import { formatRecoveryKey } from "./recovery.ts";

test("kit export and import preserve the same checked recovery secret", async () => {
  const key = await formatRecoveryKey(new Uint8Array(32).fill(7));
  const kit = await recoveryKit("test-instance", "https://deck.example", key);
  const contents = serializeRecoveryKit(kit);
  const file = { size: contents.length, text: async () => contents };
  assert.equal(await importRecoveryKit(file, "test-instance"), key);
  await assert.rejects(importRecoveryKit(file, "different-instance"), /does not belong/);
  await assert.rejects(importRecoveryKit({ ...file, size: 16_385 }, "test-instance"), /too large/);
  await assert.rejects(importRecoveryKit({ size: 2, text: async () => "{}" }, "test-instance"), /does not belong/);
});

test("imported address is never used as a navigation or fetch target", async () => {
  const key = await formatRecoveryKey(new Uint8Array(32).fill(11));
  const kit = { ...(await recoveryKit("test-instance", "https://deck.example", key)), address: "javascript:alert('untrusted')" };
  const contents = serializeRecoveryKit(kit);
  assert.equal(await importRecoveryKit({ size: contents.length, text: async () => contents }, "test-instance"), key);
});
