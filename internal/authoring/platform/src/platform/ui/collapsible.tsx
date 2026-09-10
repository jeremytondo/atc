import * as React from "react";
import { Collapsible as CollapsiblePrimitive } from "radix-ui";
import { ChevronRight } from "lucide-react";
import { cn } from "@/platform/lib/utils";

export const Collapsible = CollapsiblePrimitive.Root;
export const CollapsibleContent = CollapsiblePrimitive.Content;

export function CollapsibleTrigger({ className, children, ...props }: React.ComponentProps<typeof CollapsiblePrimitive.Trigger>) {
  return (
    <CollapsiblePrimitive.Trigger
      data-slot="collapsible-trigger"
      className={cn(
        "group flex w-full items-center gap-2 rounded-md py-1.5 text-left text-sm font-medium hover:text-primary focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring",
        className,
      )}
      {...props}
    >
      <ChevronRight className="size-4 shrink-0 transition-transform group-data-[state=open]:rotate-90" />
      {children}
    </CollapsiblePrimitive.Trigger>
  );
}
