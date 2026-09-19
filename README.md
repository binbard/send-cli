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

`send tg` opens the interactive interface:

- `Enter` sends.
- `Shift+Enter`, `Ctrl+J`, `Ctrl+\`, or `Alt+Enter` inserts a newline.
- `Ctrl+O` opens the Windows multi-file picker.
- `Ctrl+V` attaches files copied in Explorer or an image copied to the Windows
  clipboard when the terminal passes that key to the application.
- Pasted ordinary text is inserted into the message. Pasted existing paths are
  attached.
- `Ctrl+C` exits.

Incoming files are offered for download into the current directory. Press `y`
to accept one file or `A` to accept this and future files from the configured
receiver. If a filename exists, press `y` to overwrite, `r` to choose another
name, or `N` to skip. `send tg --ask-downloads` restores prompting.

## Telegram setup

The first Telegram command, or `send tg --setup`, offers two modes.

### User account

User mode uses [`gotd/td`](https://github.com/gotd/td) and supports Telegram's
phone code and 2FA login. Like Telegram Web, a release build must contain the
project's own Telegram application ID and hash. Inject them when building:

```powershell
$env:SEND_TELEGRAM_API_ID = "123456"
$env:SEND_TELEGRAM_API_HASH = "your-app-hash"
go build -o send.exe ./cmd/send
```

Official release builds can inject them with linker values instead:

```powershell
go build -ldflags "-X send-cli/internal/telegram.BuiltinAPIID=123456 -X send-cli/internal/telegram.BuiltinAPIHash=your-app-hash" -o send.exe ./cmd/send
```

Do not commit an API hash. After login, setup displays a unique marker. Send it
from any of your Telegram clients to the DM, group, or channel that should
become the default receiver.

### Bot

Bot mode needs only a BotFather token. Setup verifies the token and asks you to
send a unique marker to the bot in a DM or group. That chat becomes the fixed
receiver. Telegram bots cannot send to themselves or initiate a conversation
with a user.

Bot tokens and user sessions are sensitive. They are stored beneath the OS user
configuration directory in `send-cli`, with user-only permissions where the OS
supports them. `send tg --logout` removes the local Telegram configuration and
user session.

## Build and test

Go 1.24 or newer is required.

```powershell
go test ./...
go build -o bin/send.exe ./cmd/send
```
