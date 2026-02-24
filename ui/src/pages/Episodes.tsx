import { useState } from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useEpisodes } from "@/hooks/useEpisodes";
import { relativeTime, absoluteTime, truncate } from "@/lib/format";
import type { Episode } from "@/api/types";

export default function Episodes() {
  const [channel, setChannel] = useState("");
  const [unconsolidated, setUnconsolidated] = useState(false);
  const [limit, setLimit] = useState(25);
  const [cursorStack, setCursorStack] = useState<string[]>([]);
  const [before, setBefore] = useState<string | undefined>();

  const { data: episodes, isLoading } = useEpisodes({
    channel: channel || undefined,
    before,
    unconsolidated,
    limit,
  });

  const eps = (episodes ?? []) as Episode[];

  const goNext = () => {
    if (eps.length === 0) return;
    const lastId = eps[eps.length - 1].id;
    setCursorStack((s) => [...s, before ?? ""]);
    setBefore(lastId);
  };

  const goPrev = () => {
    const stack = [...cursorStack];
    const prev = stack.pop();
    setCursorStack(stack);
    setBefore(prev || undefined);
  };

  const reset = () => {
    setCursorStack([]);
    setBefore(undefined);
  };

  return (
    <div className="p-6 space-y-4">
      <h1 className="text-2xl font-semibold">Episodes</h1>

      {/* Filters */}
      <div className="flex flex-wrap gap-3 items-end">
        <div className="space-y-1">
          <Label htmlFor="channel-filter">Channel</Label>
          <Input
            id="channel-filter"
            placeholder="Filter by channel…"
            value={channel}
            onChange={(e) => {
              setChannel(e.target.value);
              reset();
            }}
            className="w-48"
          />
        </div>

        <div className="flex items-center gap-2">
          <Switch
            id="unconsolidated"
            checked={unconsolidated}
            onCheckedChange={(v) => {
              setUnconsolidated(v);
              reset();
            }}
          />
          <Label htmlFor="unconsolidated">Unconsolidated only</Label>
        </div>

        <div className="space-y-1">
          <Label>Per page</Label>
          <Select
            value={String(limit)}
            onValueChange={(v) => {
              setLimit(Number(v));
              reset();
            }}
          >
            <SelectTrigger className="w-24">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="25">25</SelectItem>
              <SelectItem value="50">50</SelectItem>
              <SelectItem value="100">100</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      {/* Table */}
      {isLoading ? (
        <p className="text-muted-foreground text-sm">Loading…</p>
      ) : (
        <div className="border rounded-md overflow-hidden">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Content</TableHead>
                <TableHead className="w-32">Author</TableHead>
                <TableHead className="w-28">Channel</TableHead>
                <TableHead className="w-24">Source</TableHead>
                <TableHead className="w-36">When</TableHead>
                <TableHead className="w-20 text-right">Entropy</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {eps.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={6} className="text-center text-muted-foreground py-8">
                    No episodes found.
                  </TableCell>
                </TableRow>
              ) : (
                eps.map((ep) => (
                  <TableRow key={ep.id}>
                    <TableCell className="max-w-xs">
                      <span title={ep.content}>{truncate(ep.content, 80)}</span>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">
                      {ep.author ?? "—"}
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">
                      {ep.channel ?? "—"}
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">
                      {ep.source ?? "—"}
                    </TableCell>
                    <TableCell
                      className="text-muted-foreground text-xs"
                      title={absoluteTime(ep.timestamp_event)}
                    >
                      {relativeTime(ep.timestamp_event)}
                    </TableCell>
                    <TableCell className="text-right text-xs font-mono">
                      {ep.entropy_score != null
                        ? ep.entropy_score.toFixed(2)
                        : "—"}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
      )}

      {/* Pagination */}
      <div className="flex gap-2">
        <Button
          variant="outline"
          size="sm"
          onClick={goPrev}
          disabled={cursorStack.length === 0}
        >
          <ChevronLeft className="h-4 w-4" /> Prev
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={goNext}
          disabled={eps.length < limit}
        >
          Next <ChevronRight className="h-4 w-4" />
        </Button>
      </div>
    </div>
  );
}
