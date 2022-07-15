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
var SettingsPrefix = "SETTINGS        "
var Added = color.GreenString("Added      ")
var Created = color.GreenString("Created    ")
var Removed = color.RedString("Removed    ")
var Deleted = color.RedString("Deleted    ")
var Updated = color.GreenString("Updated    ")
var Reindexed = color.YellowString("Reindexed  ")
var Info = color.YellowString("Info       ")

func UpdateTemplateAndCreateNewIndex(client *elastic.Client, filePath, newIndexName string, bulkIndexing bool, includeTypeName bool, extraSuffix string) (string, error) {
	bytes, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", errors.Wrapf(err, "failed reading file %s", filePath)
	}

	// Our index name, index template
	_, fileName := filepath.Split(filePath)
	index := strings.TrimSuffix(fileName, ".json")
	mapping := string(bytes)

	// Update the template
	indexPutTemplate, err := client.IndexPutTemplate(index).IncludeTypeName(includeTypeName).BodyString(mapping).Do(context.Background())
	if err != nil {
		return "", errors.Wrapf(err, "failed updating index template %s", index)
	}

	if !indexPutTemplate.Acknowledged {
		fmt.Printf("PUT index/%s not acknowledged\n", index)
	}
	fmt.Printf("%s %s %s\n", IndexTemplatePrefix, Updated, index)

	// Create a unique time-stamped index
	dateSuffix := strings.ReplaceAll(time.Now().Format("2006-01-02-15:04:05"), ":", "-")
	if newIndexName == "" {
		if extraSuffix != "" {
			dateSuffix = fmt.Sprintf("%s-%s", dateSuffix, extraSuffix)
		}
		newIndexName = fmt.Sprintf("%s-%s", index, dateSuffix)
	}

	exists, err := client.IndexExists(newIndexName).Do(context.Background())
	if err != nil {
		return "", errors.Wrapf(err, "failed checking for index %s", newIndexName)
	}

	if !exists {
		create, err := client.CreateIndex(newIndexName).Do(context.Background())
		if err != nil {
			return "", errors.Wrapf(err, "failed creating index %s", newIndexName)
		}
		if !create.Acknowledged {
			fmt.Println("index creation not acknowledged")
		}
		fmt.Printf("%s %s %s\n", IndexPrefix, Created, newIndexName)
	} else {
		fmt.Printf("%s %s %s\n", IndexPrefix, Updated, newIndexName)
	}

	if bulkIndexing {
		indexingSettings := `{
			"index" : {
				"refresh_interval" : "-1",
				"number_of_replicas": "0"
			}
		}
		`
		settings, err := client.IndexPutSettings(newIndexName).BodyString(indexingSettings).Do(context.Background())
		if err != nil {
			return "", errors.Wrapf(err, "failed setting index refresh_interval/replica settings for index %s", newIndexName)
		}

		if !settings.Acknowledged {
			fmt.Println("index settings not acknowledged")
		}
		fmt.Printf("%s %s refresh_interval(0)/replica(-1) for %s \n", SettingsPrefix, Updated, newIndexName)
	}

	return newIndexName, nil
}

func UpdateTemplatesAndCreateNewIndices(client *elastic.Client, templatesDir string, bulkIndexing bool, includeTypeName bool, extraSuffix string) (map[string]string, error) {
	files, err := ioutil.ReadDir(templatesDir)
	if err != nil {
		return nil, errors.Wrapf(err, "failed reading files in directory %s", templatesDir)
	}

	aliasToNewIndex := map[string]string{}

	for _, file := range files {
		// If we mount a ConfigMap directory in Kubernetes, we want to ignore the ..data symlink folder
		if strings.HasPrefix(file.Name(), ".") || file.IsDir() {
			continue
		}

		filePath := filepath.Join(templatesDir, file.Name())
		newIndex, err := UpdateTemplateAndCreateNewIndex(client, filePath, "", bulkIndexing, includeTypeName, extraSuffix)
		if err != nil {
			return nil, errors.Wrapf(err, "failed create new index from updated template %s", filePath)
		}

		alias := strings.TrimSuffix(file.Name(), ".json")
		aliasToNewIndex[alias] = newIndex
	}

	return aliasToNewIndex, err
}

