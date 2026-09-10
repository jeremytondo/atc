import * as React from "react";
import { AlertTriangle, CheckCircle2, Info, Lightbulb, XCircle } from "lucide-react";
import { cn } from "@/platform/lib/utils";

const kinds = {
  note: { icon: Info, className: "border-border bg-muted/50", iconClass: "text-primary" },
  tip: { icon: Lightbulb, className: "border-success/30 bg-success/10", iconClass: "text-success" },
  success: { icon: CheckCircle2, className: "border-success/30 bg-success/10", iconClass: "text-success" },
  warning: { icon: AlertTriangle, className: "border-warning/40 bg-warning/15", iconClass: "text-warning" },
  danger: { icon: XCircle, className: "border-destructive/30 bg-destructive/10", iconClass: "text-destructive" },
} as const;

export interface CalloutProps extends Omit<React.ComponentProps<"aside">, "title"> {
  kind?: keyof typeof kinds;
  title?: React.ReactNode;
}

/** A highlighted aside: note, tip, success, warning, or danger. */
export function Callout({ kind = "note", title, className, children, ...props }: CalloutProps) {
  const { icon: Icon, className: kindClass, iconClass } = kinds[kind];
  return (
    <aside className={cn("not-prose flex gap-3 rounded-lg border p-4 text-sm", kindClass, className)} {...props}>
      <Icon className={cn("mt-0.5 size-4 shrink-0", iconClass)} aria-hidden="true" />
      <div className="min-w-0 flex-1 space-y-1 [&_p]:leading-6">
        {title && <div className="font-medium">{title}</div>}
        <div>{children}</div>
      </div>
    </aside>
  );
}
