#!/usr/bin/env bash
# release.sh — bump version, commit, tag, push, and create a GitHub release.
# Usage: ./release.sh v0.2.0 ["optional commit message"]
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="drakkar-media/drakkar"

VERSION="${1:-}"
COMMIT_MESSAGE="${2:-}"

# ── Validation ────────────────────────────────────────────────────────────────

if [[ -z "$VERSION" ]]; then
  echo "Usage: ./release.sh v0.2.0 [\"optional commit message\"]"
  exit 1
fi

if ! [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9._-]+)?$ ]]; then
  echo "Version must look like v0.1.1 or v0.1.1-rc1"
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "gh CLI missing — install from https://cli.github.com"
  exit 1
fi

gh auth status >/dev/null

if [[ ! -d "$ROOT_DIR/.git" ]]; then
  echo "Not a git repository: $ROOT_DIR"
  exit 1
fi

PACKAGE_VERSION="${VERSION#v}"
DEFAULT_COMMIT_MESSAGE="Release $VERSION"
COMMIT_MESSAGE="${COMMIT_MESSAGE:-$DEFAULT_COMMIT_MESSAGE}"

# ── Update version files ──────────────────────────────────────────────────────

echo "==> Updating version files to $PACKAGE_VERSION"

echo "$PACKAGE_VERSION" > "$ROOT_DIR/VERSION"

cat > "$ROOT_DIR/internal/version/version.go" <<EOF
package version

// Version is the current Drakkar release. Updated by release.sh.
const Version = "$PACKAGE_VERSION"
EOF

# ── Sanity check: project builds ──────────────────────────────────────────────

echo "==> Building to verify no compile errors"
(cd "$ROOT_DIR" && go build ./...)

# ── Git: commit, tag, push ───────────────────────────────────────────────────

echo "==> Checking git state"
git -C "$ROOT_DIR" fetch origin main --tags

if git -C "$ROOT_DIR" rev-parse "$VERSION" >/dev/null 2>&1; then
  echo "Local tag already exists: $VERSION"
  exit 1
fi

if git -C "$ROOT_DIR" ls-remote --tags origin "refs/tags/$VERSION" | grep -q "$VERSION"; then
  echo "Remote tag already exists: $VERSION"
  exit 1
fi

if [[ -n "$(git -C "$ROOT_DIR" status --porcelain)" ]]; then
  echo "==> Committing version bump"
  git -C "$ROOT_DIR" add -A
  git -C "$ROOT_DIR" commit -m "$COMMIT_MESSAGE"
else
  echo "No uncommitted changes — skipping commit"
fi

echo "==> Rebasing and pushing to main"
git -C "$ROOT_DIR" pull --rebase origin main
git -C "$ROOT_DIR" push origin main

echo "==> Creating and pushing tag $VERSION"
git -C "$ROOT_DIR" tag -a "$VERSION" -m "Drakkar $VERSION"
git -C "$ROOT_DIR" push origin "$VERSION"

# ── GitHub release ───────────────────────────────────────────────────────────

echo "==> Creating GitHub release $VERSION"
if gh release view "$VERSION" -R "$REPO" >/dev/null 2>&1; then
  echo "Release already exists: $VERSION"
else
  gh release create "$VERSION" \
    -R "$REPO" \
    --verify-tag \
    --title "$VERSION" \
    --notes "Drakkar $VERSION

Changes in this release are tracked in the commit history.
Docker image: \`ghcr.io/drakkar-media/drakkar:$VERSION\`"
fi

echo
echo "Done. Released $VERSION — GitHub Actions will build and push the Docker image."
echo "Image will be available at: ghcr.io/drakkar-media/drakkar:$VERSION"
