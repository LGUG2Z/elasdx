package cli

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/LGUG2Z/elasdx/elasticsearch"
	"github.com/olivere/elastic"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

var (
	Version string
	Commit  string
)

func App() *cli.App {
	cli.VersionPrinter = func(c *cli.Context) {
		fmt.Printf("elasdx version %s (commit %s)\n", c.App.Version, Commit)
	}

	app := cli.NewApp()

	app.Name = "elasdx"
	app.Usage = "An ElasticSearch index template updating, reindexing and cleanup tool"
	app.EnableBashCompletion = true
	app.Compiled = time.Now()
	app.Version = Version
	app.Authors = []cli.Author{{
		Name:  "J. Iqbal",
		Email: "jade@beamery.com",
	}}

	app.Flags = []cli.Flag{
		cli.StringFlag{Name: "url", EnvVar: "ELASDX_URL", Usage: "ElasticSearch URL to connect to", Value: elastic.DefaultURL},
		cli.StringFlag{Name: "username", EnvVar: "ELASDX_USERNAME", Usage: "ElasticSearch basic auth username"},
		cli.StringFlag{Name: "password", EnvVar: "ELASDX_PASSWORD", Usage: "ElasticSearch basic auth password"},
		cli.BoolFlag{Name: "skip-verify", EnvVar: "ELASDX_SKIP_VERIFY", Usage: "Skip TLS verification"},
	}

	app.Commands = []cli.Command{
		Reindex(),
		Cleanup(),
	}

	return app
}

func getClient(c *cli.Context) (*elastic.Client, error) {
	url := c.GlobalString("url")
	schemeAndURL := strings.Split(url, "://")
	username := c.String("username")
	password := c.String("password")

	client := http.DefaultClient

	if c.GlobalBool("skip-verify") {
		client = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		}
	}

	hasBasicAuth := len(username) > 0
	if hasBasicAuth {
		return elastic.NewClient(
			elastic.SetScheme(schemeAndURL[0]),
			elastic.SetURL(url),
			elastic.SetHttpClient(client),
			elastic.SetBasicAuth(username, password),
			elastic.SetSniff(false),
		)
	}

	return elastic.NewClient(
		elastic.SetScheme(schemeAndURL[0]),
		elastic.SetURL(url),
		elastic.SetHttpClient(client),
		elastic.SetSniff(false),
	)
}

func Cleanup() cli.Command {
	return cli.Command{
		Name:  "cleanup",
		Usage: "clean up old indices leaving only the specified maximum index history",
		Flags: []cli.Flag{
			cli.IntFlag{Name: "max-history", Usage: "maximum number of index versions to keep (including current version)", Value: 2},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() == 0 || c.NArg() > 1 {
				fmt.Printf("This command requires a json index template file path or a directory of json index templates\n\n")
				cli.ShowCommandHelpAndExit(c, "cleanup", 1)
			}

			client, err := getClient(c)
			if err != nil {
				return errors.Wrap(err, "error connecting to ElasticSearch")
			}

			if strings.HasSuffix(c.Args().First(), ".json") {
				filePath := c.Args().First()
				_, fileName := filepath.Split(filePath)
				alias := strings.TrimSuffix(fileName, ".json")

				if err := elasticsearch.CleanupOne(client, alias, c.Int("max-history")); err != nil {
					return err

				}

				return nil
			}

			directory := c.Args().First()
			if err := elasticsearch.CleanupAll(client, directory, c.Int("max-history")); err != nil {
				return err
			}

			return nil
		},
	}
}

func Reindex() cli.Command {
	return cli.Command{
		Name:  "reindex",
		Usage: "create a new index from an index template and reindex any existing documents",
		Flags: []cli.Flag{
			cli.BoolFlag{Name: "bulk-indexing", Usage: "set refresh_interval to -1 when reindexing and revert afterwards"},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() == 0 || c.NArg() > 1 {
				fmt.Printf("This command requires a json index template file path or a directory of json index templates\n\n")
				cli.ShowCommandHelpAndExit(c, "reindex", 1)
			}

			client, err := getClient(c)
			if err != nil {
				return errors.Wrap(err, "error connecting to ElasticSearch")
			}

			if strings.HasSuffix(c.Args().First(), ".json") {
				filePath := c.Args().First()
				newIndex, err := elasticsearch.UpdateTemplateAndCreateNewIndex(client, filePath, c.Bool("bulk-indexing"))
				if err != nil {
					return err
				}

				_, fileName := filepath.Split(filePath)
				alias := strings.TrimSuffix(fileName, ".json")
				if err = elasticsearch.ReindexOne(client, alias, newIndex); err != nil {
					return err
				}

				return nil
			}

			directory := c.Args().First()
			aliasToNewIndex, err := elasticsearch.UpdateTemplatesAndCreateNewIndices(client, directory, c.Bool("bulk-indexing"))
			if err != nil {
				return err
			}

			if err = elasticsearch.ReindexAll(client, aliasToNewIndex); err != nil {
				return err
			}

			return nil
		},
	}
}
