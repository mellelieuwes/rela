// rela-desktop runs the data entry application as a native desktop app using Wails.
//
// Usage:
//
//	rela-desktop [-project .]
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"gopkg.in/yaml.v3"

	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/dataentry"
	"github.com/Sourcehaven-BV/rela/internal/desktop"
	"github.com/Sourcehaven-BV/rela/internal/git"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/scheduler"
	"github.com/Sourcehaven-BV/rela/internal/script"
	"github.com/Sourcehaven-BV/rela/internal/storage"
)

// Default window geometry, used when there is no saved state.
const (
	defaultWindowWidth  = 1280
	defaultWindowHeight = 800
)

// singleInstanceID identifies this app's instance lock. It matches the bundle
// identifier in build/package/macos/Info.plist.
const singleInstanceID = "com.sourcehaven.rela-desktop"

// SingleInstanceSecret keys the IPC between a second launch and the running
// instance. Override at build time with
// -ldflags "-X main.SingleInstanceSecret=<value>".
//
// The transport is not private: on macOS it is an NSDistributedNotification,
// readable by any process in the user's session. Unencrypted, the payload
// would disclose the project path, and any local process could forge a launch
// to make rela-desktop open a directory of its choosing. Encrypting removes
// both, and costs nothing.
//
// This is deliberately a constant rather than a random per-run value: the two
// processes derive the key independently and never negotiate, so a random key
// would simply never match. It is therefore obfuscation of a local channel,
// not a secret — which is the whole threat model here.
//
//nolint:gosec // G101: hardcoded by necessity, per the paragraph above
var SingleInstanceSecret = "rela-desktop/single-instance/v1"

// singleInstanceKey derives the 32-byte key Wails expects.
func singleInstanceKey() [32]byte {
	return sha256.Sum256([]byte(SingleInstanceSecret))
}

// Version is set at build time via -ldflags.
var Version = "dev"

// GitHubClientID is the OAuth App client ID for rela-desktop.
// Users can create their own OAuth App at https://github.com/settings/developers
// For development, use a test client ID.
const GitHubClientID = "" // Set via build flags or environment

// Desktop is the backend bound to the Wails frontend.
// It manages project lifecycle: opening a directory picker, loading a project,
// and persisting recent projects in user preferences.
type Desktop struct {
	// ctx carries the desktop Principal for the life of the process. Under
	// Wails v2 this doubled as the runtime handle; v3 separates the two, so
	// this is now only an attribution context.
	ctx   context.Context            //nolint:containedctx // lives for the struct lifetime
	wails *application.App           // Wails v3 runtime handle
	win   *application.WebviewWindow // main window
	mu    sync.RWMutex
	// registry holds every loaded project. The single-project fields below
	// still track the active one; they are the "no project loaded" path and
	// the welcome page's view of the world.
	registry          *projectRegistry
	app               *dataentry.App
	svc               *appbuild.Services // per-project services; closed on next LoadProject
	handler           http.Handler
	loadErr           string
	prefs             *desktop.Preferences
	cloneAuth         *cloneAuthState
	lastCloneDir      string           // tracks the most recent clone for project selection
	pendingSetupDir   string           // project dir awaiting data-entry.yaml setup
	pendingSetupFS    storage.FS       // fs for pending setup
	pendingSetupPaths *project.Context // project paths for pending setup
	pendingProject    string           // project to load once the instance lock is held
	menuReady         atomic.Bool      // true once the native menu exists (post-Run)
	stopScheduler     context.CancelFunc
}

// cloneAuthState tracks an in-progress OAuth device flow.
type cloneAuthState struct {
	deviceCode string
	userCode   string
	verifyURL  string
	expiresIn  int
	interval   int
}

// ServiceName identifies this service to the Wails v3 runtime.
func (d *Desktop) ServiceName() string { return "rela-desktop" }

// ServiceStartup is the Wails v3 replacement for v2's OnStartup. It receives
// the application-lifetime context, which is cancelled just before shutdown.
// coverage-ignore-func: Wails lifecycle callback
//
//nolint:unparam // signature is fixed by Wails' ServiceStartup interface
func (d *Desktop) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	d.ctx = principal.With(ctx, principal.Principal{
		User: principal.SystemUser(),
		Tool: principal.ToolDesktop,
	})

	// Deferred from main so a redundant second instance never opens the store.
	// The menu is not live yet (see refreshMenu), so any refresh triggered by
	// this load is suppressed; main sets the fully-built menu straight after.
	if d.pendingProject != "" {
		// contextcheck: LoadProject takes no ctx by design — it owns the
		// project lifetime and cancels via d.stopScheduler, not the caller's
		// context. Threading ctx here would be a wider refactor.
		//nolint:contextcheck // see above
		if errMsg := d.LoadProject(d.pendingProject); errMsg != "" {
			slog.Warn("could not load project", "path", d.pendingProject, "error", errMsg)
		}
	}
	return nil
}

// ServiceShutdown releases the loaded project on quit. Wails v2 had no
// shutdown hook wired, so the scheduler and services leaked on exit.
// Note: the interface takes no context — adding one silently opts out.
// coverage-ignore-func: Wails lifecycle callback
//
//nolint:unparam // signature is fixed by Wails' ServiceShutdown interface
func (d *Desktop) ServiceShutdown() error {
	d.releaseLoadedProject()
	return nil
}

// onSecondInstanceLaunch handles another rela-desktop being started while this
// one is running. Only one instance may hold a project: the sqlite backend
// takes an exclusive lock at Open (DEC-LFSYNY), so a second process would fail
// to open the store rather than compete for it. Instead of letting that happen,
// we adopt the second instance's project and focus this window.
//
// data.Args is the second process's argv and data.WorkingDir its cwd, so a
// relative -project must be resolved against THAT directory, not ours.
// coverage-ignore-func: requires a second process
func (d *Desktop) onSecondInstanceLaunch(data application.SecondInstanceData) {
	if dir := projectDirFromArgs(data.Args, data.WorkingDir); dir != "" {
		if errMsg := d.LoadProject(dir); errMsg != "" {
			slog.Warn("second instance: could not load project", "path", dir, "error", errMsg)
		} else {
			d.reloadWindow()
		}
	}

	// Bring this window forward regardless — the user asked for the app.
	if d.win != nil {
		d.win.Show()
		d.win.UnMinimise()
		d.win.Focus()
	}
}

