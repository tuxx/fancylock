package internal

import (
	"os/exec"
	"sync"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
	"github.com/neurlang/wayland/wl"
	zxdg "github.com/neurlang/wayland/xdg"
	ext "github.com/tuxx/wayland-ext-session-lock-go"
)

// Monitor represents a physical display
type Monitor struct {
	X      int
	Y      int
	Width  int
	Height int
}

// IdleWatcher monitors user activity
type IdleWatcher struct {
	conn         *xgb.Conn
	timeout      time.Duration
	stopChan     chan struct{}
	parentLocker *X11Locker
}

// X11Locker implements the ScreenLocker interface for X11
type X11Locker struct {
	config         Configuration
	conn           *xgb.Conn
	screen         *xproto.ScreenInfo
	window         xproto.Window
	gc             xproto.Gcontext
	width          uint16
	height         uint16
	helper         *LockHelper
	mediaPlayer    *MediaPlayer
	passwordBuf    string
	isLocked       bool
	passwordDots   []bool // true for filled, false for empty
	maxDots        int
	idleWatcher    *IdleWatcher
	dotWindows     []xproto.Window // Track password dot windows
	lockoutManager *LockoutManager // Use the shared lockout manager
	messageWindow  xproto.Window   // Window for displaying lockout messages
	textGC         xproto.Gcontext // Graphics context for drawing text
}

// MediaType defines the type of media file
type MediaType int

const (
	// MediaTypeVideo represents video files
	MediaTypeVideo MediaType = iota

	// MediaTypeImage represents image files
	MediaTypeImage

	// MediaTypeUnknown represents unsupported file types
	MediaTypeUnknown
)

// MediaFile represents a media file that can be played
type MediaFile struct {
	Path string
	Type MediaType
}

// MediaPlayer handles playing media files on the lockscreen
type MediaPlayer struct {
	config           Configuration
	mediaFiles       []MediaFile
	currentProcs     []*exec.Cmd
	currentProc      *exec.Cmd
	stopChan         chan struct{}
	doneChan         chan bool
	mutex            sync.Mutex
	running          bool
	monitors         []Monitor
	currentlyPlaying map[int]string
}

// Configuration holds the application settings
type Configuration struct {
	// Directory containing media files to play during lock screen
	MediaDir string `json:"media_dir"`

	// Whether to lock the screen immediately on startup
	LockScreen bool `json:"lock_screen"`

	// List of supported file extensions for media files
	SupportedExt []string `json:"supported_extensions"`

	// Idle timeout in seconds before auto-locking
	IdleTimeout int `json:"idle_timeout"`

	// PAM service name to use for authentication
	PamService string `json:"pam_service"`

	// Whether to include non-video media files (like images)
	IncludeImages bool `json:"include_images"`

	// How long to display each image in seconds (if static media is used)
	ImageDisplayTime int `json:"image_display_time"`

	// Background color (in hex format) for the lock screen
	BackgroundColor string `json:"background_color"`

	// Whether to blur background before locking
	BlurBackground bool `json:"blur_background"`

	// External player command to use (like mpv)
	MediaPlayerCmd string `json:"media_player_cmd"`

	// Enable debug exit with ESC or Q key
	DebugExit bool `json:"debug_exit"`

	// Command to run before locking the screen
	PreLockCommand string `json:"pre_lock_command"`

	// Command to run after unlocking the screen
	PostLockCommand string `json:"post_lock_command"`
}

// ScreenLocker interface defines methods that any screen locker should implement
type ScreenLocker interface {
	// Lock immediately locks the screen
	Lock() error

	// StartIdleMonitor starts monitoring for user inactivity and locks after the timeout
	StartIdleMonitor() error
}

// AuthResult represents the result of an authentication attempt
type AuthResult struct {
	Success bool
	Message string
}

// PamAuthenticator handles PAM-based user authentication
type PamAuthenticator struct {
	serviceName string
	username    string
}

type SecurePassword struct {
	data []byte
	mu   sync.Mutex
}

// WaylandDisplay handles the Wayland display connection
type WaylandDisplay struct {
	display    *wl.Display
	registry   *wl.Registry
	compositor *wl.Compositor
	shell      *zxdg.WmBase
	shm        *wl.Shm
	hasXrgb    bool
}

// WaylandWindow represents a Wayland window
type WaylandWindow struct {
	display          *WaylandDisplay
	width, height    int
	surface          *wl.Surface
	xdgSurface       *zxdg.Surface
	xdgToplevel      *zxdg.Toplevel
	buffers          [2]WaylandBuffer
	callback         *wl.Callback
	waitForConfigure bool
	locker           *WaylandLocker
}

// WaylandBuffer represents a buffer for drawing
type WaylandBuffer struct {
	buffer  *wl.Buffer
	shmData []byte
	busy    bool
}

// WaylandIdleWatcher monitors user activity on Wayland
type WaylandIdleWatcher struct {
	timeout      time.Duration
	stopChan     chan struct{}
	parentLocker *WaylandLocker
}

// surfaceHandler handles Wayland surface events
type surfaceHandler struct {
	client      *WaylandLocker
	surface     *wl.Surface
	lockSurface *ext.SessionLockSurface
}

// outputInfo contains information about a Wayland output
type outputInfo struct {
	x, y   int
	width  int
	height int
}

// RegistryHandler handles Wayland registry events
type RegistryHandler struct {
	wl.OutputGeometryHandler
	wl.OutputModeHandler

	registry         *wl.Registry
	compositor       *wl.Compositor
	lockManager      *ext.SessionLockManager
	seat             *wl.Seat
	shm              *wl.Shm
	outputs          map[uint32]*wl.Output
	outputGeometries map[*wl.Output]outputInfo
}

// Media file extension maps
var (
	videoExtMap = map[string]bool{
		".mp4": true, ".mkv": true, ".avi": true, ".mov": true,
		".webm": true, ".wmv": true, ".flv": true, ".3gp": true,
	}
	imageExtMap = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
		".bmp": true, ".svg": true, ".webp": true,
	}
)

// handlerFunc is a function type for handling output geometry events
type handlerFunc func(wl.OutputGeometryEvent)

// outputModeHandlerFunc is a function type for handling output mode events
type outputModeHandlerFunc func(ev wl.OutputModeEvent)

type WaylandLocker struct {
	display         *wl.Display
	registry        *wl.Registry
	registryHandler *RegistryHandler
	compositor      *wl.Compositor
	lockManager     *ext.SessionLockManager
	lock            *ext.SessionLock
	keyboard        *wl.Keyboard
	seat            *wl.Seat
	shm             *wl.Shm
	securePassword  *SecurePassword
	surfaces        map[*wl.Output]struct {
		wlSurface   *wl.Surface
		lockSurface *ext.SessionLockSurface
	}
	redrawCh        chan int
	outputs         map[uint32]*wl.Output
	mediaPlayer     *MediaPlayer
	done            chan struct{}
	config          Configuration
	helper          *LockHelper
	lockActive      bool
	countdownActive bool
	lockoutManager  *LockoutManager // Use the shared lockout manager
	mu              sync.Mutex
}
