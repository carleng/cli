package context

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"

	"gopkg.in/yaml.v3"
)

type configEntry struct {
	User  string
	Token string `yaml:"oauth_token"`
}

func parseConfigFile(fn string) (*configEntry, error) {
	f, err := os.Open(fn)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseConfig(f)
}

func parseConfig(r io.Reader) (*configEntry, error) {
	data, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var config yaml.Node
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}
	if len(config.Content) < 1 {
		return nil, fmt.Errorf("malformed config")
	}
	for i := 0; i < len(config.Content[0].Content)-1; i = i + 2 {
		if config.Content[0].Content[i].Value == defaultHostname {
			var entries []configEntry
			err = config.Content[0].Content[i+1].Decode(&entries)
			if err != nil {
				return nil, err
			}
			return &entries[0], nil
		}
	}
	return nil, fmt.Errorf("could not find config entry for %q", defaultHostname)
}
