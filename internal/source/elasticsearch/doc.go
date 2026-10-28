// Package elasticsearch is the native logs source for Elasticsearch and
// OpenSearch. It registers itself as the source type "elasticsearch"; the
// flavor key says which of the two the cluster is.
//
// Both products answer the same _search and _field_caps APIs, so the
// source calls them with plain HTTP and needs no client library. It signs
// in with an Elasticsearch API key or a user name and password, read from
// secrets for each request.
//
// Every tool only reads. The source builds every request body itself, a
// call for an index pattern the config does not allow is a tool error
// before anything is sent, every search is bounded to at most 7 days, and
// results link to the same search in Kibana Discover or OpenSearch
// Dashboards when the config gives a link template.
package elasticsearch
