import { useNavigate } from "react-router";
import { useState } from "react";
import { Brain, FileText, Users, AlertCircle, Search } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useEngrams } from "@/hooks/useEngrams";
import { useEpisodeCount } from "@/hooks/useEpisodes";
import { useEntities } from "@/hooks/useEntities";
import { useHealth } from "@/hooks/useHealth";
import { relativeTime, truncate } from "@/lib/format";

function StatCard({
  title,
  value,
  icon: Icon,
  loading,
}: {
  title: string;
  value: number | string;
  icon: React.ElementType;
  loading?: boolean;
}) {
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-sm font-medium">{title}</CardTitle>
        <Icon className="h-4 w-4 text-muted-foreground" />
      </CardHeader>
      <CardContent>
        <div className="text-2xl font-bold">
          {loading ? <span className="text-muted-foreground">…</span> : value}
        </div>
      </CardContent>
    </Card>
  );
}

export default function Dashboard() {
  const navigate = useNavigate();
  const [q, setQ] = useState("");

  const { data: engrams, isLoading: engramsLoading } = useEngrams({
    threshold: 0,
    limit: 10,
  });
  const { data: epCount, isLoading: epLoading } = useEpisodeCount();
  const { data: epUncon } = useEpisodeCount({ unconsolidated: true });
  const { data: entities, isLoading: entLoading } = useEntities({ limit: 1000 });
  const { data: health, isError: healthError } = useHealth();

  const handleSearch = (e: React.FormEvent) => {
    e.preventDefault();
    if (q.trim()) void navigate(`/search?q=${encodeURIComponent(q.trim())}`);
  };

  const recentEngrams = engrams?.slice(0, 10) ?? [];

  return (
    <div className="p-6 space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold">Dashboard</h1>
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <span
            className={`h-2 w-2 rounded-full ${healthError ? "bg-destructive" : health ? "bg-emerald-500" : "bg-muted"}`}
          />
          {healthError ? "API unreachable" : health ? "API healthy" : "Checking…"}
        </div>
      </div>

      {/* Stats */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          title="Engrams"
          value={engrams?.length ?? 0}
          icon={Brain}
          loading={engramsLoading}
        />
        <StatCard
          title="Episodes"
          value={epCount?.count ?? 0}
          icon={FileText}
          loading={epLoading}
        />
        <StatCard
          title="Unconsolidated"
          value={epUncon?.count ?? 0}
          icon={AlertCircle}
        />
        <StatCard
          title="Entities"
          value={entities?.length ?? 0}
          icon={Users}
          loading={entLoading}
        />
      </div>

      {/* Quick search */}
      <form onSubmit={handleSearch} className="flex gap-2 max-w-lg">
        <Input
          placeholder="Quick search…"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <Button type="submit" size="icon">
          <Search className="h-4 w-4" />
        </Button>
      </form>

      {/* Recent engrams */}
      <div>
        <h2 className="text-lg font-medium mb-3">Recent Engrams</h2>
        {engramsLoading ? (
          <p className="text-muted-foreground text-sm">Loading…</p>
        ) : recentEngrams.length === 0 ? (
          <p className="text-muted-foreground text-sm">No engrams yet.</p>
        ) : (
          <div className="border rounded-md overflow-hidden">
            <table className="w-full text-sm">
              <thead className="bg-muted/50">
                <tr>
                  <th className="text-left px-4 py-2 font-medium">Summary</th>
                  <th className="text-left px-4 py-2 font-medium w-24">Type</th>
                  <th className="text-left px-4 py-2 font-medium w-32">Activation</th>
                  <th className="text-left px-4 py-2 font-medium w-36">When</th>
                </tr>
              </thead>
              <tbody className="divide-y">
                {recentEngrams.map((e) => {
                  const eng = e as {
                    id: string;
                    summary: string;
                    engram_type?: string;
                    activation: number;
                    event_time: string;
                  };
                  return (
                    <tr key={eng.id} className="hover:bg-muted/30">
                      <td className="px-4 py-2 max-w-sm">
                        {truncate(eng.summary, 80)}
                      </td>
                      <td className="px-4 py-2">
                        <Badge
                          variant={
                            eng.engram_type === "operational"
                              ? "secondary"
                              : "default"
                          }
                          className="text-xs"
                        >
                          {eng.engram_type ?? "knowledge"}
                        </Badge>
                      </td>
                      <td className="px-4 py-2">
                        <div className="flex items-center gap-2">
                          <div className="h-1.5 bg-muted rounded-full flex-1">
                            <div
                              className="h-1.5 bg-primary rounded-full"
                              style={{
                                width: `${Math.min(100, eng.activation * 100)}%`,
                              }}
                            />
                          </div>
                          <span className="text-xs text-muted-foreground w-10">
                            {eng.activation.toFixed(2)}
                          </span>
                        </div>
                      </td>
                      <td className="px-4 py-2 text-muted-foreground">
                        {relativeTime(eng.event_time)}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  );
}
