#!/usr/bin/env python3
"""Automates dependency updates (used by .github/workflows/auto-release.yml).

  dependabot.py merge    merges Dependabot's open pull requests whose checks all passed, except
                         those with a major version change (left for a person to review)
  dependabot.py decide   prints release=true|false and sha=…: true when every commit since the
                         latest release is a dependency update and one of them changes the image
  dependabot.py bump F   prepares the next patch release (Chart.yaml, CHANGELOG.md) and writes
                         its release notes to F
"""
import json
import re
import subprocess
import sys
from pathlib import Path

DEPS_COMMIT = re.compile(r"^(build|chore|deps)(\(deps(-dev)?\))?: |^Bump ", re.I)
# Files whose change ends up in the image.
RUNTIME = ("go.mod", "go.sum", "Dockerfile", "web/dashboard/package.json", "web/dashboard/package-lock.json")
VERSIONS = re.compile(r"from `?v?(\d[\w.\-+]*)`? to `?v?(\d[\w.\-+]*)")


def run(*cmd, check=True):
    return subprocess.run(cmd, check=check, capture_output=True, text=True).stdout.strip()


def major(version):
    return version.split(".")[0].split("-")[0]


def changes(pr):
    """The version changes a pull request makes, from its title and its 'Updates/Bumps' lines."""
    lines = [pr["title"]] + [l for l in pr["body"].splitlines() if l.startswith(("Updates `", "Bumps "))]
    return [(a.rstrip("."), b.rstrip(".")) for line in lines for a, b in VERSIONS.findall(line)]


def merge():
    prs = json.loads(run("gh", "pr", "list", "--author", "app/dependabot", "--state", "open", "--json",
                         "number,title,body,mergeable,statusCheckRollup"))
    for pr in prs:
        n, title = pr["number"], pr["title"]
        found = changes(pr)
        if not found:
            print(f"#{n} skipped: can't tell which versions it changes ({title})")
            continue
        if any(major(a) != major(b) for a, b in found):
            print(f"#{n} left for review: major version change ({title})")
            continue
        checks = pr["statusCheckRollup"]
        if not checks or any(c.get("status") != "COMPLETED" for c in checks):
            print(f"#{n} skipped: checks still running ({title})")
            continue
        failed = [c["name"] for c in checks if c.get("conclusion") not in ("SUCCESS", "SKIPPED", "NEUTRAL")]
        if failed:
            print(f"#{n} skipped: failed checks {failed} ({title})")
            continue
        if pr["mergeable"] != "MERGEABLE":
            print(f"#{n} skipped: {pr['mergeable']} ({title})")
            continue
        result = subprocess.run(["gh", "pr", "merge", str(n), "--squash", "--delete-branch"], capture_output=True, text=True)
        print(f"#{n} merged ({title})" if result.returncode == 0 else f"#{n} not merged: {result.stderr.strip()}")


def last_tag():
    return run("git", "describe", "--tags", "--abbrev=0", "--match", "v[0-9]*")


def decide():
    run("git", "fetch", "--quiet", "--tags", "origin", "main")
    sha = run("git", "rev-parse", "origin/main")
    tag = last_tag()
    subjects = run("git", "log", "--format=%s", f"{tag}..origin/main").splitlines()
    files = run("git", "diff", "--name-only", f"{tag}..origin/main").splitlines()
    release = "false"
    if not subjects:
        print(f"Nothing since {tag}.", file=sys.stderr)
    elif not all(DEPS_COMMIT.search(s) for s in subjects):
        print(f"Changes other than dependency updates since {tag}: release by hand.", file=sys.stderr)
    elif not any(f in RUNTIME for f in files):
        print("Only build tooling changed (e.g. GitHub Actions): no release needed.", file=sys.stderr)
    else:
        release = "true"
    print(f"release={release}")
    print(f"sha={sha}")


def bump(notes_file):
    tag = last_tag()
    major_, minor, patch = (int(x) for x in tag.lstrip("v").split("."))
    version = f"{major_}.{minor}.{patch + 1}"
    subjects = [re.sub(r"\s*\(#\d+\)$", "", s) for s in run("git", "log", "--format=%s", f"{tag}..HEAD").splitlines()]
    updates = [re.sub(DEPS_COMMIT, "", s).strip() for s in subjects]
    updates = [u[0].upper() + u[1:] for u in updates if u]

    chart = Path("charts/smart-proxy/Chart.yaml")
    text = chart.read_text()
    old_chart = re.search(r"^version: (\d+)\.(\d+)\.(\d+)$", text, re.M)
    chart_version = f"{old_chart[1]}.{old_chart[2]}.{int(old_chart[3]) + 1}"
    text = re.sub(r"^version: .*$", f"version: {chart_version}", text, count=1, flags=re.M)
    text = re.sub(r'^appVersion: .*$', f'appVersion: "{version}"', text, count=1, flags=re.M)
    text = re.sub(r"(smart-proxy:)\d+\.\d+\.\d+", rf"\g<1>{version}", text)
    changes_yaml = "".join(f"    - kind: security\n      description: {u}\n" for u in updates)
    text = re.sub(r"  artifacthub\.io/changes: \|\n(?:    .*\n?)*", "  artifacthub.io/changes: |\n" + changes_yaml, text)
    chart.write_text(text)

    log = Path("CHANGELOG.md")
    entry = f"## {version} — chart {chart_version}\n\nDependency updates, released automatically after the tests, the end-to-end suite and the vulnerability scan passed:\n\n" \
        + "".join(f"- {u}\n" for u in updates) + "\n"
    log.write_text(log.read_text().replace("# Changelog\n\n", "# Changelog\n\n" + entry, 1))

    Path(notes_file).write_text("Dependency updates, released automatically after the tests, the end-to-end suite and the "
                                f"vulnerability scan (Trivy, any severity) passed. A drop-in upgrade (chart {chart_version}).\n\n"
                                + "".join(f"- {u}\n" for u in updates))
    print(f"version={version}")
    print(f"chart={chart_version}")


if __name__ == "__main__":
    {"merge": merge, "decide": decide, "bump": lambda: bump(sys.argv[2])}[sys.argv[1]]()
