package elasticsearch

import (
	"context"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/olivere/elastic"
	"github.com/pkg/errors"
)

var IndexTemplatePrefix = "INDEX TEMPLATE  "
var IndexPrefix = "INDEX           "
var DocumentsPrefix = "DOCUMENTS       "
var AliasPrefix = "ALIAS           "
var Added = color.GreenString("Added      ")
var Created = color.GreenString("Created    ")
var Removed = color.RedString("Removed    ")
var Deleted = color.RedString("Deleted    ")
var Updated = color.GreenString("Updated    ")
var Reindexed = color.YellowString("Reindexed  ")

func UpdateTemplateAndCreateNewIndex(client *elastic.Client, filePath string, bulkIndexing bool) (string, error) {
	bytes, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", errors.Wrapf(err, "failed reading file %s", filePath)
	}

	// Our index name, index template
	_, fileName := filepath.Split(filePath)
	index := strings.TrimSuffix(fileName, ".json")
	mapping := string(bytes)

	// Update the template
	indexPutTemplate, err := client.IndexPutTemplate(index).BodyString(mapping).Do(context.Background())
	if err != nil {
		return "", errors.Wrapf(err, "failed updating index template %s", index)
	}

	if !indexPutTemplate.Acknowledged {
		fmt.Printf("PUT index/%s not acknowledged\n", index)
	}

	// Create a unique time-stamped index
	dateSuffix := time.Now().Format("2006-01-02-15:04:05")
	indexWithDate := fmt.Sprintf("%s-%s", index, dateSuffix)

	create, err := client.CreateIndex(indexWithDate).Do(context.Background())
	if err != nil {
		return "", errors.Wrapf(err, "failed creating index %s", indexWithDate)
	}

	bulkIndexingSettings := `{
    "index" : {
        "refresh_interval" : "-1"
    }
}
`

	if bulkIndexing {
		settings, err := client.IndexPutSettings(indexWithDate).BodyString(bulkIndexingSettings).Do(context.Background())
		if err != nil {
			return "", errors.Wrapf(err, "failed setting refresh_interval to -1 for index %s", indexWithDate)
		}

		if !settings.Acknowledged {
			fmt.Println("index settings not acknowledged")
		}
	}

	if !create.Acknowledged {
		fmt.Println("index creation not acknowledged")
	}

	fmt.Printf("%s %s %s\n", IndexTemplatePrefix, Updated, index)
	fmt.Printf("%s %s %s\n", IndexPrefix, Created, indexWithDate)
	return indexWithDate, nil
}

func UpdateTemplatesAndCreateNewIndices(client *elastic.Client, templatesDir string, bulkIndexing bool) (map[string]string, error) {
	files, err := ioutil.ReadDir(templatesDir)
	if err != nil {
		return nil, errors.Wrapf(err, "failed reading files in directory %s", templatesDir)
	}

	aliasToNewIndex := map[string]string{}

	for _, file := range files {
		filePath := filepath.Join(templatesDir, file.Name())
		newIndex, err := UpdateTemplateAndCreateNewIndex(client, filePath, bulkIndexing)
		if err != nil {
			return nil, errors.Wrapf(err, "failed create new index from updated template %s", filePath)
		}

		alias := strings.TrimSuffix(file.Name(), ".json")
		aliasToNewIndex[alias] = newIndex
	}

	return aliasToNewIndex, err
}

