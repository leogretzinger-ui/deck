import { fetchWithAuthorization } from "../api/http.ts";

export interface VersionTimer {
  set(callback: () => void, delayMS: number): number;
  clear(id: number): void;
}

const browserTimer: VersionTimer = {
  set: (callback, delayMS) => window.setInterval(callback, delayMS),
  clear: (id) => window.clearInterval(id),
};

export function documentBuildID(doc: Document = document): string | null {
  const value = doc.querySelector<HTMLMetaElement>('meta[name="noisedeck-build-id"]')?.content.trim();
  return value === undefined || value === "" || value === "__NOISEDECK_BUILD_ID__" ? null : value;
}

export async function fetchBuildID(onAuthorizationLost?: () => void): Promise<string> {
  const response = await fetchWithAuthorization("/api/build", {
    credentials: "same-origin",
    cache: "no-store",
    headers: { Accept: "application/json" },
  }, onAuthorizationLost);
  if (!response.ok) throw new Error(`build check failed (${response.status})`);
  const body = await response.json() as { build_id?: unknown };
  if (typeof body.build_id !== "string" || body.build_id === "") {
    throw new Error("build check returned no identity");
  }
  return body.build_id;
}

export class VersionWatcher {
  private interval: number | null = null;
  private checking = false;
  private disposed = false;
  private announced = false;
  private loadedBuildID: string;
  private check: () => Promise<string>;
  private onAvailable: () => void;
  private timer: VersionTimer;
  private intervalMS: number;

  constructor(
    loadedBuildID: string,
    check: () => Promise<string>,
    onAvailable: () => void,
    timer: VersionTimer = browserTimer,
    intervalMS = 10_000,
  ) {
    this.loadedBuildID = loadedBuildID;
    this.check = check;
    this.onAvailable = onAvailable;
    this.timer = timer;
    this.intervalMS = intervalMS;
  }

  start(): void {
    if (this.disposed || this.interval !== null || this.announced) return;
    void this.poll();
    this.interval = this.timer.set(() => { void this.poll(); }, this.intervalMS);
  }

  async poll(): Promise<void> {
    if (this.disposed || this.announced || this.checking) return;
    this.checking = true;
    try {
      const currentBuildID = await this.check();
      if (!this.disposed && currentBuildID !== this.loadedBuildID) {
        this.announced = true;
        if (this.interval !== null) this.timer.clear(this.interval);
        this.interval = null;
        this.onAvailable();
      }
    } catch {
      // Only a successful response can announce a build. The request's
      // authorization callback owns teardown when workspace access is lost.
    } finally {
      this.checking = false;
    }
  }

  dispose(): void {
    this.disposed = true;
    if (this.interval !== null) this.timer.clear(this.interval);
    this.interval = null;
  }
}
