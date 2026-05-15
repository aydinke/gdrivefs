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

## Quick Start

### 1. Install

```bash
# Download latest release
curl -sL https://github.com/aydinke/gdrivefs/releases/latest/download/gdrivefs-linux-amd64 -o gdrivefs
chmod +x gdrivefs
sudo mv gdrivefs /usr/local/bin/
```

Or build from source:

```bash
git clone https://github.com/aydinke/gdrivefs.git
cd gdrivefs
go build -o gdrivefs ./cmd/gdrivefs
sudo mv gdrivefs /usr/local/bin/
```

### 2. Authenticate

```bash
gdrivefs auth login
```

This will:
1. Display a URL and code
2. Open the URL in your browser
3. Enter the code to authorize
4. Save encrypted token to `~/.local/share/gdrivefs/`

### 3. Mount

```bash
# Create mount point
mkdir -p ~/Drive

# Mount (foreground)
gdrivefs mount ~/Drive

# Mount (background/daemon)
gdrivefs mount -d ~/Drive

# Read-only
gdrivefs mount -r ~/Drive
```

### 4. Auto-mount on Boot

```bash
gdrivefs enable
systemctl --user enable --now gdrivefs
```

## Requirements

- Linux with FUSE support (kernel 2.6.19+)
- Go 1.22+ (for building)

### Install FUSE

```bash
# Ubuntu/Debian
sudo apt install fuse3

# Arch Linux
sudo pacman -S fuse3

# Fedora
sudo dnf install fuse3
```

## Usage

### Authentication

```bash
gdrivefs auth login    # Authenticate with Google
gdrivefs auth status   # Check auth status
gdrivefs auth logout   # Remove stored token
```

### Mount Operations

```bash
gdrivefs mount ~/Drive              # Foreground mount
gdrivefs mount -d ~/Drive           # Daemon mode
gdrivefs mount -r ~/Drive           # Read-only
gdrivefs unmount ~/Drive            # Unmount
```

### systemd Integration

```bash
gdrivefs enable     # Create systemd user service
gdrivefs disable    # Remove systemd service
gdrivefs status     # Show status
```

## OAuth Credentials

**Default (easiest)**: Uses embedded shared credentials. Just run `gdrivefs auth login`.

**Custom credentials** (optional): Create `~/.config/gdrivefs/config.yaml`:

```yaml
client_id: "your-client-id.apps.googleusercontent.com"
client_secret: "your-client-secret"
mounts:
  default:
    path: ~/Drive
    auto_mount: true
    read_only: false
```

### Creating Your Own OAuth App (Optional)

If you want to use your own Google Cloud project:

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create a new project
3. Enable Google Drive API
4. Go to "APIs & Services" → "Credentials"
5. Create "OAuth client ID" → "Desktop application"
6. Copy Client ID and Client Secret to config.yaml

**Note**: Apps in "Testing" mode show an "unverified app" warning. This is safe for personal use.

## How It Works

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
- **Large files**: Temp file disk usage
- **Google Docs**: Exported as PDF/Office formats (read-only for native Docs)
- **Rate limits**: Drive API quotas may limit operations
- **No offline mode**: Requires network connection

## Troubleshooting

### "transport endpoint is not connected"

```bash
fusermount -uz ~/Drive
gdrivefs mount ~/Drive
```

### "permission denied"

Ensure your user is in the `fuse` group:
```bash
sudo usermod -aG fuse $USER
# Log out and back in
```

### "not authenticated" but token exists

Token may need refresh:
```bash
gdrivefs auth logout
gdrivefs auth login
```

### Mount shows empty

Check network connectivity and Drive API status.

## File Locations

```
~/.config/gdrivefs/
├── config.yaml           # Optional custom config

~/.local/share/gdrivefs/
├── token.enc             # Encrypted OAuth token
├── token.enc.key         # Encryption key
└── uploads/              # Temp files for uploads
```

## Development

```bash
# Build
go build ./...

# Test
go test ./...

# Lint
go vet ./...
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

## Security

- OAuth tokens are encrypted using [age](https://filippo.io/age/) encryption
- Each user has a unique encryption key stored locally
- Credentials are never transmitted off the local machine
- Users authenticate directly with Google (OAuth 2.0)

## License

GPL-3.0 - See [LICENSE](LICENSE)

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Run tests and lint
5. Submit a pull request

## Similar Projects

- [rclone mount](https://rclone.org/commands/rclone_mount/) - Multi-cloud FUSE mount
- [google-drive-ocamlfuse](https://github.com/astrada/google-drive-ocamlfuse) - OCaml-based Drive FUSE
- [fuse](https://github.com/bazil/fuse) - Go FUSE library used here

## Acknowledgments

- [bazil.org/fuse](https://bazil.org/fuse/) - Go FUSE library
- [google.golang.org/api](https://pkg.go.dev/google.golang.org/api) - Google API Go client
- [filippo.io/age](https://filippo.io/age/) - Age encryption library