func ReindexOne(client *elastic.Client, alias, newIndex string) error {
	// We assume that we are reindexing from an existing index on the alias
	reindexingRequired := true

	// Try to find the alias
	aliasResult, err := client.Aliases().Alias(alias).Do(context.Background())
	if err != nil {
		// It's okay if we can't find the alias; it means we are provisioning from scratch
		if !elastic.IsNotFound(err) {
			return errors.Wrapf(err, "failed trying to lookup alias %s", alias)
		}

		// We don't need to reindex from an old index to our new index when provisioning from scratch
		reindexingRequired = false
	}

	// If we have data in an existing index that needs to be reindex with the new template to the new index
	if reindexingRequired {
		// We should only ever get one result if our naming is sensible
		indicesFromAlias := aliasResult.IndicesByAlias(alias)
		for _, index := range indicesFromAlias {
			// Reindex from the existing index as the source to the new index as the destination
			src := elastic.NewReindexSource().Index(index)
			dst := elastic.NewReindexDestination().Index(newIndex)
			refresh, err := client.Reindex().Source(src).Destination(dst).Refresh("true").Do(context.Background())
			if err != nil {
				return errors.Wrapf(err, "failed reindexing from %s to %s", index, newIndex)
			}

			fmt.Printf("%s %s %d from %s to %s\n", DocumentsPrefix, Reindexed, refresh.Total, index, newIndex)

			// Remove the existing index from the alias
			removeAlias, err := client.Alias().Remove(index, alias).Do(context.Background())
			if err != nil {
				return errors.Wrapf(err, "failed removing index %s from alias %s", index, alias)
			}

			if !removeAlias.Acknowledged {
				fmt.Println("alias removal not acknowledged")
			}

			fmt.Printf("%s %s %s from %s\n", AliasPrefix, Removed, index, alias)
		}
	}

	templates, err := client.IndexGetTemplate(alias).Do(context.Background())
	if err != nil {
		return errors.Wrapf(err, "failed to retrieve index template %s", alias)
	}

	seconds := "null"

	if refreshInterval, ok := templates[alias].Settings["index"].(map[string]interface{})["refresh_interval"]; ok {
		seconds = fmt.Sprintf(`"%s"`, refreshInterval.(string))
	}

	// Reset the refresh interval to either whatever is specified in the index template or the default (using null)
	resetRefreshInterval := fmt.Sprintf(`{
    "index" : {
        "refresh_interval" : %s
    }
}
`, seconds)

	settings, err := client.IndexPutSettings(newIndex).BodyString(resetRefreshInterval).Do(context.Background())
	if err != nil {
		return errors.Wrapf(err, "failed setting refresh_interval to -1 for index %s", newIndex)
	}

	if !settings.Acknowledged {
		fmt.Println("index settings not acknowledged")
	}

	// Add our new index which has been reindex with the existing data to the alias
	addAlias, err := client.Alias().Add(newIndex, alias).Do(context.Background())
	if err != nil {
		return errors.Wrapf(err, "failed adding index %s to alias %s", newIndex, alias)
	}

	if !addAlias.Acknowledged {
		fmt.Println("alias addition not acknowledged")
	}

	fmt.Printf("%s %s %s to %s\n", AliasPrefix, Added, newIndex, alias)

	return nil
}

func ReindexAll(client *elastic.Client, aliasToNewIndex map[string]string) error {
	for alias, newIndex := range aliasToNewIndex {
		if err := ReindexOne(client, alias, newIndex); err != nil {
			return errors.Wrapf(err, "failed reindexing to %s and adding to alias %s", newIndex, alias)
		}
	}

	return nil
}

func CleanupOne(client *elastic.Client, indexTemplateName string, maxHistory int) error {
	indices, err := client.IndexNames()
	if err != nil {
		return errors.Wrap(err, "could not get index names")
	}

	var matches []string

	for _, index := range indices {
		if strings.HasPrefix(index, indexTemplateName) {
			matches = append(matches, index)
		}
	}

	sort.Strings(matches)

	for i := 0; i < len(matches)-maxHistory; i++ {
		response, err := client.DeleteIndex(matches[i]).Do(context.Background())
		if err != nil {
			return errors.Wrapf(err, "error deleting index %s, not continuing", matches[i])
		}

		if !response.Acknowledged {
			fmt.Printf("index deletion not acknowledged")
		}

		fmt.Printf("%s %s %s\n", IndexPrefix, Deleted, matches[i])
	}

	return nil
}

func CleanupAll(client *elastic.Client, templatesDir string, maxHistory int) error {
	files, err := ioutil.ReadDir(templatesDir)
	if err != nil {
		return errors.Wrapf(err, "failed reading files in directory %s", templatesDir)
	}

	for _, file := range files {
		indexTemplateName := strings.TrimSuffix(file.Name(), ".json")
		if err := CleanupOne(client, indexTemplateName, maxHistory); err != nil {
			return err
		}
	}

	return nil
}
