// Document layout: a title block, an optional navigation sidebar built
// from the sections, and prose typography. Sections register themselves
// so the navigation never drifts from the content. Navigation scrolls by
// script rather than by fragment links: the page's URL must stay the
// artifact's own link.
import * as React from "react";
import { cn } from "@/platform/lib/utils";
import { useDocumentTitle } from "@/platform/reader/Reader";

interface SectionEntry {
  id: string;
  title: string;
  level: 2 | 3;
}

interface DocumentContextValue {
  register: (entry: SectionEntry) => () => void;
}

const DocumentContext = React.createContext<DocumentContextValue | null>(null);

export interface DocumentProps {
  /** One-paragraph summary under the heading. The heading itself is the
   *  document's title as published (or the working copy's, in preview). */
  summary?: React.ReactNode;
  /** Show the section navigation (default: when there are sections). */
  navigation?: boolean;
  className?: string;
  children: React.ReactNode;
}

export function Document({ summary, navigation, className, children }: DocumentProps) {
  const title = useDocumentTitle();
  const [sections, setSections] = React.useState<SectionEntry[]>([]);
  const register = React.useCallback((entry: SectionEntry) => {
    setSections((current) => [...current.filter((s) => s.id !== entry.id), entry]);
    return () => setSections((current) => current.filter((s) => s.id !== entry.id));
  }, []);
  const value = React.useMemo(() => ({ register }), [register]);
  const ordered = useDocumentOrder(sections);
  const showNav = navigation ?? ordered.length > 1;
  return (
    <DocumentContext.Provider value={value}>
      <div className={cn("flex gap-12", className)}>
        {showNav && <Navigation sections={ordered} />}
        <article className="prose min-w-0 flex-1">
          {title && <h1>{title}</h1>}
          {summary && <p className="text-lg text-muted-foreground">{summary}</p>}
          {children}
        </article>
      </div>
    </DocumentContext.Provider>
  );
}

/** Sort registered sections by their position in the page, measured
 *  after each commit so a reordered document reorders its navigation. */
function useDocumentOrder(sections: SectionEntry[]): SectionEntry[] {
  const [ordered, setOrdered] = React.useState<SectionEntry[]>([]);
  React.useLayoutEffect(() => {
    const positioned = sections.map((section) => ({ section, element: document.getElementById(section.id) }));
    const next = positioned
      .sort((a, b) => {
        if (!a.element || !b.element) return 0;
        return a.element.compareDocumentPosition(b.element) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1;
      })
      .map((p) => p.section);
    setOrdered((current) => (current.length === next.length && current.every((s, i) => s === next[i]) ? current : next));
  });
  return ordered;
}

export interface SectionProps extends Omit<React.ComponentProps<"section">, "title"> {
  /** Anchor id; also the navigation key. */
  id: string;
  title: React.ReactNode;
  /** Heading level: 2 (default) for top-level sections, 3 for subsections. */
  level?: 2 | 3;
}

export function Section({ id, title, level = 2, className, children, ...props }: SectionProps) {
  const context = React.useContext(DocumentContext);
  const text = typeof title === "string" ? title : id;
  React.useEffect(() => context?.register({ id, title: text, level }), [context, id, text, level]);
  const Heading = level === 2 ? "h2" : "h3";
  return (
    <section id={id} className={cn("scroll-mt-24", className)} {...props}>
      <Heading>{title}</Heading>
      {children}
    </section>
  );
}

function Navigation({ sections }: { sections: SectionEntry[] }) {
  const [active, setActive] = React.useState<string>(sections[0]?.id ?? "");
  React.useEffect(() => {
    const elements = sections.map((s) => document.getElementById(s.id)).filter((e): e is HTMLElement => e !== null);
    if (elements.length === 0) return;
    const observer = new IntersectionObserver(
      (entries) => {
        const visible = entries.filter((e) => e.isIntersecting).sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
        if (visible[0]) setActive(visible[0].target.id);
      },
      { rootMargin: "-20% 0px -70% 0px" },
    );
    elements.forEach((e) => observer.observe(e));
    return () => observer.disconnect();
  }, [sections]);
  return (
    <nav aria-label="Sections" className="sticky top-6 hidden h-fit w-56 shrink-0 self-start lg:block">
      <div className="mb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">Contents</div>
      <ul className="space-y-0.5 border-l text-sm">
        {sections.map((section) => (
          <li key={section.id}>
            <button
              type="button"
              onClick={() => document.getElementById(section.id)?.scrollIntoView({ behavior: "smooth", block: "start" })}
              className={cn(
                "-ml-px block w-full border-l py-1 text-left leading-snug transition-colors hover:text-foreground",
                section.level === 3 ? "pl-6" : "pl-3",
                active === section.id ? "border-primary font-medium text-foreground" : "border-transparent text-muted-foreground",
              )}
            >
              {section.title}
            </button>
          </li>
        ))}
      </ul>
    </nav>
  );
}