// projectDirFromArgs extracts an absolute project directory from a second
// instance's argv. It returns "" when no project was named, so the caller
// leaves the currently-open project alone.
func projectDirFromArgs(args []string, workingDir string) string {
	val := ""
	for i := 1; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-project" || a == "--project":
			if i+1 < len(args) {
				val = args[i+1]
			}
		case strings.HasPrefix(a, "-project="):
			val = strings.TrimPrefix(a, "-project=")
		case strings.HasPrefix(a, "--project="):
			val = strings.TrimPrefix(a, "--project=")
		}
	}
	if val == "" {
		return ""
	}
	if !filepath.IsAbs(val) {
		val = filepath.Join(workingDir, val)
	}
	// "." resolves to the launching shell's cwd, which is only a project if it
	// actually looks like one; otherwise treat it as "no project named".
	if !isRelaProject(val) {
		return ""
	}
	return val
}

// saveWindowState records the window geometry for the next launch. Failures
// are logged and swallowed: losing window position must never block a quit.
// coverage-ignore-func: requires Wails runtime
func (d *Desktop) saveWindowState() {
	if d.win == nil {
		return
	}
	w, h := d.win.Size()
	x, y := d.win.Position()
	st := desktop.WindowState{Width: w, Height: h, X: x, Y: y, Maximized: d.win.IsMaximised()}
	if !st.Valid() {
		return // don't overwrite good state with a minimized/degenerate size
	}
	d.prefs.Window = st
	if err := d.prefs.Save(); err != nil {
		slog.Warn("could not save window state", "error", err)
	}
}

// onOpenedWithFile handles a .rela project bundle opened from Finder (double
// click, drop on the Dock icon, or "Open With"). macOS delivers this as an
// Apple Event rather than argv, so it arrives here and not through -project.
//
// A .rela bundle is a DIRECTORY declared as a package in Info.plist, so the
// path is already the project root and needs no adjustment.
// coverage-ignore-func: requires Finder
func (d *Desktop) onOpenedWithFile(path string) {
	if path == "" {
		return
	}
	if !isRelaProject(path) {
		d.errorDialog("Not a rela project",
			filepath.Base(path)+" does not contain a schema.yaml or .rela directory.")
		return
	}
	if errMsg := d.LoadProject(path); errMsg != "" {
		d.errorDialog("Failed to open project", errMsg)
		return
	}
	d.reloadWindow()
	if d.win != nil {
		d.win.Show()
		d.win.UnMinimise()
		d.win.Focus()
	}
}

// pickDirectory shows a native directory chooser and returns the chosen path
// ("" if cancelled). Replaces v2's runtime.OpenDirectoryDialog.
// coverage-ignore-func: requires Wails runtime
func (d *Desktop) pickDirectory(title, defaultDir string) (string, error) {
	if d.wails == nil {
		return "", errors.New("application not ready")
	}
	return d.wails.Dialog.OpenFileWithOptions(&application.OpenFileDialogOptions{
		Title:     title,
		Directory: defaultDir, // v3's name for v2 DefaultDirectory
		// A .rela bundle is declared as a package in Info.plist, so the panel
		// classifies it as a FILE, not a directory. With CanChooseFiles false
		// it would be greyed out and unselectable — the very projects this
		// picker exists to open. Allowing both keeps plain project folders
		// selectable too.
		//
		// TreatsFilePackagesAsDirectories is deliberately NOT set: it makes the
		// panel descend INTO the bundle instead of selecting it.
		CanChooseDirectories: true,
		CanChooseFiles:       true,
	}).PromptForSingleSelection()
}

// errorDialog shows a native error dialog. Replaces v2's runtime.MessageDialog.
// coverage-ignore-func: requires Wails runtime
func (d *Desktop) errorDialog(title, message string) {
	if d.wails == nil {
		return
	}
	d.wails.Dialog.Error().SetTitle(title).SetMessage(message).Show()
}

// reloadWindow reloads the webview. Replaces v2's runtime.WindowReloadApp.
// coverage-ignore-func: requires Wails runtime
func (d *Desktop) reloadWindow() {
	if d.win != nil {
		d.win.Reload()
	}
}

// OpenProject opens a native directory picker and loads the selected project.
// It returns an error string (empty on success) so the JS frontend can react.
// coverage-ignore-start: os-fs-event: runtime.OpenDirectoryDialog needs the live Wails runtime and a native OS
// directory picker; unreachable
// in unit tests
func (d *Desktop) OpenProject() string {
	dir, err := d.pickDirectory("Open Rela Project", "")
	if err != nil {
		return fmt.Sprintf("dialog error: %v", err)
	}
	if dir == "" {
		return "" // user cancelled
	}
	return d.LoadProject(projectRootOf(dir))
}

// coverage-ignore-end

// OpenRecentProject loads a project from the recent projects list.
// It returns an error string (empty on success) so the JS frontend can react.
func (d *Desktop) OpenRecentProject(path string) string {
	errMsg := d.LoadProject(path)
	if errMsg != "" {
		// Project no longer valid — remove from recent list.
		d.prefs.RemoveRecentProject(path)
		_ = d.prefs.Save()
		d.refreshMenu()
	}
	return errMsg
}

// failLoad records err as the current load error and returns its
// string form, so LoadProject's error branches stay one line each.
func (d *Desktop) failLoad(err error) string {
	d.mu.Lock()
	d.loadErr = err.Error()
	d.mu.Unlock()
	return err.Error()
}

