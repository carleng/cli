package codespace

// This file defines the 'gh cs ssh' and 'gh cs cp' subcommands.

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/codespaces"
	"github.com/cli/cli/v2/internal/codespaces/api"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/liveshare"
	"github.com/hashicorp/go-multierror"
	"github.com/spf13/cobra"
)

type sshOptions struct {
	codespace  string
	profile    string
	serverPort int
	debug      bool
	debugFile  string
	stdio      bool
	scpArgs    []string // scp arguments, for 'cs cp' (nil for 'cs ssh')
}

func newSSHCmd(app *App) *cobra.Command {
	var opts sshOptions

	sshCmd := &cobra.Command{
		Use:   "ssh [<flags>...] [-- <ssh-flags>...] [<command>]",
		Short: "SSH into a codespace",
		PreRunE: func(c *cobra.Command, args []string) error {
			if opts.stdio {
				if opts.codespace == "" {
					return errors.New("`--stdio` requires explicit `--codespace`")
				}
				if opts.serverPort != 0 {
					return errors.New("cannot use `--stdio` with `--server-port`")
				}
				if opts.profile != "" {
					return errors.New("cannot use `--stdio` with `--profile`")
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.SSH(cmd.Context(), args, opts)
		},
		DisableFlagsInUseLine: true,
	}

	sshCmd.Flags().StringVarP(&opts.profile, "profile", "", "", "Name of the SSH profile to use")
	sshCmd.Flags().IntVarP(&opts.serverPort, "server-port", "", 0, "SSH server port number (0 => pick unused)")
	sshCmd.Flags().StringVarP(&opts.codespace, "codespace", "c", "", "Name of the codespace")
	sshCmd.Flags().BoolVarP(&opts.debug, "debug", "d", false, "Log debug data to a file")
	sshCmd.Flags().StringVarP(&opts.debugFile, "debug-file", "", "", "Path of the file log to")
	sshCmd.Flags().BoolVarP(&opts.stdio, "stdio", "", false, "Proxy sshd connection to stdio")

	sshCmd.AddCommand(newConfigCmd(app))

	return sshCmd
}

// SSH opens an ssh session or runs an ssh command in a codespace.
func (a *App) SSH(ctx context.Context, sshArgs []string, opts sshOptions) (err error) {
	// Ensure all child tasks (e.g. port forwarding) terminate before return.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	liveshareLogger := noopLogger()
	if opts.debug {
		debugLogger, err := newFileLogger(opts.debugFile)
		if err != nil {
			return fmt.Errorf("error creating debug logger: %w", err)
		}
		defer safeClose(debugLogger, &err)

		liveshareLogger = debugLogger.Logger
		a.errLogger.Printf("Debug file located at: %s", debugLogger.Name())
	}

	session, err := openSshSession(ctx, a, opts.codespace, liveshareLogger)
	if err != nil {
		return fmt.Errorf("error connecting to codespace: %w", err)
	}
	defer safeClose(session, &err)

	a.StartProgressIndicatorWithLabel("Fetching SSH Details")
	remoteSSHServerPort, sshUser, err := session.StartSSHServer(ctx)
	a.StopProgressIndicator()
	if err != nil {
		return fmt.Errorf("error getting ssh server details: %w", err)
	}

	if opts.stdio {
		fwd := liveshare.NewPortForwarder(session, "sshd", remoteSSHServerPort, true)
		stdio := newCombinedReadWriteCloser(os.Stdin, os.Stdout)
		err := fwd.Forward(ctx, stdio) // always non-nil
		return fmt.Errorf("tunnel closed: %w", err)
	}

	localSSHServerPort := opts.serverPort
	usingCustomPort := localSSHServerPort != 0 // suppress log of command line in Shell

	// Ensure local port is listening before client (Shell) connects.
	// Unless the user specifies a server port, localSSHServerPort is 0
	// and thus the client will pick a random port.
	listen, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localSSHServerPort))
	if err != nil {
		return err
	}
	defer listen.Close()
	localSSHServerPort = listen.Addr().(*net.TCPAddr).Port

	connectDestination := opts.profile
	if connectDestination == "" {
		connectDestination = fmt.Sprintf("%s@localhost", sshUser)
	}

	tunnelClosed := make(chan error, 1)
	go func() {
		fwd := liveshare.NewPortForwarder(session, "sshd", remoteSSHServerPort, true)
		tunnelClosed <- fwd.ForwardToListener(ctx, listen) // always non-nil
	}()

	shellClosed := make(chan error, 1)
	go func() {
		var err error
		if opts.scpArgs != nil {
			err = codespaces.Copy(ctx, opts.scpArgs, localSSHServerPort, connectDestination)
		} else {
			err = codespaces.Shell(ctx, a.errLogger, sshArgs, localSSHServerPort, connectDestination, usingCustomPort)
		}
		shellClosed <- err
	}()

	select {
	case err := <-tunnelClosed:
		return fmt.Errorf("tunnel closed: %w", err)
	case err := <-shellClosed:
		if err != nil {
			return fmt.Errorf("shell closed: %w", err)
		}
		return nil // success
	}
}

