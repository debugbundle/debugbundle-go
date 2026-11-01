#!/usr/bin/env python3
"""Build a deterministic unpublished module proxy and its exact source manifest."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import zipfile

root = Path(__file__).resolve().parent.parent
module = re.search(r"^module (\S+)", (root / "go.mod").read_text()).group(1)
version = "v" + re.search(r'Version\s*=\s*"([0-9.]+)"', (root / "config.go").read_text()).group(1)
proxy = root / ".tmp/evidence-module-proxy"
destination = proxy / module / "@v"
destination.mkdir(parents=True, exist_ok=True)
names = subprocess.check_output(
    ["git", "-C", str(root), "ls-files", "--cached", "--others", "--exclude-standard", "-z"]
).decode().split("\0")
sources = []
for name in sorted(set(names) - {""}):
    path = root / name
    parts = path.relative_to(root).parts
    if path.is_symlink() or not path.is_file() or any(p in {".git", ".tmp", ".smoke", ".debugbundle", "vendor"} for p in parts):
        continue
    if any(p.startswith(".env") for p in parts) or path.suffix in {".pem", ".key", ".p12"}:
        continue
    sources.append({"path": name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()})

archive = destination / f"{version}.zip"
with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as output:
    for source in sources:
        info = zipfile.ZipInfo(f"{module}@{version}/{source['path']}", date_time=(1980, 1, 1, 0, 0, 0))
        info.compress_type = zipfile.ZIP_DEFLATED
        info.external_attr = 0o100644 << 16
        output.writestr(info, (root / source["path"]).read_bytes())
(destination / f"{version}.mod").write_bytes((root / "go.mod").read_bytes())
base_time = subprocess.check_output(["git", "-C", str(root), "show", "-s", "--format=%cI", "HEAD"], text=True).strip()
(destination / f"{version}.info").write_text(json.dumps({"Version": version, "Time": base_time}) + "\n")
manifest = {
    "version": 1, "candidate_version": version, "module": module,
    "source_state": "uncommitted local candidate", "sources": sources,
    "base_commit": subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip(),
    "source_sha256": hashlib.sha256(json.dumps(sources, sort_keys=True, separators=(",", ":")).encode()).hexdigest(),
    "artifacts": [{"path": str(p.relative_to(root)), "sha256": hashlib.sha256(p.read_bytes()).hexdigest(), "bytes": p.stat().st_size}
                  for p in sorted(destination.glob(f"{version}.*"))],
    "qualification": "Run smoke-module-artifact against these bytes; creation alone is not installed qualification.",
}
(root / ".tmp/evidence-artifact-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
print(f"Prepared unpublished {module}@{version} module archive with {len(sources)} source identities")
