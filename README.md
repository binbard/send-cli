# send-cli

`send` is a Windows-first command-line tool for moving messages and files with
as little ceremony as possible across Telegram, WhatsApp, HTTP, ADB, SSH, and
local-network peers.

Telegram, WhatsApp, HTTP, ADB, SSH, and authenticated LAN transfer are
implemented.

## Commands

```powershell
send telegram file3.html
send tg file3.html file32.html
send tg -m "Everything is good."
send tg
send http report.pdf photo.jpg -m "Files for the team"
send http post https://example.test/upload report.pdf -m "Monthly report"
send adb report.pdf --to /sdcard/Download/
send ssh user@example.com report.pdf --to /srv/drop/
send wa --setup
send wa report.pdf --to 15551234567 -m "Monthly report"
send wa --logout
send device offer
send device join 192.168.1.20:49152
send lan serve --directory .
send lan report.pdf --to workstation --token RECEIVER_TOKEN
send list
send doctor
```

`send doctor` reports Telegram and WhatsApp setup, checks whether `adb` and
`scp` are available, and confirms the built-in HTTP and LAN channels. Use
`send doctor --strict` in setup scripts when missing sessions or dependencies
should produce a failure exit code.

## LAN

Run `send lan serve` on the receiving machine. It advertises the peer over UDP,
prints a generated transfer token, and atomically saves authenticated incoming
files without overwriting collisions. Use `send lan --list` on the sender, then
`send lan [files...] --to <peer> --token <token>`. Direct `host:port` targets and
messages with `-m` are also supported. The protocol uses unencrypted HTTP and is
intended only for trusted local networks; the token prevents unauthenticated
uploads but does not provide transport confidentiality.

## WhatsApp

`send whatsapp` (or `send wa`) links a normal WhatsApp user account through
WhatsApp's multi-device protocol using `whatsmeow`; it does not use the Facebook
Cloud API. Run `send wa --setup`, then scan the terminal QR code from WhatsApp's
**Settings > Linked devices > Link a device** screen. The linked-device session
is reused automatically and stored as `whatsapp.db` beneath the OS user
configuration directory. Run `send wa --logout` to unlink the device and remove
that local session.

Use an international phone number with `--to`. Text, images, video, audio, and
arbitrary documents are supported, and a message supplied with files becomes
the first compatible file's caption. After a successful send, that recipient is
remembered and `--to` can be omitted on later sends. `SEND_WHATSAPP_TO` overrides
the remembered recipient when set.

Running `send wa` without a message or files opens the same terminal chat
interface as Telegram, scoped to the remembered recipient. Incoming messages
and media from that conversation appear while the command is running.
`whatsmeow` is an unofficial WhatsApp Web protocol client, so server changes or
account policy enforcement may affect it without notice.

## Provision another device

On a configured device, run `send device offer`. On the new device, run the
`send device join <host:port>` command it prints, then enter the pairing code
when prompted. Keep both commands running until they finish. The 22-character
code expires after five minutes and works once. Use `send device offer --bind <address>
--port <port>` if you need a specific network interface or firewall port.

Provisioning transfers the Telegram bot token and receiver, the linked
WhatsApp session (when present), and its remembered recipient. A fresh
WhatsApp database snapshot is made while other `send` processes are excluded
from using that session. The pairing code authenticates the request and
encrypts the transfer; it is never sent over the network. The new device stores
the imported token under its own OS user protection. An existing Telegram or
WhatsApp setup on the new device is never overwritten.

The transferred WhatsApp session has the **same linked-device identity** as the
source. Use it on only one computer at a time. Even then, message and key state
can diverge between the copies, and WhatsApp may require relinking. Telegram
bots also have a shared receive queue, so use
interactive `send tg` on only one device at a time. One-shot sends can be made
from any provisioned device.

## SSH

`send ssh <user@host> [files...]` transfers regular files with the installed
OpenSSH `scp` client. It uses agent/key authentication in batch mode. Configure
`--identity`, `--port`, repeatable `--ssh-option`, `--to`, or `--scp-path` as
needed. Remote target and destination values reject shell metacharacters.

## ADB

`send adb [files...]` pushes files to `/sdcard/Download/` on the single
authorized Android device. Use `send adb --list`, `--serial`, `--to`, or
`--adb-path` when device selection or tooling differs. ADB must already be
installed and USB or wireless debugging must be authorized.

