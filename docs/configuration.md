# Configuration

The CLI and environment variables configure a named backend map. `input`
selects the backend used for keyboard and mouse actions. `output` selects the
backend used for screenshots.

## Config File

See [`config.example.yaml`](../config.example.yaml) for a commented config with
all backend options.

```yaml
server:
  listen_addr: "127.0.0.1:4243"
  mcp_path: "/mcp"
  enable_http_api: true
  enable_mcp_api: true
  auth:
    tokens:
      - token: "replace-with-a-long-random-secret"
        audience: ["http", "mcp"]
        scopes: ["read:*", "write:*"]

input: "desktop"
output: "desktop"

backends:
  desktop:
    type: "rdp" # rdp, vnc, nanokvm, command, kwin, eis, windows, or macos
    host: "127.0.0.1"
    port: 0 # 0 means backend default: 3389 for RDP, 5900 for VNC, 80 for NanoKVM
    username: "gmtest"
    password: "gmtest"
    width: 1280
    height: 720
    insecure: true
    rdp:
      domain: ""
      keyboard_layout: 1033
      graphics_mode: "auto"
```

`server.auth.tokens` is optional. When any token is configured, protected REST
and MCP-over-HTTP endpoints require bearer auth. `audience` accepts `http`,
`mcp`, or both; omitting it applies the token to both REST and MCP-over-HTTP.
Bearer auth does not apply to `stdio-mcp`.

Scopes use `action:resource` strings. `*` grants everything, `read:*` grants all
read scopes, and `write:*` grants all write scopes. Current scopes are:

- `read:status` for MCP `session_status`
- `read:screenshot` for REST/MCP screenshots
- `read:wait` for MCP `wait`
- `write:mouse` for pointer, button, drag, and scroll actions
- `write:keyboard` for keypress and text typing

KDE/Wayland can use KWin for screenshots and EIS/libei for input:

```yaml
input: "local-input"
output: "local-screen"

backends:
  local-screen:
    type: "kwin"

  local-input:
    type: "eis"
```

KWin restricts `org.kde.KWin.ScreenShot2` callers. The executable needs a
desktop entry whose `Exec` resolves to the running binary and includes:

```ini
X-KDE-DBUS-Restricted-Interfaces=org.kde.KWin.ScreenShot2
```

The Nix package installs this entry as
`share/applications/headlessdesk.desktop`. Its `Exec` points to
`bin/.headlessdesk-wrapped`, the executable KWin sees in `/proc/<pid>/exe`
after the public launcher invokes it. Keep that exact value: pointing the
entry at the wrapper script breaks KWin authorization.

KWin reads the entry through the KDE service cache. Install the package into
the session's `XDG_DATA_DIRS` and build the cache before KWin starts. For an
already running session, rebuild the cache and then restart KWin:

```bash
kbuildsycoca6 --noincremental
systemctl --user restart plasma-kwin_wayland.service
```

Windows can use the native local desktop backend for both screenshots and
keyboard/mouse input:

```yaml
input: "local"
output: "local"

backends:
  local:
    type: "windows"
```

macOS can use the native local desktop backend for the main display:

```yaml
input: "local"
output: "local"

backends:
  local:
    type: "macos"
```

The macOS backend requires an active logged-in graphical session. Screenshots
require Screen Recording permission. Keyboard and mouse input require Input
Monitoring and Accessibility permissions. The backend reports missing
permissions in status and action errors instead of failing server startup.
Coordinates are screenshot pixels; Retina display scaling is handled internally.

The NanoKVM backend can use a device for both screenshots and keyboard/mouse
input:

```yaml
input: "kvm"
output: "kvm"

backends:
  kvm:
    type: "nanokvm"
    host: "10.0.2.149"
    port: 80
    username: "admin"
    password: "admin"
```

`host` may also include `http://` or `https://`. Set `insecure: true` only when
using HTTPS with a certificate that should not be verified.

Input and output can use different backend instances:

```yaml
input: "vnc-control"
output: "rdp-visual"

backends:
  rdp-visual:
    type: "rdp"
    host: "127.0.0.1"
    port: 3391
    username: "gmtest"
    password: "gmtest"
    width: 1280
    height: 720
    insecure: true
    rdp:
      graphics_mode: "auto"

  vnc-control:
    type: "vnc"
    host: "127.0.0.1"
    port: 5900
    password: "secret"
    width: 1280
    height: 720
    vnc:
      shared: true
      view_only: false
```

Backends can extend built-in presets. Presets apply left-to-right, then fields
on the backend override preset values.

```yaml
input: "local"
output: "local"

backends:
  local:
    extends:
      - preset:command-base
      - preset:screenshot-spectacle
      - preset:input-ydotool
```

Built-in presets:

- `preset:command-base`: sets `type: command` and `command.timeout: 30s`.
- `preset:screenshot-spectacle`: captures KDE/Wayland screenshots with Spectacle.
- `preset:input-ydotool`: sends mouse, wheel, key, and text events with ydotool.

