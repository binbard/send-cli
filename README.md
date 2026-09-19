# send-cli

`send` is a Windows-first command-line tool for sending and receiving Telegram
messages and files with as little ceremony as possible.

Telegram is implemented. WhatsApp, ADB, SSH, HTTP, and LAN are intentionally
visible in `send --help` and `send list`, but currently return a clear
“not implemented” error.

## Commands

```powershell
send telegram file3.html
send tg file3.html file32.html
send tg -m "Everything is good."
send tg
send list
```

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
directory in `send-cli`, with user-only permissions where the OS supports them.
`send tg --logout` removes the local Telegram bot token and receiver
configuration.

## Build and test

Go 1.24 or newer is required.

```powershell
go test ./...
go build -trimpath -ldflags="-s -w" -o bin/send.exe ./cmd/send
```

Push a tag such as `v0.1.0` to run the release workflow. It publishes stripped
Windows, Linux, and macOS amd64 archives plus SHA-256 checksums; the tag version
is embedded in `send --version`.
