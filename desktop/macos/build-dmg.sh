#!/bin/bash
set -euo pipefail

[[ $# == 5 && $(uname -s) == Darwin ]] || { echo 'usage: build-dmg.sh APP_SOURCE ELECTRON_ZIP ENGINE_ZIP NODE OUTPUT_NEW' >&2; exit 2; }
app_src=$(cd "$1" && pwd -P)
electron_zip=$2
engine_zip=$3
node=$4
output=$5
source=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
[[ $node == /* && $output == /* && ! -e $output && ! -L $output ]] || exit 2
arch=$("$node" -e 'if (process.version !== "v22.23.5" || process.platform !== "darwin") process.exit(2); process.stdout.write(process.arch)')
electron_version=45.2.3
case "$(uname -m):$arch" in
  arm64:arm64)
    electron_sha=4d7c429064087a47b855d9bd06c76ead4819b7f02b385de232d4a5c37ecef55f
    engine_sha=c0be3f34dde2fdcb31b25120f303c6ed5b69b828f16e5e0fbecff21a882adda1
    ;;
  x86_64:x64)
    electron_sha=d87323533cc8750ef177e5b48058f7a8b381f677d4e103cc63360006c08829c2
    engine_sha=5f08d3f7d7555fdef8025f57aef89c5bc2448460827d4e225f815a308ce6976b
    ;;
  *) exit 2 ;;
esac
hash() { /usr/bin/shasum -a 256 "$1" | /usr/bin/awk '{print $1}'; }
[[ $(hash "$electron_zip") == "$electron_sha" && $(hash "$engine_zip") == "$engine_sha" ]] || { echo 'Archive checksum mismatch' >&2; exit 3; }
"$node" -e 'const p = require(process.argv[1]); if (p.main !== "desktop/open-code-macos.cjs" || p.private !== true) process.exit(2)' "$app_src/package.json"
[[ -f $app_src/desktop/open-code-macos.cjs && -d $app_src/src && -d $app_src/notices ]] || exit 2

mkdir "$output"
work="$output/work"
mkdir -p "$work/electron" "$work/disk"
/usr/bin/ditto -x -k "$electron_zip" "$work/electron"
[[ $(tr -d '\r\n' <"$work/electron/version") == "$electron_version" ]] || exit 3
app="$work/disk/ZTAPI Workbench Beta.app"
mv "$work/electron/Electron.app" "$app"
res="$app/Contents/Resources"
native="$res/ztapi-native"
mkdir -p "$res/app" "$native"
for leaf in package.json desktop src; do /usr/bin/ditto "$app_src/$leaf" "$res/app/$leaf"; done
mkdir -p "$res/app/desktop/macos"
for leaf in engine-config.mjs engine-host.mjs engine-pipe.mjs engine-project-rpc.mjs project-ports.mjs project-review.mjs project-task.mjs project-test-input.mjs; do
  /usr/bin/ditto "$source/$leaf" "$res/app/desktop/macos/$leaf"
done
/usr/bin/ditto "$app_src/notices" "$res/ZTAPI Notices"
/usr/bin/ditto "$work/electron/LICENSE" "$res/ZTAPI Notices/Electron-LICENSE"
/usr/bin/ditto "$work/electron/LICENSES.chromium.html" "$res/ZTAPI Notices/Electron-LICENSES.chromium.html"
rm -f "$res/default_app.asar"

plist() {
  printf '%s\n' '<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>' "$1" '</dict></plist>'
}
entitlement='<key>com.apple.security.app-sandbox</key><true/>'
plist "$entitlement" >"$work/sandbox.plist"
plist "$entitlement<key>com.apple.security.inherit</key><true/>" >"$work/inherit.plist"
worker() {
  local bundle=$1 id=$2 executable=$3 entry=$4
  local mac="$1/Contents/MacOS" rr="$1/Contents/Resources"
  mkdir -p "$mac" "$rr"
  plist "<key>CFBundleIdentifier</key><string>$id</string><key>CFBundleExecutable</key><string>$executable</string><key>CFBundlePackageType</key><string>APPL</string><key>CFBundleVersion</key><string>1</string>" >"$bundle/Contents/Info.plist"
  /usr/bin/clang -std=c11 -Wall -Werror "-DZTAPI_WORKER_ENTRY=\"$entry\"" "$source/ProjectTestHost.c" -o "$mac/$executable"
  /usr/bin/ditto "$node" "$mac/node"
  /usr/bin/codesign --force --sign - --entitlements "$work/inherit.plist" "$mac/node"
}

engine="$native/ZTAPI Engine.app"
worker "$engine" com.ztapi.workbench.engine ZTAPIOpenCode opencode-launch.mjs
e_res="$engine/Contents/Resources"
for leaf in opencode-launch.mjs engine-config.mjs engine-pipe.mjs engine-transport.mjs opencode-pipe-plugin.mjs; do
  /usr/bin/ditto "$source/$leaf" "$e_res/$leaf"
done
mkdir -p "$e_res/engine-config/opencode"
printf 'node_modules\n' >"$e_res/engine-config/opencode/.gitignore"
chmod 0444 "$e_res/engine-config/opencode/.gitignore"
chmod 0555 "$e_res/engine-config" "$e_res/engine-config/opencode"
engine_file="$engine/Contents/MacOS/opencode"
/usr/bin/unzip -p "$engine_zip" opencode >"$engine_file"
chmod 0755 "$engine_file"
[[ $("$engine_file" --version) == 1.19.6 ]] || exit 3
/usr/bin/codesign --force --sign - --entitlements "$work/inherit.plist" "$engine_file"
/usr/bin/codesign --force --sign - --entitlements "$work/sandbox.plist" "$engine"
/usr/bin/codesign --verify --deep --strict "$engine"

tests="$native/ZTAPI Tests.app"
worker "$tests" com.ztapi.workbench.projecttest ZTAPITestWorker project-test-worker.mjs
for leaf in project-test-worker.mjs project-test-input.mjs; do
  /usr/bin/ditto "$source/$leaf" "$tests/Contents/Resources/$leaf"
done
/usr/bin/codesign --force --sign - --entitlements "$work/sandbox.plist" "$tests"
/usr/bin/codesign --verify --deep --strict "$tests"
/usr/bin/swiftc -O "$source/ProjectBroker.swift" -o "$native/ZTAPIProjectBroker"
/usr/bin/codesign --force --sign - "$native/ZTAPIProjectBroker"

info="$app/Contents/Info.plist"
/usr/libexec/PlistBuddy -c 'Set :CFBundleIdentifier com.ztapi.workbench.macos.beta' "$info"
/usr/libexec/PlistBuddy -c 'Set :CFBundleName ZTAPI Workbench Beta' "$info"

# Some upstream Electron ZIPs omit signatures on helpers. Sign only the missing
# or invalid code in the Electron frameworks, never recursively over our workers.
while IFS= read -r -d '' item; do
  if [[ -f $item && $(/usr/bin/file -b "$item") != *Mach-O* ]]; then continue; fi
  if /usr/bin/codesign --verify --strict "$item" 2>/dev/null; then continue; fi
  if /usr/bin/codesign -d "$item" 2>/dev/null; then
    /usr/bin/codesign --force --sign - --preserve-metadata=entitlements,flags "$item"
  else
    /usr/bin/codesign --force --sign - "$item"
  fi
done < <(/usr/bin/find -d "$app/Contents/Frameworks" \( \
  -type d \( -name '*.app' -o -name '*.framework' -o -name '*.xpc' \) -o \
  -type f \( -name '*.dylib' -o -path '*/MacOS/*' -o -name 'Electron Framework' \) \) -print0)

/usr/bin/codesign --force --sign - "$app"
/usr/bin/codesign --verify --deep --strict "$app"
ln -s /Applications "$work/disk/Applications"
dmg="$output/ZTAPI-Workbench-macOS-$arch.dmg"
/usr/bin/hdiutil create -quiet -format UDZO -fs HFS+ -volname 'ZTAPI Workbench Beta' -srcfolder "$work/disk" "$dmg"
/usr/bin/hdiutil verify -quiet "$dmg"
(cd "$output" && /usr/bin/shasum -a 256 "./${dmg##*/}" >"${dmg##*/}.sha256")
printf '%s\n' "$dmg"