// LoadProject loads a rela project from the given directory.
// releaseLoadedProject stops the scheduler and closes the services of the
// currently-loaded project, leaving no project loaded.
//
// Called BEFORE opening the next project's store, which is the ordering that
// matters. The old order (open new, close old afterwards) worked only because
// no backend held an exclusive resource: the sqlite backend takes an exclusive
// lock on the database file at Open, so re-selecting the ALREADY-OPEN project
// — an ordinary click in the recent-projects menu — would fail against this
// process's own lock, with an error naming this very pid as the culprit.
//
// Consequence worth knowing: a load that then fails leaves NO project open
// rather than the previous one. That is the honest outcome — the alternative
// is holding a store the caller believes it replaced.
func (d *Desktop) releaseLoadedProject() {
	d.mu.Lock()
	if d.app != nil {
		// Drop it from the registry too, or /p/<id>/ keeps routing to a
		// handler whose store is about to close.
		d.registry.remove(projectID(d.app.ProjectRoot()))
	}
	prevSvc := d.svc
	prevStopScheduler := d.stopScheduler
	d.svc = nil
	d.app = nil
	d.handler = nil
	d.stopScheduler = nil
	d.mu.Unlock()

	// Scheduler first, so no in-flight tick lands on a closing store.
	if prevStopScheduler != nil {
		prevStopScheduler()
	}
	if prevSvc != nil {
		_ = prevSvc.Close()
	}
}

// LoadProject loads a project, replacing whichever project is currently open.
// Bound to the frontend, so it keeps the string-error convention.
func (d *Desktop) LoadProject(dir string) string {
	return d.loadProject(dir, false)
}

// loadProject opens dir. When keepExisting is set the currently-loaded project
// is left running, which is what opening a second window needs; otherwise it
// is released first — the ordering that matters, see releaseLoadedProject.
func (d *Desktop) loadProject(dir string, keepExisting bool) string {
	fs, projCtx, err := discoverProject(dir)
	if err != nil {
		return d.failLoad(err)
	}

	// Check if data-entry.yaml exists
	configPath := filepath.Join(dir, dataentry.ConfigFile)
	if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
		// Store pending setup state
		d.mu.Lock()
		d.pendingSetupDir = dir
		d.pendingSetupFS = fs
		d.pendingSetupPaths = projCtx
		d.loadErr = ""
		d.mu.Unlock()
		return "needs_setup"
	}

	// Opening a project in a NEW window must not close the one already open;
	// replacing the current project must. Both share the load below.
	if !keepExisting {
		d.releaseLoadedProject()
	}

	auditSink, auditErr := audit.NewFilesystem(filepath.Join(projCtx.CacheDir, "audit"))
	if auditErr != nil {
		d.mu.Lock()
		d.loadErr = auditErr.Error()
		d.mu.Unlock()
		return auditErr.Error()
	}
	svc, svcErr := appbuild.New(appbuild.Config{
		FS:           fs,
		Paths:        projCtx,
		ScriptEngine: script.NewEngine(),
		Audit:        auditSink,
	})
	if svcErr != nil {
		d.mu.Lock()
		d.loadErr = svcErr.Error()
		d.mu.Unlock()
		return svcErr.Error()
	}

	fieldResolver, err := dataentry.ResolverFromServices(svc)
	if err != nil {
		return d.failLoad(err)
	}

	app, err := dataentry.NewApp(
		fs, projCtx, svc.Meta(), svc.Store(), svc.Versions(),
		svc.EntityManager(), svc.Searcher(), svc.VisibleSearcher(), svc.ACL(),
		fieldResolver,
		svc.Audit(),
		svc.State(),
		// Wails asset server, no network listener: the server host IS the
		// user's machine, so commands run ungated like any local desktop app.
		dataentry.UngatedCommandAuthorizer(),
	)
	if err != nil {
		return d.failLoad(err)
	}
	// Twins: owned fields render read-only and the `_twins` read-out answers,
	// as on rela-server. Nil (no pact declared) leaves both off.
	dataentry.SetTwins(app, appbuild.Twins(svc))
	handler := app.NewRouter()

	// Start background scheduler for the new project.
	schedCtx, schedCancel := context.WithCancel(context.Background())

	d.mu.Lock()
	// The previous project's scheduler and services were already stopped and
	// closed above, before the new store was opened — see the comment there.
	d.svc = svc
	d.app = app
	d.handler = handler
	d.loadErr = ""
	d.pendingSetupDir = ""
	d.pendingSetupFS = nil
	d.pendingSetupPaths = nil
	d.stopScheduler = schedCancel
	d.mu.Unlock()

	// Register it so /p/<id>/ can reach it, and so a later Open Project on the
	// same directory can focus this project rather than loading a second copy.
	d.registry.add(&loadedProject{
		id:            projectID(app.ProjectRoot()),
		root:          app.ProjectRoot(),
		name:          app.ProjectName(),
		app:           app,
		svc:           svc,
		handler:       handler,
		stopScheduler: schedCancel,
	})

	scheduler.StartBackground(schedCtx, svc, slog.Default())

	if d.win != nil {
		d.win.SetTitle(app.ProjectName())
	}

	// Update preferences with successfully opened project.
	d.prefs.AddRecentProject(app.ProjectRoot(), app.ProjectName())
	if saveErr := d.prefs.Save(); saveErr != nil {
		slog.Warn("could not save preferences", "error", saveErr)
	}
	d.refreshMenu()

	return ""
}

// NeedsSetup returns true if a project needs data-entry.yaml setup.
func (d *Desktop) NeedsSetup() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.pendingSetupDir != ""
}

// GetSetupInfo returns information about the project needing setup.
func (d *Desktop) GetSetupInfo() map[string]any {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.pendingSetupPaths == nil {
		return map[string]any{"error": "No project pending setup"}
	}

	loader := metamodel.NewFSLoader(d.pendingSetupFS, d.pendingSetupPaths.SchemaPath)
	meta, _, err := loader.Load(context.Background())
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("Failed to load metamodel: %v", err)}
	}

	entityTypes := make([]string, 0, len(meta.Entities))
	for name := range meta.Entities {
		entityTypes = append(entityTypes, name)
	}

	return map[string]any{
		"path":         d.pendingSetupDir,
		"entity_types": entityTypes,
	}
}

