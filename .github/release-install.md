## Install

Install the Doubletake desktop app and `doubletake` into `~/.local` (no root; x86_64 and aarch64):

```sh
curl -fsSL @RAW@/install.sh | sh
```

Update to the latest release:

```sh
curl -fsSL @RAW@/update.sh | sh
```

Uninstall (pairing credentials in `~/.config/doubletake` are kept):

```sh
curl -fsSL @RAW@/uninstall.sh | sh
```

Optional: `doubletake-ctl`, the command-line control for the daemon (use `doubletake-ctl-arm64` on aarch64):

```sh
curl -fsSLo ~/.local/bin/doubletake-ctl @DL@/doubletake-ctl && chmod +x ~/.local/bin/doubletake-ctl
```

### Dependencies

The scripts do not install system packages. Install these yourself first:

```sh
# Arch Linux
sudo pacman -S --needed gstreamer gst-plugins-base gst-plugins-good gst-plugins-bad \
  gst-plugins-ugly gst-libav libpulse gtk4

# Ubuntu/Debian
sudo apt install gstreamer1.0-plugins-base gstreamer1.0-plugins-good \
  gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly gstreamer1.0-libav \
  pulseaudio-utils libgtk-4-1
```

Screen capture also needs PipeWire with xdg-desktop-portal (Wayland) or X11. The desktop app needs GTK 4.14 or newer. The installer itself uses `curl`, `tar` and `sha256sum`.
