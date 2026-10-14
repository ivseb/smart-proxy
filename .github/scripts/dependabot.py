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
import time
from pathlib import Path

DEPS_COMMIT = re.compile(r"^(build|chore|deps)(\(deps(-dev)?\))?: |^Bump ", re.I)
# Paths that don't end up in the image or the chart: changing them is no reason to hold back an
# automatic release.
NOT_SHIPPED = (".github/", "docs/", "media/", "README.md", "mkdocs.yml")
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
        mergeable = pr["mergeable"]
        for _ in range(6):  # GitHub computes it lazily, e.g. right after another merge
            if mergeable != "UNKNOWN":
                break
            time.sleep(10)
            mergeable = run("gh", "pr", "view", str(n), "--json", "mergeable", "--jq", ".mergeable")
        if mergeable != "MERGEABLE":
            print(f"#{n} skipped: {mergeable} ({title})")
            continue
        result = subprocess.run(["gh", "pr", "merge", str(n), "--squash", "--delete-branch"], capture_output=True, text=True)
        print(f"#{n} merged ({title})" if result.returncode == 0 else f"#{n} not merged: {result.stderr.strip()}")


def last_tag():
    return run("git", "describe", "--tags", "--abbrev=0", "--match", "v[0-9]*")


def shipped(sha):
    """Whether a commit changes something that ends up in the image or the chart."""
    files = run("git", "show", "--name-only", "--format=", sha).splitlines()
    return any(not f.startswith(NOT_SHIPPED) for f in files if f)


def decide():
    run("git", "fetch", "--quiet", "--tags", "origin", "main")
    sha = run("git", "rev-parse", "origin/main")
    tag = last_tag()
    commits = run("git", "log", "--format=%H %s", f"{tag}..origin/main").splitlines()
    files = run("git", "diff", "--name-only", f"{tag}..origin/main").splitlines()
    manual = [c.split(" ", 1)[1] for c in commits if not DEPS_COMMIT.search(c.split(" ", 1)[1]) and shipped(c.split(" ", 1)[0])]
    release = "false"
    if not commits:
        print(f"Nothing since {tag}.", file=sys.stderr)
    elif manual:
        print(f"Changes other than dependency updates since {tag} ({'; '.join(manual)}): release by hand.", file=sys.stderr)
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
    subjects = [re.sub(r"\s*\(#\d+\)$", "", s) for s in run("git", "log", "--format=%s", f"{tag}..HEAD").splitlines() if DEPS_COMMIT.search(s)]
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
