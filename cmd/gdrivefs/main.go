package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"github.com/aydinke/gdrivefs/internal/auth"
	"github.com/aydinke/gdrivefs/internal/cache"
	"github.com/aydinke/gdrivefs/internal/config"
	"github.com/aydinke/gdrivefs/internal/drive"
	"github.com/aydinke/gdrivefs/internal/fusefs"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

var (
	daemonFlag    bool
	readOnlyFlag  bool
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "gdrivefs",
	Short: "Google Drive FUSE filesystem for Linux",
	Long: `gdrivefs mounts your Google Drive as a local filesystem using FUSE.
Supports read/write operations with conflict detection.`,
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication",
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with Google Drive",
	Run:   runAuthLogin,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored authentication",
	Run:   runAuthLogout,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check authentication status",
	Run:   runAuthStatus,
}

var mountCmd = &cobra.Command{
	Use:   "mount [mountpoint]",
	Short: "Mount Google Drive",
	Args:  cobra.ExactArgs(1),
	Run:   runMount,
}

var unmountCmd = &cobra.Command{
	Use:   "unmount [mountpoint]",
	Short: "Unmount Google Drive",
	Args:  cobra.ExactArgs(1),
	Run:   runUnmount,
}

var enableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable systemd user service for auto-mount on boot",
	Run:   runEnable,
}

var disableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable systemd user service",
	Run:   runDisable,
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show mount status",
	Run:   runStatus,
}

var trashCmd = &cobra.Command{
	Use:   "trash",
	Short: "Manage Google Drive trash",
}

var trashListCmd = &cobra.Command{
	Use:   "list",
	Short: "List files in trash",
	Run:   runTrashList,
}

var trashEmptyCmd = &cobra.Command{
	Use:   "empty",
	Short: "Permanently delete all files in trash",
	Run:   runTrashEmpty,
}

func init() {
	rootCmd.AddCommand(authCmd, mountCmd, unmountCmd, enableCmd, disableCmd, statusCmd, trashCmd)
	authCmd.AddCommand(authLoginCmd, authLogoutCmd, authStatusCmd)
	trashCmd.AddCommand(trashListCmd, trashEmptyCmd)

	mountCmd.Flags().BoolVarP(&daemonFlag, "daemon", "d", false, "Run in background (daemon mode)")
	mountCmd.Flags().BoolVarP(&readOnlyFlag, "read-only", "r", false, "Mount read-only")
}

func runAuthLogin(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	if err := config.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize config: %v\n", err)
		os.Exit(1)
	}

	creds := config.GetCredentials()
	if !creds.IsValid() {
		fmt.Fprintln(os.Stderr, "No valid OAuth credentials configured.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Set credentials via environment variables:")
		fmt.Fprintln(os.Stderr, "  export GDRIVEFS_CLIENT_ID=\"your-client-id.apps.googleusercontent.com\"")
		fmt.Fprintln(os.Stderr, "  export GDRIVEFS_CLIENT_SECRET=\"your-client-secret\"")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Or add to ~/.config/gdrivefs/config.yaml:")
		fmt.Fprintln(os.Stderr, "  client_id: \"your-client-id.apps.googleusercontent.com\"")
		fmt.Fprintln(os.Stderr, "  client_secret: \"your-client-secret\"")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "See README for instructions on creating Google Cloud OAuth credentials.")
		os.Exit(1)
	}

	flow := auth.NewOAuthFlow(creds, 8085)

	fmt.Println("Starting OAuth flow...")
	fmt.Println("A browser window will open for you to authorize gdrivefs.")

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	token, err := flow.StartLocalServer(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get token: %v\n", err)
		os.Exit(1)
	}

	store, err := auth.NewTokenStore(config.GetTokenPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create token store: %v\n", err)
		os.Exit(1)
	}

	if err := store.Save(token); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save token: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✓ Successfully authenticated!")
}

func runAuthLogout(cmd *cobra.Command, args []string) {
	store, err := auth.NewTokenStore(config.GetTokenPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create token store: %v\n", err)
		os.Exit(1)
	}

	if !store.Exists() {
		fmt.Println("Not authenticated")
		return
	}

	if err := store.Delete(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to delete token: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Logged out successfully")
}

func runAuthStatus(cmd *cobra.Command, args []string) {
	store, err := auth.NewTokenStore(config.GetTokenPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create token store: %v\n", err)
		os.Exit(1)
	}

	if !store.Exists() {
		fmt.Println("Not authenticated")
		return
	}

	token, err := store.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load token: %v\n", err)
		os.Exit(1)
	}

	if token.Expiry.Before(time.Now()) {
		fmt.Println("Token expired (will be refreshed on next use)")
	} else {
		fmt.Printf("Authenticated until: %s\n", token.Expiry.Format(time.RFC3339))
	}
}

