# Windows tape selected by `just demo`.
Output docs/demo.gif

Set Shell cmd
Set Width 1120
Set Height 700
Set FontSize 17
Set Theme "Catppuccin Mocha"
Set TypingSpeed 30ms
Set Framerate 20
Set Padding 20
Set Margin 16
Set MarginFill "#11111b"
Set WindowBar Colorful
Set BorderRadius 8

# Replay the redacted output captured from real CLI commands.
Hide
Type "chcp 65001 >nul"
Enter
Type 'doskey send="%CD%\\build\\vhs-bin\\send.exe" $*'
Enter
Type "cls"
Enter
Show
Sleep 300ms

Type "send list"
Enter
Wait+Screen@15s /DEFAULT RECEIVER/
Sleep 2s

Type "send tg README.md"
Enter
Wait+Screen@90s /Sent 1 file/
Sleep 2s

Type 'send wa -m "Demo from send-cli: WhatsApp. No reply needed."'
Enter
Wait+Screen@90s /Sent 1 WhatsApp/
Sleep 2s

# Use an isolated profile for the rest of the walkthrough.
Hide
Type "set APPDATA=%CD%\\build\\vhs-profile"
Enter
Type 'doskey send="%CD%\\bin\\send.exe" $*'
Enter
Type "cls"
Enter
Show
Sleep 300ms
Type "send http README.md --bind 127.0.0.1 --port 0"
Enter
Sleep 4s
Ctrl+C
Sleep 600ms

Hide
Type "cls"
Enter
Show
Sleep 300ms
Type "send device --help"
Enter
Sleep 3s