// GenerateDataEntryConfig creates a data-entry.yaml from the metamodel.
func (d *Desktop) GenerateDataEntryConfig(appName string) string {
	d.mu.Lock()
	fs := d.pendingSetupFS
	paths := d.pendingSetupPaths
	dir := d.pendingSetupDir
	d.mu.Unlock()

	if paths == nil {
		return "No project pending setup"
	}

	meta, _, err := metamodel.NewFSLoader(fs, paths.SchemaPath).Load(context.Background())
	if err != nil {
		return fmt.Sprintf("Failed to load metamodel: %v", err)
	}

	config := generateDataEntryConfig(appName, meta)
	configPath := filepath.Join(dir, dataentry.ConfigFile)

	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		return fmt.Sprintf("Failed to write config: %v", err)
	}

	// Now load the project
	d.mu.Lock()
	d.pendingSetupDir = ""
	d.pendingSetupFS = nil
	d.pendingSetupPaths = nil
	d.mu.Unlock()

	return d.LoadProject(dir)
}

// GetDefaultCloneDir returns the default directory for cloning repositories,
// or "" when there is no home directory to derive one from.
//
// The empty return is deliberate. Discarding the os.UserHomeDir error left
// homeDir == "" and returned the RELATIVE path "rela-projects", which
// containedPath then resolves against the process working directory — so the
// clone lands somewhere the user never chose. Containment still holds (it is a
// real base), which is what makes it easy to miss: the guard passes while the
// destination is wrong. Returning "" instead makes CloneProject refuse rather
// than guess, matching the reasoning on git.CloneOptions.BaseDir that a
// silently-relocated boundary is a different surprise, not a smaller one
// (TKT-S2SFTG).
func (d *Desktop) GetDefaultCloneDir() string {
	if d.prefs.CloneDir != "" {
		return d.prefs.CloneDir
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		return ""
	}
	return filepath.Join(homeDir, "rela-projects")
}

// PickCloneDirectory opens a directory picker and returns the selected path.
// coverage-ignore-start: os-fs-event: runtime.OpenDirectoryDialog needs the live Wails runtime and a native OS
// directory picker; unreachable
// in unit tests
func (d *Desktop) PickCloneDirectory() string {
	dir, err := d.pickDirectory("Select Clone Destination", d.GetDefaultCloneDir())
	if err != nil || dir == "" {
		return ""
	}
	// Save as new default
	d.prefs.CloneDir = dir
	_ = d.prefs.Save()
	return dir
}

// coverage-ignore-end

// CloneProject clones a git repository and scans for rela projects.
// Returns a JSON response with status and any discovered projects.
func (d *Desktop) CloneProject(repoURL, baseDir string) map[string]any {
	if !git.IsValidRepoURL(repoURL) {
		return map[string]any{"error": "Invalid repository URL. Use HTTPS format: https://github.com/user/repo"}
	}

	// Use saved token if available
	token := d.prefs.GitHubToken

	// Determine target directory
	repoName := git.ExtractRepoName(repoURL)
	if repoName == "" {
		return map[string]any{"error": "Could not determine repository name from URL"}
	}
	if baseDir == "" {
		baseDir = d.GetDefaultCloneDir()
	}
	if baseDir == "" {
		return map[string]any{"error": "Could not determine a clone directory. Choose one explicitly."}
	}
	targetDir := filepath.Join(baseDir, repoName)

	// repoName is the URL's last path segment, so a hostile URL can make it
	// ".." — BaseDir makes Clone reject a targetDir that escapes baseDir.
	// coverage-ignore-start: os-fs-event: git.Clone (go-git PlainClone) performs real network I/O against a remote repo
	// with no injectable
	// transport here; the success path and downstream project handling are only reachable after an actual clone
	err := git.Clone(git.CloneOptions{
		URL:     repoURL,
		Path:    targetDir,
		BaseDir: baseDir,
		Token:   token,
	})
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("Clone failed: %v", err)}
	}

	// Cached only AFTER Clone validates containment (TKT-S2SFTG). Caching
	// before meant a REJECTED traversal still left the escaping path in app
	// state, where InitRelaProject would later MkdirAll it — so containment
	// stopped the clone and the poisoned value walked around it.
	d.mu.Lock()
	d.lastCloneDir = targetDir
	d.mu.Unlock()

	// Scan for rela projects (directories containing a schema file)
	projects := scanForRelaProjects(targetDir)

	if len(projects) == 0 {
		return map[string]any{
			"status":    "no_projects",
			"clone_dir": targetDir,
		}
	}

	if len(projects) == 1 {
		// Single project found - open it directly
		if errMsg := d.LoadProject(projects[0]); errMsg != "" {
			return map[string]any{"error": errMsg}
		}
		return map[string]any{"status": "opened"}
	}

	// Multiple projects found - return list for user to pick
	// Convert to relative paths for display
	relPaths := make([]string, len(projects))
	for i, p := range projects {
		rel, _ := filepath.Rel(targetDir, p)
		if rel == "." {
			rel = "(root)"
		}
		relPaths[i] = rel
	}

	return map[string]any{
		"status":    "multiple",
		"projects":  relPaths,
		"clone_dir": targetDir,
	}
	// coverage-ignore-end
}

// OpenClonedProject opens a specific project from a recently cloned repository.
func (d *Desktop) OpenClonedProject(subfolder string) string {
	d.mu.RLock()
	cloneDir := d.lastCloneDir
	d.mu.RUnlock()

	if cloneDir == "" {
		return "No recent clone. Please clone a repository first."
	}

	projectDir := cloneDir
	if subfolder != "" && subfolder != "(root)" {
		projectDir = filepath.Join(cloneDir, subfolder)
	}

	return d.LoadProject(projectDir)
}