## HTTP

`send http [files...]` starts a session-scoped web app at the LAN address root
and keeps it available while the terminal command remains open. The terminal
uses the same interactive composer as Telegram and WhatsApp: type messages,
attach more files, receive browser messages, and accept or skip incoming browser
uploads.

Press `r` in the empty terminal composer to toggle a scannable QR code. The web
interface also has a small **QR** button for connecting another device.

The responsive browser interface shows all files shared during the session,
supports downloads, live messages, and drag-and-drop uploads. Browser activity
is identified simply as `Browser`. Uploaded files use temporary session storage
and are removed when the share closes unless accepted through the terminal
download prompt.

The first share uses port 8080 and automatically tries 8081, 8082, and so on if
needed. The successful port is remembered for the next run. Use `--bind` or an
explicit `--port` to control the listener. This is unauthenticated plain HTTP
intended only for a trusted local network.

The previous outbound HTTP client remains available as
`send http post <url> [files...]`. URL-first commands such as
`send http https://example.test/upload report.pdf` also remain compatible. With
files, outbound requests stream `multipart/form-data`; use `--method`,
`-H/--header`, and `--timeout` to customize them.

Two or more compatible photos/videos are sent as Telegram media albums in
ordered groups of up to 10. Documents and unsupported mixed batches are sent
sequentially in their original order.

`send tg` opens a lightweight line-oriented terminal chat without a full-screen
TUI or animation framework:

- `Enter` sends.
- `Shift+Enter`, `Ctrl+J`, `Ctrl+\`, or `Alt+Enter` inserts a newline.
- `Ctrl+O` opens the Windows multi-file picker.
- `Ctrl+V` attaches files copied in Explorer or an image copied to the Windows
  clipboard when the terminal passes that key to the application.
- Pasted ordinary text is inserted into the message. Pasted existing paths are
  attached.
- Arrow keys, Home, End, Delete, and Backspace edit the draft. With an empty
  draft, Backspace removes the most recently added attachment. `Ctrl+U` clears
  the draft and all attachments.
- `Ctrl+C` exits.

Incoming files are offered for download into the current directory. Press `y`
to accept one file or `A` to accept this and future files from the configured
receiver. If a filename exists, press `y` to overwrite, `r` to choose another
name, or `N` to skip. `send tg --ask-downloads` restores prompting.

The receive position is saved after each update batch. Messages and files that
arrive while `send tg` is closed are shown the next time interactive mode starts.
Existing installations establish a receive baseline once when upgrading, so
old retained updates are not unexpectedly replayed.

## Telegram setup

The first Telegram command, or `send tg --setup`, asks for a
[BotFather](https://t.me/BotFather) bot token. Setup verifies the token and asks
you to send a unique marker to the bot in a DM or group. That chat becomes the
fixed receiver. Telegram bots cannot send to themselves or initiate a
conversation with a user.

In groups, BotFather privacy mode can hide ordinary messages from the bot even
though the setup marker works. To receive normal group messages, make the bot
an administrator or use BotFather's `/setprivacy` command to disable privacy
for that bot. Channels require the bot to have permission to read and post.

Only one interactive `send tg` process can receive updates for the configured
bot at a time. A local process lock prevents two instances from competing;
one-shot message and file sends can still run concurrently.

The official hosted Bot API currently limits multipart uploads to 10 MiB for
photos and 50 MiB for other files, and downloads to 20 MiB. `send` checks known
file sizes before transferring. Telegram's self-hosted local Bot API server has
different limits, but a custom Bot API endpoint is not yet configurable here.

Bot tokens are sensitive. They are stored beneath the OS user configuration
directory in `send-cli`. Windows protects the token with current-user DPAPI;
Linux and macOS use user-only file permissions. Existing Windows plaintext
tokens are migrated the next time the configuration is saved. `send tg
--logout` removes the local Telegram bot token and receiver configuration.

## Build and test

Go 1.26 or newer is required.

```powershell
go test ./...
go build -trimpath -ldflags="-s -w" -o bin/send.exe ./cmd/send
```

Push a tag such as `v0.1.0` to run the release workflow. It publishes stripped
Windows, Linux, and macOS archives for amd64 and arm64, each with the README,
plus SHA-256 checksums. The tag version is embedded in `send --version`.
