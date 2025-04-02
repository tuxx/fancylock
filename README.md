# FancyLock

[![Latest Release](https://img.shields.io/github/v/release/tuxx/fancylock)](https://github.com/tuxx/fancylock/releases)
[![Build](https://github.com/tuxx/fancylock/actions/workflows/build.yml/badge.svg)](https://github.com/tuxx/fancylock/actions/workflows/build.yml)

FancyLock is a visually appealing screen locker for Linux that plays videos or images in the background while locked. It's designed to be both secure and aesthetically pleasing.

<p align="center">
  <img src="https://github.com/user-attachments/assets/bd62c40f-d491-4f75-8771-67c2743d86f4" alt="lockscreen">
</p>

## Features

- Play videos or images as lock screen backgrounds
- Support for multiple monitors
- PAM authentication for secure login
- Customizable settings
- Embedded version metadata (`-v`)

## Installation

### Option 1: Install from latest release (Recommended)

<details>
<summary>Download and install the latest pre-built binary:</summary>
  
```bash
# Download the latest release
curl -L -o fancylock.tar.gz https://github.com/tuxx/fancylock/releases/latest/download/fancylock-linux-amd64.tar.gz

# Extract it
tar -xzvf fancylock.tar.gz

# Make it executable
chmod +x fancylock-linux-amd64

# Add a pam.d file for fancylock:
sudo curl -L -o /etc/pam.d/fancylock https://raw.githubusercontent.com/tuxx/fancylock/refs/heads/master/pam.d/fancylock

# Optional: install system-wide
sudo mv fancylock-linux-amd64 /usr/local/bin/fancylock

# Create config directory
mkdir -p ~/.config/fancylock

# Create default config file
cat > ~/.config/fancylock/config.json << 'EOF'
{
  "media_dir": "$HOME/Videos",
  "lock_screen": false,
  "supported_extensions": [".mp4", ".mkv", ".mov", ".avi", ".webm"],
  "pam_service": "fancylock",
  "include_images": true,
  "image_display_time": 30,
  "background_color": "#000000",
  "blur_background": false,
  "media_player_cmd": "mpv",
  "pre_lock_command": "",
  "post_lock_command": ""
}
EOF
```
</details>

### Option 2: Install via AUR (Arch Linux)

<details>
<summary>Download and install the latest pre-built binary:</summary>

```bash
yay -S fancylock-bin
```
</details>

This installs the latest precompiled release from GitHub.

### Option 3: Build from source

#### Prerequisites

- Go 1.21 or higher
- X11 development libraries
- mpv (for video/image playback)
- PAM development libraries
- `make` and `git`

#### Installing dependencies

<details>
<summary>Debian/Ubuntu</summary>

```bash
sudo apt install -y golang make libx11-dev libpam0g-dev mpv git
```
</details>

<details>
<summary>Arch Linux</summary>

```bash
sudo pacman -S go make libx11 pam mpv git
```
</details>

#### Building the application

<details>
<summary>Build from source</summary>

```bash
# Clone the repository
git clone https://github.com/tuxx/fancylock.git
cd fancylock

# Build for all supported architectures (amd64, arm64, arm)
make

# Optionally package them into .tar.gz files in ./dist
make package

# Build a native binary for your current system (puts it in ./bin/)
make native

# View embedded version info
./bin/fancylock-native --version

# Optional: install the native build system-wide
sudo make install
```
</details>

### Makefile Targets

| Command         | Description |
|----------------|-------------|
| `make`         | Build for `linux/amd64`, `arm64`, and `arm` |
| `make native`  | Build for your local platform only |
| `make package` | Create `.tar.gz` files in `dist/` for release |
| `make install` | Install native build to `/usr/local/bin/fancylock` |
| `make clean`   | Remove `bin/` and `dist/` directories |


## How to Use

### Basic Usage

Run FancyLock without arguments to display help:

```bash
fancylock
```

Lock your screen immediately:

```bash
fancylock -l
# or
fancylock -lock
```

Check version info:

```bash
fancylock -v
```

### Configuration

FancyLock looks for a configuration file at `~/.config/fancylock/config.json`. If it doesn't exist, a default one will be created.

You can specify a different configuration file using:

```bash
fancylock -c /path/to/config.json
```

### Sample Configuration

<details>
<summary>View sample config.json</summary>

```json
{
  "media_dir": "/home/user/Videos",
  "lock_screen": false,
  "supported_extensions": [".mp4", ".mkv", ".mov", ".avi", ".webm"],
  "pam_service": "fancylock",
  "include_images": true,
  "image_display_time": 30,
  "background_color": "#000000",
  "blur_background": false,
  "media_player_cmd": "mpv",
  "pre_lock_command": "pypr hide mywindow",
  "post_lock_command": "pypr show mywindow"
}
```
</details>

### Configuration Options

- `media_dir`: Directory containing videos/images to display while locked
- `lock_screen`: Whether to lock the screen immediately on startup
- `supported_extensions`: File extensions to look for in the media directory
- `pam_service`: PAM service name for authentication
- `include_images`: Whether to include images along with videos
- `image_display_time`: How long to display each image in seconds
- `media_player_cmd`: Command to use for playing media (default: mpv)
- `pre_lock_command`: Execute this command before locking the screen.
- `post_lock_command`: Execute this command after unlocking the screen.

Note: The configuration also includes `background_color` and `blur_background` options, but these are not currently implemented.

## Current Status

### What's Working

- ✅ X11 screen locking with PAM authentication
- ✅ Basic hyprland support
- ✅ Multi-monitor support with correct video positioning
- ✅ Video and image playback during lock screen
- ✅ Password entry with visual feedback (dots)
- ✅ Keyboard and pointer grabbing to prevent bypass
- ✅ Failed password attempt limiting 
- ✅ Embedded version metadata via `-v`

### What Needs Improvement

- ⚠️ Error handling in some edge cases
- ⚠️ Password entry UI could be more polished
- ⚠️ Memory optimization for long-running sessions
- ⚠️ Better handling of system sleep/wake events
- ⚠️ Auto-creation of default config file (if none exists)

## Future Implementations

- 🚧 Configurable UI theme and appearance
- 🚧 Blurred background option
- 🚧 More interactive lock screen elements
- 🚧 Support for additional media players
- 🚧 Screensaver mode with clock display
- 🚧 Improved multi-head support (different videos per monitor)
- 🚧 Systemd integration
- 🚧 Implementation of background color and blur options
- 🚧 Auto-generation of default config file

## Developer Setup

If you want to contribute to FancyLock development:

<details>
<summary>Developer setup instructions</summary>

```bash
# Clone the repository
git clone https://github.com/tuxx/fancylock.git
cd fancylock

# Set up the Git hooks (required for all developers)
./.githooks/setup-hooks.sh

# Build the application
go build -o fancylock
```
</details>

## Contributing

Contributions are welcome! Please feel free to submit pull requests or open issues to improve the application.

Before contributing:
1. Run `.githooks/setup-hooks.sh` if applicable
2. Follow the coding style used in the codebase
3. Fork the repo
4. Push your changes and submit a Pull Request
5. Bother [Tuxx](https://github.com/tuxx) if it sits too long 🙂

## Acknowledgements

- [xgb](https://github.com/BurntSushi/xgb) - X Go Binding
- [PAM](https://github.com/msteinert/pam) - Go wrapper for PAM
- [mpv](https://mpv.io/) - Video player
