package cmd

import (
	"errors"
	"html/template"
	"io"
	"os"
	"path"

	"github.com/aserto-dev/topaz/pkg/cli/cc"
	"github.com/fatih/color"
)

type ConfigureCmd struct {
	PolicyName       string `short:"n" help:"policy name"`
	LocalPolicyImage string `short:"l" help:"local policy image name"`
	Resource         string `short:"r" help:"resource url"`
	Stdout           bool   `short:"p" help:"generated configuration is printed to stdout but not saved"`
	EdgeDirectory    bool   `short:"d" help:"enable edge directory" default:"false"`
	SeedMetadata     bool   `short:"s" help:"enable seed metadata" default:"false"`
}

func (cmd ConfigureCmd) Run(c *cc.CommonCtx) error {
	if cmd.PolicyName == "" && cmd.Resource == "" {
		if cmd.LocalPolicyImage == "" {
			return errors.New("you either need to provide a local policy image or the resource and the policy name for the configuration")
		}
	}
	color.Green(">>> configure policy")

	configDir, err := CreateConfigDir()
	if err != nil {
		return err
	}

	if _, err := CreateCertsDir(); err != nil {
		return err
	}

	if _, err := CreateDataDir(); err != nil {
		return err
	}

	var w io.Writer

	if cmd.Stdout {
		w = c.UI.Output()
	} else {
		w, err = os.Create(path.Join(configDir, "config.yaml"))
		if err != nil {
			return err
		}
	}
	params := templateParams{
		LocalPolicyImage: cmd.LocalPolicyImage,
		PolicyName:       cmd.PolicyName,
		Resource:         cmd.Resource,
		EdgeDirectory:    cmd.EdgeDirectory,
		SeedMetadata:     cmd.SeedMetadata,
	}
	if params.LocalPolicyImage != "" {
		color.Green("using local policy image: %s", params.LocalPolicyImage)
		return WriteConfig(w, localImageTemplate, &params)
	}

	color.Green("policy name: %s", params.PolicyName)

	return WriteConfig(w, configTemplate, &params)
}

func CreateConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	configDir := path.Join(home, "/.config/topaz/cfg")
	if fi, err := os.Stat(configDir); err == nil && fi.IsDir() {
		return configDir, nil
	}

	return configDir, os.MkdirAll(configDir, 0700)
}

func CreateCertsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	certsDir := path.Join(home, "/.config/topaz/certs")
	if fi, err := os.Stat(certsDir); err == nil && fi.IsDir() {
		return certsDir, nil
	}

	return certsDir, os.MkdirAll(certsDir, 0700)
}

func CreateDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	dataDir := path.Join(home, "/.config/topaz/db")
	if fi, err := os.Stat(dataDir); err == nil && fi.IsDir() {
		return dataDir, nil
	}

	return dataDir, os.MkdirAll(dataDir, 0700)
}

func WriteConfig(w io.Writer, templ string, params *templateParams) error {
	t, err := template.New("config").Parse(templ)
	if err != nil {
		return err
	}

	err = t.Execute(w, params)
	if err != nil {
		return err
	}

	return nil
}
