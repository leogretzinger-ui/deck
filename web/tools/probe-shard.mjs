export function selectShard(entries, shard) {
  if (shard === undefined) return entries;
  const match = /^([1-9]\d*)\/([1-9]\d*)$/.exec(shard);
  if (!match) throw new Error("Probe shard must be INDEX/COUNT (one-based)");
  const index = Number(match[1]);
  const count = Number(match[2]);
  if (!Number.isSafeInteger(index) || !Number.isSafeInteger(count)
      || index > count || count > entries.length) {
    throw new Error("Probe shard must select a nonempty subset of the inventory");
  }
  return entries.filter((_, position) => position % count === index - 1);
}