func runMount(cmd *cobra.Command, args []string) {
	mountpoint := args[0]
	absMountpoint, err := filepath.Abs(mountpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve mountpoint: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(absMountpoint, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create mountpoint: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()

	store, err := auth.NewTokenStore(config.GetTokenPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create token store: %v\n", err)
		os.Exit(1)
	}

	if !store.Exists() {
		fmt.Fprintln(os.Stderr, "Not authenticated. Run 'gdrivefs auth login' first.")
		os.Exit(1)
	}

	token, err := store.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load token: %v\n", err)
		os.Exit(1)
	}

	creds := config.GetCredentials()
	oauthCfg := &oauth2.Config{
		ClientID:     creds.ClientID,
		ClientSecret: creds.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://oauth2.googleapis.com/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
		Scopes: auth.Scopes,
	}

	c := cache.New(30 * time.Second)
	client, err := drive.NewClient(ctx, token, oauthCfg, c, config.GetUploadsPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Drive client: %v\n", err)
		os.Exit(1)
	}

	rootID, err := client.GetRootID(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get root ID: %v\n", err)
		os.Exit(1)
	}

	options := []fuse.MountOption{
		fuse.FSName("gdrivefs"),
		fuse.Subtype("gdrivefs"),
	}

	if readOnlyFlag {
		options = append(options, fuse.ReadOnly())
	}

	fsc, err := fuse.Mount(absMountpoint, options...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to mount: %v\n", err)
		os.Exit(1)
	}

	defer fsc.Close()

	filesys := fusefs.NewFilesystem(client, rootID)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nUnmounting...")
		fuse.Unmount(absMountpoint)
		os.Exit(0)
	}()

	fmt.Printf("Mounted Google Drive at %s\n", absMountpoint)
	fmt.Println("Press Ctrl+C to unmount")

	if err := fs.Serve(fsc, filesys); err != nil {
		fmt.Fprintf(os.Stderr, "Serve error: %v\n", err)
		os.Exit(1)
	}
}

func runUnmount(cmd *cobra.Command, args []string) {
	mountpoint := args[0]
	absMountpoint, err := filepath.Abs(mountpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve mountpoint: %v\n", err)
		os.Exit(1)
	}

	if err := fuse.Unmount(absMountpoint); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to unmount: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Unmounted %s\n", absMountpoint)
}

func runEnable(cmd *cobra.Command, args []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get home directory: %v\n", err)
		os.Exit(1)
	}

	servicePath := filepath.Join(home, ".config", "systemd", "user", "gdrivefs.service")
	serviceContent := `[Unit]
Description=Google Drive FUSE Mount
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
ExecStart=/usr/local/bin/gdrivefs mount %h/Drive
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`

	if err := os.MkdirAll(filepath.Dir(servicePath), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create systemd directory: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write service file: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Created systemd user service at", servicePath)
	fmt.Println("\nTo enable auto-mount on boot, run:")
	fmt.Println("  systemctl --user enable --now gdrivefs")
}

func runDisable(cmd *cobra.Command, args []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get home directory: %v\n", err)
		os.Exit(1)
	}

	servicePath := filepath.Join(home, ".config", "systemd", "user", "gdrivefs.service")

	if _, err := os.Stat(servicePath); os.IsNotExist(err) {
		fmt.Println("Systemd service not installed")
		return
	}

	if err := os.Remove(servicePath); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to remove service file: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Removed systemd user service")
	fmt.Println("To fully disable, also run:")
	fmt.Println("  systemctl --user disable gdrivefs")
}

func runStatus(cmd *cobra.Command, args []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get home directory: %v\n", err)
		os.Exit(1)
	}

	servicePath := filepath.Join(home, ".config", "systemd", "user", "gdrivefs.service")
	if _, err := os.Stat(servicePath); err == nil {
		fmt.Println("Systemd service: installed")
	} else {
		fmt.Println("Systemd service: not installed")
	}

	store, err := auth.NewTokenStore(config.GetTokenPath())
	if err != nil {
		fmt.Println("Auth status: error")
		return
	}

	if store.Exists() {
		fmt.Println("Auth status: authenticated")
	} else {
		fmt.Println("Auth status: not authenticated")
	}

	fmt.Println("\nMount points:")
	for name, mnt := range config.Get().Mounts {
		fmt.Printf("  %s -> %s (auto: %v)\n", name, mnt.Path, mnt.AutoMount)
	}
}

func getHomeDir() string {
	usr, err := user.Current()
	if err != nil {
		return os.Getenv("HOME")
	}
	return usr.HomeDir
}

func getDriveClient(ctx context.Context) (*drive.Client, error) {
	store, err := auth.NewTokenStore(config.GetTokenPath())
	if err != nil {
		return nil, fmt.Errorf("failed to create token store: %w", err)
	}

	if !store.Exists() {
		return nil, fmt.Errorf("not authenticated. Run 'gdrivefs auth login' first")
	}

	token, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load token: %w", err)
	}

	creds := config.GetCredentials()
	oauthCfg := &oauth2.Config{
		ClientID:     creds.ClientID,
		ClientSecret: creds.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://oauth2.googleapis.com/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
		Scopes: auth.Scopes,
	}

	c := cache.New(30 * time.Second)
	return drive.NewClient(ctx, token, oauthCfg, c, config.GetUploadsPath())
}

func runTrashList(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	if err := config.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize config: %v\n", err)
		os.Exit(1)
	}

	client, err := getDriveClient(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	files, err := client.ListTrash(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list trash: %v\n", err)
		os.Exit(1)
	}

	if len(files) == 0 {
		fmt.Println("Trash is empty")
		return
	}

	fmt.Printf("Files in trash (%d):\n", len(files))
	for _, f := range files {
		fmt.Printf("  %s (%s)\n", f.Name, f.ID)
	}
}

func runTrashEmpty(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	if err := config.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize config: %v\n", err)
		os.Exit(1)
	}

	client, err := getDriveClient(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("Emptying trash...")
	if err := client.EmptyTrash(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to empty trash: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Trash emptied successfully")
}
