"""Read the three files in a FlagGuard export and correlate a retained reset."""
from contextlib import contextmanager
from datetime import datetime
import json
import re
from pathlib import Path
from tempfile import TemporaryDirectory
from zipfile import ZipFile


@contextmanager
def incident_directory(source):
    if source.is_dir():
        yield source
        return
    with ZipFile(source) as archive, TemporaryDirectory() as tmp:
        directory = Path(tmp)
        names = archive.namelist()
        for name in ("decision.json", "policy.yaml", "trace.json", "report.json"):
            if name not in names:
                continue
            if names.count(name) != 1 or archive.getinfo(name).file_size > 32 * 1024 * 1024:
                raise ValueError("ambiguous or oversized incident ZIP member")
            (directory / name).write_bytes(archive.read(name))
        yield directory


def instant(text):
    # Compare Go RFC3339Nano timestamps without losing the final three digits.
    parts = re.fullmatch(r"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})", text)
    if not parts:
        raise ValueError("expected a timestamp with an explicit timezone")
    base, fraction, offset = parts.groups()
    stamp = datetime.fromisoformat(base + offset.replace("Z", "+00:00"))
    return stamp, int((fraction or "").ljust(9, "0"))


def recovery_history(path, incident, receipt):
    """Keep historical corroboration separate from both live reads and replay."""
    try:
        report = json.loads(path.read_bytes())
        if report["schema_version"] != 1 or not isinstance(report["events"], list):
            raise ValueError("unknown report format")
        rows = [row for row in report["events"] if row["flag"] == incident["flag"]]
        anchors = [i for i, row in enumerate(rows) if row == incident]
        if len(anchors) != 1:
            return {"history": "INCIDENT_NOT_IDENTIFIED"}
        started = instant(incident["time"])
        cutoff = instant(report["generated_at"])
        if started > cutoff:
            raise ValueError("report predates incident")
        reset = None
        for row in rows[anchors[0]+1:]:
            kind = row["kind"]
            if kind not in ("ROLLBACK", "ROLLBACK_UNCONFIRMED", "RECOVERY_UNCONFIRMED", "RESET"):
                continue
            when = instant(row["time"])
            if not started < when <= cutoff:
                raise ValueError("inconsistent report chronology")
            started = when
            if kind != "RESET":
                return {"history": "LATER_ROLLBACK" if kind == "ROLLBACK" else "UNCONFIRMED_CHANGE"}
            change = row["change"]
            if (change["experiment_id"] != receipt["experiment"]
                    or change["feature_state_id"] != incident["change"]["feature_state_id"]
                    or change["after_enabled"] is not True
                    or not all(type(change[k]) is bool for k in ("default_after_enabled", "rollout_after_enabled"))):
                return {"history": "UNCONFIRMED_CHANGE"}
            reset = row
        if reset is None:
            return {"history": "NO_RETAINED_RESET"}
        change = reset["change"]
        matches = (change["default_after_enabled"] is receipt["default_enabled"]
                   and change["rollout_after_enabled"] is receipt["rollout_enabled"])
        return {"history": "MATCHING_RESET" if matches else "RESET_DIFFERS", "reset_time": reset["time"]}
    except (OSError, ValueError, KeyError, TypeError, AttributeError):
        return {"history": "REPORT_UNAVAILABLE"}
