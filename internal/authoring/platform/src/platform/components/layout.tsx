import * as React from "react";
import { cn } from "@/platform/lib/utils";

/** Side-by-side columns that stack on narrow screens. */
export function Columns({ className, count = 2, ...props }: React.ComponentProps<"div"> & { count?: 2 | 3 | 4 }) {
  const cols = { 2: "md:grid-cols-2", 3: "md:grid-cols-3", 4: "md:grid-cols-4" }[count];
  return <div className={cn("not-prose grid grid-cols-1 gap-4", cols, className)} {...props} />;
}

/** A figure with a caption: diagrams, images, charts. */
export function Figure({ caption, className, children, ...props }: React.ComponentProps<"figure"> & { caption?: React.ReactNode }) {
  return (
    <figure className={cn("not-prose my-6 space-y-2", className)} {...props}>
      <div className="overflow-x-auto rounded-lg border bg-card p-4">{children}</div>
      {caption && <figcaption className="text-sm text-muted-foreground">{caption}</figcaption>}
    </figure>
  );
}

/** A definition-style list of label/value pairs. */
export function KeyValue({ items, className }: { items: Array<{ label: React.ReactNode; value: React.ReactNode }>; className?: string }) {
  return (
    <dl className={cn("not-prose grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm", className)}>
      {items.map((item, i) => (
        <React.Fragment key={i}>
          <dt className="text-muted-foreground">{item.label}</dt>
          <dd className="min-w-0">{item.value}</dd>
        </React.Fragment>
      ))}
    </dl>
  );
}

/** A compact statistic tile. */
export function Stat({ label, value, detail, className }: { label: React.ReactNode; value: React.ReactNode; detail?: React.ReactNode; className?: string }) {
  return (
    <div className={cn("not-prose rounded-lg border bg-card px-4 py-3", className)}>
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="mt-1 text-2xl font-semibold tabular-nums leading-none">{value}</div>
      {detail && <div className="mt-1.5 text-xs text-muted-foreground">{detail}</div>}
    </div>
  );
}
