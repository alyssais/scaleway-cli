package init

import (
	"context"
	"fmt"
	"os"
	"reflect"

	"github.com/fatih/color"
	"github.com/scaleway/scaleway-cli/internal/account"
	"github.com/scaleway/scaleway-cli/internal/core"
	"github.com/scaleway/scaleway-cli/internal/interactive"
	"github.com/scaleway/scaleway-cli/internal/terminal"
	"github.com/scaleway/scaleway-sdk-go/logger"
	"github.com/scaleway/scaleway-sdk-go/scw"
	"github.com/scaleway/scaleway-sdk-go/validation"
)

/*
See below the schema `scw init` follows to ask for default config:

                 yes   +----------+
               +-------+Config ok?|
               |       +----------+
+---+  no +----v----+       |no
|out+<----+Override?|       v
+---+     +----+----+  +----+-----+
               |       |Read email+-----------+
               +------>+ or token |  token    |
                 yes   +----------+           |
                            |email            |
                            v                 v
                        +---+----+     +------+---+
                        |  Read  |     |Get access|
                        |password|     |   key    |
                        +---+----+     +------+---+
                            |                 |
                            v                 |
           +--------+ yes +-+-+               |
           |Read OTP+<----+2FA|               |
           +---+----+     +---+               |
               |            |no               |
               |            v                 |
               |      +-----+------+          |
               +----->+Create token|          |
                      +-----+------+          |
                            |                 |
                            v                 |
                    +-------+----------+      |
                    |ask default config+<-----+
                    +------------------+
*/

func GetCommands() *core.Commands {
	return core.NewCommands(initCommand())
}

type InitArgs struct {
	SecretKey      string
	Region         scw.Region
	Zone           scw.Zone
	OrganizationID string
	SendUsage      *bool
}

func initCommand() *core.Command {
	return &core.Command{
		Short:     `Initialize the config`,
		Long:      `Initialize the active profile of the config located in ` + scw.GetConfigPath(),
		Namespace: "init",
		NoClient:  true,
		ArgsType:  reflect.TypeOf(InitArgs{}),
		ArgSpecs: core.ArgSpecs{
			{
				Name:         "secret-key",
				ValidateFunc: core.ValidateSecretKey(),
			},
			{
				Name:       "region",
				EnumValues: []string{"fr-par", "nl-ams"},
			},
			{
				Name:       "zone",
				EnumValues: []string{"fr-par-1", "fr-par-2", "nl-ams-1"},
			},
			{
				Name:         "organization-id",
				ValidateFunc: core.ValidateOrganisationID(),
			},
			{
				Name: "send-usage",
			},
		},
		PreValidateFunc: func(ctx context.Context, argsI interface{}) error {
			args := argsI.(*InitArgs)

			// Show logo banner, or simple welcome message
			if terminal.GetWidth() >= 80 {
				interactive.Printf("%s\n%s\n\n", interactive.Center(logo), interactive.Line("-"))
			} else {
				interactive.Printf("Welcome to the Scaleway Cli\n\n")
			}

			// Check if a config exists
			// Actual creation of the new config is done in the Run()
			newConfig := false
			config, err := scw.LoadConfig()
			if err != nil {
				newConfig = true
			}

			// If it is not a new config, ask if we want to override the existing config
			if !newConfig {
				interactive.PrintlnWithoutIndent(`
					Current config is located at ` + scw.GetConfigPath() + `
					` + terminal.Style(fmt.Sprint(config), color.Faint) + `
				`)
				overrideConfig, err := interactive.PromptBoolWithConfig(&interactive.PromptBoolConfig{
					Prompt:       "Do you want to override current config?",
					DefaultValue: true,
				})
				if err != nil {
					return err
				}
				if !overrideConfig {
					return fmt.Errorf("initialization cancelled")
				}
			}

			// Manually prompt for missing args
			if args.SecretKey == "" {
				args.SecretKey, err = promptSecretKey()
				if err != nil {
					return err
				}
			}
			if args.Zone == "" {
				zone, err := interactive.PromptStringWithConfig(&interactive.PromptStringConfig{
					Prompt:          "Select a zone",
					DefaultValueDoc: "fr-par-1",
					DefaultValue:    "fr-par-1",
					ValidateFunc: func(s string) error {
						logger.Debugf("s: %v", s)
						if !validation.IsZone(s) {
							return fmt.Errorf("invalid zone")
						}
						return nil
					},
				})
				args.Zone, err = scw.ParseZone(zone)
				if err != nil {
					return err
				}
			}

			// Deduce Region from Zone
			if args.Region == "" {
				args.Region, err = args.Zone.Region()
				if err != nil {
					return err
				}
			}

			// Set OrganizationID if not done previously
			// As OrganizationID depends on args.SecretKey, we can't use a DefaultFunc or ArgPromptFunc.
			if args.OrganizationID == "" {
				args.OrganizationID, err = getOrganizationID(args.SecretKey)
				if err != nil {
					return err
				}
			}

			// Ask for send usage permission
			if args.SendUsage == nil {
				interactive.Println()
				interactive.PrintlnWithoutIndent(`
					To improve this tools we rely on diagnostic and usage data.
					Sending such data is optional and can be disable at any time by running "scw config set send_usage false"
				`)
				sendUsage, err := interactive.PromptBoolWithConfig(&interactive.PromptBoolConfig{
					Prompt:       "Do you want to send usage statistics and diagnostics?",
					DefaultValue: true,
				})
				args.SendUsage = scw.BoolPtr(sendUsage)
				if err != nil {
					return err
				}
			}

			return nil
		},
		Run: func(ctx context.Context, argsI interface{}) (i interface{}, e error) {
			args := argsI.(*InitArgs)

			// Check if a config exists
			// Creates a new one if it does not
			config, err := scw.LoadConfig()
			if err != nil {
				config = &scw.Config{}
				interactive.Printf("Creating new config at %v\n", scw.GetConfigPath())
			}

			if args.SendUsage != nil {
				config.SendUsage = *args.SendUsage
			}

			// Update active profile
			profile, err := config.GetActiveProfile()
			if err != nil {
				return nil, err
			}
			profile.SecretKey = &args.SecretKey
			profile.DefaultZone = scw.StringPtr(args.Zone.String())
			profile.DefaultRegion = scw.StringPtr(args.Region.String())
			profile.DefaultOrganizationID = &args.OrganizationID
			err = config.Save()
			if err != nil {
				return nil, err
			}

			// Get access key
			accessKey, err := account.GetAccessKey(args.SecretKey)
			if err != nil {
				interactive.Printf("Config saved at %s:\n%s\n", scw.GetConfigPath(), terminal.Style(fmt.Sprint(config), color.Faint))
				return "", &core.CliError{
					Err:     err,
					Details: "Failed to retrieve Access Key for the given Secret Key.",
				}
			}
			profile.AccessKey = &accessKey
			err = config.Save()
			if err != nil {
				return nil, err
			}

			return &core.SuccessResult{}, nil
		},
	}
}

