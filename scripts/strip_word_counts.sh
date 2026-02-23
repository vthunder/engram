#!/bin/bash
# Strip "(N words)" / "(N word)" annotations from episode, engram, and entity summaries.
# These were added by the LLM due to an ambiguous prompt suffix.
#
# Usage: ./scripts/strip_word_counts.sh [path/to/engram.db]
#        Defaults to ./engram.db if no argument given.

set -euo pipefail

DB="${1:-./engram.db}"

if [[ ! -f "$DB" ]]; then
  echo "ERROR: database not found: $DB"
  exit 1
fi

echo "Stripping word-count annotations from: $DB"

python3 - "$DB" <<'EOF'
import sqlite3
import re
import sys

db_path = sys.argv[1]
# Matches trailing patterns like: " (7 words)", " (1 word)", "\n(12 words)", etc.
pattern = re.compile(r'\s*\(\d+\s+words?\)\s*$', re.IGNORECASE)

conn = sqlite3.connect(db_path)
cur = conn.cursor()

tables = [
    ("episode_summaries", "id", "summary"),
    ("engram_summaries",  "id", "summary"),
    ("entity_summaries",  "id", "summary"),
]

total = 0
for table, pk, col in tables:
    cur.execute(f"SELECT {pk}, {col} FROM {table}")
    rows = cur.fetchall()
    updated = 0
    for row_id, text in rows:
        if text is None:
            continue
        cleaned = pattern.sub('', text)
        if cleaned != text:
            cur.execute(f"UPDATE {table} SET {col} = ? WHERE {pk} = ?", (cleaned, row_id))
            updated += 1
    print(f"  {table}: {updated} rows cleaned")
    total += updated

conn.commit()
conn.close()
print(f"Done. Total rows updated: {total}")
EOF
