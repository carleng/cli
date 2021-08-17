package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/github/ghcs/api"
	"github.com/github/ghcs/cmd/ghcs/output"
	"github.com/github/ghcs/internal/codespaces"
	"github.com/github/go-liveshare"
	"github.com/spf13/cobra"
)

func NewSSHCmd() *cobra.Command {
	var sshProfile, codespaceName string
	var sshServerPort int

	sshCmd := &cobra.Command{
		Use:   "ssh",
		Short: "SSH into a GitHub Codespace, for use with running tests/editing in vim, etc.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return SSH(sshProfile, codespaceName, sshServerPort)
		},
	}

	sshCmd.Flags().StringVarP(&sshProfile, "profile", "", "", "SSH Profile")
	sshCmd.Flags().IntVarP(&sshServerPort, "server-port", "", 0, "SSH Server Port")
	sshCmd.Flags().StringVarP(&codespaceName, "codespace", "c", "", "The `name` of the Codespace to use")

	return sshCmd
}

func init() {
	rootCmd.AddCommand(NewSSHCmd())
}

func SSH(sshProfile, codespaceName string, sshServerPort int) error {
	apiClient := api.New(os.Getenv("GITHUB_TOKEN"))
	ctx := context.Background()
	log := output.NewLogger(os.Stdout, os.Stderr, false)

	user, err := apiClient.GetUser(ctx)
	if err != nil {
		return fmt.Errorf("error getting user: %v", err)
	}

	codespace, token, err := codespaces.GetOrChooseCodespace(ctx, apiClient, user, codespaceName)
	if err != nil {
		return fmt.Errorf("get or choose codespace: %v", err)
	}

	lsclient, err := codespaces.ConnectToLiveshare(ctx, log, apiClient, token, codespace)
	if err != nil {
		return fmt.Errorf("error connecting to liveshare: %v", err)
	}

	terminal, err := liveshare.NewTerminal(lsclient)
	if err != nil {
		return fmt.Errorf("error creating liveshare terminal: %v", err)
	}

	log.Println("Preparing SSH...")
	if sshProfile == "" {
		containerID, err := getContainerID(ctx, log, terminal)
		if err != nil {
			return fmt.Errorf("error getting container id: %v", err)
		}

		if err := setupSSH(ctx, log, terminal, containerID, codespace.RepositoryName); err != nil {
			return fmt.Errorf("error creating ssh server: %v", err)
		}

		log.Print("\n")
	}

	tunnelPort, tunnelClosed, err := codespaces.MakeSSHTunnel(ctx, lsclient, sshServerPort)
	if err != nil {
		return fmt.Errorf("make ssh tunnel: %v", err)
	}

	connectDestination := sshProfile
	if connectDestination == "" {
		connectDestination = fmt.Sprintf("%s@localhost", getSSHUser(codespace))
	}

	usingCustomPort := tunnelPort == sshServerPort
	connClosed := codespaces.ConnectToTunnel(ctx, log, tunnelPort, connectDestination, usingCustomPort)

	log.Println("Ready...")
	select {
	case err := <-tunnelClosed:
		if err != nil {
			return fmt.Errorf("tunnel closed: %v", err)
		}
	case err := <-connClosed:
		if err != nil {
			return fmt.Errorf("connection closed: %v", err)
		}
	}

	return nil
}

func getContainerID(ctx context.Context, logger *output.Logger, terminal *liveshare.Terminal) (string, error) {
	logger.Print(".")

	cmd := terminal.NewCommand(
		"/",
		"/usr/bin/docker ps -aq --filter label=Type=codespaces --filter status=running",
	)

	stream, err := cmd.Run(ctx)
	if err != nil {
		return "", fmt.Errorf("error running command: %v", err)
	}

	logger.Print(".")
	scanner := bufio.NewScanner(stream)
	scanner.Scan()

	logger.Print(".")
	containerID := scanner.Text()
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error scanning stream: %v", err)
	}

	logger.Print(".")
	if err := stream.Close(); err != nil {
		return "", fmt.Errorf("error closing stream: %v", err)
	}

	return containerID, nil
}

func setupSSH(ctx context.Context, logger *output.Logger, terminal *liveshare.Terminal, containerID, repositoryName string) error {
	setupBashProfileCmd := fmt.Sprintf(`echo "cd /workspaces/%v; export $(cat /workspaces/.codespaces/shared/.env | xargs); exec /bin/zsh;" > /home/codespace/.bash_profile`, repositoryName)

	logger.Print(".")
	compositeCommand := []string{setupBashProfileCmd}
	cmd := terminal.NewCommand(
		"/",
		fmt.Sprintf("/usr/bin/docker exec -t %s /bin/bash -c '"+strings.Join(compositeCommand, "; ")+"'", containerID),
	)
	stream, err := cmd.Run(ctx)
	if err != nil {
		return fmt.Errorf("error running command: %v", err)
	}

	logger.Print(".")
	if err := stream.Close(); err != nil {
		return fmt.Errorf("error closing stream: %v", err)
	}

	time.Sleep(1 * time.Second)

	return nil
}

func getSSHUser(codespace *api.Codespace) string {
	if codespace.RepositoryNWO == "github/github" {
		return "root"
	}
	return "codespace"
}
