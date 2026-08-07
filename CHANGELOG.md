# headlessdesk

## 0.4.4

### Patch Changes

- 86ca63c: Install the KWin screenshot authorization desktop entry from the Nix package.

## 0.4.3

### Patch Changes

- 5214af0: Clarify FUSE screenshot handling in agent skill instructions.

## 0.4.2

### Patch Changes

- cfaa349: Fix release tarballs to contain a `headlessdesk` executable instead of a platform-suffixed binary name.

## 0.4.1

### Patch Changes

- 8047484: Add in-repo agent skills for headlessdesk control surfaces.
- 69d9c56: allow fuse inputs to use numeric expressions

## 0.4.0

### Minor Changes

- f8d7528: Allow HTTP and MCP numeric action fields to accept arithmetic expression strings, with float pointer coordinates where supported.
- c2d9ce0: Add a native macOS desktop backend with screen capture, input control, feature discovery, and permission prompts.
- 43f09d2: Support key chords in the `keypress` API, such as `Ctrl+L`, `Ctrl+Shift+P`, and `Alt+Tab`.

## 0.3.0

### Minor Changes

- e6ca0e2: Add optional config-backed bearer authentication for REST and MCP-over-HTTP endpoints, including multiple tokens, per-audience tokens, and scoped read/write access.

## 0.2.1

### Patch Changes

- 45f5b79: Set Gin to release mode for GoReleaser-built binaries.
- 9c53ee5: Improve Windows packaging with a no-console server binary, partial static MinGW runtime linking, and Windows service/autostart documentation.

## 0.2.0

### Minor Changes

- 0b56e54: Add native Windows backend support for local screenshots and keyboard/mouse input.

## 0.1.3

### Patch Changes

- db5cb03: embed build version metadata in binaries

## 0.1.2

### Patch Changes

- 16764c9: fix nix build dependency handling

## 0.1.1

### Patch Changes

- 348e90a: initial commit
