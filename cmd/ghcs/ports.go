package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/github/ghcs/api"
	"github.com/github/ghcs/internal/codespaces"
	"github.com/github/go-liveshare"
	"github.com/muhammadmuzzammil1998/jsonc"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

func NewPortsCmd() *cobra.Command {
	portsCmd := &cobra.Command{
		Use:   "ports",
		Short: "Forward ports from a GitHub Codespace.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return Ports()
		},
	}

	portsCmd.AddCommand(NewPortsPublicCmd())
	portsCmd.AddCommand(NewPortsPrivateCmd())
	portsCmd.AddCommand(NewPortsForwardCmd())
	return portsCmd
}

func init() {
	rootCmd.AddCommand(NewPortsCmd())
}

func Ports() error {
	apiClient := api.New(os.Getenv("GITHUB_TOKEN"))
	ctx := context.Background()

	user, err := apiClient.GetUser(ctx)
	if err != nil {
		return fmt.Errorf("error getting user: %v", err)
	}

	codespace, err := codespaces.ChooseCodespace(ctx, apiClient, user)
	if err != nil {
		if err == codespaces.ErrNoCodespaces {
			fmt.Println(err.Error())
			return nil
		}
		return fmt.Errorf("error choosing codespace: %v", err)
	}

	devContainerCh := getDevContainer(ctx, apiClient, codespace)

	token, err := apiClient.GetCodespaceToken(ctx, user.Login, codespace.Name)
	if err != nil {
		return fmt.Errorf("error getting codespace token: %v", err)
	}

	liveShareClient, err := codespaces.ConnectToLiveshare(ctx, apiClient, token, codespace)
	if err != nil {
		return fmt.Errorf("error connecting to liveshare: %v", err)
	}

	fmt.Println("Loading ports...")
	ports, err := getPorts(ctx, liveShareClient)
	if err != nil {
		return fmt.Errorf("error getting ports: %v", err)
	}

	if len(ports) == 0 {
		fmt.Println("This codespace has no open ports")
		return nil
	}

	devContainerResult := <-devContainerCh
	if devContainerResult.Err != nil {
		fmt.Printf("Failed to get port names: %v\n", devContainerResult.Err.Error())
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Label", "Source Port", "Destination Port", "Public", "Browse URL"})
	for _, port := range ports {
		sourcePort := strconv.Itoa(port.SourcePort)
		var portName string
		if devContainerResult.DevContainer != nil {
			if attributes, ok := devContainerResult.DevContainer.PortAttributes[sourcePort]; ok {
				portName = attributes.Label
			}
		}

		table.Append([]string{
			portName,
			sourcePort,
			strconv.Itoa(port.DestinationPort),
			strings.ToUpper(strconv.FormatBool(port.IsPublic)),
			fmt.Sprintf("https://%s-%s.githubpreview.dev/", codespace.Name, sourcePort),
		})
	}
	table.Render()

	return nil

}

func getPorts(ctx context.Context, lsclient *liveshare.Client) (liveshare.Ports, error) {
	server, err := liveshare.NewServer(lsclient)
	if err != nil {
		return nil, fmt.Errorf("error creating server: %v", err)
	}

	ports, err := server.GetSharedServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("error getting shared servers: %v", err)
	}

	return ports, nil
}

type devContainerResult struct {
	DevContainer *devContainer
	Err          error
}

type devContainer struct {
	PortAttributes map[string]portAttribute `json:"portsAttributes"`
}

type portAttribute struct {
	Label string `json:"label"`
}

func getDevContainer(ctx context.Context, apiClient *api.API, codespace *api.Codespace) <-chan devContainerResult {
	ch := make(chan devContainerResult)
	go func() {
		contents, err := apiClient.GetCodespaceRepositoryContents(ctx, codespace, ".devcontainer/devcontainer.json")
		if err != nil {
			ch <- devContainerResult{nil, fmt.Errorf("error getting content: %v", err)}
			return
		}

		if contents == nil {
			ch <- devContainerResult{nil, nil}
			return
		}

		convertedJSON := jsonc.ToJSON(contents)
		if !jsonc.Valid(convertedJSON) {
			ch <- devContainerResult{nil, errors.New("failed to convert json to standard json")}
			return
		}

		var container devContainer
		if err := json.Unmarshal(convertedJSON, &container); err != nil {
			ch <- devContainerResult{nil, fmt.Errorf("error unmarshaling: %v", err)}
			return
		}

		ch <- devContainerResult{&container, nil}
	}()
	return ch
}

func NewPortsPublicCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "public",
		Short: "public",
		Long:  "public",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return errors.New("[codespace_name] [source] port number are required.")
			}

			return updatePortVisibility(args[0], args[1], true)
		},
	}
}

func NewPortsPrivateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "private",
		Short: "private",
		Long:  "private",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return errors.New("[codespace_name] [source] port number are required.")
			}

			return updatePortVisibility(args[0], args[1], false)
		},
	}
}

func updatePortVisibility(codespaceName, sourcePort string, public bool) error {
	ctx := context.Background()
	apiClient := api.New(os.Getenv("GITHUB_TOKEN"))

	user, err := apiClient.GetUser(ctx)
	if err != nil {
		return fmt.Errorf("error getting user: %v", err)
	}

	token, err := apiClient.GetCodespaceToken(ctx, user.Login, codespaceName)
	if err != nil {
		return fmt.Errorf("error getting codespace token: %v", err)
	}

	codespace, err := apiClient.GetCodespace(ctx, token, user.Login, codespaceName)
	if err != nil {
		return fmt.Errorf("error getting codespace: %v", err)
	}

	lsclient, err := codespaces.ConnectToLiveshare(ctx, apiClient, token, codespace)
	if err != nil {
		return fmt.Errorf("error connecting to liveshare: %v", err)
	}

	server, err := liveshare.NewServer(lsclient)
	if err != nil {
		return fmt.Errorf("error creating server: %v", err)
	}

	port, err := strconv.Atoi(sourcePort)
	if err != nil {
		return fmt.Errorf("error reading port number: %v", err)
	}

	if err := server.UpdateSharedVisibility(ctx, port, public); err != nil {
		return fmt.Errorf("error update port to public: %v", err)
	}

	state := "PUBLIC"
	if public == false {
		state = "PRIVATE"
	}

	fmt.Println(fmt.Sprintf("Port %s is now %s.", sourcePort, state))

	return nil
}

func NewPortsForwardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forward",
		Short: "forward",
		Long:  "forward",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return errors.New("[codespace_name] [source]:[dst] port number are required.")
			}
			return forwardPort(args[0], args[1:])
		},
	}
}

func forwardPort(codespaceName string, ports []string) error {
	ctx := context.Background()
	apiClient := api.New(os.Getenv("GITHUB_TOKEN"))

	portPairs, err := getPortPairs(ports)
	if err != nil {
		return fmt.Errorf("get port pairs: %v", err)
	}

	user, err := apiClient.GetUser(ctx)
	if err != nil {
		return fmt.Errorf("error getting user: %v", err)
	}

	token, err := apiClient.GetCodespaceToken(ctx, user.Login, codespaceName)
	if err != nil {
		return fmt.Errorf("error getting codespace token: %v", err)
	}

	codespace, err := apiClient.GetCodespace(ctx, token, user.Login, codespaceName)
	if err != nil {
		return fmt.Errorf("error getting codespace: %v", err)
	}

	lsclient, err := codespaces.ConnectToLiveshare(ctx, apiClient, token, codespace)
	if err != nil {
		return fmt.Errorf("error connecting to liveshare: %v", err)
	}

	server, err := liveshare.NewServer(lsclient)
	if err != nil {
		return fmt.Errorf("error creating server: %v", err)
	}

	g, gctx := errgroup.WithContext(ctx)
	for _, portPair := range portPairs {
		portPair := portPair

		srcstr := strconv.Itoa(portPair.Src)
		if err := server.StartSharing(gctx, "share-"+srcstr, portPair.Src); err != nil {
			return fmt.Errorf("start sharing port: %v", err)
		}

		g.Go(func() error {
			fmt.Println("Forwarding port: " + srcstr + " ==> " + strconv.Itoa(portPair.Dst))
			portForwarder := liveshare.NewPortForwarder(lsclient, server, portPair.Dst)
			if err := portForwarder.Start(gctx); err != nil {
				return fmt.Errorf("error forwarding port: %v", err)
			}

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}

	return nil
}

type portPair struct {
	Src, Dst int
}

func getPortPairs(ports []string) ([]portPair, error) {
	pp := make([]portPair, 0, len(ports))

	for _, portString := range ports {
		parts := strings.Split(portString, ":")
		if len(parts) < 2 {
			return pp, fmt.Errorf("port pair: '%v' is not valid", portString)
		}

		srcp, err := strconv.Atoi(parts[0])
		if err != nil {
			return pp, fmt.Errorf("convert source port to int: %v", err)
		}

		dstp, err := strconv.Atoi(parts[1])
		if err != nil {
			return pp, fmt.Errorf("convert dest port to int: %v", err)
		}

		pp = append(pp, portPair{srcp, dstp})
	}

	return pp, nil
}
