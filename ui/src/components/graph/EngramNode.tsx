import { memo } from "react";
import { Handle, Position } from "@xyflow/react";
import type { Engram } from "@/api/types";

interface EngramNodeData {
  engram: Engram;
}

function EngramNode({ data }: { data: EngramNodeData }) {
  const { engram } = data;
  const isLabile =
    engram.labile_until && new Date(engram.labile_until) > new Date();
  const isOperational = engram.engram_type === "operational";

  return (
    <div
      className={`w-48 rounded-md border-2 bg-white shadow-sm text-xs ${
        isOperational ? "border-amber-500" : "border-blue-500"
      }`}
    >
      <Handle type="target" position={Position.Left} className="!bg-gray-400" />

      <div className="px-3 py-2 space-y-1.5">
        {/* Header row */}
        <div className="flex items-center gap-1.5">
          <span
            className={`rounded-sm px-1.5 py-0.5 text-[10px] font-medium ${
              isOperational
                ? "bg-amber-100 text-amber-700"
                : "bg-blue-100 text-blue-700"
            }`}
          >
            {isOperational ? "oper" : "know"}
          </span>
          {isLabile && (
            <span
              className="h-1.5 w-1.5 rounded-full bg-amber-400 animate-pulse"
              title="Labile"
            />
          )}
          <span className="ml-auto font-mono text-[10px] text-gray-400">
            {engram.activation.toFixed(2)}
          </span>
        </div>

        {/* Summary */}
        <p
          className="line-clamp-3 leading-snug text-gray-800"
          title={engram.summary}
        >
          {engram.summary}
        </p>
      </div>

      <Handle
        type="source"
        position={Position.Right}
        className="!bg-gray-400"
      />
    </div>
  );
}

export default memo(EngramNode);
