# elasdx
`elasdx` is a command line tool to make ElasticSearch index template updates, reindexing and cleanup a more simple and
CI-friendly process.

## Installation
### Go Get
```bash
go get -u github.com/LGUG2Z/elasdx
cd ${GOPATH}/src/github.com/LGUG2Z/elasdx
make install
```

### Binary
```bash
export ELASDX_VERSION=x.x.x
curl -Lo elasdx.tar.gz https://github.com/LGUG2Z/elasdx/releases/download/v${ELASDX_VERSION}/elasdx_${ELASDX_VERSION}_linux_amd64.tar.gz
mkdir elasdx && tar -xzf elasdx.tar.gz -C elasdx && mv elasdx/elasdx /usr/local/bin/elasdx && chmod +x /usr/local/bin/elasdx && rm -rf elasdx.tar.gz elasdx
```

### Docker
```bash
export ELASDX_VERSION=x.x.x
docker pull lgug2z/elasdx:${ELASDX_VERSION}
docker pull lgug2z/elasdx:latest
```

### Homebrew
```bash
brew tap LGUG2Z/tap
brew install LGUG2Z/tap/elasdx
```

## Background
[ElasticSearch](https://www.elastic.co/products/elasticsearch) provides a comprehensive API to do just about anything,
so it's unfortunate that until now I have always been stuck writing `bash` scripts full of `curl` commands to interact
with it. One of the most painful things to do with `bash` and `curl` when it comes to ElasticSearch is reindexing.

Typically this process requires the following steps:

* Make sure environment variables are set correctly
* [Update a template](https://www.elastic.co/guide/en/elasticsearch/reference/current/indices-templates.html):
```bash
curl -X PUT "localhost:9200/_template/template_1" -H 'Content-Type: application/json' -d'
{
  "index_patterns": ["te*", "bar*"],
  "settings": {
    "number_of_shards": 1
  },
  "mappings": {
    "_source": {
      "enabled": false
    },
    "properties": {
      "host_name": {
        "type": "keyword"
      },
      "created_at": {
        "type": "date",
        "format": "EEE MMM dd HH:mm:ss Z yyyy"
      }
    }
  }
}
'
```

* [Create a new index](https://www.elastic.co/guide/en/elasticsearch/reference/current/indices-create-index.html):
```bash
curl -X PUT "localhost:9200/twitter"
```

* Update the settings on the index for
[bulk reindexing](https://www.elastic.co/guide/en/elasticsearch/reference/current/indices-update-settings.html#bulk):
```bash
curl -X PUT "localhost:9200/twitter/_settings" -H 'Content-Type: application/json' -d'
{
    "index" : {
        "refresh_interval" : "-1"
    }
}
'
```

* [Reindex](https://www.elastic.co/guide/en/elasticsearch/reference/current/docs-reindex.html) from the old index to the new index:
```bash
curl -X POST "localhost:9200/_reindex" -H 'Content-Type: application/json' -d'
{
  "source": {
    "index": "twitter"
  },
  "dest": {
    "index": "new_twitter"
  }
}
'
```

* Set the desired refresh interval once the reindexing is done:
```bash
curl -X PUT "localhost:9200/twitter/_settings" -H 'Content-Type: application/json' -d'
{
    "index" : {
        "refresh_interval" : "1s"
    }
}
'
```

* [Associate an alias](https://www.elastic.co/guide/en/elasticsearch/reference/current/indices-aliases.html) with the new index:
```bash
curl -X POST "localhost:9200/_aliases" -H 'Content-Type: application/json' -d'
{
    "actions" : [
        { "add" : { "index" : "test1", "alias" : "alias1" } }
    ]
}
'
```

* Remove the old index from an alias:
```bash
curl -X POST "localhost:9200/_aliases" -H 'Content-Type: application/json' -d'
{
    "actions" : [
        { "remove" : { "index" : "test1", "alias" : "alias1" } }
    ]
}
'
```

* [Clean up old indices](https://www.elastic.co/guide/en/elasticsearch/reference/current/indices-delete-index.html):
```bash
curl -X DELETE "localhost:9200/twitter"
```

## Usage
`elasdx` can operate either on a single index template `json` file or on a directory containing multiple index templates.
The ElasticSearch URL can be set using the `ELASDX_URL` environment variable or using the `--url` command line flag.
This value defaults to `http://127.0.0.1:9200`. If using basic auth, the username and password can be provided using the
`ELASDX_USERNAME`, `--username` or `ELASDX_PASSWORD`, `--password` environment variables and flags respectively.

Optionally, `elasdx` can make a connection to an instance or a cluster at a `https://` url without providing a valid
certificate by setting either `ELASDX_SKIP_VERIFY` or `--skip-verify`.

### Reindex
The `reindex` command assumes either a file or a directory of files named to match the index template and the eventual
alias desired.

Running `elasdx reindex twitter.json` would create or update an index template with the name `twitter`
and create a new index and associate it with the alias `twitter`. Each new index will take the alias name and append a
timestamp: `twitter-2019-07-07-10:47:17`.

If provisioning a fresh instance of ElasticSearch, this command will skip trying to reindex from a matching existing
index and simply create a new index and associate it with the correct alias.

If reindexing, the `--bulk-indexing` flag can optionally be passed to optimise for bulk indexing by setting the refresh
rate of the new index to `-1` before restoring it to either the default refresh rate once the reindexing is finished, or
to the refresh rate defined in the index template `json` file.

```text
❯ elasdx reindex --bulk-indexing twitter.json
INDEX TEMPLATE   Updated     twitter
INDEX            Created     twitter-2019-07-07-10:54:40
DOCUMENTS        Reindexed   100 from twitter-2019-07-07-10:54:33 to twitter-2019-07-07-10:54:40
ALIAS            Removed     twitter-2019-07-07-10:54:33 from twitter
ALIAS            Added       twitter-2019-07-07-10:54:40 to twitter
```
```json5
// ❯ curl -X GET "localhost:9200/twitter/_settings" | jq
{
  "twitter-2019-07-07-10:54:40": {
    "settings": {
      "index": {
        "refresh_interval": "10s", // Desired refresh interval from template is set
        "number_of_shards": "5",
        "provided_name": "twitter-2019-07-07-10:54:40",
        "creation_date": "1562492836239",
        "number_of_replicas": "1",
        "uuid": "d9EkiyzwTMGt5-vaChULcQ",
        "version": {
          "created": "6020299"
        }
      }
    }
  }
}
```
### Cleanup
The `cleanup` command is intended to work on the same file or directory structure used for the `reindex` command.

The maximum history of indices to keep is set using the `--max-history` flag, and defaults to `2`, meaning that the
current index associated with the alias and the previous index will be kept, and all other previous indices will be
deleted. If `--max-history` is set to `0`, all indices including the current index associated with the alias will be
deleted; this can be useful during development.

```text
❯ elasdx cleanup twitter.json
INDEX            Deleted     twitter-2019-07-07-10:15:31
INDEX            Deleted     twitter-2019-07-07-10:15:39
INDEX            Deleted     twitter-2019-07-07-10:47:15
```

```text
❯ elasdx cleanup --max-history 0 twitter.json
INDEX            Deleted     twitter-2019-07-07-10:54:33
INDEX            Deleted     twitter-2019-07-07-10:54:40
```