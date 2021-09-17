package ghcs

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/github/ghcs/cmd/ghcs/output"
	"github.com/github/ghcs/internal/api"
	"github.com/github/ghcs/internal/codespaces"
	"github.com/github/go-liveshare"
	"github.com/spf13/cobra"
)

func newSSHCmd() *cobra.Command {
	var sshProfile, codespaceName string
	var sshServerPort int

	sshCmd := &cobra.Command{
		Use:   "ssh",
		Short: "SSH into a codespace",
		RunE: func(cmd *cobra.Command, args []string) error {
			return ssh(context.Background(), sshProfile, codespaceName, sshServerPort)
		},
	}

	sshCmd.Flags().StringVarP(&sshProfile, "profile", "", "", "Name of the SSH profile to use")
	sshCmd.Flags().IntVarP(&sshServerPort, "server-port", "", 0, "SSH server port number (0 => pick unused)")
	sshCmd.Flags().StringVarP(&codespaceName, "codespace", "c", "", "Name of the codespace")

	return sshCmd
}

func ssh(ctx context.Context, sshProfile, codespaceName string, localSSHServerPort int) error {
	// Ensure all child tasks (e.g. port forwarding) terminate before return.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	apiClient := api.New(os.Getenv("GITHUB_TOKEN"))
	log := output.NewLogger(os.Stdout, os.Stderr, false)

	user, err := apiClient.GetUser(ctx)
	if err != nil {
		return fmt.Errorf("error getting user: %w", err)
	}

	codespace, token, err := getOrChooseCodespace(ctx, apiClient, user, codespaceName)
	if err != nil {
		return fmt.Errorf("get or choose codespace: %w", err)
	}

	session, err := codespaces.ConnectToLiveshare(ctx, log, apiClient, user.Login, token, codespace)
	if err != nil {
		return fmt.Errorf("error connecting to Live Share: %w", err)
	}

	log.Println("Fetching SSH Details...")
	remoteSSHServerPort, sshUser, err := session.StartSSHServer(ctx)
	if err != nil {
		return fmt.Errorf("error getting ssh server details: %w", err)
	}

	usingCustomPort := localSSHServerPort != 0 // suppress log of command line in Shell

	// Ensure local port is listening before client (Shell) connects.
	listen, err := net.Listen("tcp", fmt.Sprintf(":%d", localSSHServerPort))
	if err != nil {
		return err
	}
	defer listen.Close()
	localSSHServerPort = listen.Addr().(*net.TCPAddr).Port

	connectDestination := sshProfile
	if connectDestination == "" {
		connectDestination = fmt.Sprintf("%s@localhost", sshUser)
	}

	log.Println("Ready...")
	tunnelClosed := make(chan error, 1)
	go func() {
		fwd := liveshare.NewPortForwarder(session, "sshd", remoteSSHServerPort)
		tunnelClosed <- fwd.ForwardToListener(ctx, listen) // always non-nil
	}()

	shellClosed := make(chan error, 1)
	go func() {
		shellClosed <- codespaces.Shell(ctx, log, localSSHServerPort, connectDestination, usingCustomPort)
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
