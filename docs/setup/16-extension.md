# Step 16: Chrome extension (optional)

## What this step does

The extension adds a side panel and page widgets to your everyday Chrome, so
you can evaluate a posting or fill a form while you browse. It talks only to
the Applysmith server on your own computer. If you skip it, the panel, page
widgets and session sync are unavailable. `applysmith setup skip extension`
records the decision.

## Before you start

The app or `applysmith serve` must be running. The extension is loaded as an
unpacked developer extension, because it is not in the Chrome Web Store.

## Steps

In the app, open Settings, Browser extension. It names the folder the app
keeps the extension in (`~/Library/Application Support/Applysmith/extension`),
with a Copy button for its path; the app writes each new build there when it
starts after an update. In Chrome open `chrome://extensions`, switch on
Developer mode and press Load unpacked; in the folder dialog press
Command-Shift-G, paste the path, press Return, then Select. After an app
update, press Reload extension in the extension's popup.

Without the app (a browser on `applysmith serve`), write the extension into an
empty folder:

```
applysmith extension export <folder>
```

and load that folder the same way. Then pair it: press Pair the extension in
Settings, Browser extension to show a pairing code (valid for 120 seconds) and
enter it in the extension's popup.

If you need the API token by hand (for another local client):

```
applysmith pair
```

Treat the token like a password. To replace it, which signs every paired client
out:

```
applysmith token rotate
```

## Where this is stored

The paired-device record is in the workspace database. The extension keeps its
own copy of the token in Chrome's extension storage. Nothing is stored in your
Chrome profile outside the extension.

## How to check it worked

```
applysmith doctor --only extension
```

```text
[PASS] extension.pairing   ...
```

The check passes after the server has received a check-in from the paired
extension.

## If something goes wrong

| What you see | What to do |
|---|---|
| `Load unpacked` refuses the folder | Choose the exported folder itself, the one that holds `manifest.json`. Export into an empty folder. |
| The pairing code is refused | It lasts 120 seconds; show a new one in Settings. |
| The extension stops working after `token rotate` | Pair it again. |
| `extension.pairing` warns | The server has seen no check-in yet; open the extension popup once. |

Next: [Step 17: orientation](17-orientation.md).
