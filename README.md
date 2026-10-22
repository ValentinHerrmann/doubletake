# doubletake

AirPlay and Miracast screen mirroring sender for Linux. Streams your desktop,
or a single window, to an Apple TV or another AirPlay receiver, or to a
Miracast display (TV, projector or dongle) over Wi-Fi Direct.

This is a fork of [omarroth/doubletake](https://github.com/omarroth/doubletake).
It adds:

- **Doubletake desktop app** (`doubletake-ui`): a native GTK 4 window to pick a
  receiver, enter its PIN and start or stop mirroring.
- **Freeze, Blackout and Mute** while casting, in the app, its tray menu and
  `doubletake-ctl`, for AirPlay and Miracast receivers.
- **Capture source choice**: `-source any|monitor|window` on the CLI and the
  daemon, and a Screen / Window / Ask switch in the app.
- **Miracast** (experimental): Wi-Fi Direct displays in the same app and
  daemon as AirPlay receivers; see [Miracast](#miracast).
- **One-line install, update and uninstall** from this fork's releases.

The AUR packages (`doubletake`, `doubletake-git`, `doubletake-bin`) build
upstream, without these additions.

## Install

Install the latest release of the desktop app and `doubletake` into
`~/.local` (no root needed):

```sh
curl -fsSL https://raw.githubusercontent.com/ValentinHerrmann/doubletake-ui/main/scripts/install.sh | sh
```

Update to the latest release, or remove it again (pairing credentials in
`~/.config/doubletake` are kept):

```sh
curl -fsSL https://raw.githubusercontent.com/ValentinHerrmann/doubletake-ui/main/scripts/update.sh | sh
curl -fsSL https://raw.githubusercontent.com/ValentinHerrmann/doubletake-ui/main/scripts/uninstall.sh | sh
```

The installer downloads the bundle for your architecture (x86_64 or aarch64),
verifies it against the release's `SHA256SUMS` and adds a **Doubletake** entry
to the app menu. It does not install `doubletake-ctl`; download it from the
[latest release](https://github.com/ValentinHerrmann/doubletake-ui/releases/latest)
or [build from source](#build-from-source).

### Runtime requirements

The install scripts do not install these system packages; install them first.

- GStreamer 1.0 with the base, good, bad, ugly and libav plugins
- PulseAudio utilities (`pactl`)
- PipeWire and xdg-desktop-portal (Wayland) or X11 for screen capture
- GTK 4.14 or newer for the desktop app
- For Miracast only: NetworkManager with the wpa_supplicant Wi-Fi backend and a
  Wi-Fi card that supports P2P (see [Miracast](#miracast))

```sh
# Arch Linux
sudo pacman -S gstreamer gst-plugins-base gst-plugins-good gst-plugins-bad \
  gst-plugins-ugly gst-libav libpulse gtk4

# Ubuntu/Debian
sudo apt install gstreamer1.0-plugins-base gstreamer1.0-plugins-good \
  gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly gstreamer1.0-libav \
  pulseaudio-utils libgtk-4-1
```

The `openh264` encoder needs the GStreamer `openh264enc` element, normally from
the plugins-bad package. Check with `gst-inspect-1.0 openh264enc`.

## Desktop app

Start **Doubletake** from the app menu, or run `doubletake-ui`. The window:

- lists the AirPlay receivers on the network (discovery runs continuously)
  and the Miracast displays nearby (searched while the window is open);
- starts mirroring when you click a receiver;
- asks for the PIN shown on the receiver, or its password, when needed;
- stops with **Stop casting**.

The **Screen / Window / Ask** switch chooses what the screen-sharing dialog
offers for the next connection. **Screen** remembers the chosen screen per
receiver, so the dialog appears only once.

```sh
doubletake-ui                         # open the window
doubletake-ui -source window          # preselect Window
doubletake-ui -no-audio               # video only (when the app starts the daemon)
doubletake-ui -hidden                 # start in the system tray
```

The app drives the doubletake daemon over its control socket, the same
interface as `doubletake-ctl` and the [Plasma widget](plasmoid/). If no daemon
is running, it starts `doubletake -daemonize` from the binary next to itself
(or `doubletake-src`, then `doubletake` on `PATH`; override with
`-doubletake <path>`). `-no-audio` applies only to a daemon the app starts.

Closing the window does not stop a cast: the daemon keeps running. Stop it
with **Stop casting** or `doubletake-ctl disconnect`. Only one window runs per
user; starting the app again raises it and, with `-source`, switches its
capture source.

### Freeze, Blackout and Mute

While casting, three toggles under the receiver list change what every
receiver shows, without stopping the cast:

- **Freeze** keeps showing the current picture. Your desktop stays live, so
  you can look something up unseen.
- **Blackout** shows a black picture. It takes precedence over Freeze: ending
  it returns to the frozen picture if Freeze is still on.
- **Mute** mutes the cast's audio. It is hidden when the cast has no audio.

Freeze and Blackout work for AirPlay and Miracast. The receiver gets the still
picture as ordinary video, repeated every second, so it does not time out.
Live video returns at the encoder's next keyframe, within two seconds. Audio
continues during Freeze and Blackout; use Mute to silence it. All three end
when the last cast stops.

### Keep screen awake

**Keep screen awake while casting** (window menu ☰ or tray menu) blocks the
screensaver, screen lock and suspend while a cast runs. It is off by default
and saved in `~/.config/doubletake/ui.json`. The block lasts only while the
app runs: after **Quit**, a cast that continues no longer keeps the screen
awake.

### System tray and start on login

With a system tray, closing the window hides it to a tray icon. Click the
icon to show or hide the window. Its menu has **Show Doubletake**, **Stop
casting**, **Freeze**, **Blackout**, **Mute**, **Start on login**, **Keep
screen awake while casting** and **Quit**. KDE Plasma has a tray built in.
GNOME needs the
[AppIndicator and KStatusNotifierItem Support](https://extensions.gnome.org/extension/615/appindicator-support/)
extension. Without a tray, closing the window quits the app as before.

**Start on login** is off by default. Turn it on from the tray menu or the
window menu (☰). It writes `~/.config/autostart/doubletake-ui.desktop`, which
starts `doubletake-ui -hidden` in the tray. If no tray appears within a few
seconds, `-hidden` shows the window instead. Turning the option off removes the
file, and so does the uninstall script.

## Command line

Without the desktop app, `doubletake` discovers receivers, lets you pick one
and mirrors until you press Ctrl+C:

```sh
# Discover Apple TVs on the network and stream
doubletake

# Share a single window instead of the whole screen (Wayland)
doubletake -source window

# Disable audio for video-only mirroring
doubletake -no-audio

# Connect to a specific Apple TV
doubletake -target 192.168.1.77

# First-time pairing with PIN (saves credentials for reuse)
doubletake -target 192.168.1.77 -pair

# Use saved credentials
doubletake -target 192.168.1.77 -creds airplay-credentials.json

# Adjust stream settings (bitrate 0 = auto)
doubletake -target 192.168.1.77 -fps 30 -bitrate 0

# Force a lower bitrate on weaker Wi-Fi
doubletake -target 192.168.1.77 -bitrate 4500

# Override the automatic audio/video playout latencies with one joint value
doubletake -target 192.168.1.77 -target-latency-ms 100

# Hardware encoding
doubletake -target 192.168.1.77 -hwaccel nvenc   # NVIDIA
doubletake -target 192.168.1.77 -hwaccel vaapi   # Intel/AMD

# OpenH264 software encoding
doubletake -target 192.168.1.77 -hwaccel openh264

# Automatic H.264 compatibility mode is the default
doubletake -target 192.168.1.77

# Force one codec (HEVC uses the receiver's maximum canvas)
doubletake -target 192.168.1.77 -video-codec hevc
doubletake -target 192.168.1.77 -video-codec h264

# Debug mode (verbose protocol logging)
doubletake -target 192.168.1.77 -debug

# Run daemon mode and control from a second shell
doubletake -daemonize -port-range 60000-60010
doubletake-ctl status
doubletake-ctl connect 192.168.1.77
doubletake-ctl connect 192.168.1.133
doubletake-ctl disconnect 192.168.1.77
doubletake-ctl disconnect
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-target` | | Apple TV IP (skip mDNS discovery) |
| `-port` | 7000 | AirPlay port |
| `-code` | | Pairing PIN shown on the receiver, or its configured password when "Require Password" is enabled (see [Password-protected receivers](#password-protected-receivers)); prefer `$DOUBLETAKE_CODE` |
| `-port-range` | | Local UDP range for timing/audio and receiver control traffic (at least 3 ports) |
| `-cred-backend` | `file` | Credential backend (`file` or `keyring`) |
| `-creds` | `~/.config/doubletake/credentials.json` | Credentials file path |
| `-pair` | false | Force new pairing |
| `-fps` | 30 | Frames per second |
| `-bitrate` | 0 | Video bitrate in kbps (`0` = auto) |
| `-target-latency-ms` | 0 | Joint audio/video playout latency override in milliseconds (`0` = automatic wired/wireless profiles with separate audio/video defaults) |
| `-hwaccel` | auto | Encoder preference: `auto`, `nvenc`, `vaapi`, `openh264`, `none` |
| `-video-codec` | auto | Screen codec: compatible H.264 `auto`, forced `h264`, or forced `hevc` |
| `-no-encrypt` | false | Disable RTSP header encryption (debugging only) |
| `-direct-key` | false | Use `shk`/`shiv` directly without SHA-512 derivation |
| `-no-audio` | false | Disable audio streaming |
| `-test` | false | Use synthetic video source |
| `-daemonize` | false | Run as background daemon with Unix socket control interface |
| `-source` | `any` | Wayland capture source offered by the screen-sharing dialog: `any`, `monitor` (remembers the chosen screen) or `window`. With `-daemonize` the default stays `monitor`; a daemon `connect` request may set its own `source` |
| `-socket` | `$XDG_RUNTIME_DIR/doubletake.sock` | Daemon control socket path |
| `-miracast` | | Experimental: `list` searches for Miracast displays; a display's P2P address mirrors to it (see [Miracast](#miracast)) |
| `-debug` | false | Verbose debug logging |

Only `-hwaccel auto` tries fallback encoders, in the order `vulkanh264enc`,
`nvh264enc`, `vah264enc`, `openh264enc`, then `x264enc`. Explicit selections
fail if their required GStreamer encoder is unavailable; `none` forces x264 for
H.264 or x265 for explicitly requested HEVC.
The normal `-video-codec auto` path uses H.264 at the receiver's nominal canvas
for compatibility across AirPlay receiver generations. Explicit
`-video-codec hevc` remains available with `nvh265enc` or `x265enc`; use
`-hwaccel none` to force the software path.

Main10 alone does not turn an SDR X11 or portal capture into HDR: doubletake
preserves encoder-provided HDR SEI but does not invent PQ/HLG mastering metadata
or relabel SDR colors as HDR.

## Miracast

Miracast support is experimental. Doubletake forms a Wi-Fi Direct group with
the display through NetworkManager, negotiates the stream over WFD RTSP and
sends H.264 (Constrained Baseline, up to 1080p) with 48 kHz LPCM audio as
MPEG-TS over RTP.

In the desktop app, Miracast displays appear in the receiver list marked
**Miracast**. The search runs only while the window is open and nothing is
casting, because a Wi-Fi Direct search takes the radio off its channel. Found
displays stay listed for two minutes. On the command line:

```sh
doubletake -miracast list                 # search for 15 s and list displays
doubletake -miracast DA:C0:BF:B4:E3:48    # mirror to a display by its P2P address
doubletake-ctl connect p2p:DA:C0:BF:B4:E3:48
```

Requirements and limits:

- The Wi-Fi card must support P2P (`iw list` shows `P2P-GO` and `P2P-client`),
  and NetworkManager must use the wpa_supplicant backend. Check with
  `nmcli device`: a `wifi-p2p` device such as `p2p-dev-wlan0` must exist.
- The display connects back to TCP 7236 on the Wi-Fi Direct interface. A host
  firewall must allow that port there.
- No HDCP. A display that insists on content protection rejects the stream.
- One Miracast display at a time. AirPlay receivers can stream alongside it.
- `-target-latency-ms` sets the display's playout buffer (default 150 ms).

Troubleshooting:

- **The display joins but never connects** ("did not open its control
  connection"): many displays stay stuck after a session that ended without a
  proper stop (a crash, `kill -9`, or a second Ctrl+C). Power-cycle the
  display. A normal stop (**Stop casting**, `doubletake-ctl disconnect`,
  Ctrl+C once) always tears the session down first.
- **Motion stutters** (half speed, then catching up) while connected to a normal
  Wi-Fi network: the Wi-Fi Direct group then runs on a different channel than
  your network and the card switches between both. When your access point uses
  a DFS channel (52–144), the group cannot share it. Disconnecting from the
  network while casting, or `-target-latency-ms 300`, helps.

## Daemon and `doubletake-ctl`

`doubletake -daemonize` runs in the background and is controlled over a Unix
socket by `doubletake-ctl`, the desktop app and the
[KDE Plasma widget](plasmoid/). It can mirror to several receivers at once.

```sh
doubletake-ctl status
doubletake-ctl discover
doubletake-ctl devices
doubletake-ctl connect [target] [PIN-or-password]
doubletake-ctl pin <PIN-or-password>
doubletake-ctl disconnect [target]
doubletake-ctl mute [target]
doubletake-ctl unmute [target]
doubletake-ctl freeze | unfreeze
doubletake-ctl blackout | unblackout
```

- `disconnect` without a target stops all active streams.
- `disconnect <target>` stops only that receiver.
- `mute`/`unmute` can operate globally or per target.
- `freeze`/`unfreeze` and `blackout`/`unblackout` apply to all receivers. See
  [Freeze, Blackout and Mute](#freeze-blackout-and-mute). `status` reports
  them as `video_frozen` and `video_black`.
- `pin` retains its historical command name, but submits whichever credential
  the daemon requests: an on-screen PIN or a configured password. It is
  targetless and therefore requires exactly one waiting receiver; use
  `connect <target> <PIN-or-password>` when multiple receivers are waiting.

Daemon streams are grouped by codec and the normalized even-sized canvas resolved
during each receiver's SETUP. Targets with the same key share one capture and
encoder; targets with a different codec or canvas use independent encoders,
so connection order does not determine another receiver's size. For example, a
receiver reporting a 1920x1080 canvas and a 3840x2160 maximum joins the
1920x1080 H.264 group in automatic mode. An explicit HEVC request uses the
maximum canvas and forms a separate group.
Fan-out uses a bounded queue per target within each group. A
stalled target is detached when its queue fills, without blocking peers that
share the encoder. Other canvas groups continue independently as well.

## Troubleshooting

### Password-protected receivers

If the receiver has **Require Password** enabled (on an Apple TV: Settings →
AirPlay and HomeKit), it challenges the mirroring `SETUP` request with HTTP
Digest auth and mirroring fails with `HTTP 401` until doubletake answers it.
Pass the password with `-code`:

```sh
DOUBLETAKE_CODE='...' doubletake -target 192.168.1.77
```

`-code` carries whatever the receiver is asking for — the onscreen pairing PIN
during `-pair`, or the fixed password when "Require Password" is enabled.
`$DOUBLETAKE_CODE` is preferred over the flag: a command line is visible to
other users via `ps` and lands in shell history. The environment variable takes
precedence when both are set.

Pairing and Digest authentication remain separate wire protocols, but
doubletake deliberately uses one credential flow for both: a PIN/password
entered during pairing is retained for later Digest challenges and reconnects.
A receiver may consume that value during pairing, Digest auth, both, or neither.
Supplying `-code` does not by itself trigger re-pairing.

When the capability policy selects first-party CoreUtils/HAP pairing, a
configured fixed password is reserved for Digest authentication and is never
reused as an SRP pairing PIN, including with `-pair`. A legacy/HKP pairing flow
may consume the same entered value during both SRP and Digest when its protocol
requires it.

The CLI and Plasma applet prompt once after inspecting the receiver's security
mode. In the Plasma applet, an on-screen PIN uses a visible four-digit field,
while a configured password uses an unrestricted masked field. Doubletake does
not request or claim that a PIN is visible in password mode. The daemon exposes
the distinction while retaining `doubletake-ctl pin <PIN-or-password>` for
command compatibility when exactly one receiver is waiting. With concurrent
prompts, submit each value with `doubletake-ctl connect TARGET PIN-or-password`.

One thing that makes this confusing to diagnose: **"Require Password" is a
fixed password you set, not a rotating onscreen code.** Nothing appears on the
TV during `-pair`, and the prompt is asking for that configured password.

Run with `-debug` to see the challenge and whether the retry was accepted.

### Firewall

doubletake binds three consecutive local UDP ports for each stream:

- `N`: timing. A receiver-initiated NTP session probes this port during SETUP.
- `N+1`: audio control and receiver retransmission requests.
- `N+2`: outgoing audio data.

PTP and sender-initiated NTP sessions do not require a new inbound timing
connection, but the receiver can still send audio retransmission requests to
the control port. The event and video data channels are outbound TCP
connections from doubletake to ports returned by the receiver. Doubletake does
not listen for inbound TCP connections.

No router port forwarding or Internet exposure is needed. If the host firewall
allows local-network traffic, no firewall configuration is necessary. With a
default-deny host firewall, use `-port-range MIN-MAX` to replace unpredictable
ephemeral UDP ports with a range you can allow from the receiver. The range
must contain at least three ports:

```sh
doubletake -target 192.168.1.77 -port-range 60000-60010
```

Daemon mode uses the same range for every managed stream. Reserve at least
three available ports per receiver that may stream simultaneously.

For example, restrict a UFW rule to the receiver's address:

```sh
sudo ufw allow from 192.168.1.77 proto udp to any port 60000:60010
```

Add a rule for each receiver, or use the trusted LAN subnet as the source. For
nftables/firewalld, add an equivalent inbound UDP rule for the selected source
and range.

NTP and PTP use the same presentation policy; the timing protocol only changes
how timestamps are represented. In automatic mode, an ordinary connection uses
a 75 ms video lead and an 85 ms screen-audio lead.

## Tested devices


These are devices that have been tested with doubletake. If there are devices not listed here that you have confirmed working or non-functional, please open an issue.

- AppleTV3,2 (2013 3rd generation)
- AppleTV11,1 (4K, 2021 2nd gen)
- AppleTV14,1 (4K, 2022 3rd gen) + Homepod (1st gen)
- AppleTV14,1 (4K, 2022 3rd gen)
- Mac17,2 (MacBook Pro, M5 14")
- Mac16,10 (Mac mini, M4)
- Roku Streaming Stick 4K (3820R2)
- Samsung TV TU8300 Series 4K UHD
- TCL 55Q6C
- Hisense 55A6QU
- Xiaomi 4K HDR TV (AFTBR92D74) (currently non-functional, see [#4](https://github.com/omarroth/doubletake/issues/4))
- MiraScreen Miracast dongle (Miracast, 1080p30 with audio)

## Build from source

Building needs Go 1.25+, the [runtime requirements](#runtime-requirements),
and for the desktop app the GTK 4.14+ and GObject Introspection development
files:

```sh
# Arch Linux
sudo pacman -S go gtk4 gobject-introspection

# Ubuntu/Debian
sudo apt install golang-go libgtk-4-dev libgirepository1.0-dev
```

```sh
make                     # builds bin/doubletake, doubletake-ctl, doubletake-ui, doubletake-test-receiver
make test
make install-user        # into ~/.local, no root (undo: make uninstall-user)
sudo make install        # into /usr/local for all users (undo: sudo make uninstall)
```

The first build of `doubletake-ui` compiles the GTK bindings and takes several
minutes; later builds are cached. `make ui-bundle` builds the release tarball
locally. Packagers can stage with `make install DESTDIR=... PREFIX=/usr`.

### Optional AAC-ELD support

Doubletake selects screen audio from the receiver's advertised
`supportedFormats.screenStream` mask. Default builds use the built-in ALAC
encoder when the receiver advertises it. AAC-ELD remains an explicit opt-in
because it depends on the system `libfdk-aac` development files and cgo:

```sh
CGO_ENABLED=1 go build -tags fdk_aac -o bin/doubletake ./cmd/doubletake
```

Install the package that provides the `fdk-aac` pkg-config file and headers for
your distribution before running that command. A default build reports a clear
error if advertised capabilities select AAC-ELD. A nonzero screen-audio mask
which advertises neither ALAC nor AAC-ELD is also rejected instead of silently
sending an unadvertised format. Use `-no-audio` to keep testing pairing, timing,
and video when the advertised audio format is unavailable.

## Features

- Full AirPlay mirroring protocol (RTSP/HTTP + encrypted video stream)
- FairPlay SAP authentication (clean Go implementation)
- SRP-6a pairing with PIN and persistent credential storage
- Wayland (PipeWire/xdg-desktop-portal) and X11 screen capture
- H.264 encoding with NVENC, VA-API, OpenH264, and x264
- Capability-gated HEVC Main10/high-resolution encoding with NVENC or x265
- ChaCha20-Poly1305 stream encryption
- mDNS device discovery
- Daemon mode with multi-target streaming control (`doubletake-ctl`)
- In-process test receiver for hardware-free pairing and media-flow tests
- Automatic connection-aware screen/audio latency policy with an optional `-target-latency-ms` override
- KDE Plasma widget for quick access (see [plasmoid/](plasmoid/))
- GTK 4 desktop app and selectable capture source (this fork)
- Experimental Miracast sender over Wi-Fi Direct via NetworkManager (this fork)

## Releases

Each [GitHub release](https://github.com/ValentinHerrmann/doubletake-ui/releases)
contains:

- `doubletake`, `doubletake-ctl`, `doubletake-test-receiver`: static x86_64
  binaries (`-arm64` suffix for aarch64)
- `doubletake-ui`: x86_64 binary linked against the system GTK 4.14+
  (`-arm64` suffix for aarch64)
- `doubletake-ui-linux-amd64.tar.gz`, `doubletake-ui-linux-arm64.tar.gz`: the
  desktop app bundle that the install script uses; it contains `install.sh`
  (`./install.sh --uninstall` removes it)
- `doubletake-manpages.tar.gz`
- `SHA256SUMS`: verify downloads with `sha256sum -c --ignore-missing SHA256SUMS`

Releases are cut only from `main`. Push an annotated `v*` tag on a commit on
`main`; the [Release workflow](.github/workflows/release.yml) refuses other
tags, runs the CI tests and builds, then publishes the release with the tag
message as its notes, followed by the install commands. Tags with a `-`
(e.g. `v0.7.0-rc1`) become pre-releases.

```sh
git switch main && git pull
git tag -a v0.7.0 -m "doubletake v0.7.0: ..."
git push origin v0.7.0
```

## Disclaimer

The majority of code for this project was written by LLMs. I've read through the code to make sure there's nothing obviously stupid, but if you're in a production or security-sensitive environment and need to use AirPlay (for whatever reason), do not use this project.

Since I assume most of the code for this project was trained from [UxPlay](https://github.com/FDH2/UxPlay) and similar projects, I've provided this project under a similar license. Most of the reverse engineering work has already been done by many other people and this project would not be possible without them.

## License

This project is licensed under the [GNU Lesser General Public License v3.0 or later](LICENSE) (`LGPL-3.0-or-later`). See the LICENSE file for the LGPL terms and [COPYING.GPL](COPYING.GPL) for the incorporated GPLv3 terms.

Releases v0.3.2 and earlier were provided under the GNU General Public License v3.0 or later (`GPL-3.0-or-later`).
