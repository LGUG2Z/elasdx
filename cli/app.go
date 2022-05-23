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
		UpdateAlias(),
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
		Usage: "Reindex from a src index to a dest index optionally creating a new index from an index template",
		Flags: []cli.Flag{
			cli.StringFlag{Name: "dest-index", Usage: "Optionally specify destination index, otherwise one will be generated and created for you."},
			cli.BoolFlag{Name: "bulk-indexing", Usage: "set refresh_interval to -1 and set number_of_replicas to 0 when reindexing and revert afterwards."},
			cli.BoolFlag{Name: "version-external", Usage: "set version_type to external. This will only index documents if they don't exist or the source doc is at a higher version"},
			cli.BoolFlag{Name: "no-update-alias", Usage: "don't update the index alias. This setting will also not revert the refresh_interval and number_of_replicas if bulk-indexing is set"},
			cli.BoolFlag{Name: "include-type-name", Usage: "passes 'include_type_name=true' for put index template request. Used for ES6->7 upgrade. https://www.elastic.co/blog/moving-from-types-to-typeless-apis-in-elasticsearch-7-0"},
			cli.StringFlag{Name: "reindex-host-allocation", Usage: "Optional target host for the reindex to happen on. eg. 'es-reindex-*'"},
			cli.StringFlag{Name: "dest-host-allocation", Usage: "Optional target host once the reindex is complete. eg. 'es-data-*'"},
			cli.StringFlag{Name: "extra-suffix", Usage: "Optional extra suffix name to add to index name (after date). Ignored if dest-index is set"},
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
				// Single index
				filePath := c.Args().First()

				newIndex, err := elasticsearch.UpdateTemplateAndCreateNewIndex(client, filePath, c.String("dest-index"), c.Bool("bulk-indexing"), c.Bool("include-type-name"), c.String("extra-suffix"))
				if err != nil {
					return err
				}

				if c.IsSet("reindex-host-allocation") {
					// Update index settings to force allocation to specified host pattern for reindex
					err := elasticsearch.UpdateHostAllocation(client, newIndex, c.String("reindex-host-allocation"))
					if err != nil {
						return err
					}
				}

				_, fileName := filepath.Split(filePath)
				alias := strings.TrimSuffix(fileName, ".json")
				if err = elasticsearch.ReindexOne(client, alias, newIndex, c.Bool("version-external"), c.Bool("no-update-alias"), c.Bool("bulk-indexing")); err != nil {
					return err
				}

				if c.IsSet("dest-host-allocation") {
					// Update index settings to force allocation to specified host pattern after reindex is complete
					err := elasticsearch.UpdateHostAllocation(client, newIndex, c.String("dest-host-allocation"))
					if err != nil {
						return err
					}
				}
				return nil
			}

			// Multiple indexes
			if c.IsSet("dest-index") {
				fmt.Printf("--dest-index not supported with multiple indexes, please only specify one index template .json.")
				cli.ShowCommandHelpAndExit(c, "reindex", 1)
			}

			directory := c.Args().First()
			aliasToNewIndex, err := elasticsearch.UpdateTemplatesAndCreateNewIndices(client, directory, c.Bool("bulk-indexing"), c.Bool("include-type-name"), c.String("extra-suffix"))
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

func UpdateAlias() cli.Command {
	return cli.Command{
		Name:  "update-alias",
		Usage: "Swap an index alias to another index",
		Flags: []cli.Flag{
			cli.StringFlag{Name: "alias", Usage: "Name of the alias."},
			cli.StringFlag{Name: "dest-index", Usage: "Name of the destination index."},
		},
		Action: func(c *cli.Context) error {
			if !c.IsSet("alias") || !c.IsSet("dest-index") {
				fmt.Printf("This command requires a json index template file path or a directory of json index templates\n\n")
				cli.ShowCommandHelpAndExit(c, "reindex", 1)
			}
			client, err := getClient(c)
			if err != nil {
				return errors.Wrap(err, "error connecting to ElasticSearch")
			}

			if err = elasticsearch.UpdateAlias(client, c.String("alias"), c.String("dest-index")); err != nil {
				return err
			}
			return nil
		},
	}
}
