#!/bin/bash
# Builds dist/PersonalCU.app ("release", the default, or "debug") and signs it.
# "install" is the one step a user runs: it creates, once, the local
# code-signing identity the build signs with, builds the release, replaces
# /Applications/PersonalCU.app with it and opens it. Why the identity keeps the
# macOS grants: docs/architecture/computer-use.md at the repository root.
set -euo pipefail
cd "$(dirname "$0")/.."
bundle_id=dev.applysmith.personal-cu
identity="${PERSONALCU_SIGN_IDENTITY:-PersonalCU Local Signing}"
app="dist/PersonalCU.app"

# Prints the SHA-1 hash of each code-signing identity named exactly $identity;
# with -v, only the ones macOS trusts for code signing.
identity_hashes() {
  security find-identity "$@" -p codesigning |
    awk -F'"' -v want="$identity" '$2 == want { split($1, field, " "); print field[2] }' | sort -u
}

# A trusted identity gives every build the same designated requirement (the
# bundle id and the certificate), which the macOS grants are bound to; an ad-hoc
# signature's requirement is its code hash, new with every build. The bundle's
# only code is its main executable, which signing the bundle signs; the resource
# bundle is a plain folder, sealed as a resource.
sign_app() {
  local hashes signer=-
  hashes="$(identity_hashes -v)"
  if [[ -z "$hashes" ]]; then
    echo "No trusted code-signing identity named \"$identity\": signed ad hoc, so the macOS grants will not survive a rebuild. \`make computer-use-install\` creates the identity." >&2
  elif [[ "$hashes" == *$'\n'* ]]; then
    echo "More than one trusted code-signing identity is named \"$identity\"; delete all but one in Keychain Access." >&2
    exit 1
  else
    signer="$hashes"
  fi
  codesign --force --sign "$signer" --identifier "$bundle_id" --options runtime --timestamp=none "$app"
  codesign --verify --deep --strict "$app"
  codesign -d -r- "$app"
}

# A self-signed certificate for code signing only, trusted for code signing
# only, with its key in the login keychain, usable by codesign alone and not
# exportable. Trusting the certificate asks for the login password. Outside the
# keychain the key and the one-time p12 file exist only in a private
# temporary directory, removed on exit.
create_identity() {
  local keychain="$HOME/Library/Keychains/login.keychain-db" trusted named password
  trusted="$(identity_hashes -v)"
  [[ -z "$trusted" ]] || return 0
  echo "First install: creating the code-signing identity \"$identity\". macOS asks for your login password once."
  named="$(identity_hashes)"
  if [[ -n "$named" ]]; then
    echo "An identity named \"$identity\" exists but is not trusted for code signing; delete it (security delete-identity -c \"$identity\") and run this again." >&2
    exit 1
  fi
  umask 077
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
  chmod 700 "$work"
  password="$(/usr/bin/openssl rand -hex 32)"
  # The system LibreSSL, not a Homebrew OpenSSL 3: its p12 encryption is
  # the one security import reads.
  /usr/bin/openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -subj "/CN=$identity" \
    -addext "keyUsage=critical,digitalSignature" -addext "extendedKeyUsage=critical,codeSigning" \
    -addext "basicConstraints=critical,CA:false" -keyout "$work/key.pem" -out "$work/certificate.pem"
  P12_PASSWORD="$password" /usr/bin/openssl pkcs12 -export -name "$identity" -inkey "$work/key.pem" \
    -in "$work/certificate.pem" -passout env:P12_PASSWORD -out "$work/identity.p12"
  security import "$work/identity.p12" -k "$keychain" -f pkcs12 -P "$password" -x -T /usr/bin/codesign
  security add-trusted-cert -r trustRoot -p codeSign -k "$keychain" "$work/certificate.pem"
  trusted="$(identity_hashes -v)"
  if [[ -z "$trusted" ]]; then
    echo "\"$identity\" was imported, but macOS does not trust it for code signing." >&2
    exit 1
  fi
  rm -rf "$work"
  trap - EXIT
}

# Asks the running app with bundle id $1, if any, to quit and waits up to 30 s
# for it to exit. It is never killed: a Quit releases held buttons and stops
# the JavaScript worker. osascript may report an error for an app that exits
# before it replies, so whether the app is gone is the verdict.
quit_app() {
  local running
  running="$(lsappinfo find bundleid="$1")"
  [[ -n "$running" ]] || return 0
  osascript -e "quit app id \"$1\"" >/dev/null || true
  for _ in $(seq 60); do
    running="$(lsappinfo find bundleid="$1")"
    [[ -n "$running" ]] || return 0
    sleep 0.5
  done
  echo "PersonalCU ($1) did not quit within 30 s; quit it from its menu and run this again." >&2
  exit 1
}

# The installed copy may be an earlier build with another bundle id, so both
# ids are asked to quit. The old bundle is removed before the copy, so no
# running process has its executable rewritten in place.
install_app() {
  local installed=/Applications/PersonalCU.app id ids
  if ! codesign --verify --deep --strict "$app"; then
    echo "$PWD/$app does not verify; run make computer-use first." >&2
    exit 1
  fi
  ids=("$bundle_id")
  if [[ -f "$installed/Contents/Info.plist" ]]; then
    ids+=("$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$installed/Contents/Info.plist")")
  fi
  for id in "${ids[@]}"; do
    quit_app "$id"
  done
  rm -rf "$installed"
  ditto "$app" "$installed"
  open "$installed"
  echo "Installed and opened $installed. Its window says what is left to do: where it reads \"Needs ...\", press Open Settings beside that permission, switch PersonalCU on, then quit and reopen PersonalCU. A later install keeps the grants."
}

build_app() {
  local configuration="$1" binary_directory
scripts/swift-tool.sh build --configuration "$configuration" --product personal-cu
binary_directory="$(swift build --configuration "$configuration" --show-bin-path)"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$binary_directory/personal-cu" "$app/Contents/MacOS/personal-cu"
cp -R "$binary_directory/PersonalCU_PersonalCU.bundle" "$app/Contents/Resources/"
cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>$bundle_id</string>
<key>CFBundleName</key><string>PersonalCU</string>
<key>CFBundleExecutable</key><string>personal-cu</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>0.2.0</string>
<key>CFBundleVersion</key><string>2</string>
<key>LSMinimumSystemVersion</key><string>14.0</string>
<key>LSUIElement</key><true/>
</dict></plist>
PLIST
sign_app
echo "Built $PWD/$app"
}

case "${1:-release}" in
  debug|release) build_app "${1:-release}" ;;
  install) create_identity; build_app release; install_app ;;
  *) echo "Usage: $0 [debug|release|install]" >&2; exit 2 ;;
esac
