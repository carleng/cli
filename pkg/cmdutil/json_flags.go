package cmdutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/cli/cli/pkg/export"
	"github.com/cli/cli/pkg/jsoncolor"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type JSONFlagError struct {
	error
}

func AddJSONFlags(cmd *cobra.Command, exportTarget **ExportFormat, fields []string) {
	f := cmd.Flags()
	f.StringSlice("json", nil, "Output JSON with the specified `fields`")
	f.StringP("jq", "q", "", "Filter JSON output using a jq `expression`")
	f.StringP("template", "t", "", "Format JSON output using a Go template")

	oldPreRun := cmd.PreRunE
	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		if oldPreRun != nil {
			if err := oldPreRun(c, args); err != nil {
				return err
			}
		}
		if export, err := checkJSONFlags(c); err == nil {
			*exportTarget = export
		} else {
			return err
		}
		return nil
	}

	cmd.SetFlagErrorFunc(func(c *cobra.Command, e error) error {
		if e.Error() == "flag needs an argument: --json" {
			sort.Strings(fields)
			return JSONFlagError{fmt.Errorf("Specify one or more comma-separated fields for `--json`:\n  %s", strings.Join(fields, "\n  "))}
		}
		return c.Parent().FlagErrorFunc()(c, e)
	})
}

func checkJSONFlags(cmd *cobra.Command) (*ExportFormat, error) {
	f := cmd.Flags()
	jsonFlag := f.Lookup("json")
	jqFlag := f.Lookup("jq")
	tplFlag := f.Lookup("template")
	webFlag := f.Lookup("web")

	if jsonFlag.Changed {
		if webFlag != nil && webFlag.Changed {
			return nil, errors.New("cannot use `--web` with `--json`")
		}
		jv := jsonFlag.Value.(pflag.SliceValue)
		return &ExportFormat{
			Fields:   jv.GetSlice(),
			Filter:   jqFlag.Value.String(),
			Template: tplFlag.Value.String(),
		}, nil
	} else if jqFlag.Changed {
		return nil, errors.New("cannot use `--jq` without specifying `--json`")
	} else if tplFlag.Changed {
		return nil, errors.New("cannot use `--template` without specifying `--json`")
	}
	return nil, nil
}

type ExportFormat struct {
	Fields   []string
	Filter   string
	Template string
}

func (e *ExportFormat) Write(w io.Writer, data interface{}, colorEnabled bool) error {
	buf := bytes.Buffer{}
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(data); err != nil {
		return err
	}

	if e.Filter != "" {
		return export.FilterJSON(w, &buf, e.Filter)
	} else if e.Template != "" {
		return export.ExecuteTemplate(w, &buf, e.Template, colorEnabled)
	} else if colorEnabled {
		return jsoncolor.Write(w, &buf, "  ")
	}

	_, err := io.Copy(w, &buf)
	return err
}
