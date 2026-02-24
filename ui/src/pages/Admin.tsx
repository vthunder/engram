import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Zap, Activity, Trash2, CheckCircle, XCircle } from "lucide-react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { consolidate, decayActivation, resetMemory } from "@/api/admin";
import type { ConsolidateResponse, DecayResponse } from "@/api/types";

function ResultLine({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="flex justify-between text-sm py-0.5">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono font-medium">{value}</span>
    </div>
  );
}

export default function Admin() {
  const [resetInput, setResetInput] = useState("");
  const [resetOpen, setResetOpen] = useState(false);

  const consolidateMut = useMutation<ConsolidateResponse, Error>({
    mutationFn: consolidate,
  });

  const decayMut = useMutation<DecayResponse, Error>({
    mutationFn: decayActivation,
  });

  const resetMut = useMutation({
    mutationFn: resetMemory,
    onSuccess: () => {
      setResetOpen(false);
      setResetInput("");
    },
  });

  return (
    <div className="p-6 space-y-6">
      <h1 className="text-2xl font-semibold">Admin</h1>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {/* Consolidate */}
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Zap className="h-4 w-4" /> Consolidate
            </CardTitle>
            <CardDescription>
              Run consolidation to create new engrams from unconsolidated episodes.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <Button
              onClick={() => consolidateMut.mutate()}
              disabled={consolidateMut.isPending}
              className="w-full"
            >
              {consolidateMut.isPending ? "Running…" : "Run Consolidation"}
            </Button>
            {consolidateMut.isSuccess && (
              <div className="rounded-md bg-muted p-3">
                <div className="flex items-center gap-1 text-sm font-medium text-emerald-600 mb-1">
                  <CheckCircle className="h-3.5 w-3.5" /> Done
                </div>
                <ResultLine
                  label="Engrams created"
                  value={consolidateMut.data.engrams_created}
                />
                <ResultLine
                  label="Duration"
                  value={`${consolidateMut.data.duration_ms}ms`}
                />
              </div>
            )}
            {consolidateMut.isError && (
              <div className="flex items-center gap-1 text-sm text-destructive">
                <XCircle className="h-3.5 w-3.5" />
                {consolidateMut.error.message}
              </div>
            )}
          </CardContent>
        </Card>

        {/* Decay */}
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Activity className="h-4 w-4" /> Decay Activation
            </CardTitle>
            <CardDescription>
              Apply time-based activation decay to all engrams.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <Button
              variant="secondary"
              onClick={() => decayMut.mutate()}
              disabled={decayMut.isPending}
              className="w-full"
            >
              {decayMut.isPending ? "Running…" : "Run Decay"}
            </Button>
            {decayMut.isSuccess && (
              <div className="rounded-md bg-muted p-3">
                <div className="flex items-center gap-1 text-sm font-medium text-emerald-600 mb-1">
                  <CheckCircle className="h-3.5 w-3.5" /> Done
                </div>
                <ResultLine label="Updated" value={decayMut.data.updated} />
              </div>
            )}
            {decayMut.isError && (
              <div className="flex items-center gap-1 text-sm text-destructive">
                <XCircle className="h-3.5 w-3.5" />
                {decayMut.error.message}
              </div>
            )}
          </CardContent>
        </Card>

        {/* Reset */}
        <Card className="border-destructive/50">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-destructive">
              <Trash2 className="h-4 w-4" /> Danger Zone
            </CardTitle>
            <CardDescription>
              Permanently delete all memory data. This cannot be undone.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <Dialog open={resetOpen} onOpenChange={setResetOpen}>
              <DialogTrigger asChild>
                <Button variant="destructive" className="w-full">
                  Reset Memory
                </Button>
              </DialogTrigger>
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>Reset All Memory</DialogTitle>
                  <DialogDescription>
                    This will permanently delete all engrams, episodes, and entities.
                    Type <strong>RESET</strong> to confirm.
                  </DialogDescription>
                </DialogHeader>
                <Input
                  placeholder="RESET"
                  value={resetInput}
                  onChange={(e) => setResetInput(e.target.value)}
                />
                <DialogFooter>
                  <Button
                    variant="outline"
                    onClick={() => {
                      setResetOpen(false);
                      setResetInput("");
                    }}
                  >
                    Cancel
                  </Button>
                  <Button
                    variant="destructive"
                    disabled={resetInput !== "RESET" || resetMut.isPending}
                    onClick={() => resetMut.mutate()}
                  >
                    {resetMut.isPending ? "Resetting…" : "Confirm Reset"}
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>
            {resetMut.isSuccess && (
              <div className="flex items-center gap-1 text-sm text-emerald-600">
                <CheckCircle className="h-3.5 w-3.5" /> Memory reset complete.
              </div>
            )}
            {resetMut.isError && (
              <div className="flex items-center gap-1 text-sm text-destructive">
                <XCircle className="h-3.5 w-3.5" />
                {(resetMut.error as Error).message}
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
