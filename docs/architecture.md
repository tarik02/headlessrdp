# Architecture and Backends

`headlessdesk` connects to named input and output backends. Output backends
provide screenshots as Go `image.Image` values. Input backends provide keyboard
and mouse control. A single backend can provide both, or input and output can
use different protocols.

The HTTP, REST, MCP, and FUSE layers use shared protocol-neutral desktop
interfaces.

Supported backend types:

- `rdp`: implemented through `internal/freerdp` and FreeRDP.
- `vnc`: implemented through `internal/vnc` and LibVNCClient.
- `nanokvm`: implemented through `internal/nanokvm` and NanoKVM's HTTP/WebSocket APIs.
- `command`: implemented through `internal/commandbackend` and configured
  argv/script templates.
- `kwin`: implemented through `internal/kwin` and KDE KWin's
  `org.kde.KWin.ScreenShot2` DBus API.
- `eis`: implemented through `internal/kwineis`, KWin's private EIS remote
  desktop DBus endpoint, and libei.
- `windows`: implemented through `internal/winlocal` and Win32 APIs.
- `macos`: implemented through `internal/macoslocal` and macOS
  ApplicationServices/CoreGraphics APIs.

Backend configs can also extend built-in presets before validation. Presets are
embedded YAML files loaded by `internal/backendpreset`, merged left-to-right,
then explicit backend fields override them. Current presets cover command
defaults, Spectacle screenshots, and ydotool input.

## RDP Backend

The RDP backend uses FreeRDP 3 through cgo.

`--insecure` accepts unknown or changed certificates for RDP. Without
`--insecure`, certificate validation stays strict.

RDP sessions implement both screenshot output and keyboard/mouse input.

## VNC Backend

The VNC backend uses LibVNCClient for RFB negotiation, server-message parsing,
framebuffer update requests, and keyboard/pointer input.

Current VNC behavior:

- connects to `backends.<name>.host:backends.<name>.port`, defaulting to port `5900`;
- uses `backends.<name>.password` for VNC password authentication while still allowing servers that offer no-auth;
- maps `backends.<name>.vnc.shared` to the RFB shared/exclusive connection flag;
- maintains an in-memory framebuffer from server framebuffer updates;
- sends key events with X11/RFB keysyms, including common named keys and Latin-1 text input;
- sends pointer movement, button, and wheel events;
- allows `backends.<name>.vnc.view_only=true` only when the VNC backend is selected for output and not input.

## NanoKVM Backend

The NanoKVM backend connects to the device web API, authenticates with the same
AES-wrapped password flow as the bundled UI, decodes the first JPEG frame from
`/api/stream/mjpeg` for screenshots, and sends USB HID keyboard and absolute
mouse reports over `/api/ws`. Text typing uses NanoKVM's `/api/hid/paste`
endpoint.

`backends.<name>.host` may be a plain host/IP or an `http://`/`https://` URL.
Port defaults to `80`. `username` and `password` are required.

## KWin Backend

The KWin backend is output-only. It connects to the user session bus and calls
`org.kde.KWin.ScreenShot2.CaptureWorkspace` for full screenshots and
`CaptureArea` for cropped screenshots. KWin writes raw image bytes into a Unix
file descriptor passed over DBus and returns image metadata; the backend encodes
that image as PNG. KWin restricts this DBus interface to executables whose
desktop entry requests `X-KDE-DBUS-Restricted-Interfaces=org.kde.KWin.ScreenShot2`.

## KWin EIS Backend

The KWin EIS backend is input-only. It connects to
`org.kde.KWin.EIS.RemoteDesktop.connectToEIS`, keeps one libei sender context
alive, and sends pointer, button, wheel, key, and ASCII text events through that
context. It reads EIS absolute-pointer regions and maps screenshot-space
coordinates into the logical EIS coordinate space before sending input. It
avoids ydotool/uinput for local Plasma Wayland control.

## Windows Backend

The Windows backend is available only on Windows and implements both screenshot
output and keyboard/mouse input for the local desktop. Screenshots use GDI over
the full virtual screen, including multi-monitor layouts. Input uses
`SetCursorPos` and `SendInput` for pointer movement, buttons, wheel events,
named keys, scancodes, and Unicode text. Screenshot coordinates are relative to
the captured virtual-screen image; the backend translates them to the desktop's
native virtual-screen origin before sending pointer input.

## macOS Backend

The macOS backend is available only on macOS and implements both screenshot
output and keyboard/mouse input for the local desktop. It targets an active
logged-in graphical user session and exposes the main display. Screenshots are
one-shot CoreGraphics captures, including native cropped captures. Input uses
Quartz event posting for pointer movement, buttons, wheel events, named keys,
evdev-style scancodes mapped to macOS virtual key codes, and Unicode text.

macOS requires Screen Recording permission for screenshots and Input Monitoring
plus Accessibility permission for input. The backend starts even when those
permissions are missing; status reports the affected input or output side as
not ready and actions return permission-specific errors. Screenshot coordinates
are image pixels, and the backend maps them to macOS display coordinates for
input.

## Command Backend

The command backend executes configured `argv` or `script` templates for
screenshots and input events. `argv` execution does not run through a shell.
`script` execution runs with `sh -s`.

Screenshot commands must write PNG bytes to stdout. Optional cropped screenshot
commands receive `X`, `Y`, `W`, and `H` template values. Command screenshots are
decoded from PNG stdout into `image.Image`. If no cropped command is configured,
the control service crops the full image in process.

Input commands receive action-specific template values such as coordinates,
button name, key name, wheel delta, and typed text. Stderr is included in command
errors and truncated before returning through HTTP or MCP. Templates include
Sprig functions plus command helpers for common ydotool conversions:
`ydotoolButton`, `ydotoolButtonEvent`, `ydotoolWheelX`, `ydotoolWheelY`,
`ydotoolKey`, and `ydotoolKeyEvent`.

If `command.ssh` is configured, the backend opens one SSH client connection at
startup and reuses it. Each action opens a new SSH session/channel on that
connection. Rendered `argv` is shell-quoted into one remote command string.
Rendered `script` is sent to remote `sh -s`. Closing the backend closes the
shared SSH client.

## FUSE Filesystem

The FUSE filesystem is an adapter over `internal/control.Service`. It does not
add a backend type. Read files capture fresh service output on open, so
`screenshot.png`, `crop/<x>,<y>,<w>,<h>.png`, and `health.json` reflect current
state. Crop files are dynamic directory lookups with deterministic inode numbers
derived from the filename. Input files collect write chunks per open file handle
and execute the corresponding service command on close, which lets normal shell
redirection work with JSON payloads.

Possible VNC improvements:

1. Add fake RFB server tests that cover handshake, framebuffer, and input-event paths without requiring an external VNC service.
2. Add richer status metadata for the negotiated RFB protocol/security type if LibVNCClient exposes it.
