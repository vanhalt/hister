#!/usr/bin/env bash
# Packages the Hister browser extension for Firefox.
# Builds the extension, stages dist/ with the Firefox manifest, and writes a
# zip that can be sideloaded via about:debugging ("Load Temporary Add-on").
#
# Usage: ./package-firefox.sh [output.zip]  (default: ../../hister-firefox.zip)
set -e
cd "$(dirname -- "$0")"

OUT="${1:-../../hister-firefox.zip}"
OUT_ABS="$(realpath -m "$OUT")"

npm run build

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
cp -r dist/. "$STAGE/"
mv "$STAGE/manifest_ff.json" "$STAGE/manifest.json"

python3 - "$STAGE" "$OUT_ABS" << 'EOF'
import os
import sys
import zipfile

stage, out = sys.argv[1], sys.argv[2]
os.makedirs(os.path.dirname(out) or '.', exist_ok=True)
names = []
for root, _, files in os.walk(stage):
    for name in files:
        names.append(os.path.relpath(os.path.join(root, name), stage))
with zipfile.ZipFile(out, 'w', zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
    for name in sorted(names):
        zf.write(os.path.join(stage, name), name)
print(f'wrote {out} ({len(names)} files)')
EOF
