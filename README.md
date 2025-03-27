# FancyLock

FancyLock is a visually appealing screen locker for Linux that plays videos or images in the background while locked. It's designed to be both secure and aesthetically pleasing.

<p align="center">
  <img src="https://github.com/user-attachments/assets/bd62c40f-d491-4f75-8771-67c2743d86f4" alt="lockscreen">
</p>

## Features

- Play videos or images as lock screen backgrounds
- Support for multiple monitors
- PAM authentication for secure login
- Automatic locking after idle time
- Customizable settings

## Installation

### Prerequisites

- Go 1.16 or higher
- X11 development libraries
- mpv (for video/image playback)
- PAM development libraries

### Installing dependencies

On Debian/Ubuntu systems:

```bash
sudo apt install golang libx11-dev libpam0g-dev mpv
```

On Arch Linux:

```bash
sudo pacman -S go libx11 pam mpv
```

### Building from source

```bash
# Clone the repository
git clone https://github.com/yourusername/fancylock.git
cd fancylock

# Build the application
go build -o fancylock

# Optional: install system-wide
sudo cp fancylock /usr/local/bin/
```

## How to Use

### Basic Usage

Lock your screen immediately:

```bash
fancylock -l
# or
fancylock --lock
```

Start in idle monitor mode (will lock after idle timeout):

```bash
fancylock
```

### Configuration

FancyLock looks for a configuration file at `~/.config/fancylock/config.json`. If it doesn't exist, a default one will be created.

You can specify a different configuration file using:

```bash
fancylock -c /path/to/config.json
```

### Sample Configuration

```json
{
  "media_dir": "/home/user/Videos",
  "lock_screen": false,
  "supported_extensions": [".mp4", ".mkv", ".mov", ".avi", ".webm"],
  "idle_timeout": 300,
  "pam_service": "system-auth",
  "include_images": true,
  "image_display_time": 30,
  "background_color": "#000000",
  "blur_background": false,
  "media_player_cmd": "mpv"
}
```

### Configuration Options

- `media_dir`: Directory containing videos/images to display while locked
- `lock_screen`: Whether to lock the screen immediately on startup
- `supported_extensions`: File extensions to look for in the media directory
- `idle_timeout`: Time in seconds before auto-locking (when not using -l flag)
- `pam_service`: PAM service name for authentication
- `include_images`: Whether to include images along with videos
- `image_display_time`: How long to display each image in seconds
- `media_player_cmd`: Command to use for playing media (default: mpv)

Note: The configuration also includes `background_color` and `blur_background` options, but these are not currently implemented.

## Current Status

### What's Working

- ✅ X11 screen locking with PAM authentication
- ✅ Multi-monitor support with correct video positioning
- ✅ Video and image playback during lock screen
- ✅ Password entry with visual feedback (dots)
- ✅ Idle monitoring for automatic locking
- ✅ Keyboard and pointer grabbing to prevent bypass

### What Needs Improvement

- ⚠️ Error handling in some edge cases
- ⚠️ Password entry UI could be more polished
- ⚠️ Failed password attempt limiting (currently allows unlimited tries)
- ⚠️ Video transition effects between media files
- ⚠️ Memory optimization for long-running sessions
- ⚠️ Better handling of system sleep/wake events
- ⚠️ Auto-creation of default config file (if none exists)

## Future Implementations

- 🚧 Password attempt limiting with temporary lockout
- 🚧 Wayland support
- 🚧 Configurable UI theme and appearance
- 🚧 Blurred background option
- 🚧 More interactive lock screen elements
- 🚧 Support for additional media players
- 🚧 Screensaver mode with clock display
- 🚧 Improved multi-head support (different videos per monitor)
- 🚧 Systemd integration
- 🚧 Implementation of background color and blur options
- 🚧 Auto-generation of default config file

## Contributing

Contributions are welcome! Please feel free to submit pull requests or open issues to improve the application.

## Acknowledgements

- [xgb](https://github.com/BurntSushi/xgb) - X Go Binding
- [PAM](https://github.com/msteinert/pam) - Go wrapper for PAM
- [mpv](https://mpv.io/) - Video player
