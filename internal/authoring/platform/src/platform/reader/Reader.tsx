// The standard ATC reader shell: a header with the title, publication
// date, version, and history navigation, an older-version notice when a
// fixed link is not the current one, and the theme toggle. The main link
// shows the artifact's current title; a fixed link, current or not, shows
// the title its version was published with. In a local preview (no
// injected metadata) it shows the working copy's title with an
// unpublished badge. The title is also the document's own heading, so a
// document never carries a second one.
import * as React from "react";
import { ChevronDown, History, Moon, Sun } from "lucide-react";
import { readMetadata, type Metadata } from "./metadata";
import { ThemeProvider, useTheme } from "./theme";
import { Badge } from "@/platform/ui/badge";
import { Button } from "@/platform/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/platform/ui/dropdown-menu";
import { formatDate } from "@/platform/lib/utils";

const TitleContext = React.createContext<string>("");

/** The title the reader shows: the document's heading. */
export function useDocumentTitle() {
  return React.useContext(TitleContext);
}

/** The main link shows the artifact's current title; a fixed link shows
 *  the version's. The route decides, not whether the version is current. */
function displayTitle(metadata: Metadata | null): string {
  if (!metadata) return previewTitle() || "Untitled document";
  const onMainLink = window.location.pathname === metadata.latestPath;
  return onMainLink ? metadata.title : metadata.version.title;
}

export function Reader({ children }: { children: React.ReactNode }) {
  const [metadata] = React.useState(readMetadata);
  const title = displayTitle(metadata);
  React.useEffect(() => {
    document.title = metadata ? `${title} · ATC` : title ? `${title} · preview` : "ATC artifact";
  }, [metadata, title]);
  const older = metadata !== null && metadata.version.number !== metadata.currentVersion;
  return (
    <ThemeProvider>
      <TitleContext.Provider value={title}>
        <div className="min-h-screen">
          <ReaderHeader metadata={metadata} title={title} />
          {older && <OlderVersionNotice metadata={metadata} />}
          <main className="mx-auto w-full max-w-6xl px-6 py-10">{children}</main>
          <footer className="mx-auto w-full max-w-6xl px-6 pb-10 text-xs text-muted-foreground">
            Published documents are snapshots of their moment; they are not maintained against later changes.
          </footer>
        </div>
      </TitleContext.Provider>
    </ThemeProvider>
  );
}

function previewTitle(): string {
  return (import.meta.env.VITE_ATC_TITLE as string | undefined) ?? "";
}

function ReaderHeader({ metadata, title }: { metadata: Metadata | null; title: string }) {
  return (
    <header className="border-b bg-card/60 backdrop-blur">
      <div className="mx-auto flex w-full max-w-6xl flex-wrap items-center gap-x-4 gap-y-2 px-6 py-3">
        <div className="min-w-0 flex-1">
          <div className="truncate text-base font-semibold leading-tight">{title}</div>
          <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
            {metadata ? (
              <>
                <span>Published {formatDate(metadata.version.publishedAt)}</span>
                <span aria-hidden="true">·</span>
                <span>
                  Version {metadata.version.number} of {metadata.currentVersion}
                  {metadata.version.restoredFrom ? ` (restored from ${metadata.version.restoredFrom})` : ""}
                </span>
                {metadata.version.provenance?.revision && (
                  <>
                    <span aria-hidden="true">·</span>
                    <span className="font-mono" title="Repository revision the document describes">
                      {metadata.version.provenance.revision.slice(0, 12)}
                    </span>
                  </>
                )}
              </>
            ) : (
              <Badge variant="warning">Unpublished preview</Badge>
            )}
          </div>
        </div>
        <div className="flex items-center gap-1">
          {metadata && <HistoryMenu metadata={metadata} />}
          <ThemeToggle />
        </div>
      </div>
    </header>
  );
}

function HistoryMenu({ metadata }: { metadata: Metadata }) {
  const versions = [...metadata.versions].sort((a, b) => b.number - a.number);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="sm" aria-label="Version history">
          <History />
          History
          <ChevronDown className="opacity-60" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel>Versions</DropdownMenuLabel>
        <DropdownMenuItem asChild>
          <a href={metadata.latestPath} className="justify-between">
            <span>Latest</span>
            <span className="text-xs text-muted-foreground">v{metadata.currentVersion}</span>
          </a>
        </DropdownMenuItem>
        {versions.map((version) => (
          <DropdownMenuItem key={version.number} asChild>
            <a href={version.path} className="flex-col items-start gap-0" aria-current={version.number === metadata.version.number ? "page" : undefined}>
              <span className={version.number === metadata.version.number ? "font-medium" : undefined}>
                v{version.number} · {version.title}
              </span>
              <span className="text-xs text-muted-foreground">{formatDate(version.publishedAt)}</span>
            </a>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function OlderVersionNotice({ metadata }: { metadata: Metadata }) {
  return (
    <div className="border-b bg-warning/15">
      <div className="mx-auto flex w-full max-w-6xl flex-wrap items-center gap-x-3 gap-y-1 px-6 py-2 text-sm">
        <span>
          This is version {metadata.version.number}, published {formatDate(metadata.version.publishedAt)}. The current version is {metadata.currentVersion}.
        </span>
        <a href={metadata.latestPath} className="font-medium text-primary underline underline-offset-4">
          Open the latest version
        </a>
      </div>
    </div>
  );
}

function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  return (
    <Button variant="ghost" size="icon" aria-label={theme === "dark" ? "Switch to light" : "Switch to dark"} onClick={() => setTheme(theme === "dark" ? "light" : "dark")}>
      {theme === "dark" ? <Sun /> : <Moon />}
    </Button>
  );
}
