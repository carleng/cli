package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/github/ghcs/api"
	"github.com/github/go-liveshare"
	"github.com/spf13/cobra"
)

func NewSSHCmd() *cobra.Command {
	var sshProfile string

	sshCmd := &cobra.Command{
		Use:   "ssh",
		Short: "ssh",
		Long:  "ssh",
		RunE: func(cmd *cobra.Command, args []string) error {
			return SSH(sshProfile)
		},
	}

	sshCmd.Flags().StringVarP(&sshProfile, "profile", "", "", "SSH Profile")

	return sshCmd
}

func init() {
	rootCmd.AddCommand(NewSSHCmd())
}

func SSH(sshProfile string) error {
	apiClient := api.New(os.Getenv("GITHUB_TOKEN"))
	ctx := context.Background()

	user, err := apiClient.GetUser(ctx)
	if err != nil {
		return fmt.Errorf("error getting user: %v", err)
	}

	codespaces, err := apiClient.ListCodespaces(ctx, user)
	if err != nil {
		return fmt.Errorf("error getting codespaces: %v", err)
	}

	if len(codespaces) == 0 {
		fmt.Println("You have no codespaces.")
		return nil
	}

	codespaces.SortByRecent()

	codespacesByName := make(map[string]*api.Codespace)
	codespacesNames := make([]string, 0, len(codespaces))
	for _, codespace := range codespaces {
		codespacesByName[codespace.Name] = codespace
		codespacesNames = append(codespacesNames, codespace.Name)
	}

	sshSurvey := []*survey.Question{
		{
			Name: "codespace",
			Prompt: &survey.Select{
				Message: "Choose Codespace:",
				Options: codespacesNames,
				Default: codespacesNames[0],
			},
			Validate: survey.Required,
		},
	}

	answers := struct {
		Codespace string
	}{}
	if err := survey.Ask(sshSurvey, &answers); err != nil {
		return fmt.Errorf("error getting answers: %v", err)
	}

	codespace := codespacesByName[answers.Codespace]

	token, err := apiClient.GetCodespaceToken(ctx, codespace)
	if err != nil {
		return fmt.Errorf("error getting codespace token: %v", err)
	}

	if codespace.Environment.State != api.CodespaceEnvironmentStateAvailable {
		fmt.Println("Starting your codespace...")
		if err := apiClient.StartCodespace(ctx, token, codespace); err != nil {
			return fmt.Errorf("error starting codespace: %v", err)
		}
	}

	retries := 0
	for codespace.Environment.Connection.SessionID == "" || codespace.Environment.State != api.CodespaceEnvironmentStateAvailable {
		if retries > 1 {
			if retries%2 == 0 {
				fmt.Print(".")
			}

			time.Sleep(1 * time.Second)
		}

		if retries == 30 {
			return errors.New("timed out while waiting for the codespace to start")
		}

		codespace, err = apiClient.GetCodespace(ctx, token, codespace.OwnerLogin, codespace.Name)
		if err != nil {
			return fmt.Errorf("error getting codespace: %v", err)
		}

		retries += 1
	}

	if retries >= 2 {
		fmt.Print("\n")
	}

	fmt.Println("Connecting to your codespace...")

	liveShare, err := liveshare.New(
		liveshare.WithWorkspaceID(codespace.Environment.Connection.SessionID),
		liveshare.WithToken(codespace.Environment.Connection.SessionToken),
	)
	if err != nil {
		return fmt.Errorf("error creating live share: %v", err)
	}

	liveShareClient := liveShare.NewClient()
	if err := liveShareClient.Join(ctx); err != nil {
		return fmt.Errorf("error joining liveshare client: %v", err)
	}

	terminal, err := liveShareClient.NewTerminal()
	if err != nil {
		return fmt.Errorf("error creating liveshare terminal: %v", err)
	}

	fmt.Println("Preparing SSH...")
	if sshProfile == "" {
		containerID, err := getContainerID(ctx, terminal)
		if err != nil {
			return fmt.Errorf("error getting container id: %v", err)
		}

		if err := setupSSH(ctx, terminal, containerID, codespace.RepositoryName); err != nil {
			return fmt.Errorf("error creating ssh server: %v", err)
		}
	}

	server, err := liveShareClient.NewServer()
	if err != nil {
		return fmt.Errorf("error creating server: %v", err)
	}

	rand.Seed(time.Now().Unix())
	port := rand.Intn(9999-2000) + 2000 // improve this obviously
	if err := server.StartSharing(ctx, "sshd", 2222); err != nil {
		return fmt.Errorf("error sharing sshd port: %v", err)
	}

	portForwarder := liveshare.NewLocalPortForwarder(liveShareClient, server, port)
	go func() {
		if err := portForwarder.Start(ctx); err != nil {
			panic(fmt.Errorf("error forwarding port: %v", err))
		}
	}()

	fmt.Println("Ready...")
	if err := connect(ctx, port, sshProfile); err != nil {
		return fmt.Errorf("error connecting via SSH: %v", err)
	}

	return nil
}