func ReindexOne(client *elastic.Client, alias, newIndex, remoteSrc string, versionExternal, noUpdateAlias, bulkIndexing bool) error {
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
	// Also assume the remote index exists if specificed a remote source cluster
	if reindexingRequired || remoteSrc != "" {
		// Default to alias name (used for remote clusters only)
		indicesFromAlias := []string{alias}
		if remoteSrc == "" {
			// Search for indexname based off alias. We should only ever get one result if our naming is sensible
			indicesFromAlias = aliasResult.IndicesByAlias(alias)
		}
		for _, index := range indicesFromAlias {
			// Reindex from the existing index as the source to the new index as the destination
			targetVersionType := "internal"
			if versionExternal {
				targetVersionType = "external"
			}
			src := elastic.NewReindexSource().Index(index)
			if remoteSrc != "" {
				// A remote cluster
				remoteReindexSrc := elastic.NewReindexRemoteInfo().Host(remoteSrc).SocketTimeout("5m")
				src = elastic.NewReindexSource().Index(index).RemoteInfo(remoteReindexSrc)
			}
			dst := elastic.NewReindexDestination().Index(newIndex).VersionType(targetVersionType)
			refresh, err := client.Reindex().Source(src).Destination(dst).Refresh("true").Conflicts("proceed").Do(context.Background())
			if err != nil {
				return errors.Wrapf(err, "failed reindexing from %s to %s", index, newIndex)
			}

			fmt.Printf("%s %s %d from %s to %s\n", DocumentsPrefix, Reindexed, refresh.Total, index, newIndex)
		}
	}

	// We don't reset the refresh/replca values unless we're either updating the index Alias or bulkIndexing isn't set
	if !noUpdateAlias || !bulkIndexing {
		templates, err := client.IndexGetTemplate(alias).Do(context.Background())
		if err != nil {
			return errors.Wrapf(err, "failed to retrieve index template %s", alias)
		}

		seconds := "null"
		replicas := "null"

		if refreshInterval, ok := templates[alias].Settings["index"].(map[string]interface{})["refresh_interval"]; ok {
			seconds = fmt.Sprintf(`"%s"`, refreshInterval.(string))
		}
		if replicaCount, ok := templates[alias].Settings["index"].(map[string]interface{})["number_of_replicas"]; ok {
			replicas = fmt.Sprintf(`"%s"`, replicaCount.(string))
		}

		// Reset the refresh interval to either whatever is specified in the index template or the default (using null)
		resetRefreshInterval := fmt.Sprintf(`{
		"index" : {
			"refresh_interval" : %s,
			"number_of_replicas" : %s
		}
	}
	`, seconds, replicas)

		settings, err := client.IndexPutSettings(newIndex).BodyString(resetRefreshInterval).Do(context.Background())
		if err != nil {
			return errors.Wrapf(err, "failed setting index bulkimport/replica settings for index %s", newIndex)
		}

		if !settings.Acknowledged {
			fmt.Println("index settings not acknowledged")
		}
		fmt.Printf("%s %s refresh_interval/replica for %s reset to template values\n", SettingsPrefix, Updated, newIndex)
	}

	if !noUpdateAlias {
		return UpdateAlias(client, alias, newIndex)
	}
	return nil
}

func UpdateAlias(client *elastic.Client, alias, newIndex string) error {
	if exists, err := client.IndexExists(newIndex).Do(context.Background()); err != nil || !exists {
		fmt.Printf("failed checking for index %s", newIndex)
		return errors.Wrapf(err, "failed checking for index %s", newIndex)
	}

	aliasResult, err := client.Aliases().Alias(alias).Do(context.Background())
	if err == nil {
		// Remove the existing index from the alias
		indicesFromAlias := aliasResult.IndicesByAlias(alias)
		for _, index := range indicesFromAlias {
			removeAlias, err := client.Alias().Remove(index, alias).Do(context.Background())
			if err != nil {
				return errors.Wrapf(err, "failed removing index %s from alias %s", index, alias)
			}

			if !removeAlias.Acknowledged {
				fmt.Println("alias removal not acknowledged")
			}

			fmt.Printf("%s %s %s from %s\n", AliasPrefix, Removed, index, alias)
		}
	} else {
		if !elastic.IsNotFound(err) {
			return errors.Wrapf(err, "failed trying to lookup alias %s", alias)
		}
		fmt.Printf("%s %s %s not found, must be a new index\n", AliasPrefix, Info, alias)
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

func UpdateHostAllocation(client *elastic.Client, newIndex, allocation string) error {

	resetAllocation := fmt.Sprintf(`{
		"index" : {
			"routing.allocation.require._name" : "%s"
		}
	}
	`, allocation)

	_, err := client.IndexPutSettings(newIndex).BodyString(resetAllocation).Do(context.Background())
	if err != nil {
		return errors.Wrapf(err, "failed setting routing allocation to %s for index %s", allocation, newIndex)
	}

	fmt.Printf("%s %s Allocation set to %s\n", SettingsPrefix, Updated, allocation)
	return nil
}

func ReindexAll(client *elastic.Client, aliasToNewIndex map[string]string, remoteSrc string) error {
	for alias, newIndex := range aliasToNewIndex {
		if err := ReindexOne(client, alias, newIndex, remoteSrc, false, false, false); err != nil {
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
