# gdrivefs

Google Drive FUSE filesystem for Linux. Mount your Google Drive as a local filesystem with read/write support and conflict detection.

## Features

- **FUSE-based**: Runs in userspace, no kernel module required
- **Read/Write**: Full read and write support with automatic upload on file close
- **Conflict Detection**: Fails on write conflicts (remote file changed since read)
- **OAuth 2.0 Device Flow**: Headless authentication for CLI/servers
- **Token Encryption**: Tokens stored encrypted with age
- **systemd Integration**: Auto-remount on boot via user service
- **Metadata Caching**: Reduces API calls with configurable TTL

## Requirements

- Linux with FUSE support (kernel 2.6.19+)
- Go 1.22+
- Google Cloud project with Drive API enabled

## Installation

### From Source

```bash
git clone https://github.com/aydinke/gdrivefs.git
cd gdrivefs
go build -o gdrivefs ./cmd/gdrivefs
sudo mv gdrivefs /usr/local/bin/
```

### Dependencies

```bash
# Ubuntu/Debian
sudo apt install fuse3

# Arch Linux
sudo pacman -S fuse3
```

## Setup

### 1. Create Google Cloud Project

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create a new project
3. Enable Google Drive API
4. Create OAuth 2.0 credentials (Desktop application)
5. Note your Client ID and Client Secret

### 2. Configure gdrivefs

Create the config file:

```bash
mkdir -p ~/.config/gdrivefs
cat > ~/.config/gdrivefs/config.yaml << EOF
client_id: "YOUR_CLIENT_ID.apps.googleusercontent.com"
client_secret: "YOUR_CLIENT_SECRET"
mounts:
  default:
    path: ~/Drive
    auto_mount: true
    read_only: false
EOF
```

### 3. Authenticate

```bash
gdrivefs auth login
```

This will:
1. Print a URL and code
2. Open the URL in your browser
3. Enter the code to authorize
4. Save encrypted token to `~/.local/share/gdrivefs/token.enc`

## Usage

### Mount Google Drive

```bash
# Foreground (Ctrl+C to unmount)
gdrivefs mount ~/Drive

# Daemon mode (background)
gdrivefs mount -d ~/Drive

# Read-only
gdrivefs mount -r ~/Drive
```

### Unmount

```bash
gdrivefs unmount ~/Drive
# or
fusermount -u ~/Drive
```

### Auto-mount on Boot

```bash
# Enable systemd user service
gdrivefs enable
systemctl --user enable --now gdrivefs

# Disable
gdrivefs disable
systemctl --user disable gdrivefs
```

### Check Status

```bash
gdrivefs status
gdrivefs auth status
```

## File Layout

```
~/.config/gdrivefs/
├── config.yaml           # Configuration

~/.local/share/gdrivefs/
├── token.enc             # Encrypted OAuth token
├── token.enc.key         # Encryption key
└── uploads/              # Temp files for uploads
```

## How It Works

### Architecture

```
┌─────────────────────────────────────────────┐
│              User Applications               │
└─────────────────┬───────────────────────────┘
                  │
                  ▼
┌─────────────────────────────────────────────┐
│            VFS (Kernel Layer)               │
└─────────────────┬───────────────────────────┘
                  │
                  ▼ /dev/fuse
┌─────────────────────────────────────────────┐
│            gdrivefs (FUSE)                  │
├─────────────────────────────────────────────┤
│  ┌─────────┐  ┌─────────┐  ┌─────────────┐ │
│  │  Cache  │  │  Drive  │  │    Auth     │ │
│  │         │  │ Client  │  │   Manager   │ │
│  └─────────┘  └────┬────┘  └─────────────┘ │
└────────────────────┼───────────────────────┘
                     │
                     ▼ REST API
┌─────────────────────────────────────────────┐
│            Google Drive API                 │
└─────────────────────────────────────────────┘
```

### Write Flow

1. Application opens file for write
2. gdrivefs downloads file to temp location
3. Application writes to temp file
4. On close/flush, gdrivefs checks for conflicts
5. If no conflict, uploads to Drive
6. If conflict, returns error (EBUSY)

### Conflict Detection

When writing a file:
1. Compare remote `modifiedTime` with time of initial read
2. If remote is newer, fail with `EBUSY`
3. User must resolve conflict manually

## Limitations

- **Partial writes**: Downloaded to temp file, uploaded on close
- **Large files**: Memory/disk usage for temp files
- **Google Docs**: Exported as PDF/Office formats (read-only for Docs)
- **Rate limits**: Drive API quotas may limit operations
- **No offline mode**: Requires network connection

## Development

### Build

```bash
go build ./...
go test ./...
```

### Project Structure

```
gdrivefs/
├── cmd/gdrivefs/main.go    # CLI entrypoint
├── internal/
│   ├── auth/               # OAuth + token storage
│   ├── cache/              # Metadata cache
│   ├── config/             # Configuration
│   ├── drive/              # Drive API client
│   └── fuse/               # FUSE operations
└── go.mod
```

## Troubleshooting

### "transport endpoint is not connected"

```bash
fusermount -uz ~/Drive
```

### "permission denied"

Ensure your user is in the `fuse` group:
```bash
sudo usermod -aG fuse $USER
# Log out and back in
```

### Token expired

Tokens are automatically refreshed. If refresh fails:
```bash
gdrivefs auth logout
gdrivefs auth login
```

## License

GPL-3.0 - See [LICENSE](LICENSE)

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Run tests
5. Submit a pull request

## Acknowledgments

- [bazil.org/fuse](https://bazil.org/fuse/) - Go FUSE library
- [google.golang.org/api](https://pkg.go.dev/google.golang.org/api) - Google API Go client
- [filippo.io/age](https://filippo.io/age/) - Age encryption library