// InitRelaProject initializes a new rela project in the cloned repository.
func (d *Desktop) InitRelaProject(subfolder string) string {
	d.mu.RLock()
	cloneDir := d.lastCloneDir
	d.mu.RUnlock()

	if cloneDir == "" {
		return "No recent clone. Please clone a repository first."
	}

	projectDir := cloneDir
	if subfolder != "" {
		projectDir = filepath.Join(cloneDir, subfolder)
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			return fmt.Sprintf("Failed to create directory: %v", err)
		}
	}

	// Refuse if a schema already exists under EITHER name — writing a default
	// one next to an operator's real (legacy-named) schema would leave the
	// project loading the empty default, since discovery prefers the new name.
	if existing, _, found := project.SchemaFileAt(projectDir, storage.NewSafeFS(storage.NewOsFS())); found {
		return fmt.Sprintf("Directory is already a rela project (%s exists)", filepath.Base(existing))
	}

	// Create minimal schema.yaml
	schemaPath := filepath.Join(projectDir, project.SchemaFile)
	minimalSchema := `# Rela Project Configuration
# See https://github.com/Sourcehaven-BV/rela for documentation

entity_types: {}
relation_types: {}
`
	if err := os.WriteFile(schemaPath, []byte(minimalSchema), 0o644); err != nil {
		return fmt.Sprintf("Failed to create %s: %v", project.SchemaFile, err)
	}

	// Create entities directory
	entitiesDir := filepath.Join(projectDir, "entities")
	if err := os.MkdirAll(entitiesDir, 0o755); err != nil {
		return fmt.Sprintf("Failed to create entities directory: %v", err)
	}

	return d.LoadProject(projectDir)
}

// scanForRelaProjects recursively finds directories containing a schema file.
func scanForRelaProjects(root string) []string {
	var projects []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil {
			return filepath.SkipDir
		}
		// Skip hidden directories and common non-project dirs
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		// Match the legacy name too, so pre-rename projects keep showing up in
		// the picker. Only the new name appends when both are present,
		// otherwise such a directory would be listed twice.
		switch info.Name() {
		case project.SchemaFile:
			projects = append(projects, filepath.Dir(path))
		case project.LegacySchemaFile:
			dir := filepath.Dir(path)
			if _, err := os.Stat(filepath.Join(dir, project.SchemaFile)); err != nil {
				projects = append(projects, dir)
			}
		}
		return nil
	})
	return projects
}

// StartGitHubAuth initiates the GitHub OAuth device flow.
// Returns the user code and verification URL, or an error string.
func (d *Desktop) StartGitHubAuth() map[string]string {
	clientID := GitHubClientID
	if clientID == "" {
		clientID = os.Getenv("RELA_GITHUB_CLIENT_ID")
	}
	if clientID == "" {
		return map[string]string{"error": "GitHub OAuth not configured. Set RELA_GITHUB_CLIENT_ID environment variable."}
	}

	// coverage-ignore-start: os-fs-event: RequestDeviceCode makes a live HTTPS request to the hardcoded
	// github.com/login/device/code endpoint (no
	// injectable base URL); unreachable without real network
	oauth := git.NewOAuth(git.OAuthConfig{ClientID: clientID})
	resp, err := oauth.RequestDeviceCode(context.Background())
	if err != nil {
		return map[string]string{"error": fmt.Sprintf("Failed to start auth: %v", err)}
	}

	d.mu.Lock()
	d.cloneAuth = &cloneAuthState{
		deviceCode: resp.DeviceCode,
		userCode:   resp.UserCode,
		verifyURL:  resp.VerificationURI,
		expiresIn:  resp.ExpiresIn,
		interval:   resp.Interval,
	}
	d.mu.Unlock()

	return map[string]string{
		"user_code":        resp.UserCode,
		"verification_url": resp.VerificationURI,
	}
	// coverage-ignore-end
}

// CompleteGitHubAuth waits for the user to authorize and stores the token.
// Returns empty string on success, error message on failure.
func (d *Desktop) CompleteGitHubAuth() string {
	d.mu.RLock()
	auth := d.cloneAuth
	d.mu.RUnlock()

	if auth == nil {
		return "No auth in progress. Call StartGitHubAuth first."
	}

	// coverage-ignore-start: os-fs-event: WaitForAuthorization polls the hardcoded github.com/login/oauth/access_token
	// endpoint (no injectable
	// base URL) until the user authorizes; unreachable without real network and human interaction
	clientID := GitHubClientID
	if clientID == "" {
		clientID = os.Getenv("RELA_GITHUB_CLIENT_ID")
	}

	oauth := git.NewOAuth(git.OAuthConfig{ClientID: clientID})

	const authTimeout = 5 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), authTimeout)
	defer cancel()

	token, err := oauth.WaitForAuthorization(ctx, auth.deviceCode, auth.interval)
	if err != nil {
		if errors.Is(err, git.ErrAccessDenied) {
			return "Authorization denied by user"
		}
		if errors.Is(err, git.ErrExpiredToken) {
			return "Authorization expired. Please try again."
		}
		return fmt.Sprintf("Authorization failed: %v", err)
	}

	// Store token
	d.prefs.GitHubToken = token.AccessToken
	if err := d.prefs.Save(); err != nil {
		slog.Warn("could not save token", "error", err)
	}

	d.mu.Lock()
	d.cloneAuth = nil
	d.mu.Unlock()

	return ""
	// coverage-ignore-end
}

// HasGitHubToken returns true if a GitHub token is stored.
func (d *Desktop) HasGitHubToken() bool {
	return d.prefs.GitHubToken != ""
}

// ClearGitHubToken removes the stored GitHub token.
func (d *Desktop) ClearGitHubToken() {
	d.prefs.GitHubToken = ""
	_ = d.prefs.Save()
}

