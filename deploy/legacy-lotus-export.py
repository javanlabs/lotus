#!/usr/bin/env python3
"""Export the legacy Lotus's metric history for `lotus import-legacy`.

Runs on the legacy server (collab) against its SQLite database, opened
read-only. Writes JSON Lines to stdout: a header {"kind": "meta",
"exported_at": ...}, then every SonarQube, GitLab and Trivy snapshot,
oldest first. Only those three tables are read — nothing from chat,
sessions, users or tokens — and raw_json is left out (the importer keeps
the columns; raw detail ages out after 30 days anyway).

    sudo python3 legacy-lotus-export.py /opt/lotus/data/lotus.db > legacy.jsonl
"""

import json
import sqlite3
import sys
from datetime import datetime, timezone

TABLES = {
    "sonar": ("sonar_snapshots", [
        "service", "captured_at", "quality_gate", "coverage", "bugs", "vulnerabilities",
        "code_smells", "security_hotspots", "duplicated_lines_density", "ncloc", "complexity",
        "reliability_rating", "security_rating", "sqale_rating", "sqale_index_min",
        "test_execution_time_ms", "languages", "last_scan_at",
    ]),
    "gitlab": ("gitlab_snapshots", [
        "service", "captured_at", "primary_language", "languages_json", "default_branch",
        "branches_total", "branches_active", "branches_stale", "last_pipeline_status",
        "last_pipeline_id", "last_pipeline_duration_sec", "last_pipeline_web_url",
        "last_pipeline_finished_at", "last_pipeline_ref", "pipeline_success_rate",
        "pipeline_sample_size", "last_activity_at", "commits_30d", "commits_30d_capped",
        "default_branch_protected", "visibility", "archived", "web_url", "last_committer_name",
    ]),
    "trivy": ("trivy_snapshots", [
        "repo", "image", "captured_at", "digest", "tags", "severity", "total", "fixable",
        "critical", "high", "medium", "low", "unknown", "pulls", "artifact_count",
        "image_pushed_at", "scan_finished_at",
    ]),
}


def sort_key(ts):
    """A sortable UTC datetime from the legacy DATETIME text (which may carry
    nanoseconds and a +00:00 offset)."""
    s = str(ts).strip().replace("T", " ")
    date_time, offset = s, ""
    for sign in ("+", "-"):
        i = s.rfind(sign)
        if i > 10:
            date_time, offset = s[:i], s[i:]
            break
    if date_time.endswith("Z"):
        date_time, offset = date_time[:-1], "+00:00"
    if "." in date_time:
        head, frac = date_time.split(".", 1)
        date_time = head + "." + frac[:6].ljust(6, "0")
    dt = datetime.fromisoformat(date_time + (offset or "+00:00"))
    return dt.astimezone(timezone.utc)


def main():
    if len(sys.argv) != 2:
        sys.exit("usage: legacy-lotus-export.py <path to lotus.db>")
    db = sqlite3.connect("file:%s?mode=ro" % sys.argv[1], uri=True)
    db.row_factory = sqlite3.Row
    rows = []
    for kind, (table, cols) in TABLES.items():
        for r in db.execute("SELECT %s FROM %s" % (", ".join(cols), table)):
            rec = {"kind": kind}
            for c in cols:
                if r[c] is not None:
                    rec[c] = r[c]
            rows.append((sort_key(rec["captured_at"]), rec))
    rows.sort(key=lambda x: x[0])
    out = sys.stdout
    out.write(json.dumps({"kind": "meta", "exported_at": datetime.now(timezone.utc).isoformat()}) + "\n")
    for _, rec in rows:
        out.write(json.dumps(rec, ensure_ascii=False) + "\n")
    counts = {k: sum(1 for _, r in rows if r["kind"] == k) for k in TABLES}
    print("exported %s" % counts, file=sys.stderr)


if __name__ == "__main__":
    main()