func (a *App) ListOpensshConfig(ctx context.Context, opts configOptions) error {
	// Ensure all child tasks (e.g. port forwarding) terminate before return.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var err error
	var codespaces []*api.Codespace
	if opts.codespace == "" {
		a.StartProgressIndicatorWithLabel("Fetching codespaces")
		codespaces, err = a.apiClient.ListCodespaces(ctx, -1)
		a.StopProgressIndicator()
	} else {
		var codespace *api.Codespace
		codespace, err = getOrChooseCodespace(ctx, a.apiClient, opts.codespace)
		codespaces = []*api.Codespace{codespace}
	}
	if err != nil {
		return fmt.Errorf("error getting codespace info: %w", err)
	}

	t, err := template.New("ssh_config").Parse(`Host cs.{{.Name}}.{{.EscapedRef}}
	User {{.SshUser}}
	ProxyCommand {{.GHExec}} cs ssh -c {{.Name}} --stdio
	UserKnownHostsFile=/dev/null
	StrictHostKeyChecking no
	LogLevel quiet
	ControlMaster auto

`)
	if err != nil {
		return fmt.Errorf("error formatting template: %w", err)
	}

	ghexec, err := os.Executable()
	if err != nil {
		return err
	}

	sshUsers := map[string]string{}

	for _, cs := range codespaces {

		if cs.State != "Available" {
			fmt.Fprintf(os.Stderr, "skipping unavailable codespace %s: %s\n", cs.Name, cs.State)
			continue
		}

		var sshUser string
		var ok bool

		if sshUser, ok = sshUsers[cs.Repository.FullName]; !ok {
			session, err := openSshSession(ctx, a, cs.Name, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error connecting to codespace: %v\n", err)
				continue
			}
			defer session.Close()

			a.StartProgressIndicatorWithLabel(fmt.Sprintf("Fetching SSH Details for %s", cs.Name))
			_, sshUser, err = session.StartSSHServer(ctx)
			a.StopProgressIndicator()
			if err != nil {
				fmt.Fprintf(os.Stderr, "error getting ssh server details: %v", err)
				continue
			}
			sshUsers[cs.Repository.FullName] = sshUser
		}

		conf := codespaceSshConfig{
			Name:       cs.Name,
			EscapedRef: strings.ReplaceAll(cs.GitStatus.Ref, "/", "-"),
			SshUser:    sshUser,
			GHExec:     ghexec,
		}
		if err := t.Execute(a.io.Out, conf); err != nil {
			return err
		}
	}

	return nil
}

type codespaceSshConfig struct {
	Name       string
	EscapedRef string
	SshUser    string
	GHExec     string
}

func openSshSession(ctx context.Context, a *App, csName string, liveshareLogger *log.Logger) (*liveshare.Session, error) {
	codespace, err := getOrChooseCodespace(ctx, a.apiClient, csName)
	if err != nil {
		return nil, fmt.Errorf("get or choose codespace: %w", err)
	}

	// TODO(josebalius): We can fetch the user in parallel to everything else
	// we should convert this call and others to happen async
	user, err := a.apiClient.GetUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("error getting user: %w", err)
	}

	authkeys := make(chan error, 1)
	go func() {
		authkeys <- checkAuthorizedKeys(ctx, a.apiClient, user.Login)
	}()

	if liveshareLogger == nil {
		liveshareLogger = noopLogger()
	}

	session, err := codespaces.ConnectToLiveshare(ctx, a, liveshareLogger, a.apiClient, codespace)
	if err != nil {
		return nil, fmt.Errorf("error connecting to codespace: %w", err)
	}

	if err := <-authkeys; err != nil {
		return nil, err
	}

	return session, nil
}

type cpOptions struct {
	sshOptions
	recursive bool // -r
	expand    bool // -e
}