// ServeHTTP dispatches to the loaded app router or the welcome page.
func (d *Desktop) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	h := d.handler
	loadErr := d.loadErr
	d.mu.RUnlock()

	// The shell's own endpoints answer before any project's router, and from
	// any window: a switcher in a prefixed window still asks for the list.
	if p := r.URL.Path; p == projectsPath || strings.HasSuffix(p, projectsPath) {
		d.serveProjects(w, r)
		return
	}

	// A /p/<id>/ request names its project explicitly; anything else is served
	// by the active one, which is what the welcome page and any window opened
	// without a prefix expect.
	base := ""
	if id, rest, ok := splitProjectPath(r.URL.Path); ok {
		p := d.registry.get(id)
		if p == nil {
			// An unknown id means a stale window or a hand-typed URL. Serving
			// the welcome page is more useful than a bare 404: the project may
			// simply have been closed.
			serveWelcomePage(w, d.prefs, "That project is not open.")
			return
		}
		h = p.handler
		base = projectPrefix + id
		// Route within the project as if it were mounted at the root; the
		// project's own mux knows nothing about the prefix.
		r = r.Clone(r.Context())
		r.URL.Path = rest
	}

	if h != nil {
		// Buffer HTML so the multi-window script and base tag can be appended;
		// everything else, including SSE, streams through untouched.
		inj := &htmlInjector{ResponseWriter: w, base: base}
		h.ServeHTTP(inj, r)
		inj.finish()
		return
	}

	serveWelcomePage(w, d.prefs, loadErr)
}

// openProjectFromMenu handles File > Open Project from the native menu bar.
// coverage-ignore-func: menu callback - requires Wails runtime
func (d *Desktop) openProjectFromMenu(_ *application.Context) {
	dir, err := d.pickDirectory("Open Rela Project", "")
	if err != nil || dir == "" {
		return
	}
	root := projectRootOf(dir)

	// Already open? Bring its window forward instead of loading it twice.
	if p := d.registry.byRoot(root); p != nil {
		if errMsg := d.OpenWindow(projectPrefix+p.id+"/", p.name); errMsg != "" {
			d.errorDialog("Failed to open window", errMsg)
		}
		return
	}

	// Keep the current project running: Open Project adds a project, it does
	// not replace the one the user is looking at.
	if errMsg := d.loadProject(root, true); errMsg != "" {
		d.errorDialog("Failed to open project", errMsg)
		return
	}

	p := d.registry.byRoot(root)
	if p == nil {
		// needs_setup returns "" without registering; fall back to reloading
		// so the current window shows the setup prompt.
		d.reloadWindow()
		return
	}
	if errMsg := d.OpenWindow(projectPrefix+p.id+"/", p.name); errMsg != "" {
		d.errorDialog("Failed to open window", errMsg)
	}
}

// cloneFromGitMenu handles File > Clone from Git from the native menu bar.
// It navigates to the welcome page and triggers the clone dialog.
// coverage-ignore-func: menu callback - requires Wails runtime
func (d *Desktop) cloneFromGitMenu(_ *application.Context) {
	// Unload current project to show welcome page
	d.mu.Lock()
	d.handler = nil
	d.loadErr = ""
	d.mu.Unlock()

	// Reload app to show welcome page, then emit event to show clone dialog
	d.reloadWindow()

	// Give the page time to load before emitting the event.
	// TODO(wails3): replace this sleep with a page-ready handshake.
	go func() {
		time.Sleep(100 * time.Millisecond)
		if d.wails != nil {
			d.wails.Event.Emit("show-clone-dialog")
		}
	}()
}

// showAbout displays a dialog with version and build information.
// coverage-ignore-func: menu callback - requires Wails runtime
func (d *Desktop) showAbout(_ *application.Context) {
	if d.wails == nil {
		return
	}
	d.wails.Dialog.Info().
		SetTitle("About Rela Desktop").
		SetMessage(fmt.Sprintf("Rela Desktop\nVersion %s\n\n%s/%s", Version, goruntime.GOOS, goruntime.GOARCH)).
		Show()
}

// buildAppMenu constructs the application menu bar including recent projects.
//
// Wails v3 supplies cross-platform roles (AppMenu/EditMenu carry the
// platform-correct items), so the v2 GOOS branching is no longer needed:
// on non-macOS the role expands to nothing. Accelerator "CmdOrCtrl+o"
// maps to Command on macOS and Control elsewhere.
func (d *Desktop) buildAppMenu() *application.Menu {
	appMenu := application.NewMenu()

	if goruntime.GOOS == "darwin" {
		appMenu.AddRole(application.AppMenu)
	}

	fileMenu := appMenu.AddSubmenu("File")
	fileMenu.Add("New Window").SetAccelerator("CmdOrCtrl+n").OnClick(func(*application.Context) {
		if errMsg := d.OpenWindow("/", ""); errMsg != "" {
			slog.Warn("could not open window", "error", errMsg)
		}
	})
	fileMenu.AddSeparator()
	fileMenu.Add("Open Project...").SetAccelerator("CmdOrCtrl+o").OnClick(d.openProjectFromMenu)
	fileMenu.Add("Clone from Git...").SetAccelerator("CmdOrCtrl+shift+o").OnClick(d.cloneFromGitMenu)
	fileMenu.AddSeparator()

	// Recent Projects submenu
	if len(d.prefs.RecentProjects) > 0 {
		recentMenu := fileMenu.AddSubmenu("Recent Projects")
		for _, rp := range d.prefs.RecentProjects {
			proj := rp // capture for closure
			label := proj.Name
			if label == "" {
				label = filepath.Base(proj.Path)
			}
			recentMenu.Add(label).OnClick(func(_ *application.Context) {
				if errMsg := d.LoadProject(proj.Path); errMsg != "" {
					d.errorDialog("Failed to open project", errMsg)
					return
				}
				d.reloadWindow()
			})
		}
		recentMenu.AddSeparator()
		recentMenu.Add("Clear Recent Projects").OnClick(func(_ *application.Context) {
			d.prefs.ClearRecentProjects()
			if err := d.prefs.Save(); err != nil {
				slog.Warn("could not save preferences", "error", err)
			}
			d.refreshMenu()
		})
		fileMenu.AddSeparator()
	}

	if goruntime.GOOS == "darwin" {
		// Close the window rather than quitting, matching v2's Cmd+W.
		fileMenu.Add("Close Window").SetAccelerator("CmdOrCtrl+w").OnClick(func(_ *application.Context) {
			if d.win != nil {
				d.win.Close()
			}
		})
		appMenu.AddRole(application.EditMenu)
	} else {
		fileMenu.Add("Quit").SetAccelerator("CmdOrCtrl+q").OnClick(func(_ *application.Context) {
			if d.wails != nil {
				d.wails.Quit()
			}
		})
		helpMenu := appMenu.AddSubmenu("Help")
		helpMenu.Add("About Rela Desktop").OnClick(d.showAbout)
	}

	return appMenu
}

