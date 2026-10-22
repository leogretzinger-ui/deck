// What the Assets panel says while it is closed: how many files agents
// wrote since you last had the panel open, and how old the newest
// is. "New" is a file whose mtime is later than the project's last-seen
// instant. Opening the panel moves that instant to the newest listed
// mtime, never to the browser's clock, so a file that lands while the
// panel is open is seen and an older file can never turn new again.
import { formatSince } from "../../lib/format.ts";
import type { AssetListing } from "../../api/assets.ts";
import type { FileApiClient } from "../../api/files.ts";

export interface AssetsUnseen {
  count: number;
  // newest is the mtime of the newest unseen file, or null when none is.
  newest: string | null;
}

export const NOTHING_UNSEEN: AssetsUnseen = { count: 0, newest: null };

// A project with no stored instant whose folders are empty starts from the
// epoch, so the first file an agent writes there is new.
const EPOCH = "1970-01-01T00:00:00Z";

export type SeenStorage = Pick<Storage, "getItem" | "setItem">;

export function assetsSeenKey(projectID: string): string {
  return `noisedeck.assets.seen.${projectID}`;
}

// browserStorage is localStorage when the page may use it. Reading the
// property itself throws on an origin with storage disabled.
export function browserStorage(): SeenStorage | null {
  try {
    return globalThis.localStorage ?? null;
  } catch {
    return null;
  }
}

// newestMtime is the latest file mtime in the listing; folders do not
// count, because a folder's mtime moves when a file inside it is removed.
export function newestMtime(listing: AssetListing): string | null {
  let newest: string | null = null;
  let newestAt = -Infinity;
  for (const location of listing.locations) {
    for (const entry of location.entries) {
      if (entry.kind !== "file") continue;
      const at = Date.parse(entry.mtime);
      if (at > newestAt) {
        newestAt = at;
        newest = entry.mtime;
      }
    }
  }
  return newest;
}

export function unseenAssets(listing: AssetListing, lastSeen: string): AssetsUnseen {
  const seenAt = Date.parse(lastSeen);
  let count = 0;
  let newest: string | null = null;
  let newestAt = -Infinity;
  for (const location of listing.locations) {
    for (const entry of location.entries) {
      if (entry.kind !== "file") continue;
      const at = Date.parse(entry.mtime);
      if (!(at > seenAt)) continue;
      count++;
      if (at > newestAt) {
        newestAt = at;
        newest = entry.mtime;
      }
    }
  }
  return { count, newest };
}

// The info bar's line: "3 new assets · newest 1m ago", "1 new asset · just
// now". Null when nothing is new.
export function assetsNews(unseen: AssetsUnseen, now: number): string | null {
  if (unseen.count === 0) return null;
  const noun = unseen.count === 1 ? "asset" : "assets";
  const since = formatSince(unseen.newest ?? undefined, now);
  const age = since === null || since === "< 1m" ? "just now" : `${since} ago`;
  return `${unseen.count} new ${noun} · ${unseen.count === 1 ? "" : "newest "}${age}`;
}

// AssetsSeen is one project's last-seen instant. Storage is read on every
// question, so a panel opened in another tab clears this one's count at
// its next check; when storage is blocked, the instant held in memory
// stands for this page only.
export class AssetsSeen {
  private key: string;
  private storage: SeenStorage | null;
  private held: string | null = null;

  constructor(projectID: string, storage: SeenStorage | null) {
    this.key = assetsSeenKey(projectID);
    this.storage = storage;
  }

  get value(): string | null {
    try {
      const stored = this.storage?.getItem(this.key) ?? null;
      if (stored !== null && !Number.isNaN(Date.parse(stored))) return stored;
    } catch {
      // Blocked storage: fall back to the instant held for this page.
    }
    return this.held;
  }

  // baseline is the first visit: with no stored instant, everything
  // already listed counts as seen, so the badge never counts a project's
  // whole history the first time Deck looks at it.
  baseline(listing: AssetListing): string {
    const current = this.value;
    if (current !== null) return current;
    const start = newestMtime(listing) ?? EPOCH;
    this.write(start);
    return start;
  }

  // markSeen moves the instant forward to the newest listed file. It never
  // moves it back: a listing taken after a delete may have an older newest.
  markSeen(listing: AssetListing): void {
    const newest = newestMtime(listing);
    const current = this.value;
    if (newest === null) {
      if (current === null) this.write(EPOCH);
      return;
    }
    if (current !== null && !(Date.parse(newest) > Date.parse(current))) return;
    this.write(newest);
  }

  private write(value: string): void {
    this.held = value;
    try {
      this.storage?.setItem(this.key, value);
    } catch {
      // Blocked storage: the instant holds for this page only.
    }
  }
}

// The watcher's interval: the listing is re-read this often when the
// change feed has closed for good, and the news line's age is repainted on
// the same beat while something is unseen.
export const WATCH_INTERVAL_MS = 30_000;

type WatchClient = Pick<FileApiClient, "listProjectAssets" | "subscribeProjectAssetEvents">;