func newCpCmd(app *App) *cobra.Command {
	var opts cpOptions

	cpCmd := &cobra.Command{
		Use:   "cp [-e] [-r] <sources>... <dest>",
		Short: "Copy files between local and remote file systems",
		Long: heredoc.Docf(`
			The cp command copies files between the local and remote file systems.

			As with the UNIX %[1]scp%[1]s command, the first argument specifies the source and the last
			specifies the destination; additional sources may be specified after the first,
			if the destination is a directory.

			The %[1]s--recursive%[1]s flag is required if any source is a directory.

			A "remote:" prefix on any file name argument indicates that it refers to
			the file system of the remote (Codespace) machine. It is resolved relative
			to the home directory of the remote user.

			By default, remote file names are interpreted literally. With the %[1]s--expand%[1]s flag,
			each such argument is treated in the manner of %[1]sscp%[1]s, as a Bash expression to
			be evaluated on the remote machine, subject to expansion of tildes, braces, globs,
			environment variables, and backticks. For security, do not use this flag with arguments
			provided by untrusted users; see <https://lwn.net/Articles/835962/> for discussion.
		`, "`"),
		Example: heredoc.Doc(`
			$ gh codespace cp -e README.md 'remote:/workspaces/$RepositoryName/'
			$ gh codespace cp -e 'remote:~/*.go' ./gofiles/
			$ gh codespace cp -e 'remote:/workspaces/myproj/go.{mod,sum}' ./gofiles/
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.Copy(cmd.Context(), args, opts)
		},
		DisableFlagsInUseLine: true,
	}

	// We don't expose all sshOptions.
	cpCmd.Flags().BoolVarP(&opts.recursive, "recursive", "r", false, "Recursively copy directories")
	cpCmd.Flags().BoolVarP(&opts.expand, "expand", "e", false, "Expand remote file names on remote shell")
	cpCmd.Flags().StringVarP(&opts.codespace, "codespace", "c", "", "Name of the codespace")
	return cpCmd
}

// Copy copies files between the local and remote file systems.
// The mechanics are similar to 'ssh' but using 'scp'.
func (a *App) Copy(ctx context.Context, args []string, opts cpOptions) error {
	if len(args) < 2 {
		return fmt.Errorf("cp requires source and destination arguments")
	}
	if opts.recursive {
		opts.scpArgs = append(opts.scpArgs, "-r")
	}
	opts.scpArgs = append(opts.scpArgs, "--")
	hasRemote := false
	for _, arg := range args {
		if rest := strings.TrimPrefix(arg, "remote:"); rest != arg {
			hasRemote = true
			// scp treats each filename argument as a shell expression,
			// subjecting it to expansion of environment variables, braces,
			// tilde, backticks, globs and so on. Because these present a
			// security risk (see https://lwn.net/Articles/835962/), we
			// disable them by shell-escaping the argument unless the user
			// provided the -e flag.
			if !opts.expand {
				arg = `remote:'` + strings.Replace(rest, `'`, `'\''`, -1) + `'`
			}

		} else if !filepath.IsAbs(arg) {
			// scp treats a colon in the first path segment as a host identifier.
			// Escape it by prepending "./".
			// TODO(adonovan): test on Windows, including with a c:\\foo path.
			const sep = string(os.PathSeparator)
			first := strings.Split(filepath.ToSlash(arg), sep)[0]
			if strings.Contains(first, ":") {
				arg = "." + sep + arg
			}
		}
		opts.scpArgs = append(opts.scpArgs, arg)
	}
	if !hasRemote {
		return cmdutil.FlagErrorf("at least one argument must have a 'remote:' prefix")
	}
	return a.SSH(ctx, nil, opts.sshOptions)
}

type configOptions struct {
	codespace string
}

func newConfigCmd(app *App) *cobra.Command {
	var opts configOptions

	configCmd := &cobra.Command{
		Use:   "config [-c codespace]",
		Short: "Write OpenSSH configuration to stdout",
		Long: heredoc.Docf(`
			The config command generates ssh connection configuration in OpenSSH format.

			If -c/--codespace is specified, configuration is generated for that codespace
			only. Otherwise configuration is emitted for all available codespaces.
		`, "`"),
		Example: heredoc.Doc(`
			$ gh codespace config > ~/.ssh/codespaces
			$ echo 'include ~/.ssh/codespaces' >> ~/.ssh/config'
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.ListOpensshConfig(cmd.Context(), opts)
		},
		DisableFlagsInUseLine: true,
	}

	configCmd.Flags().StringVarP(&opts.codespace, "codespace", "c", "", "Name of the codespace")

	return configCmd
}

// fileLogger is a wrapper around an log.Logger configured to write
// to a file. It exports two additional methods to get the log file name
// and close the file handle when the operation is finished.
type fileLogger struct {
	*log.Logger

	f *os.File
}

// newFileLogger creates a new fileLogger. It returns an error if the file
// cannot be created. The file is created on the specified path, if the path
// is empty it is created in the temporary directory.
func newFileLogger(file string) (fl *fileLogger, err error) {
	var f *os.File
	if file == "" {
		f, err = ioutil.TempFile("", "")
		if err != nil {
			return nil, fmt.Errorf("failed to create tmp file: %w", err)
		}
	} else {
		f, err = os.Create(file)
		if err != nil {
			return nil, err
		}
	}

	return &fileLogger{
		Logger: log.New(f, "", log.LstdFlags),
		f:      f,
	}, nil
}

func (fl *fileLogger) Name() string {
	return fl.f.Name()
}

func (fl *fileLogger) Close() error {
	return fl.f.Close()
}

type combinedReadWriteCloser struct {
	reader *os.File
	writer *os.File
}

func newCombinedReadWriteCloser(reader *os.File, writer *os.File) (crwc *combinedReadWriteCloser) {
	return &combinedReadWriteCloser{
		reader: reader,
		writer: writer,
	}
}

func (crwc *combinedReadWriteCloser) Read(p []byte) (n int, err error) {
	return crwc.reader.Read(p)
}

func (crwc *combinedReadWriteCloser) Write(p []byte) (n int, err error) {
	return crwc.writer.Write(p)
}

func (crwc *combinedReadWriteCloser) Close() error {
	var errs error
	if err := crwc.writer.Close(); err != nil {
		errs = multierror.Append(errs, err)
	}
	if err := crwc.reader.Close(); err != nil {
		errs = multierror.Append(errs, err)
	}
	return errs
}