// refreshMenu rebuilds and applies the application menu.
func (d *Desktop) refreshMenu() {
	// The native menu does not exist until app.Run() has built it. Setting it
	// earlier is not merely a no-op: MenuManager.Set panics on a nil menuImpl
	// (application_darwin.go setApplicationMenu). ServiceStartup runs before
	// that point, so this must stay guarded on the post-Run flag rather than
	// on d.wails, which is assigned in main.
	if d.wails == nil || !d.menuReady.Load() {
		return
	}
	// coverage-ignore-start: os-fs-event: runtime.MenuSet/UpdateApplicationMenu need the live Wails runtime; only
	// reachable once d.ctx is a real
	// Wails context
	m := d.buildAppMenu()
	d.wails.Menu.Set(m)
	m.Update()
	// coverage-ignore-end
}

// coverage-ignore-func: main function - entry point
// coverage-ignore-start: main-or-wiring: process entry point — flag parsing, DI wiring, and wails.Run event loop;
// exercised only at real
// process start
func main() {
	projectDir := flag.String("project", ".", "Path to the rela project directory")
	verbose := flag.Bool("verbose", false, "Verbose (debug) logging")
	quiet := flag.Bool("quiet", false, "Quiet (warn-only) logging")
	flag.Parse()

	configureLogging(*verbose, *quiet)

	// Fail fast if the embedded SPA is missing (BUG-W144 class regression).
	if err := dataentry.CheckEmbeddedSPA(); err != nil {
		slog.Error("embedded SPA check failed", "error", err)
		os.Exit(1)
	}

	// Load desktop preferences.
	prefs, err := desktop.Load()
	if err != nil {
		slog.Warn("could not load preferences", "error", err)
		prefs = &desktop.Preferences{}
	}

	d := &Desktop{prefs: prefs, registry: newProjectRegistry()}

	// Which project to open — resolved now, but NOT loaded yet. Opening the
	// store here would mean a redundant second instance takes the sqlite and
	// search locks before app.Run() discovers it should exit, which is the
	// contention SingleInstance exists to prevent. The load happens in
	// ServiceStartup, which only runs once this process owns the lock.
	d.pendingProject = resolveProjectDir(*projectDir, prefs)

	// The window is created before the project loads, so it opens with a
	// generic title; ServiceStartup renames it once the name is known.
	title := "Rela Desktop"

	app := application.New(application.Options{
		Name: "Rela Desktop",
		// The whole Go API + SPA is served through this one handler, exactly
		// as under v2. Verified streaming (SSE) works through it.
		Assets: application.AssetOptions{Handler: d},
		Services: []application.Service{
			application.NewService(d),
		},
		// v2 had no shutdown hook, so the scheduler and services leaked on
		// quit. ServiceShutdown now releases them.
		OnShutdown: func() { slog.Info("shutting down") },
		// Lets Finder hand .rela bundles to this app; see onOpenedWithFile.
		FileAssociations: []string{".rela"},
		// One instance only: see onSecondInstanceLaunch.
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID:               singleInstanceID,
			EncryptionKey:          singleInstanceKey(),
			OnSecondInstanceLaunch: d.onSecondInstanceLaunch,
		},
	})
	d.wails = app

	winOpts := application.WebviewWindowOptions{
		Title:  title,
		Width:  defaultWindowWidth,
		Height: defaultWindowHeight,
	}
	// Restore the previous geometry when we have usable saved state. X/Y are
	// only honored with InitialPosition set; the default centers the window.
	if ws := prefs.Window; ws.Valid() {
		winOpts.Width, winOpts.Height = ws.Width, ws.Height
		winOpts.X, winOpts.Y = ws.X, ws.Y
		winOpts.InitialPosition = application.WindowXY
	}
	d.win = app.Window.NewWithOptions(winOpts)

	// Finder handing us a .rela bundle. Registered before Run so a cold launch
	// (where the event arrives during startup) is not missed.
	app.Event.OnApplicationEvent(events.Common.ApplicationOpenedWithFile,
		func(e *application.ApplicationEvent) {
			d.onOpenedWithFile(e.Context().Filename())
		})

	// Persist geometry on close. Reading it after the window is gone returns
	// zeroes, so this must run while the window still exists.
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		d.win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
			d.saveWindowState()
		})
	})

	// Wails calls this on the main thread while initializing, which is the
	// only context where the native menu can be built. Any refreshMenu before
	// this point is suppressed by menuReady; the ServiceStartup load happens
	// first, so the menu built here already reflects it.
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		d.menuReady.Store(true)
		d.refreshMenu()
	})

	app.Menu.Set(d.buildAppMenu())

	if err := app.Run(); err != nil {
		slog.Error("wails error", "error", err)
		os.Exit(1)
	}
}

// coverage-ignore-end