func connect(ctx context.Context, port int, sshProfile string) error {
	var cmd *exec.Cmd
	if sshProfile != "" {
		cmd = exec.CommandContext(ctx, "ssh", sshProfile, "-p", strconv.Itoa(port), "-C")
	} else {
		cmd = exec.CommandContext(ctx, "ssh", "codespace@localhost", "-C", "-p", strconv.Itoa(port), "-o", "NoHostAuthenticationForLocalhost=yes")
	}

	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("error running ssh: %v", err)
	}

	go func() {
		if err := cmd.Wait(); err != nil {
			log.Println(fmt.Errorf("error waiting for ssh to finish: %v", err))
		}
	}()

	done := make(chan bool)
	<-done

	return nil
}

func getContainerID(ctx context.Context, terminal *liveshare.Terminal) (string, error) {
	cmd := terminal.NewCommand(
		"/",
		"/usr/bin/docker ps -aq --filter label=Type=codespaces --filter status=running",
	)
	stream, err := cmd.Run(ctx)
	if err != nil {
		return "", fmt.Errorf("error running command: %v", err)
	}

	scanner := bufio.NewScanner(stream)
	scanner.Scan()

	containerID := scanner.Text()
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error scanning stream: %v", err)
	}

	if err := stream.Close(); err != nil {
		return "", fmt.Errorf("error closing stream: %v", err)
	}

	return containerID, nil
}

func setupSSH(ctx context.Context, terminal *liveshare.Terminal, containerID, repositoryName string) error {
	getUsernameCmd := "GITHUB_USERNAME=\"$(jq .CODESPACE_NAME /workspaces/.codespaces/shared/environment-variables.json -r | cut -f1 -d -)\""
	makeSSHDirCmd := "mkdir /home/codespace/.ssh"
	getUserKeysCmd := "curl --silent --fail \"https://github.com/$(echo $GITHUB_USERNAME).keys\" > /home/codespace/.ssh/authorized_keys"
	setupSecretsCmd := `cat /workspaces/.codespaces/shared/.user-secrets.json | jq -r ".[] | select (.type==\"EnvironmentVariable\") | .name+\"=\"+.value" > /home/codespace/.zshenv`
	setupLoginDirCmd := fmt.Sprintf("echo \"cd /workspaces/%v; exec /bin/zsh;\" > /home/codespace/.bash_profile", repositoryName)

	compositeCommand := []string{getUsernameCmd, makeSSHDirCmd, getUserKeysCmd, setupSecretsCmd, setupLoginDirCmd}
	cmd := terminal.NewCommand(
		"/",
		fmt.Sprintf("/usr/bin/docker exec -t %s /bin/bash -c '"+strings.Join(compositeCommand, "; ")+"'", containerID),
	)
	stream, err := cmd.Run(ctx)
	if err != nil {
		return fmt.Errorf("error running command: %v", err)
	}

	if err := stream.Close(); err != nil {
		return fmt.Errorf("error closing stream: %v", err)
	}

	time.Sleep(1 * time.Second)

	return nil
}