Command backends execute configured `argv` or `script` templates. `argv` has no
implicit shell. `script` runs with `sh -s`. Screenshot commands must write PNG
bytes to stdout. Templates include Sprig plus helpers such as
`ydotoolButtonEvent`, `ydotoolWheelX`, `ydotoolWheelY`, and `ydotoolKeyEvent`.

```yaml
input: "shell"
output: "shell"

backends:
  shell:
    type: "command"
    command:
      timeout: "30s"
      screenshot:
        script: |
          f=$(mktemp --suffix=.png)
          trap 'rm -f "$f"' EXIT
          grim "$f"
          cat "$f"
      screenshot_crop:
        argv: ["grim", "-g", "{{.X}},{{.Y}} {{.W}}x{{.H}}", "-"]
      move_mouse:
        argv: ["ydotool", "mousemove", "--absolute", "-x", "{{.X}}", "-y", "{{.Y}}"]
      mouse_button:
        argv: ["ydotool", "click", "{{ ydotoolButtonEvent .Button .Down }}"]
      mouse_wheel:
        argv: ["ydotool", "mousemove", "--wheel", "--", "{{ ydotoolWheelX .Delta .Horizontal }}", "{{ ydotoolWheelY .Delta .Horizontal }}"]
      key:
        argv: ["ydotool", "key", "{{ ydotoolKeyEvent .Key .Down }}"]
      type_text:
        argv: ["ydotool", "type", "{{.Text}}"]
```

Command backends can also keep one SSH connection open and execute each action
through a fresh SSH channel on that connection. Remote `argv` is shell-quoted
into one SSH exec command string. Remote `script` is sent to `sh -s`.

```yaml
input: "remote-shell"
output: "remote-shell"

backends:
  remote-shell:
    type: "command"
    command:
      timeout: "30s"
      ssh:
        host: "box"
        port: 22
        username: "desktop"
        private_key_path: "~/.ssh/id_ed25519"
        known_hosts_path: "~/.ssh/known_hosts"
      screenshot:
        script: "grim -"
      move_mouse:
        argv: ["ydotool", "mousemove", "--absolute", "-x", "{{.X}}", "-y", "{{.Y}}"]
      mouse_button:
        argv: ["ydotool", "click", "{{ ydotoolButtonEvent .Button .Down }}"]
      mouse_wheel:
        argv: ["ydotool", "mousemove", "--wheel", "--", "{{ ydotoolWheelX .Delta .Horizontal }}", "{{ ydotoolWheelY .Delta .Horizontal }}"]
      key:
        argv: ["ydotool", "key", "{{ ydotoolKeyEvent .Key .Down }}"]
      type_text:
        argv: ["ydotool", "type", "{{.Text}}"]
```

SSH auth supports `password` or `private_key_path` plus optional
`private_key_passphrase`. Host keys are verified through `known_hosts_path`
unless `insecure_ignore_host_key: true` is set.

`screenshot_crop` is optional. If absent, the service captures a full screenshot
and crops it in process. `key_scancode` is optional; when absent, scancode input
returns an unsupported-action error.

## Environment Variables

Environment variables use the `HEADLESSDESK_` prefix and flatten nested keys with
underscores:

```bash
HEADLESSDESK_INPUT=default
HEADLESSDESK_OUTPUT=default
HEADLESSDESK_BACKENDS_DEFAULT_TYPE=rdp
HEADLESSDESK_BACKENDS_DEFAULT_HOST=127.0.0.1
HEADLESSDESK_BACKENDS_DEFAULT_USERNAME=gmtest
HEADLESSDESK_BACKENDS_DEFAULT_PASSWORD=gmtest
HEADLESSDESK_SERVER_LISTEN_ADDR=127.0.0.1:4243
HEADLESSDESK_BACKENDS_DEFAULT_RDP_GRAPHICS_MODE=bitmap
HEADLESSDESK_BACKENDS_DEFAULT_VNC_SHARED=true
```

## RDP Graphics and Acceleration

`backends.<name>.rdp.graphics_mode` controls the FreeRDP graphics path:

- `auto`: try AVC/H.264 first, then graphics pipeline without H.264, then bitmap;
- `avc` or `h264`: require the graphics pipeline with H.264;
- `gfx` or `graphics`: require the graphics pipeline without H.264;
- `bitmap` or `legacy`: require classic bitmap updates.

Some RDP servers only support the graphics pipeline. For example, KRDP requires
graphics pipeline support, so `bitmap` mode will not connect there.

When FreeRDP uses VAAPI for H.264 decode on NVIDIA, the VAAPI driver may need to
be selected explicitly:

```bash
LIBVA_DRIVER_NAME=nvidia
LIBVA_DRIVERS_PATH=/opt/libva-nvidia-driver-git/lib/dri
NVD_BACKEND=direct
```

The `LIBVA_DRIVERS_PATH` value is distribution-specific. Use the directory that
contains `nvidia_drv_video.so` on the target system.

Client-side hardware decode does not imply server-side hardware encode. KRDP can
still consume significant CPU on NVIDIA systems because its H.264 encoder falls
back to software encoding when VAAPI encoding is unavailable. Lowering KRDP's
video quality or adding a systemd CPU quota can limit the impact, but it does
not remove the underlying software encoding cost.