func promptSecretKey() (string, error) {
	UUIDOrEmail, err := interactive.Readline(&interactive.ReadlineConfig{
		PromptFunc: func(value string) string {
			secretKey, email := "secret-key", "email"
			switch {
			case validation.IsEmail(value):
				email = terminal.Style(email, color.FgBlue)
			case validation.IsUUID(value):
				secretKey = terminal.Style(secretKey, color.FgBlue)
			}
			return terminal.Style(fmt.Sprintf("Enter a valid %s or an %s: ", secretKey, email), color.Bold)
		},
		ValidateFunc: func(s string) error {
			if validation.IsEmail(s) || validation.IsSecretKey(s) {
				return nil
			}
			return fmt.Errorf("invalid email or secret-key")
		},
	})
	if err != nil {
		return "", err
	}

	switch {
	case validation.IsEmail(UUIDOrEmail):
		email := UUIDOrEmail
		password, err := interactive.PromptPassword("Enter your " + terminal.Style("password", color.Bold))
		if err != nil {
			return "", err
		}
		hostname, _ := os.Hostname()
		loginReq := &account.LoginRequest{
			Email:       email,
			Password:    password,
			Description: fmt.Sprintf("scw-cli %s@%s", os.Getenv("USER"), hostname),
		}
		var t *account.Token
		var twoFactorRequired bool
		for {
			t, twoFactorRequired, err = account.Login(loginReq)
			if err != nil {
				return "", err
			}
			if !twoFactorRequired {
				return t.SecretKey, nil
			}
			loginReq.TwoFactorToken, err = interactive.PromptString("Enter your 2FA code")
			if err != nil {
				return "", err
			}
		}

	case validation.IsUUID(UUIDOrEmail):
		return UUIDOrEmail, nil

	default:
		return "", fmt.Errorf("invalid email or secret-key: '%v'", UUIDOrEmail)
	}
}

// getOrganizationId handles prompting for the argument organization-id
// If we have only 1 id : we use it, and don't prompt
// If we have more than 1 id, we prompt, with id[0] as default value.
func getOrganizationID(secretKey string) (string, error) {
	IDs, err := account.GetOrganizationsIds(secretKey)
	if err != nil {
		logger.Warningf("%v", err)
		return promptOrganizationID(IDs)
	}
	if len(IDs) != 1 {
		return promptOrganizationID(IDs)
	}
	return IDs[0], nil
}

func promptOrganizationID(IDs []string) (string, error) {
	config := &interactive.PromptStringConfig{
		Prompt:       "Enter your Organization ID",
		ValidateFunc: interactive.ValidateOrganizationID(),
	}
	if len(IDs) > 0 {
		config.DefaultValue = IDs[0]
		config.DefaultValueDoc = IDs[0]
	}
	ID, err := interactive.PromptStringWithConfig(config)
	if err != nil {
		return "", err
	}
	return ID, nil
}

const logo = `
  @@@@@@@@@@@@@@@.
@@@@@@@@@@@@@@@@@@@@        __          __  _
@@@               @@@@      \ \        / / | |
@@@    @@@@@@@     .@@@      \ \  /\  / /__| | ___ ___  _ __ ___   ___
@@@   @@@@@@@@      @@@       \ \/  \/ / _ \ |/ __/ _ \| '_ ` + "`" + ` _ \ / _ \
@@@   @@@           @@@        \  /\  /  __/ | (_| (_) | | | | | |  __/
@@@   @@@     @@@   @@@         \/  \/ \___|_|\___\___/|_| |_| |_|\___|
@@@   @@@     @@@   @@@                                         _  _
@@@           @@@   @@@                                        | |(_)
@@@      .@@@@@@@   @@@             ___   ___ __      __   ___ | | _
@@@      @@@@@@@    @@@            / __| / __|\ \ /\ / /  / __|| || |
 @@@.               @@@            \__ \| (__  \ V  V /  | (__ | || |
  @@@@@@.         .@@@@            |___/ \___|  \_/\_/    \___||_||_|
     @@@@@@@@@@@@@@@@.
`