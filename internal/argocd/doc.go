// Package argocd is a small client for Argo CD's REST API, with Bearer token
// authentication on the plain HTTP client of internal/httpclient. It does
// not import the Argo CD module, which is very large. The Argo CD source
// uses it to read applications; the Argo CD rollback action also uses its
// rollback call.
//
// The client reads applications of one namespace, named by project and
// name, "project/name", as Argo CD's RBAC policies name them.
//
// What the client relies on from Argo CD, as its public source and API
// specification at the tag v3.5.5 of github.com/argoproj/argo-cd show it:
//
//   - assets/swagger.json has GET /api/v1/applications (query: projects,
//     appNamespace), GET /api/v1/applications/{name} (query: project,
//     appNamespace) and GET /api/v1/applications/{name}/revisions/{revision}/metadata
//     (query: appNamespace, project, versionId, sourceIndex). The server reads
//     the token from the header "Authorization: Bearer <token>"
//     (server/server.go, docs/developer-guide/api-docs.md).
//   - Both reads need the RBAC action "applications, get" on the object
//     "<project>/<name>" (server/application/application.go, assets/builtin-policy.csv).
//     An application in another namespace than the one Argo CD runs in has
//     the object "<project>/<namespace>/<name>" (util/security/rbac.go). The
//     list leaves out the applications the token may not get. An application
//     with no project is in the project "default"
//     (ApplicationSpec.GetProject in pkg/apis/application/v1alpha1/types.go).
//   - A get that names no project answers 403 for an application that does
//     not exist, so as not to tell that it exists. A get that names the
//     project answers 404 for an application that does not exist, or is in
//     another project, and 403 when the token may not get it
//     (getAppEnforceRBAC in server/application/application.go).
//   - Without appNamespace, a list covers every namespace Argo CD watches,
//     and a get uses the namespace Argo CD runs in. The client always sends
//     appNamespace and drops items of other namespaces.
//   - status.history holds the successful syncs of the whole application,
//     oldest first, as many as spec.revisionHistoryLimit (10 when unset),
//     each with an increasing id (persistRevisionHistory in controller/state.go
//     and controller/sync.go, RevisionHistoryLimit in
//     pkg/apis/application/v1alpha1/application_defaults.go). An entry of an
//     application with spec.sources has sources and revisions and no
//     revision. initiatedBy has a username, or automated set when the
//     application controller started the sync; it is not in the history of
//     v2.10.0, and it is in v2.11.0 and v3.5.5.
//   - Automated sync is on when spec.syncPolicy.automated is set and its
//     enabled field is not false (SyncPolicy.IsAutomatedSyncEnabled in
//     pkg/apis/application/v1alpha1/types.go). The enabled field is not in
//     v3.0.0 and is in v3.1.0 and v3.5.5.
//   - status.sync.revision is the revision the application was compared
//     with, not the deployed one. The deployed one is in the newest history
//     entry and in status.operationState.syncResult.revision.
//   - Since Argo CD 3.0 the health of each resource is not stored in
//     status.resources unless controller.resource.health.persist is true
//     (status.resourceHealthSource is then "appTree"). A get fills it in from
//     the resource tree cache; a list does not
//     (docs/operator-manual/upgrading/2.14-3.0.md, inferResourcesStatusHealth
//     in server/application/application.go).
//   - The revision metadata call takes the history id as versionId, and then
//     reads the revision from the repository of that history entry.
//   - The UI page of an application is <base>/applications/<namespace>/<name>
//     (getAppUrl in ui/src/app/applications/components/utils.tsx).
//
// What the rollback call relies on, from the same tag:
//
//   - POST /api/v1/applications/{name}/rollback takes a body with id (int64,
//     the history ID), dryRun, prune, appNamespace, project and name, and
//     answers 200 with the application (assets/swagger.json,
//     applicationApplicationRollbackRequest). It needs the RBAC action
//     "applications, sync" on the application, and not "get"
//     (Rollback in server/application/application.go).
//   - It refuses with the gRPC code FailedPrecondition and the message
//     "rollback cannot be initiated when auto-sync is enabled" while
//     auto-sync is on, and with InvalidArgument when the application has no
//     history entry with that ID (same function). The client reads any 4xx
//     answer as a *StatusError with Argo CD's message, and does not depend on
//     the exact status code, which the gRPC gateway chooses.
//   - It sets an operation that syncs the entry's own revision, source and
//     sources, with the apply strategy, which skips hooks, with the prune
//     setting of the request and the caller as initiatedBy (same function;
//     SyncOperation.IsApplyStrategy in pkg/apis/application/v1alpha1/types.go
//     and the skip-hooks argument of sync.WithOperationSettings in
//     controller/sync.go). It does not check sync windows; the controller
//     leaves a blocked operation in the phase Running with the message "Sync
//     operation blocked by sync window" (SyncAppState in controller/sync.go).
//   - Setting an operation clears status.operationState, and fails with
//     FailedPrecondition "another operation is already in progress" while
//     another operation is set (SetAppOperation in util/argo/argo.go).
//   - When the operation ends, the controller sets status.operationState.phase
//     to Succeeded, Failed or Error and clears the operation in one patch
//     (setOperationState in controller/appcontroller.go; the phases are in
//     gitops-engine/pkg/sync/common/types.go).
//   - A successful sync that is not a dry run and not of some resources adds a
//     history entry with the ID of the newest entry plus one, or 0 for the
//     first, and cuts the list to the revision history limit
//     (persistRevisionHistory in controller/state.go, and the call in
//     controller/sync.go).
//   - An error answer is JSON with a message field (the gRPC gateway's error
//     body). The gRPC codes Unauthenticated (invalid session, server/server.go),
//     PermissionDenied (common/common.go) and NotFound become the HTTP
//     statuses 401, 403 and 404.
package argocd
