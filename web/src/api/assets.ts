// AssetEntry is one item the server listed inside an agent location under
// .deck/. name is the path inside its location ("actual/home.png"); size
// is bytes and mtime RFC 3339, formatted by the client. version is opaque
// and is what a delete must quote. locked is why the row cannot be
// selected (a folder is "Folder — view only"), or null when it can.
export interface AssetEntry {
  path: string;
  name: string;
  kind: "file" | "dir";
  size: number;
  mtime: string;
  version: string;
  locked: string | null;
}

// AssetLocation is one of the five agent folders, in the server's order.
// truncated means the scan stopped at a bound and the entries are not the
// whole folder; totals then count listed files only.
export interface AssetLocation {
  id: string;
  name: string;
  path: string;
  truncated: boolean;
  entries: AssetEntry[];
}

// AssetListing is the scan of every agent location. scanned is the server's
// clock at the scan; preview_max_bytes is the largest file to preview
// inline.
export interface AssetListing {
  locations: AssetLocation[];
  scanned: string;
  preview_max_bytes: number;
}

// AssetDeleteFile names one selected file by the version its listing
// reported, so a file that changed since is skipped rather than deleted.
export interface AssetDeleteFile {
  path: string;
  version: string;
}

// AssetDeleteResult is the server's answer for one file. A file with no
// result is unconfirmed; that is the client's word, never the server's.
export interface AssetDeleteResult {
  path: string;
  outcome: "deleted" | "skipped";
  reason?: string;
}
