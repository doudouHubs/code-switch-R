#!/usr/bin/env bash
set -euo pipefail

if ! command -v gh >/dev/null 2>&1; then
  echo "gh CLI is required. Install from https://cli.github.com/" >&2
  exit 1
fi

if [ $# -lt 1 ]; then
  echo "Usage: scripts/publish_release.sh <tag> [notes-file]" >&2
  exit 1
fi

TAG="$1"
NOTES="${2:-RELEASE_NOTES.md}"
VERSION="${TAG#v}"

if [ ! -f "$NOTES" ]; then
  echo "Release notes file '$NOTES' not found" >&2
  exit 1
fi

MAC_APP_PRIMARY="bin/codeswitch.app"
MAC_APP_ALT="bin/CodeSwitch.app"
MAC_ARCHS=("arm64" "amd64")
MAC_ZIPS=()

package_macos_arch() {
  local arch="$1"
  local staging_app="bin/codeswitch-${arch}.app"
  local zip_path="bin/CodeSwitch-v${VERSION}-macos-${arch}.zip"

  echo "==> Building macOS ${arch}"
  env ARCH="$arch" wails3 task package ${BUILD_OPTS:-}

  local bundle_path="$MAC_APP_PRIMARY"
  if [ ! -d "$bundle_path" ] && [ -d "$MAC_APP_ALT" ]; then
    bundle_path="$MAC_APP_ALT"
  fi

  if [ ! -d "$bundle_path" ]; then
    echo "Missing asset: $MAC_APP_PRIMARY (or $MAC_APP_ALT)" >&2
    exit 1
  fi

  rm -rf "$staging_app"
  mv "$bundle_path" "$staging_app"

  echo "==> Archiving macOS app bundle (${arch})"
  rm -f "$zip_path"
  ditto -c -k --sequesterRsrc --keepParent "$staging_app" "$zip_path"
  rm -rf "$staging_app"

  MAC_ZIPS+=("$zip_path")
}

perl -0pi -e "s/const\\s+AppVersion\\s*=\\s*\"[^\"]*\"/const AppVersion = \"$TAG\"/" version_service.go

wails3 task common:update:build-assets
for arch in "${MAC_ARCHS[@]}"; do
  package_macos_arch "$arch"
done

env ARCH=amd64 wails3 task windows:package ${BUILD_OPTS:-}

# 统一文件名大小写，并使手工发布资产与 CI/自动更新的版本化命名保持一致。
if [ -f "bin/codeswitch.exe" ] && [ ! -f "bin/CodeSwitch.exe" ]; then
  mv "bin/codeswitch.exe" "bin/CodeSwitch.exe"
fi

WINDOWS_PORTABLE="bin/CodeSwitch-v${VERSION}.exe"
WINDOWS_INSTALLER="bin/CodeSwitch-v${VERSION}-amd64-installer.exe"

if [ -f "bin/CodeSwitch.exe" ]; then
  mv "bin/CodeSwitch.exe" "$WINDOWS_PORTABLE"
fi

if [ -f "build/windows/nsis/CodeSwitch-amd64-installer.exe" ]; then
  mv "build/windows/nsis/CodeSwitch-amd64-installer.exe" "$WINDOWS_INSTALLER"
elif [ -f "bin/CodeSwitch-amd64-installer.exe" ]; then
  mv "bin/CodeSwitch-amd64-installer.exe" "$WINDOWS_INSTALLER"
fi

# 生成 SHA256 哈希文件
echo "==> Generating SHA256 checksums"
generate_sha256() {
  local file="$1"
  if [ -f "$file" ]; then
    local hash_file="${file}.sha256"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum "$file" | awk '{print $1 "  " FILENAME}' FILENAME="$(basename "$file")" > "$hash_file"
    elif command -v shasum >/dev/null 2>&1; then
      shasum -a 256 "$file" | awk '{print $1 "  " FILENAME}' FILENAME="$(basename "$file")" > "$hash_file"
    else
      echo "Warning: no sha256sum or shasum available, skipping hash for $file" >&2
      return 1
    fi
    echo "  hash: $hash_file"
  fi
}

generate_sha256 "$WINDOWS_PORTABLE"

ASSETS=(
  "${MAC_ZIPS[@]}"
  "$WINDOWS_INSTALLER"
  "$WINDOWS_PORTABLE"
  "${WINDOWS_PORTABLE}.sha256"
)

for asset in "${ASSETS[@]}"; do
  [ -e "$asset" ] || { echo "Missing asset: $asset" >&2; exit 1; }
  echo "  asset: $asset"
done

gh release create "$TAG" "${ASSETS[@]}" \
  --title "$TAG" \
  --notes-file "$NOTES"
