// Package backstage is the native Backstage source. It registers itself as
// the source type "backstage".
//
// A source reads one Backstage software catalog through its REST API with
// a static token, and only reads. Its tools tell an investigation who owns
// a service, what it depends on, what depends on it, and which system it
// is part of; every result links to the entity's page in Backstage.
//
// The source is also a source.ServiceCatalog: ServiceDependencies reads
// the dependsOn and dependencyOf relations of every component and resource,
// which the grouper uses to group alerts of services that depend on each
// other.
package backstage