// AssetsWatcher keeps the count while the panel is closed: it follows the
// project's change feed and re-reads the listing on each event. A burst of
// events while a read is in flight costs one more read, not one per event.
// onChange's first flag marks the reading a start makes: those files were
// already waiting, not arriving.
export class AssetsWatcher {
  private client: WatchClient;
  private projectID: string;
  private seen: AssetsSeen;
  private onChange: (unseen: AssetsUnseen, first: boolean) => void;
  private unsubscribe: (() => void) | null = null;
  private timer: ReturnType<typeof setInterval> | null = null;
  private polling = false;
  private generation = 0;
  private reading = false;
  private again = false;
  private first = true;
  private last: AssetsUnseen = NOTHING_UNSEEN;

  constructor(client: WatchClient, projectID: string, seen: AssetsSeen, onChange: (unseen: AssetsUnseen, first: boolean) => void) {
    this.client = client;
    this.projectID = projectID;
    this.seen = seen;
    this.onChange = onChange;
  }

  get running(): boolean {
    return this.unsubscribe !== null;
  }

  get unseen(): AssetsUnseen {
    return this.last;
  }

  start(): void {
    if (this.running) return;
    const generation = ++this.generation;
    this.polling = false;
    this.first = true;
    this.unsubscribe = this.client.subscribeProjectAssetEvents(
      this.projectID,
      () => void this.check(generation),
      () => {
        if (generation === this.generation) this.polling = true;
      },
    );
    this.timer = setInterval(() => {
      if (this.polling) void this.check(generation);
      else if (this.last.count > 0) this.onChange(this.last, false);
    }, WATCH_INTERVAL_MS);
    void this.check(generation);
  }

  // stop drops the feed and forgets the count: the panel is open, or the
  // project is gone. A read still in flight lands nowhere.
  stop(): void {
    ++this.generation;
    this.unsubscribe?.();
    this.unsubscribe = null;
    if (this.timer !== null) clearInterval(this.timer);
    this.timer = null;
    this.reading = false;
    this.again = false;
    this.last = NOTHING_UNSEEN;
  }

  private async check(generation: number): Promise<void> {
    if (generation !== this.generation) return;
    if (this.reading) {
      this.again = true;
      return;
    }
    this.reading = true;
    try {
      do {
        this.again = false;
        let listing: AssetListing;
        try {
          listing = await this.client.listProjectAssets(this.projectID);
        } catch {
          // The count keeps its last value; the next event or poll may read.
          return;
        }
        if (generation !== this.generation) return;
        this.last = unseenAssets(listing, this.seen.baseline(listing));
        const first = this.first;
        this.first = false;
        this.onChange(this.last, first);
      } while (this.again && generation === this.generation);
    } finally {
      if (generation === this.generation) this.reading = false;
    }
  }
}

// AssetsFeed is the count for the selected session's project. It lives per
// project, not per session: switching sessions inside one project keeps
// the feed, the count and the time the count last grew, so the badge does
// not blink to zero and an old count does not come back as fresh news.
// Only a different project, or no session at all, starts it over.
export class AssetsFeed {
  private client: WatchClient;
  private storage: () => SeenStorage | null;
  // onChange's arrived flag is a real increase reported by the running
  // watcher: never the reading a start or a resume makes, never a pause.
  private onChange: (arrived: boolean) => void;
  private now: () => number;
  private watcher: AssetsWatcher | null = null;
  private project: string | null = null;
  private seenStore: AssetsSeen | null = null;
  private current: AssetsUnseen = NOTHING_UNSEEN;
  // grewAt is when the count last went up: the news line's age in the
  // info bar's arbitration. Null while nothing is new.
  private grewAt: number | null = null;

  constructor(client: WatchClient, storage: () => SeenStorage | null, onChange: (arrived: boolean) => void, now: () => number = Date.now) {
    this.client = client;
    this.storage = storage;
    this.onChange = onChange;
    this.now = now;
  }

  get projectID(): string | null {
    return this.project;
  }

  get seen(): AssetsSeen | null {
    return this.seenStore;
  }

  get unseen(): AssetsUnseen {
    return this.current;
  }

  get count(): number {
    return this.current.count;
  }

  get at(): number | null {
    return this.grewAt;
  }

  // follow points the feed at the selected session's project. The same
  // project keeps everything; the feed only runs while the panel is closed.
  follow(projectID: string, panelOpen: boolean): void {
    if (this.project !== projectID) {
      this.stop();
      this.project = projectID;
      this.seenStore = new AssetsSeen(projectID, this.storage());
      this.watcher = new AssetsWatcher(this.client, projectID, this.seenStore, (unseen, first) => this.fold(unseen, first));
    }
    if (panelOpen) this.pause();
    else this.resume();
  }

  // pause is the panel opening: it shows everything, so nothing is unseen.
  pause(): void {
    this.watcher?.stop();
    this.reset();
  }

  resume(): void {
    this.watcher?.start();
  }

  // stop forgets the project: no session is open, or you left.
  stop(): void {
    this.watcher?.stop();
    this.watcher = null;
    this.seenStore = null;
    this.project = null;
    this.reset();
  }

  private reset(): void {
    const had = this.current.count > 0;
    this.current = NOTHING_UNSEEN;
    this.grewAt = null;
    if (had) this.onChange(false);
  }

  private fold(unseen: AssetsUnseen, first: boolean): void {
    const grew = unseen.count > this.current.count;
    if (grew) this.grewAt = this.now();
    else if (unseen.count === 0) this.grewAt = null;
    this.current = unseen;
    this.onChange(grew && !first);
  }
}
