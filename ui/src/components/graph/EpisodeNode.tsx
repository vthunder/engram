import { memo } from "react";
import { Handle, Position } from "@xyflow/react";
import type { Episode } from "@/api/types";
import { relativeTime, truncate } from "@/lib/format";

interface EpisodeNodeData {
  episode: Episode;
}

function EpisodeNode({ data }: { data: EpisodeNodeData }) {
  const { episode } = data;

  return (
    <div className="w-36 rounded border bg-gray-50 shadow-sm text-xs">
      <Handle type="target" position={Position.Left} className="!bg-gray-400" />

      <div className="px-2 py-1.5 space-y-1">
        <p
          className="leading-snug text-gray-700 line-clamp-2"
          title={episode.content}
        >
          {truncate(episode.content, 60)}
        </p>
        <div className="text-[10px] text-gray-400 space-y-0.5">
          {episode.author && <div>{episode.author}</div>}
          <div>{relativeTime(episode.timestamp_event)}</div>
        </div>
      </div>

      <Handle
        type="source"
        position={Position.Right}
        className="!bg-gray-400"
      />
    </div>
  );
}

export default memo(EpisodeNode);
