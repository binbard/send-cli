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
send http https://example.test/upload report.pdf -m "Monthly report"
send adb report.pdf --to /sdcard/Download/
send ssh user@example.com report.pdf --to /srv/drop/
send wa report.pdf --to 15551234567 -m "Monthly report"
send lan serve --directory .
send lan report.pdf --to workstation --token RECEIVER_TOKEN
send list
```

## LAN

Run `send lan serve` on the receiving machine. It advertises the peer over UDP,
prints a generated transfer token, and atomically saves authenticated incoming
files without overwriting collisions. Use `send lan --list` on the sender, then
`send lan [files...] --to <peer> --token <token>`. Direct `host:port` targets and
messages with `-m` are also supported. The protocol uses unencrypted HTTP and is
intended only for trusted local networks; the token prevents unauthenticated
uploads but does not provide transport confidentiality.

## WhatsApp

`send whatsapp` (or `send wa`) uses Meta's official WhatsApp Cloud API. Set
`SEND_WHATSAPP_TOKEN` and `SEND_WHATSAPP_PHONE_NUMBER_ID`, then provide `--to`.
Text, JPEG/PNG images, MP4/3GPP video, supported audio, and common document
formats are supported. Local files are uploaded first and then sent by media ID.
The access token is never written to send-cli configuration or command output.
Use `--api-version` or `--graph-url` to override the API endpoint.

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

`send http <url> [files...]` sends a plain UTF-8 request body when only `-m` is
provided. With files, it streams `multipart/form-data` containing a `message`
field and repeated `files` parts. Use `--method`, repeatable `-H/--header`, and
`--timeout` to customize the request.

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

Go 1.24 or newer is required.

```powershell
go test ./...
go build -trimpath -ldflags="-s -w" -o bin/send.exe ./cmd/send
```

Push a tag such as `v0.1.0` to run the release workflow. It publishes stripped
Windows, Linux, and macOS amd64 archives plus SHA-256 checksums; the tag version
is embedded in `send --version`.
