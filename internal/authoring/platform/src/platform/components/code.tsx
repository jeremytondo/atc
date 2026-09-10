import * as React from "react";
import { Highlight, themes, type Language } from "prism-react-renderer";
import { Check, Copy } from "lucide-react";
import { useTheme } from "@/platform/reader/theme";
import { cn } from "@/platform/lib/utils";

export interface CodeBlockProps {
  code: string;
  /** Prism language name, e.g. "tsx", "go", "json", "bash". */
  language?: Language;
  /** Caption shown above the block, typically a file name. */
  title?: React.ReactNode;
  /** 1-based lines to emphasize. */
  highlight?: number[];
  showLineNumbers?: boolean;
  className?: string;
}

/** Syntax-highlighted code, themed with the reader, with a copy button. */
export function CodeBlock({ code, language = "tsx", title, highlight = [], showLineNumbers = false, className }: CodeBlockProps) {
  const { theme } = useTheme();
  const [copied, setCopied] = React.useState(false);
  const source = code.replace(/\n$/, "");
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(source);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard access can be denied; the button is a convenience.
    }
  };
  return (
    <figure className={cn("not-prose overflow-hidden rounded-lg border bg-code text-[13px]", className)}>
      <figcaption className="flex items-center justify-between gap-2 border-b px-3 py-1.5 text-xs text-muted-foreground">
        <span className="font-mono">{title ?? language}</span>
        <button type="button" onClick={copy} className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 hover:bg-accent hover:text-accent-foreground" aria-label="Copy code">
          {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
          {copied ? "Copied" : "Copy"}
        </button>
      </figcaption>
      <Highlight code={source} language={language} theme={theme === "dark" ? themes.nightOwl : themes.github}>
        {({ tokens, getLineProps, getTokenProps }) => (
          <pre className="overflow-x-auto p-3 font-mono leading-6" style={{ background: "transparent" }}>
            {tokens.map((line, i) => {
              const lineProps = getLineProps({ line });
              const emphasized = highlight.includes(i + 1);
              return (
                <div key={i} {...lineProps} className={cn(lineProps.className, "flex", emphasized && "-mx-3 bg-primary/10 px-3")}>
                  {showLineNumbers && <span className="mr-4 w-6 shrink-0 select-none text-right text-muted-foreground/60">{i + 1}</span>}
                  <span className="flex-1">
                    {line.map((token, key) => (
                      <span key={key} {...getTokenProps({ token })} />
                    ))}
                  </span>
                </div>
              );
            })}
          </pre>
        )}
      </Highlight>
    </figure>
  );
}
