#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
operation="${1:-build}"
shift || true
frameworks="$(xcode-select -p)/Library/Developer/Frameworks"
flags=()
if [[ -d "$frameworks/Testing.framework" ]]; then
  flags=(-Xswiftc -F -Xswiftc "$frameworks" -Xswiftc -Xfrontend -Xswiftc -disable-cross-import-overlays -Xlinker -rpath -Xlinker "$frameworks")
fi
if [[ "$operation" == test ]]; then
  exec swift test --disable-xctest --enable-swift-testing --jobs 4 "${flags[@]}" "$@"
fi
exec swift "$operation" --jobs 4 "${flags[@]}" "$@"
