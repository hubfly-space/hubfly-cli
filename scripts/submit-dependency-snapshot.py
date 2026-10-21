#!/usr/bin/env python3
"""Submit a Go dependency snapshot to the GitHub Dependency Submission API.

The snapshot mirrors the `require` directives of go.mod, with the versions
resolved through the module graph (MVS), and is submitted as the `go.mod`
manifest. Submitting it from CI keeps the repository dependency graph in sync
with every push to the default branch, so Dependabot alerts reflect the
versions that are actually built instead of lagging behind go.mod changes.

Usage:
    submit-dependency-snapshot.py [output.json]

Environment:
    GITHUB_TOKEN, GITHUB_REPOSITORY, GITHUB_SHA  (required for submission)
    GITHUB_REF, GITHUB_RUN_ID, GITHUB_WORKFLOW, GITHUB_JOB  (optional)
    SNAPSHOT_DETECTOR_NAME, SNAPSHOT_DETECTOR_URL, SNAPSHOT_DETECTOR_VERSION,
    SNAPSHOT_CORRELATOR                                (optional)
"""
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request


def run(args):
    result = subprocess.run(args, capture_output=True, text=True)
    if result.returncode != 0:
        raise SystemExit(f"command failed: {' '.join(args)}\n{result.stderr}")
    return result.stdout


def go_mod_requires():
    """Return [(path, version, indirect)] for every require directive."""
    mod = json.loads(run(["go", "mod", "edit", "-json"]))
    return [
        (req["Path"], req["Version"], bool(req.get("Indirect")))
        for req in mod.get("Require") or []
    ]


def resolved_versions():
    """Return {path: version} for the versions selected by the module graph."""
    versions = {}
    for line in run(["go", "list", "-m", "all"]).splitlines():
        parts = line.split()
        if len(parts) == 2:
            versions[parts[0]] = parts[1]
    return versions


def submit(payload, owner, repo, token):
    """POST the snapshot to the Dependency Submission API."""
    url = f"https://api.github.com/repos/{owner}/{repo}/dependency-graph/snapshots"
    request = urllib.request.Request(
        url,
        data=json.dumps(payload).encode(),
        method="POST",
        headers={
            "Authorization": f"Bearer {token}",
            "Accept": "application/vnd.github+json",
            "Content-Type": "application/json",
        },
    )
    with urllib.request.urlopen(request) as response:
        return json.load(response)


def main():
    out_path = sys.argv[1] if len(sys.argv) > 1 else "snapshot.json"

    owner, _, repo = os.environ["GITHUB_REPOSITORY"].partition("/")
    selected = resolved_versions()

    resolved = {}
    for path, version, indirect in go_mod_requires():
        version = selected.get(path, version)
        resolved[path] = {
            "package_url": f"pkg:golang/{path}@{version}",
            "relationship": "indirect" if indirect else "direct",
            "scope": "runtime",
            "dependencies": [],
        }

    payload = {
        "version": 0,
        "sha": os.environ["GITHUB_SHA"],
        "ref": os.environ.get("GITHUB_REF", "refs/heads/main"),
        "scanned": run(["date", "-u", "+%Y-%m-%dT%H:%M:%SZ"]).strip(),
        "detector": {
            "name": os.environ.get("SNAPSHOT_DETECTOR_NAME", "hubfly-cli"),
            "url": os.environ.get(
                "SNAPSHOT_DETECTOR_URL", "https://github.com/hubfly-space/hubfly-cli"
            ),
            "version": os.environ.get("SNAPSHOT_DETECTOR_VERSION", "1.0.0"),
        },
        "job": {
            "correlator": os.environ.get(
                "SNAPSHOT_CORRELATOR",
                f"{os.environ.get('GITHUB_WORKFLOW', 'CI')}"
                f"-{os.environ.get('GITHUB_JOB', 'dependency-submission')}",
            ),
            "id": os.environ.get("GITHUB_RUN_ID", "0"),
        },
        "manifests": {
            "go.mod": {
                "name": "go.mod",
                "file": {"source_location": "go.mod"},
                "metadata": {},
                "resolved": resolved,
            }
        },
    }

    with open(out_path, "w") as handle:
        json.dump(payload, handle, indent=2)

    print(f"wrote {out_path} with {len(resolved)} dependencies")

    token = os.environ.get("GITHUB_TOKEN")
    if not token:
        print("GITHUB_TOKEN is not set, skipping submission")
        return

    try:
        result = submit(payload, owner, repo, token)
    except urllib.error.HTTPError as error:
        raise SystemExit(f"submission failed: {error.code} {error.read().decode()}")
    print(f"submission result: {result.get('result')} {result.get('message', '')}")


if __name__ == "__main__":
    main()