// configureLogging sets the default slog logger based on verbose/quiet flags.
func configureLogging(verbose, quiet bool) {
	level := slog.LevelInfo
	switch {
	case verbose:
		level = slog.LevelDebug
	case quiet:
		level = slog.LevelWarn
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

// resolveProjectDir determines which project directory to load at startup.
// Priority: explicit -project flag > current dir if rela project > last project from preferences.
func resolveProjectDir(flagValue string, prefs *desktop.Preferences) string {
	// Explicit flag or current directory is a rela project.
	if flagValue != "." || isRelaProject(flagValue) {
		return flagValue
	}
	// Fall back to last project from preferences.
	if prefs.LastProject != "" && isRelaProject(prefs.LastProject) {
		return prefs.LastProject
	}
	return ""
}

// projectRootOf normalises a picked path to a project root. The open panel
// accepts files as well as directories (a .rela bundle is a package, so it is
// classified as a file), which means the selection can be an ordinary file
// inside a project — pointing at its parent is what the user meant.
// A path that is already a project, or is nothing recognizable, is returned
// unchanged so LoadProject reports the real error.
func projectRootOf(path string) string {
	if isRelaProject(path) {
		return path
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		if parent := filepath.Dir(path); isRelaProject(parent) {
			return parent
		}
	}
	return path
}

// isRelaProject checks if the directory looks like a rela project, accepting
// either schema file name.
func isRelaProject(dir string) bool {
	_, _, found := project.SchemaFileAt(dir, storage.NewSafeFS(storage.NewOsFS()))
	return found
}

// discoverProject returns the filesystem and project context for the
// project rooted at projectDir.
func discoverProject(projectDir string) (storage.FS, *project.Context, error) {
	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, nil, err
	}
	fs := storage.NewSafeFS(storage.NewOsFS())
	projCtx, err := project.Discover(absDir, fs)
	if err != nil {
		return nil, nil, fmt.Errorf("discovering project: %w", err)
	}
	// The desktop app opens projects without going through appbuild.Discover,
	// so it needs its own call for the notice to reach desktop-only operators.
	project.WarnIfLegacySchema(projCtx)
	return fs, projCtx, nil
}

// generateDataEntryConfig creates a minimal data-entry.yaml from the metamodel.
// Builds a typed structure and marshals via yaml.v3 so every string value is
// correctly escaped regardless of the characters a user's metamodel contains.
func generateDataEntryConfig(appName string, meta *metamodel.Metamodel) string {
	entityTypes := make([]string, 0, len(meta.Entities))
	for name := range meta.Entities {
		entityTypes = append(entityTypes, name)
	}
	sort.Strings(entityTypes)

	forms := yaml.Node{Kind: yaml.MappingNode}
	lists := yaml.Node{Kind: yaml.MappingNode}
	navigation := yaml.Node{Kind: yaml.SequenceNode}

	const maxColumns = 4
	for _, typeName := range entityTypes {
		entDef := meta.Entities[typeName]
		formID := strings.ReplaceAll(typeName, "-", "_")
		listID := formID + "s"

		propNames := make([]string, 0, len(entDef.Properties))
		for name := range entDef.Properties {
			propNames = append(propNames, name)
		}
		sort.Strings(propNames)

		// No generated field/column labels: a label is authored, never derived
		// (DEC-6C1NAA). Emitting titleCase(property) here would bake an English
		// orthographic guess into every new project's config. An unlabelled
		// field renders its raw property name until the user writes a label in
		// their own language. Titles and nav DO get a label because entDef.Label
		// is a required, user-authored metamodel field — not a derivation. The
		// loader enforces that it is set, but this generator can also be handed
		// an in-memory metamodel, so fall back to the raw type name rather than
		// emitting an empty title (or a bare "s" for the plural).
		typeLabel := entDef.Label
		if typeLabel == "" {
			typeLabel = typeName
		}
		typeLabelPlural := entDef.LabelPlural
		if typeLabelPlural == "" {
			typeLabelPlural = typeLabel + "s"
		}

		fields := make([]map[string]string, 0, len(propNames))
		for _, propName := range propNames {
			fields = append(fields, map[string]string{"property": propName})
		}
		appendMapEntry(&forms, formID, map[string]any{
			"entity_type": typeName,
			"title":       typeLabel,
			"fields":      fields,
		})

		columns := make([]map[string]string, 0, maxColumns)
		for i, propName := range propNames {
			if i >= maxColumns {
				break
			}
			columns = append(columns, map[string]string{"property": propName})
		}
		appendMapEntry(&lists, listID, map[string]any{
			"entity_type": typeName,
			"title":       typeLabelPlural,
			"columns":     columns,
			"create_form": formID,
			"edit_form":   formID,
		})

		navItem := yaml.Node{Kind: yaml.MappingNode}
		appendMapEntry(&navItem, "label", typeLabelPlural)
		appendMapEntry(&navItem, "list", listID)
		navigation.Content = append(navigation.Content, &navItem)
	}

	root := yaml.Node{Kind: yaml.MappingNode}
	appendMapEntry(&root, "version", "1")
	appendMapEntry(&root, "app", map[string]any{
		"name":        appName,
		"description": "Generated data entry configuration",
	})
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "forms"}, &forms,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "lists"}, &lists,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "navigation"}, &navigation,
	)

	out, err := yaml.Marshal(&root)
	if err != nil {
		// coverage-ignore-start: defensive: yaml.Marshal cannot fail on this locally-built node graph of
		// scalar/mapping/sequence nodes
		// yaml.Marshal on a manually-built node graph shouldn't fail; surface
		// as a comment so the generated file is still valid YAML.
		return fmt.Sprintf("# failed to generate config: %v\n", err)
		// coverage-ignore-end
	}
	return string(out)
}

// appendMapEntry adds a key/value pair to a yaml MappingNode. Value may be any
// Go value yaml.Marshal accepts (string, map, slice, etc.) or a pre-built
// *yaml.Node.
func appendMapEntry(m *yaml.Node, key string, value any) {
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key}
	var valNode *yaml.Node
	if n, ok := value.(*yaml.Node); ok {
		valNode = n
	} else {
		valNode = &yaml.Node{}
		_ = valNode.Encode(value)
	}
	m.Content = append(m.Content, keyNode, valNode)
}
