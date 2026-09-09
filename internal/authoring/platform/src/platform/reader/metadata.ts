// The document metadata the ATC document origin injects into every
// published page (internal/documents.Metadata), as far as the reader
// uses it. Absent during local preview.

export interface Version {
  number: number;
  title: string;
  publishedAt: string;
  restoredFrom?: number;
  provenance?: { threadId?: string; revision?: string; links?: string[] };
  path: string;
}

export interface Metadata {
  /** The artifact's current title. */
  title: string;
  currentVersion: number;
  latestPath: string;
  version: Version;
  versions: Version[];
}

/** Read the injected metadata, or null in a local preview. */
export function readMetadata(): Metadata | null {
  const element = document.getElementById("atc-document");
  if (!element?.textContent) return null;
  try {
    return JSON.parse(element.textContent) as Metadata;
  } catch {
    return null;
  }
}
