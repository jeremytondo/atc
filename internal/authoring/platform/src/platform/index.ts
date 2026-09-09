// The ATC artifact platform: everything a document imports. Documents
// use these components with the fixed dependency set; custom React
// components and layouts are welcome on top of them. The reader shell
// (header, history, theme) is not a document concern and stays private.
export { Document, Section } from "./components/document";
export { Callout } from "./components/callout";
export { CodeBlock } from "./components/code";
export { Columns, Figure, KeyValue, Stat } from "./components/layout";
export { Button } from "./ui/button";
export { Badge } from "./ui/badge";
export { Card, CardHeader, CardTitle, CardDescription, CardContent, CardFooter } from "./ui/card";
export { Tabs, TabsList, TabsTrigger, TabsContent } from "./ui/tabs";
export { Collapsible, CollapsibleTrigger, CollapsibleContent } from "./ui/collapsible";
export { Table, TableHeader, TableBody, TableFooter, TableRow, TableHead, TableCell, TableCaption } from "./ui/table";
export { cn } from "./lib/utils";
